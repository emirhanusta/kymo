// Package config handles loading, parsing, and validating application configuration
// from environment variables, an optional YAML config file, and built-in defaults.
package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"

	"github.com/emirhanusta/kymo/internal/model"
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

	// File is the absolute path of the config file in use, or "" when the
	// configuration comes from defaults and environment variables only.
	File string `mapstructure:"-"`
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

// Source describes a single collector source (a Kubernetes cluster).
// Multiple sources are supported.
type Source struct {
	// Name is a human-readable identifier used to tag every container, event, and
	// resource sample originating from this source (e.g. "prod-k8s").
	Name string `mapstructure:"name"`

	// Type is the runtime type; only "kubernetes" is supported.
	Type string `mapstructure:"type"`

	// KubeconfigPath is the path to a kubeconfig file used when Type == "kubernetes".
	// Leave empty to fall back to in-cluster credentials then ~/.kube/config.
	KubeconfigPath string `mapstructure:"kubeconfig_path"`

	// KubeContext selects a named context from the kubeconfig file.
	// Leave empty to use the current-context from the file.
	KubeContext string `mapstructure:"kube_context"`

	// PollInterval overrides the global stats poll interval for this source.
	// Zero means use the global default.
	PollInterval time.Duration `mapstructure:"poll_interval"`

	// ReadOnly disables management actions (delete pod, scale,
	// rollout restart) for this source.
	ReadOnly bool `mapstructure:"read_only"`
}

// CollectorConfig controls which runtime collectors are enabled and how they behave.
type CollectorConfig struct {
	// Sources is the list of collector sources to activate. Each source maps to
	// one collector instance (Kubernetes cluster).
	// If empty, KYMO starts with no sources; they can be added from the UI.
	Sources []Source `mapstructure:"sources"`

	// StatsPollInterval is the default interval at which collectors sample
	// container resource usage. Individual sources can override this.
	StatsPollInterval time.Duration `mapstructure:"stats_poll_interval"`

	// ScoringInterval controls how often the stability scorer re-evaluates all containers.
	ScoringInterval time.Duration `mapstructure:"scoring_interval"`

	// ScoringWindow is the look-back duration used by the scorer when querying events
	// and resource samples (e.g. "24h" means the last 24 hours of data).
	ScoringWindow time.Duration `mapstructure:"scoring_window"`

	// CrashLogs controls capturing the tail of a container's logs when it
	// crashes or is OOM-killed.
	CrashLogs CrashLogsConfig `mapstructure:"crash_logs"`
}

// Crash-log capture limits.
const (
	DefaultCrashLogTailLines = 200
	MaxCrashLogTailLines     = 2000
	DefaultCrashLogMaxBytes  = 64 * 1024
	MaxCrashLogMaxBytes      = 1024 * 1024
)

// CrashLogsConfig configures crash-time log snapshots. Logs can contain
// sensitive data; set Enabled to false to turn the feature off.
type CrashLogsConfig struct {
	// Enabled turns snapshot capture on or off (default true).
	Enabled bool `mapstructure:"enabled"`
	// TailLines is how many trailing log lines are fetched (default 200, max 2000).
	TailLines int `mapstructure:"tail_lines"`
	// MaxBytes caps the snapshot size in bytes (default 65536, max 1 MiB).
	MaxBytes int `mapstructure:"max_bytes"`
}

// normalized returns c with out-of-range values replaced by defaults / clamped.
func (c CrashLogsConfig) normalized() CrashLogsConfig {
	if c.TailLines <= 0 {
		c.TailLines = DefaultCrashLogTailLines
	}
	if c.TailLines > MaxCrashLogTailLines {
		c.TailLines = MaxCrashLogTailLines
	}
	if c.MaxBytes <= 0 {
		c.MaxBytes = DefaultCrashLogMaxBytes
	}
	if c.MaxBytes > MaxCrashLogMaxBytes {
		c.MaxBytes = MaxCrashLogMaxBytes
	}
	return c
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
func Load() (*Config, error) { return load("") }

// LoadFile is like Load but reads exactly the given file (it must exist).
// Environment overrides, defaults and validation apply as in Load.
func LoadFile(path string) (*Config, error) {
	if path == "" {
		return nil, fmt.Errorf("config: no config file in use")
	}
	return load(path)
}

// Reload re-reads the config file this Config was loaded from. It returns an
// error when no file is in use or the new content is invalid.
func (c *Config) Reload() (*Config, error) { return LoadFile(c.File) }

// SourceSpecs converts the configured collector sources into source specs
// (origin "config"). Bootstrap and live reload both use it.
func SourceSpecs(cfg *Config) []model.SourceSpec {
	specs := make([]model.SourceSpec, 0, len(cfg.Collector.Sources))
	for _, src := range cfg.Collector.Sources {
		specs = append(specs, model.SourceSpec{
			Name:           src.Name,
			Type:           src.Type,
			Origin:         model.SourceOriginConfig,
			Enabled:        true,
			ReadOnly:       src.ReadOnly,
			KubeconfigPath: src.KubeconfigPath,
			KubeContext:    src.KubeContext,
			PollInterval:   src.PollInterval,
		})
	}
	return specs
}

func load(path string) (*Config, error) {
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
	v.SetDefault("collector.crash_logs.enabled", true)
	v.SetDefault("collector.crash_logs.tail_lines", DefaultCrashLogTailLines)
	v.SetDefault("collector.crash_logs.max_bytes", DefaultCrashLogMaxBytes)

	v.SetDefault("secret_key", "")
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "json")

	// --- Config file (optional) ---
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("config: reading %s: %w", path, err)
		}
	} else {
		v.AddConfigPath(".")
		v.AddConfigPath("/etc/kymo")
		_ = v.ReadInConfig() // not an error if the file is absent
	}

	// --- Environment overrides ---
	v.SetEnvPrefix("KYMO")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	cfg := &Config{}
	if f := v.ConfigFileUsed(); f != "" {
		if abs, err := filepath.Abs(f); err == nil {
			f = abs
		}
		cfg.File = f
	}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("config: unmarshal failed: %w", err)
	}

	cfg.Collector.CrashLogs = cfg.Collector.CrashLogs.normalized()

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
		if s.Type != model.SourceTypeKubernetes {
			return fmt.Errorf("config: source %q has type %q: only %q sources are supported", s.Name, s.Type, model.SourceTypeKubernetes)
		}
	}
	return nil
}
