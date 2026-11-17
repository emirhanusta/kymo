package timescale

import (
	"context"
	"testing"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
)

func seedSourceData(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	mk := func(id, src, wl string, first, last time.Duration) {
		c := newContainer(id)
		c.SourceName, c.WorkloadName = src, wl
		c.FirstSeenAt, c.LastSeenAt = at(first), at(last)
		mustUpsert(t, db, c)
	}
	// alpha: 3 containers, 2 distinct workloads + one with empty workload_id.
	mk("a1", "alpha", "web", 0, time.Hour)
	mk("a2", "alpha", "web", 10*time.Minute, 2*time.Hour)
	mk("a3", "alpha", "", 20*time.Minute, 30*time.Minute)
	mk("a4", "alpha", "api", 5*time.Minute, 45*time.Minute)
	mk("b1", "beta", "web", 0, 3*time.Hour)

	for i, src := range []string{"cron-only", "cron-only", "beta"} {
		if _, err := db.CronJobs().Save(ctx, &model.CronJobExecution{
			SourceName: src, Namespace: "ns", CronJobName: "job", JobName: "job-" + string(rune('a'+i)),
			PodName: "p", SampledAt: at(time.Duration(i+1) * 10 * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, w := range []struct{ id, src string }{
		{"alpha:ns:Deployment:web", "alpha"}, {"alpha:ns:Deployment:api", "alpha"},
		{"alphabet:ns:Deployment:x", "alphabet"}, {"beta:ns:Deployment:web", "beta"},
	} {
		if _, err := db.pool.Exec(ctx, `INSERT INTO workload_scores (workload_id, source_name, score, calculated_at)
			VALUES ($1,$2,90,$3)`, w.id, w.src, at(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSourceHistory(t *testing.T) {
	db := reset(t)
	seedSourceData(t, db)

	got, err := db.Containers().SourceHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 sources, got %+v", got)
	}
	// Newest last_seen first: beta (3h), alpha (2h), cron-only (cron runs at +10m,+20m).
	if got[0].Name != "beta" || got[1].Name != "alpha" || got[2].Name != "cron-only" {
		t.Fatalf("order: %+v", got)
	}
	a, b, c := got[1], got[0], got[2]
	if a.Containers != 4 || a.Workloads != 2 || a.CronJobRuns != 0 ||
		!a.FirstSeenAt.Equal(at(0)) || !a.LastSeenAt.Equal(at(2*time.Hour)) {
		t.Errorf("alpha: %+v", a)
	}
	if b.Containers != 1 || b.Workloads != 1 || b.CronJobRuns != 1 {
		t.Errorf("beta: %+v", b)
	}
	if c.Containers != 0 || c.Workloads != 0 || c.CronJobRuns != 2 ||
		!c.FirstSeenAt.Equal(at(10*time.Minute)) || !c.LastSeenAt.Equal(at(20*time.Minute)) {
		t.Errorf("cron-only: %+v", c)
	}
}

func TestSourceHistoryEmpty(t *testing.T) {
	db := reset(t)
	got, err := db.Containers().SourceHistory(context.Background())
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestPurgeSourceComplete(t *testing.T) {
	db := reset(t)
	seedSourceData(t, db)
	ctx := context.Background()

	n, err := db.Containers().PurgeSource(ctx, "alpha")
	if err != nil || n != 4 {
		t.Fatalf("purge = %d, %v", n, err)
	}
	if c := count(t, db, "containers", "source_name = 'alpha'"); c != 0 {
		t.Errorf("alpha containers left: %d", c)
	}
	if c := count(t, db, "workload_scores", "source_name = 'alpha' OR workload_id LIKE 'alpha:%'"); c != 0 {
		t.Errorf("alpha workload_scores left: %d", c)
	}
	// Prefix-lookalike and other sources are untouched.
	if c := count(t, db, "workload_scores", ""); c != 2 {
		t.Errorf("workload_scores = %d, want 2 (alphabet, beta)", c)
	}
	if c := count(t, db, "containers", ""); c != 1 {
		t.Errorf("containers = %d, want 1 (beta)", c)
	}
	if c := count(t, db, "cronjob_executions", ""); c != 3 {
		t.Errorf("cronjob_executions = %d, want 3", c)
	}

	// Cron-only source: purge removes its runs, reports 0 containers.
	n, err = db.Containers().PurgeSource(ctx, "cron-only")
	if err != nil || n != 0 {
		t.Fatalf("cron-only purge = %d, %v", n, err)
	}
	if c := count(t, db, "cronjob_executions", "source_name = 'cron-only'"); c != 0 {
		t.Errorf("cron-only runs left: %d", c)
	}
	if c := count(t, db, "cronjob_executions", "source_name = 'beta'"); c != 1 {
		t.Errorf("beta cron runs = %d, want 1", c)
	}
}
