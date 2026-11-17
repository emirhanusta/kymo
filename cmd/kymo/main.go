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
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/collector"
	k8scollector "github.com/emirhanusta/kymo/internal/collector/kubernetes"
	"github.com/emirhanusta/kymo/internal/config"
	httphandler "github.com/emirhanusta/kymo/internal/handler/http"
	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository/timescale"
	"github.com/emirhanusta/kymo/internal/secret"
	"github.com/emirhanusta/kymo/internal/service"
	"github.com/emirhanusta/kymo/migrations"
)

func main() {
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
	// Recorder + management + sources
	// -------------------------------------------------------------------------
	svc := service.NewRecorderService(
		db.Containers(),
		db.Events(),
		db.Resources(),
		db.Scores(),
		db.CronJobs(),
		db.Workloads(),
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

	factory := func(spec model.SourceSpec) collector.Collector {
		interval := spec.PollInterval
		if interval <= 0 {
			interval = cfg.Collector.StatsPollInterval
		}
		return k8scollector.New(log, spec.Name, spec.KubeconfigPath, spec.Kubeconfig, spec.KubeContext, interval)
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
	router.Use(gin.Recovery())
	router.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	})
	httphandler.New(svc, mgmt, sources, db, log, router)
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
		log.Info("config.yaml sources reloaded",
			zap.Strings("added", res.Added), zap.Strings("removed", res.Removed),
			zap.Strings("updated", res.Updated), zap.Strings("unchanged", res.Unchanged),
			zap.Strings("conflicts", res.Conflicts))
	}
	if err := config.Watch(rootCtx, cfg.File, 500*time.Millisecond, reload); err != nil {
		log.Warn("cannot watch config file; use POST /api/v1/sources/reload", zap.String("file", cfg.File), zap.Error(err))
	}

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
// running config and a freshly loaded one. Only collector.sources is applied live.
func restartOnlyChanges(old, fresh *config.Config) string {
	var changed []string
	if old.Server != fresh.Server {
		changed = append(changed, "server")
	}
	if old.Database != fresh.Database {
		changed = append(changed, "database")
	}
	if old.Log != fresh.Log {
		changed = append(changed, "log")
	}
	if old.SecretKey != fresh.SecretKey {
		changed = append(changed, "secret_key")
	}
	oc, fc := old.Collector, fresh.Collector
	if oc.StatsPollInterval != fc.StatsPollInterval || oc.ScoringInterval != fc.ScoringInterval || oc.ScoringWindow != fc.ScoringWindow {
		changed = append(changed, "collector (poll/scoring)")
	}
	return strings.Join(changed, ", ")
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
