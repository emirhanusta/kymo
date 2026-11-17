package config

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestWatchRegularFileWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("a: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var n atomic.Int32
	if err := Watch(ctx, path, 20*time.Millisecond, func() { n.Add(1) }); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("a: 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return n.Load() >= 1 })
}

// TestWatchKubernetesConfigMapSwap mimics how the kubelet refreshes a mounted
// ConfigMap: config.yaml -> ..data/config.yaml, and ..data is a symlink to a
// timestamped directory that gets atomically replaced.
func TestWatchKubernetesConfigMapSwap(t *testing.T) {
	dir := t.TempDir()
	mk := func(name, content string) {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name, "config.yaml"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mk("..2024_01_01", "v: 1\n")
	if err := os.Symlink("..2024_01_01", filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.Symlink(filepath.Join("..data", "config.yaml"), path); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var n atomic.Int32
	if err := Watch(ctx, path, 20*time.Millisecond, func() { n.Add(1) }); err != nil {
		t.Fatal(err)
	}

	mk("..2024_01_02", "v: 2\n")
	tmp := filepath.Join(dir, "..data_tmp")
	if err := os.Symlink("..2024_01_02", tmp); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return n.Load() >= 1 })

	got, err := os.ReadFile(path)
	if err != nil || string(got) != "v: 2\n" {
		t.Fatalf("config via symlink = %q, %v", got, err)
	}
}
