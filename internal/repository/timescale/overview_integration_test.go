package timescale

import (
	"context"
	"testing"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

func TestOverviewActivity(t *testing.T) {
	db := reset(t)
	ctx := context.Background()

	web := newContainer("ns/web-1/app") // src1, Deployment
	mustUpsert(t, db, web)
	other := newContainer("ns/api-1/app")
	other.SourceName, other.WorkloadName = "src2", "api"
	mustUpsert(t, db, other)
	sys := newContainer("kube-system/dns-1/dns")
	sys.IsSystem = true
	mustUpsert(t, db, sys)
	job := newContainer("ns/job-1/run")
	job.WorkloadKind, job.WorkloadName = model.WorkloadCronJob, "nightly"
	mustUpsert(t, db, job)

	// "now" is 10 minutes into the hour at base+3h; a 4h window covers base..base+4h, previous base-4h..base.
	now := at(3*time.Hour + 10*time.Minute)
	saveEvent(t, db, "ns/web-1/app", model.EventTypeStart, "src1", at(10*time.Minute))
	saveEvent(t, db, "ns/web-1/app", model.EventTypeStart, "src1", at(50*time.Minute))
	saveEvent(t, db, "ns/web-1/app", model.EventTypeCrash, "src1", at(90*time.Minute))
	saveEvent(t, db, "ns/web-1/app", model.EventTypeOOM, "src1", at(3*time.Hour+5*time.Minute))
	saveEvent(t, db, "ns/web-1/app", model.EventTypeStop, "src1", at(2*time.Hour)) // not counted
	saveEvent(t, db, "ns/api-1/app", model.EventTypeCrash, "src2", at(2*time.Hour+time.Minute))
	// previous window
	saveEvent(t, db, "ns/web-1/app", model.EventTypeCrash, "src1", at(-30*time.Minute))
	saveEvent(t, db, "ns/web-1/app", model.EventTypeOOM, "src1", at(-3*time.Hour))
	saveEvent(t, db, "ns/web-1/app", model.EventTypeStart, "src1", at(-2*time.Hour)) // starts do not count as previous crashes
	// excluded: system workload, CronJob pod, out of range (too old)
	saveEvent(t, db, "kube-system/dns-1/dns", model.EventTypeCrash, "src1", at(30*time.Minute))
	saveEvent(t, db, "ns/job-1/run", model.EventTypeCrash, "src1", at(30*time.Minute))
	saveEvent(t, db, "ns/web-1/app", model.EventTypeCrash, "src1", at(-5*time.Hour))

	get := func(s repository.OverviewScope) *model.Activity {
		t.Helper()
		a, err := db.Overview().Activity(ctx, repository.ActivityQuery{OverviewScope: s, Now: now, Hours: 4})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}

	a := get(repository.OverviewScope{}) // every cluster
	if len(a.Buckets) != 4 || !a.Buckets[0].Start.Equal(base) || !a.Buckets[3].Start.Equal(base.Add(3*time.Hour)) {
		t.Fatalf("buckets: %+v", a.Buckets)
	}
	want := []model.ActivityBucket{
		{Starts: 2},
		{Crashes: 1},
		{Crashes: 1},
		{OOMs: 1},
	}
	for i, w := range want {
		g := a.Buckets[i]
		if g.Starts != w.Starts || g.Crashes != w.Crashes || g.OOMs != w.OOMs {
			t.Fatalf("bucket %d: %+v want %+v", i, g, w)
		}
	}
	if a.PreviousCrashes != 2 {
		t.Fatalf("previous crashes = %d", a.PreviousCrashes)
	}

	a = get(repository.OverviewScope{Source: "src2"})
	if a.Buckets[2].Crashes != 1 || a.Buckets[0].Starts != 0 || a.PreviousCrashes != 0 {
		t.Fatalf("src2: %+v prev=%d", a.Buckets, a.PreviousCrashes)
	}

	// Registered-clusters restriction; an empty (non-nil) list matches nothing.
	a = get(repository.OverviewScope{Sources: []string{"src2"}})
	if a.Buckets[0].Starts != 0 || a.Buckets[2].Crashes != 1 {
		t.Fatalf("sources=[src2]: %+v", a.Buckets)
	}
	a = get(repository.OverviewScope{Sources: []string{}})
	for _, b := range a.Buckets {
		if b.Starts+b.Crashes+b.OOMs != 0 {
			t.Fatalf("empty sources: %+v", a.Buckets)
		}
	}
}

func seedRev(t *testing.T, db *DB, id, src, name, rev, image string, seen time.Duration) {
	t.Helper()
	c := newContainer(id)
	c.SourceName, c.WorkloadName, c.Revision, c.Image = src, name, rev, image
	c.FirstSeenAt, c.LastSeenAt = at(seen), at(seen)
	mustUpsert(t, db, c)
}

func TestOverviewChanges(t *testing.T) {
	db := reset(t)
	ctx := context.Background()

	// web: r1 -> r2 -> r3 (two changes). Two pods per revision; the revision time is the earliest pod.
	seedRev(t, db, "ns/web-a1/app", "src1", "web", "r1", "web:1", 0)
	seedRev(t, db, "ns/web-a2/app", "src1", "web", "r1", "web:1", time.Minute)
	seedRev(t, db, "ns/web-b1/app", "src1", "web", "r2", "web:2", time.Hour)
	seedRev(t, db, "ns/web-b2/app", "src1", "web", "r2", "web:2", time.Hour+time.Minute)
	seedRev(t, db, "ns/web-c1/app", "src1", "web", "r3", "web:3", 2*time.Hour)
	// api (other cluster): only one revision -> no change.
	seedRev(t, db, "ns/api-a1/app", "src2", "api", "x1", "api:1", 30*time.Minute)
	// worker: rollout in the other cluster.
	seedRev(t, db, "ns/wk-a1/app", "src2", "worker", "w1", "wk:1", 10*time.Minute)
	seedRev(t, db, "ns/wk-b1/app", "src2", "worker", "w2", "wk:2", 90*time.Minute)
	// no revision, CronJob and system workloads are ignored.
	seedRev(t, db, "ns/bare-1/app", "src1", "bare", "", "bare:1", 0)
	seedRev(t, db, "ns/bare-2/app", "src1", "bare", "", "bare:2", time.Hour)
	for _, id := range []string{"ns/cj-1/run", "ns/cj-2/run"} {
		c := newContainer(id)
		c.WorkloadKind, c.WorkloadName, c.Revision, c.Image = model.WorkloadCronJob, "nightly", "job-"+id, "cj:1"
		c.FirstSeenAt, c.LastSeenAt = at(3*time.Hour), at(3*time.Hour)
		mustUpsert(t, db, c)
	}

	got, err := db.Overview().Changes(ctx, repository.ChangesQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	type row struct{ name, rev, prev, img, prevImg string }
	var rows []row
	for _, c := range got {
		rows = append(rows, row{c.Name, c.Revision, c.PreviousRevision, c.Image, c.PreviousImage})
	}
	want := []row{
		{"web", "r3", "r2", "web:3", "web:2"},
		{"worker", "w2", "w1", "wk:2", "wk:1"},
		{"web", "r2", "r1", "web:2", "web:1"},
	}
	if len(rows) != len(want) {
		t.Fatalf("changes: %+v", rows)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Fatalf("row %d: %+v want %+v", i, rows[i], want[i])
		}
	}
	if !got[0].At.Equal(at(2*time.Hour)) || got[0].Kind != "Deployment" || got[0].Namespace != "ns" || got[0].SourceName != "src1" || got[0].WorkloadID == "" {
		t.Fatalf("first: %+v", got[0])
	}
	if !got[2].At.Equal(at(time.Hour)) {
		t.Fatalf("revision time must be its earliest pod: %v", got[2].At)
	}

	// limit and scope
	got, _ = db.Overview().Changes(ctx, repository.ChangesQuery{Limit: 1})
	if len(got) != 1 || got[0].Name != "web" {
		t.Fatalf("limit: %+v", got)
	}
	got, _ = db.Overview().Changes(ctx, repository.ChangesQuery{OverviewScope: repository.OverviewScope{Source: "src2"}, Limit: 10})
	if len(got) != 1 || got[0].Name != "worker" {
		t.Fatalf("src2: %+v", got)
	}
	got, _ = db.Overview().Changes(ctx, repository.ChangesQuery{OverviewScope: repository.OverviewScope{Sources: []string{"src1"}}, Limit: 10})
	if len(got) != 2 {
		t.Fatalf("sources=[src1]: %+v", got)
	}
	got, _ = db.Overview().Changes(ctx, repository.ChangesQuery{OverviewScope: repository.OverviewScope{Sources: []string{}}, Limit: 10})
	if len(got) != 0 {
		t.Fatalf("empty sources: %+v", got)
	}

	// An archived workload (no active instance left) is not listed.
	for _, id := range []string{"ns/wk-a1/app", "ns/wk-b1/app"} {
		if err := db.Containers().SoftDelete(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	got, _ = db.Overview().Changes(ctx, repository.ChangesQuery{OverviewScope: repository.OverviewScope{Source: "src2"}, Limit: 10})
	if len(got) != 0 {
		t.Fatalf("archived: %+v", got)
	}
}
