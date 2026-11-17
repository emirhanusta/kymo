package timescale

import (
	"context"
	"io/fs"
	"sort"
	"testing"
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
