package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validYAML = "collector:\n  sources:\n    - name: a\n      type: kubernetes\n"

func writeCfg(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeCfg(t, path, validYAML)
	cfg, err := LoadFile(path)
	if err != nil || cfg.File != path {
		t.Fatalf("LoadFile: %+v %v", cfg, err)
	}
	writeCfg(t, path, validYAML+"    - name: a\n      type: kubernetes\n")
	if _, err := cfg.Reload(); err == nil {
		t.Fatal("duplicate source names accepted")
	}
	writeCfg(t, path, validYAML+"    - name: b\n      type: kubernetes\n")
	fresh, err := cfg.Reload()
	if err != nil || len(fresh.Collector.Sources) != 2 {
		t.Fatalf("Reload: %+v %v", fresh, err)
	}
	if specs := SourceSpecs(fresh); len(specs) != 2 || specs[1].Origin != "config" {
		t.Fatalf("SourceSpecs: %+v", specs)
	}
	if _, err := (&Config{}).Reload(); err == nil {
		t.Fatal("reload without a file must fail")
	}
}

func TestWatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeCfg(t, path, validYAML)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fired := make(chan struct{}, 10)
	if err := Watch(ctx, path, 50*time.Millisecond, func() { fired <- struct{}{} }); err != nil {
		t.Fatal(err)
	}
	// Editors replace the file via rename.
	tmp := path + ".tmp"
	writeCfg(t, tmp, validYAML+"    - name: b\n      type: kubernetes\n")
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fired:
	case <-time.After(3 * time.Second):
		t.Fatal("onChange not called after rewrite")
	}

	if err := Watch(ctx, "", time.Millisecond, func() {}); err != nil {
		t.Fatalf("empty path must be a no-op: %v", err)
	}
}

func TestDockerSourceRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeCfg(t, path, "collector:\n  sources:\n    - name: x\n      type: docker\n")
	_, err := LoadFile(path)
	if err == nil || !strings.Contains(err.Error(), `source "x" has type "docker": only "kubernetes" sources are supported`) {
		t.Fatalf("docker source not rejected: %v", err)
	}
}

func TestNoSourcesIsValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeCfg(t, path, "server:\n  address: \":9999\"\n")
	cfg, err := LoadFile(path)
	if err != nil || len(cfg.Collector.Sources) != 0 {
		t.Fatalf("empty sources: %+v %v", cfg, err)
	}
}

func TestCrashLogsNormalized(t *testing.T) {
	got := CrashLogsConfig{Enabled: true, TailLines: 99999, MaxBytes: 0}.normalized()
	if got.TailLines != MaxCrashLogTailLines || got.MaxBytes != DefaultCrashLogMaxBytes {
		t.Fatalf("normalized = %+v", got)
	}
	got = CrashLogsConfig{TailLines: 10, MaxBytes: 1 << 30}.normalized()
	if got.TailLines != 10 || got.MaxBytes != MaxCrashLogMaxBytes {
		t.Fatalf("normalized = %+v", got)
	}
}

func TestK8sEventsDefaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	k := cfg.Collector.K8sEvents
	if !k.Enabled || len(k.NormalReasons) != len(DefaultK8sEventNormalReasons) || len(k.ExcludeNamespaces) != 0 {
		t.Fatalf("defaults: %+v", k)
	}
}

func TestIssuesAndNotificationsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeCfg(t, path, "server:\n  address: \":9999\"\n")
	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	i := cfg.Issues
	if i.EvaluateInterval != 30*time.Second || i.DegradedAfter != 3*time.Minute || i.ResolveAfter != 5*time.Minute ||
		i.ReopenWindow != 15*time.Minute || i.IncludeSystem {
		t.Fatalf("issues defaults: %+v", i)
	}
	if !cfg.Notifications.Enabled || len(cfg.Notifications.Channels) != 0 || cfg.Server.PublicURL != "" {
		t.Fatalf("notification defaults: %+v / %q", cfg.Notifications, cfg.Server.PublicURL)
	}
}

func TestNotificationChannelsFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeCfg(t, path, `
server:
  public_url: "https://kymo.example.com/"
issues:
  evaluate_interval: 10s
  include_system: true
notifications:
  enabled: false
  channels:
    - name: ops
      type: slack
      url: https://hooks.slack.com/services/T/B/X
      min_severity: warning
      sources: [prod]
      repeat_minutes: 30
      notify_resolved: false
    - name: hook
      type: webhook
      url: http://localhost:9000/in
      secret: s
`)
	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.PublicURL != "https://kymo.example.com" || cfg.Issues.EvaluateInterval != 10*time.Second ||
		!cfg.Issues.IncludeSystem || cfg.Notifications.Enabled {
		t.Fatalf("cfg: %+v", cfg)
	}
	ch := cfg.Notifications.Channels
	if len(ch) != 2 || ch[0].MinSeverity != "warning" || ch[0].Sources[0] != "prod" || ch[0].RepeatMinutes != 30 ||
		ch[0].NotifyResolved == nil || *ch[0].NotifyResolved || ch[1].NotifyResolved != nil || ch[1].Secret != "s" {
		t.Fatalf("channels: %+v", ch)
	}
}

func TestNotificationChannelValidation(t *testing.T) {
	for name, body := range map[string]string{
		"no name":    "- type: webhook\n      url: https://example.com/x",
		"bad type":   "- name: a\n      type: email\n      url: https://example.com/x",
		"http":       "- name: a\n      type: webhook\n      url: http://example.com/x",
		"slack host": "- name: a\n      type: slack\n      url: https://example.com/x",
		"severity":   "- name: a\n      type: webhook\n      url: https://example.com/x\n      min_severity: info",
		"issue type": "- name: a\n      type: webhook\n      url: https://example.com/x\n      types: [nope]",
		"dup":        "- name: a\n      type: webhook\n      url: https://example.com/x\n    - name: a\n      type: webhook\n      url: https://example.com/y",
		"neg repeat": "- name: a\n      type: webhook\n      url: https://example.com/x\n      repeat_minutes: -1",
	} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		writeCfg(t, path, "notifications:\n  channels:\n    "+body+"\n")
		if _, err := LoadFile(path); err == nil {
			t.Errorf("%s: invalid channel accepted", name)
		}
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeCfg(t, path, "server:\n  public_url: \"kymo.example.com\"\n")
	if _, err := LoadFile(path); err == nil {
		t.Error("public_url without scheme accepted")
	}
}
