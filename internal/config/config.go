// Package config handles loading, parsing, and validating application configuration
// from environment variables, an optional YAML config file, and built-in defaults.
package config

import (
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
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
	Issues    IssuesConfig    `mapstructure:"issues"`

	// LogCapture configures opt-in error-log capture.
	LogCapture LogCaptureConfig `mapstructure:"log_capture"`

	Notifications NotificationsConfig `mapstructure:"notifications"`

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

	// PublicURL is the externally reachable base URL of the web UI (e.g.
	// "https://kymo.example.com"). Notifications use it to link to workloads.
	PublicURL string `mapstructure:"public_url"`
}

// IssuesConfig tunes the server-side issue engine.
type IssuesConfig struct {
	// EvaluateInterval is how often all workloads are evaluated (default 30s).
	EvaluateInterval time.Duration `mapstructure:"evaluate_interval"`
	// DegradedAfter is how long a workload must stay partially ready before a
	// "degraded" issue opens (default 3m).
	DegradedAfter time.Duration `mapstructure:"degraded_after"`
	// ResolveAfter is how long a problem must be gone before its issue resolves (default 5m).
	ResolveAfter time.Duration `mapstructure:"resolve_after"`
	// ReopenWindow is how long after resolving a returning problem reopens the
	// same issue as a flap instead of opening a new one (default 15m).
	ReopenWindow time.Duration `mapstructure:"reopen_window"`
	// IncludeSystem also evaluates system workloads (kube-system, …).
	IncludeSystem bool `mapstructure:"include_system"`
	// ErrorRateThreshold is the number of captured error log lines in 5 minutes
	// at which an "error_rate" issue opens (default 20).
	ErrorRateThreshold int `mapstructure:"error_rate_threshold"`
}

// Defaults of log_capture.
const (
	DefaultLogCaptureMaxStreams = 50
	DefaultLogCaptureMaxLines   = 600
	DefaultLogCaptureSamples    = 50
	DefaultLogCaptureRetention  = "7d"
	DefaultErrorRateThreshold   = 20
)

// LogCaptureConfig configures opt-in error-log capture: for opted-in workloads
// KYMO follows container logs and keeps error lines grouped into signatures.
// Logs can contain sensitive data; masking is best-effort.
type LogCaptureConfig struct {
	// Enabled is the global kill switch (default true). Nothing is captured
	// unless a workload / namespace is opted in as well.
	Enabled bool `mapstructure:"enabled"`
	// MaxStreamsPerSource caps concurrently followed containers per source (default 50).
	MaxStreamsPerSource int `mapstructure:"max_streams_per_source"`
	// MaxLinesPerMinute caps captured error entries per container and minute (default 600).
	MaxLinesPerMinute int `mapstructure:"max_lines_per_minute"`
	// SamplesPerSignature is how many newest occurrences are kept per signature (default 50).
	SamplesPerSignature int `mapstructure:"samples_per_signature"`
	// Retention is how long occurrences and counts are kept: "7d", "48h" (default 7d).
	// Applied at startup.
	Retention string `mapstructure:"retention"`
	// ExtraPatterns are regexes that additionally mark a line as an error.
	ExtraPatterns []string `mapstructure:"extra_patterns"`
	// IgnorePatterns are regexes that discard a line.
	IgnorePatterns []string `mapstructure:"ignore_patterns"`
	// Targets are opt-ins declared in the file (read-only in the UI).
	Targets []LogCaptureTarget `mapstructure:"targets"`
}

// LogCaptureTarget is a target declared in config.yaml.
type LogCaptureTarget struct {
	Source    string `mapstructure:"source"`
	Namespace string `mapstructure:"namespace"`
	// Workload is "Kind/name" (e.g. "Deployment/worker"); empty = whole namespace.
	Workload string `mapstructure:"workload"`
}

// WorkloadID returns the workload id of the target ("" for a namespace target).
func (t LogCaptureTarget) WorkloadID() string {
	kind, name, ok := strings.Cut(t.Workload, "/")
	if !ok {
		return ""
	}
	return model.WorkloadID(t.Source, t.Namespace, kind, name)
}

// RetentionDuration parses Retention ("7d", "48h", "90m").
func (c LogCaptureConfig) RetentionDuration() (time.Duration, error) {
	return ParseRetention(c.Retention)
}

// ParseRetention parses a duration that also accepts a "d" (days) suffix.
func ParseRetention(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		s = DefaultLogCaptureRetention
	}
	if n, ok := strings.CutSuffix(s, "d"); ok {
		var days int
		if _, err := fmt.Sscanf(n, "%d", &days); err != nil || fmt.Sprint(days) != n {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

// TargetSpecs converts the declared targets into model targets (origin config).
func (c LogCaptureConfig) TargetSpecs() []*model.LogCaptureTarget {
	out := make([]*model.LogCaptureTarget, 0, len(c.Targets))
	for _, t := range c.Targets {
		out = append(out, &model.LogCaptureTarget{
			SourceName: t.Source, Namespace: t.Namespace, WorkloadID: t.WorkloadID(), Origin: model.TargetOriginConfig,
		})
	}
	return out
}

// NotificationsConfig configures issue notifications.
type NotificationsConfig struct {
	// Enabled turns sending on or off (default true). Channels are still managed.
	Enabled bool `mapstructure:"enabled"`
	// Channels are channels declared in the file: locked in the UI. The URL is
	// stored in plain text in the file, so keep the file private.
	Channels []NotificationChannel `mapstructure:"channels"`
}

// NotificationChannel is a channel declared in config.yaml. Pointer fields are
// optional; omitted ones take the defaults of API-created channels.
type NotificationChannel struct {
	Name           string   `mapstructure:"name"`
	Type           string   `mapstructure:"type"` // slack | discord | webhook
	URL            string   `mapstructure:"url"`
	Secret         string   `mapstructure:"secret"` // webhook HMAC secret
	Enabled        *bool    `mapstructure:"enabled"`
	MinSeverity    string   `mapstructure:"min_severity"` // critical (default) | warning
	Sources        []string `mapstructure:"sources"`
	Namespaces     []string `mapstructure:"namespaces"`
	Types          []string `mapstructure:"types"`
	NotifyResolved *bool    `mapstructure:"notify_resolved"`
	IncludeLogs    *bool    `mapstructure:"include_logs"`
	RepeatMinutes  int      `mapstructure:"repeat_minutes"`
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

	// K8sEvents controls persisting Kubernetes Events (which expire in the
	// cluster after about an hour).
	K8sEvents K8sEventsConfig `mapstructure:"k8s_events"`
}

// DefaultK8sEventNormalReasons are the Normal-type event reasons kept by default.
var DefaultK8sEventNormalReasons = []string{
	"ScalingReplicaSet", "SuccessfulCreate", "SuccessfulDelete", "Killing", "SawCompletedJob",
	"SuccessfulRescale", "Preempted", "Rebooted", "NodeNotReady", "RegisteredNode", "RemovingNode", "Evicted",
}

// K8sEventsConfig configures Kubernetes Event persistence. Warning events are
// always kept; Normal events only when their reason is in NormalReasons.
type K8sEventsConfig struct {
	// Enabled turns event collection on or off (default true).
	Enabled bool `mapstructure:"enabled"`
	// NormalReasons lists the Normal-event reasons to keep.
	NormalReasons []string `mapstructure:"normal_reasons"`
	// ExcludeNamespaces drops events from these namespaces.
	ExcludeNamespaces []string `mapstructure:"exclude_namespaces"`
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
	v.SetDefault("server.public_url", "")

	v.SetDefault("issues.evaluate_interval", "30s")
	v.SetDefault("issues.degraded_after", "3m")
	v.SetDefault("issues.resolve_after", "5m")
	v.SetDefault("issues.reopen_window", "15m")
	v.SetDefault("issues.include_system", false)
	v.SetDefault("issues.error_rate_threshold", DefaultErrorRateThreshold)

	v.SetDefault("log_capture.enabled", true)
	v.SetDefault("log_capture.max_streams_per_source", DefaultLogCaptureMaxStreams)
	v.SetDefault("log_capture.max_lines_per_minute", DefaultLogCaptureMaxLines)
	v.SetDefault("log_capture.samples_per_signature", DefaultLogCaptureSamples)
	v.SetDefault("log_capture.retention", DefaultLogCaptureRetention)
	v.SetDefault("notifications.enabled", true)

	v.SetDefault("database.dsn", "postgres://kymo:kymo@localhost:5432/kymo?sslmode=disable")
	v.SetDefault("database.max_conns", 20)
	v.SetDefault("database.auto_migrate", true)

	v.SetDefault("collector.stats_poll_interval", "10s")
	v.SetDefault("collector.scoring_interval", "60s")
	v.SetDefault("collector.scoring_window", "24h")
	v.SetDefault("collector.crash_logs.enabled", true)
	v.SetDefault("collector.crash_logs.tail_lines", DefaultCrashLogTailLines)
	v.SetDefault("collector.crash_logs.max_bytes", DefaultCrashLogMaxBytes)

	v.SetDefault("collector.k8s_events.enabled", true)
	v.SetDefault("collector.k8s_events.normal_reasons", DefaultK8sEventNormalReasons)
	v.SetDefault("collector.k8s_events.exclude_namespaces", []string{})

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
	c.Server.PublicURL = strings.TrimRight(strings.TrimSpace(c.Server.PublicURL), "/")
	if c.Server.PublicURL != "" {
		if u, err := url.Parse(c.Server.PublicURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("config: server.public_url %q must be an http(s) URL", c.Server.PublicURL)
		}
	}
	seenCh := make(map[string]bool, len(c.Notifications.Channels))
	for i, ch := range c.Notifications.Channels {
		if ch.Name == "" {
			return fmt.Errorf("config: notifications.channels[%d] has no name", i)
		}
		if seenCh[ch.Name] {
			return fmt.Errorf("config: duplicate notification channel name %q", ch.Name)
		}
		seenCh[ch.Name] = true
		if err := model.ValidateChannelType(ch.Type); err != nil {
			return fmt.Errorf("config: notification channel %q: %w", ch.Name, err)
		}
		if err := model.ValidateChannelURL(ch.Type, ch.URL); err != nil {
			return fmt.Errorf("config: notification channel %q: %w", ch.Name, err)
		}
		if sev := ch.MinSeverity; sev != "" && sev != "critical" && sev != "warning" {
			return fmt.Errorf("config: notification channel %q: min_severity must be critical or warning", ch.Name)
		}
		for _, t := range ch.Types {
			if !model.ValidIssueType(t) {
				return fmt.Errorf("config: notification channel %q: unknown issue type %q", ch.Name, t)
			}
		}
		if ch.RepeatMinutes < 0 {
			return fmt.Errorf("config: notification channel %q: repeat_minutes must be >= 0", ch.Name)
		}
	}
	return c.validateLogCapture()
}

// validateLogCapture checks the log_capture and error-rate settings.
func (c *Config) validateLogCapture() error {
	lc := c.LogCapture
	if lc.MaxStreamsPerSource < 1 || lc.MaxStreamsPerSource > 1000 {
		return fmt.Errorf("config: log_capture.max_streams_per_source must be between 1 and 1000")
	}
	if lc.MaxLinesPerMinute < 1 {
		return fmt.Errorf("config: log_capture.max_lines_per_minute must be >= 1")
	}
	if lc.SamplesPerSignature < 1 || lc.SamplesPerSignature > 1000 {
		return fmt.Errorf("config: log_capture.samples_per_signature must be between 1 and 1000")
	}
	d, err := lc.RetentionDuration()
	if err != nil || d < time.Hour || d > 365*24*time.Hour {
		return fmt.Errorf("config: log_capture.retention %q must be a duration between 1h and 365d (e.g. 7d)", lc.Retention)
	}
	for _, p := range lc.ExtraPatterns {
		if _, err := regexp.Compile(p); err != nil {
			return fmt.Errorf("config: log_capture.extra_patterns %q: %w", p, err)
		}
	}
	for _, p := range lc.IgnorePatterns {
		if _, err := regexp.Compile(p); err != nil {
			return fmt.Errorf("config: log_capture.ignore_patterns %q: %w", p, err)
		}
	}
	seen := map[string]bool{}
	for i, t := range lc.Targets {
		if t.Source == "" || t.Namespace == "" {
			return fmt.Errorf("config: log_capture.targets[%d] needs source and namespace", i)
		}
		if t.Workload != "" {
			kind, name, ok := strings.Cut(t.Workload, "/")
			if !ok || kind == "" || name == "" || strings.Contains(t.Workload, ":") {
				return fmt.Errorf("config: log_capture.targets[%d]: workload %q must look like Deployment/name", i, t.Workload)
			}
		}
		key := t.Source + "\x00" + t.Namespace + "\x00" + t.Workload
		if seen[key] {
			return fmt.Errorf("config: duplicate log_capture target %s/%s %s", t.Source, t.Namespace, t.Workload)
		}
		seen[key] = true
	}
	if c.Issues.ErrorRateThreshold < 1 {
		return fmt.Errorf("config: issues.error_rate_threshold must be >= 1")
	}
	return nil
}
