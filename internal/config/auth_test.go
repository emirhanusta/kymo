package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func loadAuth(t *testing.T, extra string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeCfg(t, path, validYAML+extra)
	return LoadFile(path)
}

func TestAuthDefaults(t *testing.T) {
	cfg, err := loadAuth(t, "")
	if err != nil {
		t.Fatal(err)
	}
	a := cfg.Auth
	if !a.Enabled || a.SessionIdleTimeout != 7*24*time.Hour || a.SessionMaxAge != 30*24*time.Hour || a.OIDC.Enabled ||
		len(cfg.Server.AllowedOrigins) != 0 {
		t.Fatalf("defaults: %+v origins=%v", a, cfg.Server.AllowedOrigins)
	}
}

func TestAuthEnvOverrides(t *testing.T) {
	t.Setenv("KYMO_AUTH_ENABLED", "false")
	t.Setenv("KYMO_AUTH_OIDC_CLIENT_SECRET", "from-env")
	cfg, err := loadAuth(t, `
server:
  public_url: https://kymo.example.com
auth:
  oidc:
    enabled: false
    issuer: https://issuer.example.com
`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.Enabled || cfg.Auth.OIDC.ClientSecret != "from-env" {
		t.Errorf("%+v", cfg.Auth)
	}
}

func TestOIDCValidation(t *testing.T) {
	oidc := func(body string) string {
		return "server:\n  public_url: https://kymo.example.com\nauth:\n  oidc:\n    enabled: true\n    issuer: https://accounts.google.com\n    client_id: id\n    client_secret: s\n" + body
	}
	cfg, err := loadAuth(t, oidc("    allowed_domains: ['@Example.com']\n    groups_claim: groups\n    role_mapping:\n      Admin: [a]\n      operator: [b]\n"))
	if err != nil {
		t.Fatal(err)
	}
	o := cfg.Auth.OIDC
	if o.DefaultRole != "viewer" || !o.AutoCreate || o.Name != "SSO" || o.AllowedDomains[0] != "example.com" ||
		len(o.RoleMapping["admin"]) != 1 || len(o.Scopes) != 3 {
		t.Errorf("normalized: %+v", o)
	}

	bad := map[string]string{
		"requires public_url": "auth:\n  oidc:\n    enabled: true\n    issuer: https://i.example.com\n    client_id: id\n    client_secret: s\n",
		"missing secret":      "server:\n  public_url: https://k.example.com\nauth:\n  oidc:\n    enabled: true\n    issuer: https://i.example.com\n    client_id: id\n",
		"bad issuer":          "server:\n  public_url: https://k.example.com\nauth:\n  oidc:\n    enabled: true\n    issuer: nope\n    client_id: id\n    client_secret: s\n",
		"owner default":       oidc("    default_role: owner\n"),
		"unknown default":     oidc("    default_role: root\n"),
		"owner mapping":       oidc("    groups_claim: g\n    role_mapping:\n      owner: [x]\n"),
		"mapping w/o claim":   oidc("    role_mapping:\n      admin: [x]\n"),
		"bad origin":          "server:\n  allowed_origins: ['https://x.example.com/path']\n",
		"idle > max":          "auth:\n  session_idle_timeout: 48h\n  session_max_age: 24h\n",
	}
	for name, y := range bad {
		if _, err := loadAuth(t, y); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	_, err = loadAuth(t, bad["requires public_url"])
	if err == nil || !strings.Contains(err.Error(), "public_url") {
		t.Errorf("error must mention public_url: %v", err)
	}
	// A disabled auth section skips OIDC validation.
	if _, err := loadAuth(t, "auth:\n  enabled: false\n  oidc:\n    enabled: true\n"); err != nil {
		t.Errorf("auth disabled: %v", err)
	}
	cfg, err = loadAuth(t, "server:\n  allowed_origins: ['https://x.example.com/']\n")
	if err != nil || cfg.Server.AllowedOrigins[0] != "https://x.example.com" {
		t.Errorf("origins: %v %v", cfg.Server.AllowedOrigins, err)
	}
}
