// Package config handles loading, parsing, and validating application configuration
// from environment variables, an optional YAML config file, and built-in defaults.
package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config is the top-level configuration struct for the KYMO service.
// Values are resolved in priority order: environment variables > config file > defaults.
type Config struct {
	Server    ServerConfig    `mapstructure:"server"`
	Database  DatabaseConfig  `mapstructure:"database"`
	Collector CollectorConfig `mapstructure:"collector"`
	Log       LogConfig       `mapstructure:"log"`

	// SecretKey encrypts kubeconfigs uploaded through the UI (env KYMO_SECRET_KEY).
	// Without it, Kubernetes sources can only reference kubeconfig files on the server.
	SecretKey string `mapstructure:"secret_key"`
}

// ServerConfig controls the HTTP server binding and timeout behaviour.
type ServerConfig struct {
	// Address is the TCP address the HTTP server listens on (e.g. ":8080").
	Address string `mapstructure:"address"`

	// ReadTimeout is the maximum duration allowed to read the entire incoming request,
	// including the body. Prevents slow-client attacks.
	ReadTimeout time.Duration `mapstructure:"read_timeout"`

	// WriteTimeout is the maximum duration allowed to write the response.
	// Should be set longer than the slowest expected handler.
	// NOTE: it is not applied to the HTTP server because log streaming (SSE)
	// connections are long-lived.
	WriteTimeout time.Duration `mapstructure:"write_timeout"`

	// StaticDir, when set, is a directory containing the built frontend
	// (frontend/dist/frontend/browser). It is served at "/" with SPA fallback to
	// index.html for any non-API path.
	StaticDir string `mapstructure:"static_dir"`
}

// DatabaseConfig holds TimescaleDB/PostgreSQL connection parameters.
type DatabaseConfig struct {
	// DSN is the pgx-compatible connection string.
	// Example: "postgres://kymo:s3cr3t@localhost:5432/kymo?sslmode=disable"
	DSN string `mapstructure:"dsn"`

	// MaxConns is the maximum size of the pgx connection pool.
	MaxConns int32 `mapstructure:"max_conns"`

	// AutoMigrate applies the embedded SQL migrations on startup (default true).
	AutoMigrate bool `mapstructure:"auto_migrate"`
}

// Source describes a single collector source — either a Docker daemon or a
// Kubernetes cluster. Multiple sources of the same type are supported.
type Source struct {
	// Name is a human-readable identifier used to tag every container, event, and
	// resource sample originating from this source (e.g. "local-docker", "prod-k8s").
	Name string `mapstructure:"name"`

	// Type is the runtime type: "docker" or "kubernetes".
	Type string `mapstructure:"type"`

	// Socket is the Docker daemon socket path (e.g. "unix:///var/run/docker.sock").
	// Only used when Type == "docker". Leave empty to use the DOCKER_HOST env var.
	Socket string `mapstructure:"socket"`

	// KubeconfigPath is the path to a kubeconfig file used when Type == "kubernetes".
	// Leave empty to fall back to in-cluster credentials then ~/.kube/config.
	KubeconfigPath string `mapstructure:"kubeconfig_path"`

	// KubeContext selects a named context from the kubeconfig file.
	// Leave empty to use the current-context from the file.
	KubeContext string `mapstructure:"kube_context"`

	// PollInterval overrides the global stats poll interval for this source.
	// Zero means use the global default.
	PollInterval time.Duration `mapstructure:"poll_interval"`

	// ReadOnly disables management actions (start/stop/kill, delete pod,
	// scale, rollout restart) for this source.
	ReadOnly bool `mapstructure:"read_only"`
}

// CollectorConfig controls which runtime collectors are enabled and how they behave.
type CollectorConfig struct {
	// Sources is the list of collector sources to activate. Each source maps to
	// one collector instance (Docker daemon or Kubernetes cluster).
	// If empty, a default local-docker source is added automatically.
	Sources []Source `mapstructure:"sources"`

	// StatsPollInterval is the default interval at which collectors sample
	// container resource usage. Individual sources can override this.
	StatsPollInterval time.Duration `mapstructure:"stats_poll_interval"`

	// ScoringInterval controls how often the stability scorer re-evaluates all containers.
	ScoringInterval time.Duration `mapstructure:"scoring_interval"`

	// ScoringWindow is the look-back duration used by the scorer when querying events
	// and resource samples (e.g. "24h" means the last 24 hours of data).
	ScoringWindow time.Duration `mapstructure:"scoring_window"`
}

// LogConfig controls the structured logger output format and verbosity.
type LogConfig struct {
	// Level sets the minimum log level. Valid values: "debug", "info", "warn", "error".
	Level string `mapstructure:"level"`

	// Format selects the encoder: "json" for production deployments,
	// "console" for human-readable development output.
	Format string `mapstructure:"format"`
}

// Load reads configuration from the following sources in descending priority:
//  1. Environment variables prefixed with KYMO_ (e.g. KYMO_SERVER_ADDRESS).
//     Nested keys use underscores (KYMO_DATABASE_DSN maps to database.dsn).
//  2. A "config.yaml" file in the working directory or /etc/kymo/.
//  3. Built-in defaults defined below.
//
// It returns an error if the resolved configuration cannot be decoded into Config.
func Load() (*Config, error) {
	v := viper.New()

	// --- Defaults ---
	v.SetDefault("server.address", ":8080")
	v.SetDefault("server.read_timeout", "15s")
	v.SetDefault("server.write_timeout", "15s")
	v.SetDefault("server.static_dir", "")

	v.SetDefault("database.dsn", "postgres://kymo:kymo@localhost:5432/kymo?sslmode=disable")
	v.SetDefault("database.max_conns", 20)
	v.SetDefault("database.auto_migrate", true)

	v.SetDefault("collector.stats_poll_interval", "10s")
	v.SetDefault("collector.scoring_interval", "60s")
	v.SetDefault("collector.scoring_window", "24h")

	v.SetDefault("secret_key", "")
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "json")

	// --- Config file (optional) ---
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(".")
	v.AddConfigPath("/etc/kymo")
	_ = v.ReadInConfig() // not an error if the file is absent

	// --- Environment overrides ---
	v.SetEnvPrefix("KYMO")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("config: unmarshal failed: %w", err)
	}

	// If no sources are configured, default to a single local Docker source.
	if len(cfg.Collector.Sources) == 0 {
		cfg.Collector.Sources = []Source{
			{Name: "local-docker", Type: "docker"},
		}
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// validate rejects configurations that would otherwise fail confusingly at
// runtime (duplicate or unnamed sources, unknown source types).
func (c *Config) validate() error {
	seen := make(map[string]bool, len(c.Collector.Sources))
	for i, s := range c.Collector.Sources {
		if s.Name == "" {
			return fmt.Errorf("config: collector.sources[%d] has no name", i)
		}
		if seen[s.Name] {
			return fmt.Errorf("config: duplicate collector source name %q", s.Name)
		}
		seen[s.Name] = true
		if s.Type != "docker" && s.Type != "kubernetes" {
			return fmt.Errorf("config: source %q has unknown type %q (want docker or kubernetes)", s.Name, s.Type)
		}
	}
	return nil
}
