package config

import "testing"

func TestExampleConfigLoads(t *testing.T) {
	cfg, err := LoadFile("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Issues.ResolveAfter.Minutes() != 5 || !cfg.Notifications.Enabled {
		t.Fatalf("%+v", cfg.Issues)
	}
}
