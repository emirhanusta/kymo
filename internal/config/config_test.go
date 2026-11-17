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
