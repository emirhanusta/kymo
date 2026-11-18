// Command kymo is the entry point for the KYMO service.
// It wires together configuration, logging, database, collectors, and the HTTP
// server, then runs until it receives SIGINT or SIGTERM.
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/auth"
	"github.com/emirhanusta/kymo/internal/collector"
	k8scollector "github.com/emirhanusta/kymo/internal/collector/kubernetes"
	"github.com/emirhanusta/kymo/internal/config"
	httphandler "github.com/emirhanusta/kymo/internal/handler/http"
	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository/timescale"
	"github.com/emirhanusta/kymo/internal/secret"
	"github.com/emirhanusta/kymo/internal/service"
	"github.com/emirhanusta/kymo/internal/service/errorlogs"
	"github.com/emirhanusta/kymo/internal/service/issues"
	"github.com/emirhanusta/kymo/internal/service/notify"
	"github.com/emirhanusta/kymo/migrations"
)

func main() {
	if len(os.Args) > 1 {
		os.Exit(runCLI(os.Args[1:]))
	}
	// -------------------------------------------------------------------------
	// Configuration
	// -------------------------------------------------------------------------
	cfg, err := config.Load()
	if err != nil {
		panic("failed to load configuration: " + err.Error())
	}

	// -------------------------------------------------------------------------
	// Logger
	// -------------------------------------------------------------------------
	log, err := buildLogger(cfg.Log)
	if err != nil {
		panic("failed to initialise logger: " + err.Error())
	}
	defer log.Sync() //nolint:errcheck

	// -------------------------------------------------------------------------
	// Database
	// -------------------------------------------------------------------------
	dbCtx, dbCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer dbCancel()

	db, err := timescale.New(dbCtx, cfg.Database.DSN, cfg.Database.MaxConns, log)
	if err != nil {
		log.Fatal("database connection failed", zap.Error(err))
	}
	defer db.Close()

	if cfg.Database.AutoMigrate {
		migCtx, migCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		err := db.Migrate(migCtx, migrations.FS)
		migCancel()
		if err != nil {
			log.Fatal("database migration failed", zap.Error(err))
		}
	}

	// -------------------------------------------------------------------------
	// Accounts & access control
	// -------------------------------------------------------------------------
	authSvc, err := auth.New(db.Auth(), auth.OptionsFromConfig(cfg), log)
	if err != nil {
		log.Fatal("invalid auth configuration", zap.Error(err))
	}
	setupAuth(authSvc, log)

	// -------------------------------------------------------------------------
	// Recorder + management + sources
	// -------------------------------------------------------------------------
	svc := service.NewRecorderService(
		db.Containers(),
		db.Events(),
		db.Resources(),
		db.Scores(),
		db.CronJobs(),
		db.Workloads(),
		db.CrashLogs(),
		db.K8sEvents(),
		service.ScoringConfig{
			Interval: cfg.Collector.ScoringInterval,
			Window:   cfg.Collector.ScoringWindow,
		},
		log,
	)
	mgmt := service.NewManagementService(log)

	box, err := secret.New(cfg.SecretKey)
	if err != nil {
		log.Fatal("invalid secret key", zap.Error(err))
	}
	if !box.Configured() {
		log.Info("KYMO_SECRET_KEY not set; uploading kubeconfigs through the UI is disabled")
	}

	notifier := notify.New(db.Notifications(), db.CrashLogs(), box, db.Issues(), notify.Options{
		Enabled:   cfg.Notifications.Enabled,
		PublicURL: cfg.Server.PublicURL,
	}, log)
	if err := syncNotificationChannels(notifier, cfg.Notifications.Channels, log); err != nil {
		log.Warn("syncing notification channels from config.yaml failed", zap.Error(err))
	}
	errorSvc, err := errorlogs.New(db.ErrorLogs(), cfg.LogCapture, svc, log)
	if err != nil {
		log.Fatal("invalid log_capture configuration", zap.Error(err))
	}
	applyLogCapture(errorSvc, cfg.LogCapture, log)
	engine := issues.New(issues.Config{
		Interval:      cfg.Issues.EvaluateInterval,
		DegradedAfter: cfg.Issues.DegradedAfter,
		ResolveAfter:  cfg.Issues.ResolveAfter,
		ReopenWindow:  cfg.Issues.ReopenWindow,
		IncludeSystem: cfg.Issues.IncludeSystem,

		ErrorRateThreshold: cfg.Issues.ErrorRateThreshold,
	}, issues.Deps{
		Workloads: db.Workloads(),
		Events:    db.Events(),
		K8sEvents: db.K8sEvents(),
		CronJobs:  db.CronJobs(),
		Issues:    db.Issues(),
		ErrorLogs: db.ErrorLogs(),
		Sources:   svc,
		Notifier:  notifier,
		Log:       log,
	})

	factory := func(spec model.SourceSpec) collector.Collector {
		interval := spec.PollInterval
		if interval <= 0 {
			interval = cfg.Collector.StatsPollInterval
		}
		return k8scollector.New(log, spec.Name, spec.KubeconfigPath, spec.Kubeconfig, spec.KubeContext, interval, k8scollector.Options{
			CrashLogs: k8scollector.CrashLogOptions{
				Enabled:   cfg.Collector.CrashLogs.Enabled,
				TailLines: cfg.Collector.CrashLogs.TailLines,
				MaxBytes:  cfg.Collector.CrashLogs.MaxBytes,
			},
			K8sEvents: k8scollector.K8sEventOptions{
				Enabled:           cfg.Collector.K8sEvents.Enabled,
				NormalReasons:     cfg.Collector.K8sEvents.NormalReasons,
				ExcludeNamespaces: cfg.Collector.K8sEvents.ExcludeNamespaces,
			},
			ErrorLogs: errorSvc,
		})
	}
	sources := service.NewSourceManager(db.Sources(), box, svc, mgmt, factory, log)

	configSpecs := config.SourceSpecs(cfg)
	if cfg.File != "" {
		running := cfg
		sources.SetConfigReloader(cfg.File, func() ([]model.SourceSpec, error) {
			fresh, err := running.Reload()
			if err != nil {
				return nil, err
			}
			if note := restartOnlyChanges(running, fresh); note != "" {
				log.Info("config.yaml: these settings changed and need a restart to apply", zap.String("settings", note))
			}
			if err := syncNotificationChannels(notifier, fresh.Notifications.Channels, log); err != nil {
				log.Warn("syncing notification channels from config.yaml failed", zap.Error(err))
			}
			applyLogCapture(errorSvc, fresh.LogCapture, log)
			return config.SourceSpecs(fresh), nil
		})
	}
	bootCtx, bootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	err = sources.Bootstrap(bootCtx, configSpecs)
	bootCancel()
	if err != nil {
		log.Fatal("loading sources failed", zap.Error(err))
	}

	// -------------------------------------------------------------------------
	// HTTP Server
	// -------------------------------------------------------------------------
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	// UseRawPath makes Gin route against the raw (percent-encoded) URL so that
	// %2F in a path parameter is NOT split on the slash. UnescapePathValues then
	// decodes the captured parameter before handing it to handlers, so
	// c.Param("id") returns the logical ID (e.g. "namespace/pod/container").
	router.UseRawPath = true
	router.UnescapePathValues = true
	if len(cfg.Server.TrustedProxies) > 0 {
		if err := router.SetTrustedProxies(cfg.Server.TrustedProxies); err != nil {
			log.Fatal("invalid server.trusted_proxies", zap.Error(err))
		}
	}
	router.Use(gin.Recovery())
	// Same-origin by default; only server.allowed_origins get CORS (with credentials).
	router.Use(auth.CORS(cfg.Server.AllowedOrigins))
	router.Use(auth.CSRF(cfg.Server.PublicURL, cfg.Server.AllowedOrigins))
	httphandler.New(svc, mgmt, sources, db, log, router,
		httphandler.WithAuth(authSvc),
		httphandler.WithIssues(db.Issues()),
		httphandler.WithNotifications(notifier),
		httphandler.WithErrorLogs(errorSvc),
		httphandler.WithServerInfo(cfg.Server.PublicURL, cfg.Notifications.Enabled))
	if cfg.Server.StaticDir != "" {
		if err := httphandler.ServeSPA(router, cfg.Server.StaticDir); err != nil {
			log.Fatal("cannot serve frontend", zap.String("static_dir", cfg.Server.StaticDir), zap.Error(err))
		}
		log.Info("serving frontend", zap.String("static_dir", cfg.Server.StaticDir))
	}

	server := &http.Server{
		Addr:        cfg.Server.Address,
		Handler:     router,
		ReadTimeout: cfg.Server.ReadTimeout,
		// WriteTimeout is intentionally omitted (0 = no timeout) so that SSE
		// log-streaming connections are not cut off mid-stream.
	}

	// -------------------------------------------------------------------------
	// Graceful Shutdown
	// -------------------------------------------------------------------------
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		log.Info("shutdown signal received", zap.String("signal", sig.String()))
		rootCancel()

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shutdownCancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Error("http server shutdown error", zap.Error(err))
		}
	}()

	// Apply config.yaml source changes live.
	reload := func() {
		res, err := sources.ReloadConfig(rootCtx)
		if err != nil {
			log.Error("config.yaml reload failed; keeping current sources", zap.Error(err))
			return
		}
		if len(res.Added)+len(res.Removed)+len(res.Updated) > 0 {
			authSvc.Record(rootCtx, auth.SystemActor, "source.config_reload", "config", cfg.File,
				map[string]any{"added": res.Added, "removed": res.Removed, "updated": res.Updated})
		}
		log.Info("config.yaml sources reloaded",
			zap.Strings("added", res.Added), zap.Strings("removed", res.Removed),
			zap.Strings("updated", res.Updated), zap.Strings("unchanged", res.Unchanged),
			zap.Strings("conflicts", res.Conflicts))
	}
	if err := config.Watch(rootCtx, cfg.File, 500*time.Millisecond, reload); err != nil {
		log.Warn("cannot watch config file; use POST /api/v1/sources/reload", zap.String("file", cfg.File), zap.Error(err))
	}

	go authSvc.Cleanup(rootCtx)

	// Issue engine and notification workers.
	notifier.Start(rootCtx)
	go engine.Run(rootCtx)
	go errorSvc.Run(rootCtx)

	// Run the recorder event loop in the background.
	go func() {
		if err := svc.Run(rootCtx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("recorder exited with error", zap.Error(err))
			rootCancel()
		}
	}()

	log.Info("http server listening", zap.String("address", cfg.Server.Address))

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal("http server error", zap.Error(err))
	}

	log.Info("kymo stopped cleanly")
}

// restartOnlyChanges lists the top-level settings that differ between the
// running config and a freshly loaded one. collector.sources, the notification
// channels and log_capture (except retention) are applied live.
func restartOnlyChanges(old, fresh *config.Config) string {
	var changed []string
	if !reflect.DeepEqual(old.Server, fresh.Server) {
		changed = append(changed, "server")
	}
	if !reflect.DeepEqual(old.Auth, fresh.Auth) {
		changed = append(changed, "auth")
	}
	if old.Database != fresh.Database {
		changed = append(changed, "database")
	}
	if old.Log != fresh.Log {
		changed = append(changed, "log")
	}
	if old.Issues != fresh.Issues {
		changed = append(changed, "issues")
	}
	if old.Notifications.Enabled != fresh.Notifications.Enabled {
		changed = append(changed, "notifications.enabled")
	}
	if old.SecretKey != fresh.SecretKey {
		changed = append(changed, "secret_key")
	}
	if old.LogCapture.Retention != fresh.LogCapture.Retention {
		changed = append(changed, "log_capture.retention")
	}
	oc, fc := old.Collector, fresh.Collector
	if oc.StatsPollInterval != fc.StatsPollInterval || oc.ScoringInterval != fc.ScoringInterval || oc.ScoringWindow != fc.ScoringWindow || oc.CrashLogs != fc.CrashLogs || !slices.Equal(oc.K8sEvents.NormalReasons, fc.K8sEvents.NormalReasons) || !slices.Equal(oc.K8sEvents.ExcludeNamespaces, fc.K8sEvents.ExcludeNamespaces) || oc.K8sEvents.Enabled != fc.K8sEvents.Enabled {
		changed = append(changed, "collector (poll/scoring/crash_logs/k8s_events)")
	}
	return strings.Join(changed, ", ")
}

// syncNotificationChannels mirrors the channels declared in config.yaml into the
// database (they are locked in the UI).
func syncNotificationChannels(n *notify.Service, declared []config.NotificationChannel, log *zap.Logger) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := n.SyncConfig(ctx, declared); err != nil {
		return err
	}
	if len(declared) > 0 {
		log.Info("notification channels loaded from config.yaml", zap.Int("count", len(declared)))
	}
	return nil
}

// applyLogCapture applies the log_capture section live (settings, patterns and
// the config.yaml targets). A bad section keeps the previous settings.
func applyLogCapture(s *errorlogs.Service, lc config.LogCaptureConfig, log *zap.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.ApplyConfig(ctx, lc); err != nil {
		log.Warn("applying log_capture from config.yaml failed", zap.Error(err))
		return
	}
	if len(lc.Targets) > 0 {
		log.Info("log capture targets loaded from config.yaml", zap.Int("count", len(lc.Targets)))
	}
}

// buildLogger constructs a zap.Logger from the provided LogConfig.
// Format "console" produces a human-readable output suitable for development;
// any other value (including "json") produces structured JSON output.
func buildLogger(cfg config.LogConfig) (*zap.Logger, error) {
	var zapCfg zap.Config
	if cfg.Format == "console" {
		zapCfg = zap.NewDevelopmentConfig()
	} else {
		zapCfg = zap.NewProductionConfig()
	}

	level, err := zap.ParseAtomicLevel(cfg.Level)
	if err != nil {
		return nil, err
	}
	zapCfg.Level = level

	return zapCfg.Build()
}

// setupAuth logs the auth mode and performs first-run bootstrap: when no users
// exist yet the initial admin is created from KYMO_ADMIN_* (or with a random
// password that is logged once). The variables are ignored once users exist.
func setupAuth(a *auth.Service, log *zap.Logger) {
	if !a.Enabled() {
		log.Warn("AUTHENTICATION IS DISABLED (auth.enabled: false): every request acts as the owner. Use this for local development only and never expose KYMO like this")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	opts := auth.BootstrapOptions{
		Username:   os.Getenv("KYMO_ADMIN_USERNAME"),
		Email:      os.Getenv("KYMO_ADMIN_EMAIL"),
		Name:       os.Getenv("KYMO_ADMIN_NAME"),
		Password:   os.Getenv("KYMO_ADMIN_PASSWORD"),
		MustChange: envBool("KYMO_ADMIN_MUST_CHANGE_PASSWORD"),
	}
	if _, err := a.BootstrapAdmin(ctx, opts); err != nil {
		log.Fatal("creating the initial admin account failed", zap.Error(err))
	}
	if a.OIDC != nil {
		log.Info("single sign-on enabled; register this redirect URL at the identity provider", zap.String("redirect_url", a.OIDC.RedirectURL()))
	}
}

// envBool reads a boolean environment variable (1/true/yes/on).
func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
