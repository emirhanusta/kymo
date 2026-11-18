package timescale

import (
	"context"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/migrations"
)

func TestMigrateFreshIsIdempotentAndRecordsEveryFile(t *testing.T) {
	db := newIsolatedDB(t)
	ctx := context.Background()

	if err := db.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("fresh migrate: %v", err)
	}

	want, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil || len(want) == 0 {
		t.Fatalf("glob: %v %v", want, err)
	}
	sort.Strings(want)

	snapshot := func() map[string]time.Time {
		rows, err := db.pool.Query(ctx, `SELECT version, applied_at FROM schema_migrations`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		m := map[string]time.Time{}
		for rows.Next() {
			var v string
			var ts time.Time
			if err := rows.Scan(&v, &ts); err != nil {
				t.Fatal(err)
			}
			m[v] = ts
		}
		return m
	}

	first := snapshot()
	if len(first) != len(want) {
		t.Fatalf("schema_migrations has %d rows, want %d", len(first), len(want))
	}
	for _, f := range want {
		if _, ok := first[f]; !ok {
			t.Errorf("migration %s not recorded", f)
		}
	}

	// A second run must be a no-op: nothing re-applied, nothing re-stamped.
	if err := db.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	second := snapshot()
	if len(second) != len(first) {
		t.Fatalf("second migrate changed row count: %d -> %d", len(first), len(second))
	}
	for v, ts := range first {
		if !second[v].Equal(ts) {
			t.Errorf("%s applied_at changed on re-run", v)
		}
	}

	// Core tables exist and are usable.
	for _, tbl := range []string{"containers", "lifecycle_events", "resource_samples", "stability_scores",
		"workload_scores", "cronjob_executions", "sources", "crash_logs"} {
		var reg *string
		if err := db.pool.QueryRow(ctx, `SELECT to_regclass($1)::text`, tbl).Scan(&reg); err != nil || reg == nil {
			t.Errorf("table %s missing (err=%v)", tbl, err)
		}
	}
}

func TestMigrateReappliesWhenVersionRowMissing(t *testing.T) {
	// Shipped migrations are documented as idempotent: forgetting the
	// bookkeeping rows must not break a re-run.
	db := newIsolatedDB(t)
	ctx := context.Background()
	if err := db.Migrate(ctx, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(ctx, `DELETE FROM schema_migrations`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("re-migrate over existing schema: %v", err)
	}
}

func TestNewRejectsBadDSN(t *testing.T) {
	if _, err := New(context.Background(), "not a dsn ::", 0, zap.NewNop()); err == nil {
		t.Fatal("expected error")
	}
}

// TestMigration015BackfillsUsernames migrates a database up to 014 with legacy
// users, applies 015 and checks the backfill, the new constraints and that the
// migration can be applied twice.
func TestMigration015BackfillsUsernames(t *testing.T) {
	db := newIsolatedDB(t)
	ctx := context.Background()

	// Migrate only up to 014 (the pre-username schema).
	old := fstest.MapFS{}
	all, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range all {
		if f >= "015" {
			continue
		}
		b, _ := fs.ReadFile(migrations.FS, f)
		old[f] = &fstest.MapFile{Data: b}
	}
	if err := db.Migrate(ctx, old); err != nil {
		t.Fatalf("migrate to 014: %v", err)
	}

	legacy := []string{"alice@example.com", "Alice.Smith@corp.io", "alice@other.org", "alice@third.net", "bob+tag@example.com", "j@example.com", "a.b@x.io", ".dot@x.io", "ünï@x.io"}
	for _, e := range legacy {
		// The legacy schema forces lower-case emails.
		if _, err := db.pool.Exec(ctx, `INSERT INTO users (email, name) VALUES ($1, '')`, strings.ToLower(e)); err != nil {
			t.Fatalf("legacy insert %s: %v", e, err)
		}
	}
	if err := db.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("migrate 015: %v", err)
	}

	got := map[string]string{} // email -> username
	rows, err := db.pool.Query(ctx, `SELECT email, username FROM users ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var e, u string
		if err := rows.Scan(&e, &u); err != nil {
			t.Fatal(err)
		}
		got[e] = u
	}
	rows.Close()
	want := map[string]string{
		"alice@example.com":   "alice",
		"alice.smith@corp.io": "alice.smith",
		"alice@other.org":     "alice-2",
		"alice@third.net":     "alice-3",
		"bob+tag@example.com": "bob-tag",
		"j@example.com":       "user", // too short -> fallback
		"a.b@x.io":            "a.b",
		".dot@x.io":           "dot",
		"ünï@x.io":            "n-",
	}
	for e, u := range want {
		if got[e] != u {
			t.Errorf("backfill %s: got %q, want %q", e, got[e], u)
		}
	}
	repo := db.Auth()
	for e, u := range got {
		if _, err := auth015Normalize(u); err != nil {
			t.Errorf("backfilled username %q for %s is invalid", u, e)
		}
	}

	// Existing rows stay usable through the repository.
	if u, err := repo.GetUserByUsername(ctx, "alice-2"); err != nil || u.Email != "alice@other.org" || u.MustChangePassword {
		t.Errorf("get: %+v %v", u, err)
	}

	// Constraints: username NOT NULL + unique, email nullable + partial unique.
	if _, err := db.pool.Exec(ctx, `INSERT INTO users (email) VALUES ('nouser@example.com')`); err == nil {
		t.Error("username must be NOT NULL")
	}
	if _, err := db.pool.Exec(ctx, `INSERT INTO users (username) VALUES ('alice')`); err == nil {
		t.Error("username must be unique")
	}
	if _, err := db.pool.Exec(ctx, `INSERT INTO users (username) VALUES ('noemail-1'), ('noemail-2')`); err != nil {
		t.Errorf("users without email: %v", err)
	}
	if _, err := db.pool.Exec(ctx, `INSERT INTO users (username, email) VALUES ('dupmail', 'alice@example.com')`); err == nil {
		t.Error("email must stay unique when set")
	}
	var legacyUnique int
	_ = db.pool.QueryRow(ctx, `SELECT COUNT(*) FROM pg_constraint WHERE conrelid = 'users'::regclass AND conname = 'users_email_key'`).Scan(&legacyUnique)
	if legacyUnique != 0 {
		t.Error("old users_email_key constraint must be dropped")
	}

	// Applying 015 a second time is a no-op that keeps every username.
	body, _ := fs.ReadFile(migrations.FS, "015_usernames.sql")
	for i, stmt := range splitSQLStatements(string(body)) {
		if _, err := db.pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("re-applying 015 statement %d: %v", i+1, err)
		}
	}
	var u2 string
	if err := db.pool.QueryRow(ctx, `SELECT username FROM users WHERE email = 'alice@third.net'`).Scan(&u2); err != nil || u2 != "alice-3" {
		t.Errorf("after re-apply: %q %v", u2, err)
	}
}

// auth015Normalize mirrors auth.NormalizeUsername without importing the auth
// package into the repository tests.
func auth015Normalize(s string) (string, error) {
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,63}$`).MatchString(s) {
		return "", fmt.Errorf("invalid username %q", s)
	}
	return s, nil
}
