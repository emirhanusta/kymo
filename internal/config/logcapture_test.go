package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLogCaptureDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeCfg(t, path, validYAML)
	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lc := cfg.LogCapture
	if !lc.Enabled || lc.MaxStreamsPerSource != 50 || lc.MaxLinesPerMinute != 600 || lc.SamplesPerSignature != 50 || lc.Retention != "7d" ||
		len(lc.Targets) != 0 || cfg.Issues.ErrorRateThreshold != 20 {
		t.Fatalf("defaults: %+v issues=%+v", lc, cfg.Issues)
	}
	if d, err := lc.RetentionDuration(); err != nil || d != 7*24*time.Hour {
		t.Errorf("retention = %v %v", d, err)
	}
}

func TestLogCaptureParsedAndValidated(t *testing.T) {
	dir := t.TempDir()
	load := func(yaml string) (*Config, error) {
		path := filepath.Join(dir, "config.yaml")
		writeCfg(t, path, validYAML+yaml)
		return LoadFile(path)
	}
	cfg, err := load(`
log_capture:
  enabled: false
  max_streams_per_source: 5
  retention: "48h"
  extra_patterns: ["boom"]
  ignore_patterns: ["healthz"]
  targets:
    - source: kymo-test
      namespace: shop
      workload: Deployment/worker
    - source: kymo-test
      namespace: ops
issues:
  error_rate_threshold: 3
`)
	if err != nil {
		t.Fatal(err)
	}
	lc := cfg.LogCapture
	if lc.Enabled || lc.MaxStreamsPerSource != 5 || lc.MaxLinesPerMinute != 600 || len(lc.ExtraPatterns) != 1 || cfg.Issues.ErrorRateThreshold != 3 {
		t.Fatalf("parsed: %+v", lc)
	}
	if d, _ := lc.RetentionDuration(); d != 48*time.Hour {
		t.Errorf("retention = %v", d)
	}
	specs := lc.TargetSpecs()
	if len(specs) != 2 || specs[0].WorkloadID != "kymo-test:shop:Deployment:worker" || specs[1].WorkloadID != "" || specs[0].Origin != "config" {
		t.Fatalf("specs: %+v %+v", specs[0], specs[1])
	}

	bad := map[string]string{
		"max_streams_per_source: 0":                           "max_streams_per_source",
		"max_lines_per_minute: 0":                             "max_lines_per_minute",
		"samples_per_signature: 5000":                         "samples_per_signature",
		"retention: forever":                                  "retention",
		"retention: 10m":                                      "retention",
		`extra_patterns: ["("]`:                               "extra_patterns",
		`ignore_patterns: ["["]`:                              "ignore_patterns",
		"targets: [{source: a}]":                              "needs source and namespace",
		"targets: [{source: a, namespace: b, workload: web}]": "must look like",
		"targets: [{source: a, namespace: b}, {source: a, namespace: b}]": "duplicate",
	}
	for body, want := range bad {
		_, err := load("log_capture:\n  " + body + "\n")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want error containing %q", body, err, want)
		}
	}
	if _, err := load("issues:\n  error_rate_threshold: 0\n"); err == nil {
		t.Error("threshold 0 accepted")
	}
}

func TestParseRetention(t *testing.T) {
	for in, want := range map[string]time.Duration{"7d": 168 * time.Hour, "1d": 24 * time.Hour, "36h": 36 * time.Hour, "": 168 * time.Hour} {
		if got, err := ParseRetention(in); err != nil || got != want {
			t.Errorf("%q = %v %v", in, got, err)
		}
	}
	for _, in := range []string{"xd", "1.5d", "-"} {
		if _, err := ParseRetention(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}
