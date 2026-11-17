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
	"syscall"
	"time"

	dockerclient "github.com/docker/docker/client"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	dockercollector "github.com/emirhanusta/kymo/internal/collector/docker"
	k8scollector "github.com/emirhanusta/kymo/internal/collector/kubernetes"
	"github.com/emirhanusta/kymo/internal/config"
	httphandler "github.com/emirhanusta/kymo/internal/handler/http"
	"github.com/emirhanusta/kymo/internal/repository/timescale"
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
	// Collectors — one per configured source
	// -------------------------------------------------------------------------
	var sources []service.CollectorSource

	// Build clients for the management service, keyed by source name.
	dockerClients := make(map[string]*dockerclient.Client)
	k8sClients := make(map[string]kubernetes.Interface)
	var dockerOrder, k8sOrder []string

	for _, src := range cfg.Collector.Sources {
		interval := src.PollInterval
		if interval <= 0 {
			interval = cfg.Collector.StatsPollInterval
		}

		switch src.Type {
		case "docker":
			c := dockercollector.New(log, src.Name, src.Socket, interval)
			sources = append(sources, service.CollectorSource{Collector: c, Name: src.Name, Type: src.Type})
			log.Info("docker collector configured", zap.String("source", src.Name))
			if cli := buildDockerClientWithSocket(src.Socket, log); cli != nil {
				dockerClients[src.Name] = cli
				dockerOrder = append(dockerOrder, src.Name)
			}

		case "kubernetes":
			c := k8scollector.New(log, src.Name, src.KubeconfigPath, src.KubeContext, interval)
			sources = append(sources, service.CollectorSource{Collector: c, Name: src.Name, Type: src.Type})
			log.Info("kubernetes collector configured", zap.String("source", src.Name))
			if cli := buildK8sClient(src.KubeconfigPath, src.KubeContext, log); cli != nil {
				k8sClients[src.Name] = cli
				k8sOrder = append(k8sOrder, src.Name)
			}

		default:
			log.Warn("unknown source type; skipping", zap.String("source", src.Name), zap.String("type", src.Type))
		}
	}

	if len(sources) == 0 {
		log.Warn("no sources configured; the service will start but collect no data")
	}

	// -------------------------------------------------------------------------
	// Recorder Service
	// -------------------------------------------------------------------------
	svc := service.NewRecorderService(
		sources,
		db.Containers(),
		db.Events(),
		db.Resources(),
		db.Scores(),
		db.CronJobs(),
		service.ScoringConfig{
			Interval: cfg.Collector.ScoringInterval,
			Window:   cfg.Collector.ScoringWindow,
		},
		log,
	)

	// -------------------------------------------------------------------------
	// Management Service
	// -------------------------------------------------------------------------
	mgmt := service.NewManagementService(dockerClients, dockerOrder, k8sClients, k8sOrder, log)

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
	httphandler.New(svc, mgmt, db, log, router)
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

// buildDockerClientWithSocket creates a Docker API client, optionally targeting
// the given socket path. Pass "" to use the DOCKER_HOST env var.
// Returns nil (with a warning) if the daemon is unreachable.
func buildDockerClientWithSocket(socketPath string, log *zap.Logger) *dockerclient.Client {
	opts := []dockerclient.Opt{
		dockerclient.FromEnv,
		dockerclient.WithAPIVersionNegotiation(),
	}
	if socketPath != "" {
		opts = append(opts, dockerclient.WithHost(socketPath))
	}
	cli, err := dockerclient.NewClientWithOpts(opts...)
	if err != nil {
		log.Warn("docker management client unavailable", zap.Error(err))
		return nil
	}
	return cli
}

// buildK8sClient creates a Kubernetes clientset using the given kubeconfig path
// and optional context name (falling back to in-cluster credentials then ~/.kube/config).
// Returns nil (with a warning) when no valid configuration is found.
func buildK8sClient(kubeconfigPath, kubeContext string, log *zap.Logger) kubernetes.Interface {
	var restCfg *rest.Config
	var err error

	if kubeconfigPath != "" || kubeContext != "" {
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		if kubeconfigPath != "" {
			loadingRules.ExplicitPath = kubeconfigPath
		}
		overrides := &clientcmd.ConfigOverrides{}
		if kubeContext != "" {
			overrides.CurrentContext = kubeContext
		}
		restCfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			loadingRules, overrides,
		).ClientConfig()
	} else {
		restCfg, err = rest.InClusterConfig()
		if errors.Is(err, rest.ErrNotInCluster) {
			restCfg, err = clientcmd.BuildConfigFromFlags("", "")
		}
	}
	if err != nil {
		log.Warn("kubernetes management client unavailable", zap.Error(err))
		return nil
	}

	cli, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		log.Warn("failed to create kubernetes management client", zap.Error(err))
		return nil
	}
	return cli
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
