package timescale

import (
	"context"
	"testing"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

func TestContainerUpsertInsert(t *testing.T) {
	db := reset(t)
	ctx := context.Background()

	c := newContainer("ns/pod-a/app")
	c.StartCount, c.CrashCount, c.OOMCount = 7, 8, 9 // must be ignored on insert
	c.Revision = "rev-1"
	got, err := db.Containers().Upsert(ctx, c, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.StartCount != 0 || got.CrashCount != 0 || got.OOMCount != 0 {
		t.Errorf("counters = %d/%d/%d, want 0/0/0", got.StartCount, got.CrashCount, got.OOMCount)
	}
	wantWL := model.WorkloadID("src1", "ns", "Deployment", "web")
	if wantWL == "" || got.WorkloadID != wantWL {
		t.Errorf("workload_id = %q, want %q", got.WorkloadID, wantWL)
	}
	if got.Name != c.Name || got.Image != c.Image || got.Namespace != "ns" || got.NodeName != "node-1" ||
		got.SourceName != "src1" || got.Runtime != model.RuntimeKubernetes || got.Revision != "rev-1" ||
		got.CurrentStatus != model.StatusRunning || got.DeletedAt != nil || got.Ignored || got.Score != nil {
		t.Errorf("unexpected row: %+v", got)
	}
	if !got.FirstSeenAt.Equal(base) || !got.LastSeenAt.Equal(base) {
		t.Errorf("timestamps = %v / %v", got.FirstSeenAt, got.LastSeenAt)
	}

	// Unknown workload kind / empty name: kind stored as 'unknown', no workload id.
	bare := newContainer("ns/bare/app")
	bare.WorkloadKind, bare.WorkloadName = "", ""
	bare.CurrentStatus = ""
	g2 := mustUpsert(t, db, bare)
	if g2.WorkloadKind != model.WorkloadUnknown || g2.WorkloadID != "" || g2.CurrentStatus != model.StatusUnknown {
		t.Errorf("bare container: kind=%q wid=%q status=%q", g2.WorkloadKind, g2.WorkloadID, g2.CurrentStatus)
	}
}

func TestContainerUpsertConflict(t *testing.T) {
	ctx := context.Background()
	const id = "ns/pod-a/app"

	seed := func(t *testing.T) *DB {
		db := reset(t)
		c := newContainer(id)
		c.Revision = "rev-1"
		mustUpsert(t, db, c)
		return db
	}

	t.Run("never overwrites counters", func(t *testing.T) {
		db := seed(t)
		if err := db.Containers().IncrementCounters(ctx, id, 3, 2, 1); err != nil {
			t.Fatal(err)
		}
		again := newContainer(id)
		again.StartCount, again.CrashCount, again.OOMCount = 0, 0, 0
		got := mustUpsert(t, db, again)
		if got.StartCount != 3 || got.CrashCount != 2 || got.OOMCount != 1 {
			t.Errorf("counters = %d/%d/%d, want 3/2/1", got.StartCount, got.CrashCount, got.OOMCount)
		}
	})

	t.Run("empty fields do not clobber", func(t *testing.T) {
		db := seed(t)
		empty := &model.Container{ID: id, Runtime: model.RuntimeKubernetes, LastSeenAt: at(time.Minute),
			CurrentStatus: model.StatusPaused}
		got := mustUpsert(t, db, empty)
		orig := newContainer(id)
		if got.Name != orig.Name || got.Image != orig.Image || got.SourceName != orig.SourceName ||
			got.Namespace != orig.Namespace || got.NodeName != orig.NodeName ||
			got.WorkloadKind != orig.WorkloadKind || got.WorkloadName != orig.WorkloadName ||
			got.WorkloadID != model.WorkloadID("src1", "ns", "Deployment", "web") || got.Revision != "rev-1" {
			t.Errorf("identity clobbered: %+v", got)
		}
		if got.CurrentStatus != model.StatusPaused {
			t.Errorf("status = %s, want paused (always updated)", got.CurrentStatus)
		}
	})

	t.Run("non empty fields overwrite", func(t *testing.T) {
		db := seed(t)
		n := newContainer(id)
		n.Name, n.Image, n.NodeName, n.Revision = "renamed", "img:2", "node-2", "rev-2"
		n.WorkloadName = "api"
		got := mustUpsert(t, db, n)
		if got.Name != "renamed" || got.Image != "img:2" || got.NodeName != "node-2" ||
			got.Revision != "rev-2" || got.WorkloadName != "api" ||
			got.WorkloadID != model.WorkloadID("src1", "ns", "Deployment", "api") {
			t.Errorf("not updated: %+v", got)
		}
	})

	t.Run("last_seen only moves forward", func(t *testing.T) {
		db := seed(t)
		older := newContainer(id)
		older.LastSeenAt = at(-time.Hour)
		if got := mustUpsert(t, db, older); !got.LastSeenAt.Equal(base) {
			t.Errorf("moved backwards to %v", got.LastSeenAt)
		}
		newer := newContainer(id)
		newer.LastSeenAt = at(time.Hour)
		if got := mustUpsert(t, db, newer); !got.LastSeenAt.Equal(at(time.Hour)) {
			t.Errorf("did not advance: %v", got.LastSeenAt)
		}
		if got := mustFind(t, db, id); !got.FirstSeenAt.Equal(base) {
			t.Errorf("first_seen_at changed: %v", got.FirstSeenAt)
		}
	})

	t.Run("is_system is sticky", func(t *testing.T) {
		db := seed(t)
		s := newContainer(id)
		s.IsSystem = true
		mustUpsert(t, db, s)
		if got := mustUpsert(t, db, newContainer(id)); !got.IsSystem {
			t.Error("is_system cleared by later upsert")
		}
	})

	t.Run("restore flag", func(t *testing.T) {
		db := seed(t)
		if err := db.Containers().SoftDelete(ctx, id); err != nil {
			t.Fatal(err)
		}
		if got, err := db.Containers().Upsert(ctx, newContainer(id), false); err != nil || got.DeletedAt == nil {
			t.Fatalf("restore=false must keep deleted_at (got %+v, err %v)", got, err)
		}
		got, err := db.Containers().Upsert(ctx, newContainer(id), true)
		if err != nil || got.DeletedAt != nil {
			t.Fatalf("restore=true must clear deleted_at (got %+v, err %v)", got, err)
		}
		if mustFind(t, db, id) == nil {
			t.Fatal("not active again")
		}
	})

	t.Run("restore does not un-ignore", func(t *testing.T) {
		db := seed(t)
		if err := db.Containers().Ignore(ctx, id); err != nil {
			t.Fatal(err)
		}
		got, err := db.Containers().Upsert(ctx, newContainer(id), true)
		if err != nil || !got.Ignored {
			t.Fatalf("ignored flag must survive upsert (got %+v, err %v)", got, err)
		}
	})
}

func TestContainerUpdateState(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	mustUpsert(t, db, newContainer("ns/p/a"))

	ok, err := db.Containers().UpdateState(ctx, &model.Container{ID: "ns/p/a", CurrentStatus: model.StatusCrashing, LastSeenAt: at(time.Minute)})
	if err != nil || !ok {
		t.Fatalf("existing: ok=%v err=%v", ok, err)
	}
	got := mustFind(t, db, "ns/p/a")
	if got.CurrentStatus != model.StatusCrashing || !got.LastSeenAt.Equal(at(time.Minute)) {
		t.Errorf("state = %s / %v", got.CurrentStatus, got.LastSeenAt)
	}

	// Older timestamp must not move last_seen_at backwards.
	if _, err := db.Containers().UpdateState(ctx, &model.Container{ID: "ns/p/a", CurrentStatus: model.StatusRunning, LastSeenAt: base}); err != nil {
		t.Fatal(err)
	}
	got = mustFind(t, db, "ns/p/a")
	if got.CurrentStatus != model.StatusRunning || !got.LastSeenAt.Equal(at(time.Minute)) {
		t.Errorf("state = %s / %v", got.CurrentStatus, got.LastSeenAt)
	}

	ok, err = db.Containers().UpdateState(ctx, &model.Container{ID: "missing", CurrentStatus: model.StatusRunning})
	if err != nil || ok {
		t.Errorf("missing: ok=%v err=%v, want false/nil", ok, err)
	}
}

func TestContainerIncrementCounters(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	mustUpsert(t, db, newContainer("ns/p/a"))
	r := db.Containers()

	for _, d := range [][3]int{{1, 0, 0}, {2, 3, 4}, {0, 0, 0}, {0, 1, 0}} {
		if err := r.IncrementCounters(ctx, "ns/p/a", d[0], d[1], d[2]); err != nil {
			t.Fatal(err)
		}
	}
	got := mustFind(t, db, "ns/p/a")
	if got.StartCount != 3 || got.CrashCount != 4 || got.OOMCount != 4 {
		t.Errorf("counters = %d/%d/%d, want 3/4/4", got.StartCount, got.CrashCount, got.OOMCount)
	}
	if err := r.IncrementCounters(ctx, "missing", 1, 1, 1); err != nil {
		t.Errorf("missing id should be a silent no-op, got %v", err)
	}
}

func TestContainerTouch(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	mustUpsert(t, db, newContainer("ns/p/a"))
	r := db.Containers()

	if ok, err := r.Touch(ctx, "ns/p/a", at(time.Hour)); err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got := mustFind(t, db, "ns/p/a"); !got.LastSeenAt.Equal(at(time.Hour)) {
		t.Errorf("last_seen = %v", got.LastSeenAt)
	}
	if _, err := r.Touch(ctx, "ns/p/a", at(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := mustFind(t, db, "ns/p/a"); !got.LastSeenAt.Equal(at(time.Hour)) {
		t.Errorf("touch moved backwards: %v", got.LastSeenAt)
	}
	if ok, err := r.Touch(ctx, "missing", base); err != nil || ok {
		t.Errorf("missing: ok=%v err=%v", ok, err)
	}
}

func TestContainerMarkMissingStopped(t *testing.T) {
	ctx := context.Background()
	seed := func(t *testing.T) *DB {
		db := reset(t)
		for id, st := range map[string]model.ContainerStatus{
			"ns/p/running": model.StatusRunning, "ns/p/paused": model.StatusPaused,
			"ns/p/crashing": model.StatusCrashing, "ns/p/unknown": model.StatusUnknown,
			"ns/p/stopped": model.StatusStopped, "ns/p/present": model.StatusRunning,
		} {
			c := newContainer(id)
			c.CurrentStatus = st
			mustUpsert(t, db, c)
		}
		other := newContainer("ns/p/other-src")
		other.SourceName = "src2"
		mustUpsert(t, db, other)
		return db
	}
	status := func(t *testing.T, db *DB, id string) model.ContainerStatus { return mustFind(t, db, id).CurrentStatus }

	t.Run("keeps present ids and other sources", func(t *testing.T) {
		db := seed(t)
		n, err := db.Containers().MarkMissingStopped(ctx, "src1", []string{"ns/p/present"})
		if err != nil {
			t.Fatal(err)
		}
		if n != 4 { // running, paused, crashing, unknown — not the already stopped one
			t.Errorf("changed %d rows, want 4", n)
		}
		for _, id := range []string{"ns/p/running", "ns/p/paused", "ns/p/crashing", "ns/p/unknown", "ns/p/stopped"} {
			if s := status(t, db, id); s != model.StatusStopped {
				t.Errorf("%s = %s, want stopped", id, s)
			}
		}
		if s := status(t, db, "ns/p/present"); s != model.StatusRunning {
			t.Errorf("present = %s", s)
		}
		if s := status(t, db, "ns/p/other-src"); s != model.StatusRunning {
			t.Errorf("other source touched: %s", s)
		}
	})

	t.Run("nil present list stops everything active", func(t *testing.T) {
		db := seed(t)
		n, err := db.Containers().MarkMissingStopped(ctx, "src1", nil)
		if err != nil || n != 5 {
			t.Fatalf("n=%d err=%v, want 5", n, err)
		}
		if s := status(t, db, "ns/p/other-src"); s != model.StatusRunning {
			t.Errorf("other source touched: %s", s)
		}
	})
}

// seedFilterSet creates the containers used by the filter / paging tests.
//
//	a  src1 running  alpha-web  prod/Deployment web     seen +10m
//	b  src1 crashing betadb     prod/StatefulSet db     seen +20m
//	c  src2 running  100%_legit dev/Deployment api      seen +30m
//	d  src2 stopped  100xlegit  dev/Job batch           seen +5m
//	e  archived, f ignored (never listed)
func seedFilterSet(t *testing.T, db *DB) {
	t.Helper()
	mk := func(id, src string, st model.ContainerStatus, name, image, ns, kind, wl string, seen time.Duration) {
		c := newContainer(id)
		c.SourceName, c.CurrentStatus, c.Name, c.Image, c.Namespace = src, st, name, image, ns
		c.WorkloadKind, c.WorkloadName, c.LastSeenAt = kind, wl, at(seen)
		mustUpsert(t, db, c)
	}
	mk("a", "src1", model.StatusRunning, "alpha-web", "nginx:1", "prod", "Deployment", "web", 10*time.Minute)
	mk("b", "src1", model.StatusCrashing, "betadb", "postgres:16", "prod", "StatefulSet", "db", 20*time.Minute)
	mk("c", "src2", model.StatusRunning, "100%_legit", "x:1", "dev", "Deployment", "api", 30*time.Minute)
	mk("d", "src2", model.StatusStopped, "100xlegit", "y:1", "dev", "Job", "batch", 5*time.Minute)
	mk("e", "src1", model.StatusRunning, "archived", "z", "prod", "Deployment", "web", 40*time.Minute)
	mk("f", "src1", model.StatusRunning, "ignored", "z", "prod", "Deployment", "web", 50*time.Minute)
	ctx := context.Background()
	if err := db.Containers().SoftDelete(ctx, "e"); err != nil {
		t.Fatal(err)
	}
	if err := db.Containers().Ignore(ctx, "f"); err != nil {
		t.Fatal(err)
	}
}

func TestContainerFindAllAndPageFilters(t *testing.T) {
	db := reset(t)
	seedFilterSet(t, db)
	ctx := context.Background()
	webID := model.WorkloadID("src1", "prod", "Deployment", "web")

	tests := []struct {
		name string
		f    repository.ContainerFilter
		want []string // FindAll order: last_seen_at DESC
	}{
		{"no filter excludes archived and ignored", repository.ContainerFilter{}, []string{"c", "b", "a", "d"}},
		{"runtime", repository.ContainerFilter{Runtime: model.RuntimeKubernetes}, []string{"c", "b", "a", "d"}},
		{"runtime no match", repository.ContainerFilter{Runtime: "docker"}, nil},
		{"source", repository.ContainerFilter{Source: "src1"}, []string{"b", "a"}},
		{"status", repository.ContainerFilter{Status: model.StatusCrashing}, []string{"b"}},
		{"workload kind", repository.ContainerFilter{WorkloadKind: "Job"}, []string{"d"}},
		{"workload name", repository.ContainerFilter{Workload: "web"}, []string{"a"}},
		{"workload id", repository.ContainerFilter{WorkloadID: webID}, []string{"a"}},
		{"seen since inclusive", repository.ContainerFilter{SeenSince: at(10 * time.Minute)}, []string{"c", "b", "a"}},
		{"seen since", repository.ContainerFilter{SeenSince: at(15 * time.Minute)}, []string{"c", "b"}},
		{"query name case-insensitive", repository.ContainerFilter{Query: "ALPHA"}, []string{"a"}},
		{"query trims space", repository.ContainerFilter{Query: "  alpha  "}, []string{"a"}},
		{"query image", repository.ContainerFilter{Query: "postgres"}, []string{"b"}},
		{"query namespace", repository.ContainerFilter{Query: "dev"}, []string{"c", "d"}},
		{"query workload name", repository.ContainerFilter{Query: "batch"}, []string{"d"}},
		{"query percent is literal", repository.ContainerFilter{Query: "%"}, []string{"c"}},
		{"query underscore is literal", repository.ContainerFilter{Query: "_"}, []string{"c"}},
		{"query 100% only matches literal", repository.ContainerFilter{Query: "100%"}, []string{"c"}},
		{"query backslash is literal", repository.ContainerFilter{Query: `\`}, nil},
		{"combined", repository.ContainerFilter{Source: "src2", Status: model.StatusRunning, Query: "legit"}, []string{"c"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			all, err := db.Containers().FindAll(ctx, tc.f)
			if err != nil {
				t.Fatal(err)
			}
			if !sameStrings(ids(all), tc.want) {
				t.Errorf("FindAll = %s, want %s", joined(ids(all)), joined(tc.want))
			}
			page, total, err := db.Containers().FindPage(ctx, tc.f, 100, 0, "last_seen_at", "desc")
			if err != nil {
				t.Fatal(err)
			}
			if !sameStrings(ids(page), tc.want) || total != int64(len(tc.want)) {
				t.Errorf("FindPage = %s total=%d, want %s total=%d", joined(ids(page)), total, joined(tc.want), len(tc.want))
			}
		})
	}
}

func TestContainerFindPageSortingAndPaging(t *testing.T) {
	db := reset(t)
	seedFilterSet(t, db)
	ctx := context.Background()
	r := db.Containers()

	// counters: a -> starts 1 crashes 5 ooms 0 ; b -> starts 4 crashes 2 ooms 3 ; c -> 2/0/1 ; d -> 3/9/2
	for id, d := range map[string][3]int{"a": {1, 5, 0}, "b": {4, 2, 3}, "c": {2, 0, 1}, "d": {3, 9, 2}} {
		if err := r.IncrementCounters(ctx, id, d[0], d[1], d[2]); err != nil {
			t.Fatal(err)
		}
	}
	// scores: a has two (latest 90), b has 40; c and d none.
	sc := db.Scores()
	for _, s := range []*model.StabilityScore{
		{ContainerID: "a", SourceName: "src1", Score: 10, CalculatedAt: at(time.Minute)},
		{ContainerID: "a", SourceName: "src1", Score: 90, CalculatedAt: at(2 * time.Minute)},
		{ContainerID: "b", SourceName: "src1", Score: 40, CalculatedAt: at(time.Minute)},
	} {
		if _, err := sc.Save(ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		field, dir string
		want       []string
	}{
		{"last_seen_at", "desc", []string{"c", "b", "a", "d"}},
		{"last_seen_at", "asc", []string{"d", "a", "b", "c"}},
		{"name", "asc", []string{"c", "d", "a", "b"}}, // "100%_legit" < "100xlegit" < "alpha-web" < "betadb"
		{"name", "desc", []string{"b", "a", "d", "c"}},
		{"crash_count", "desc", []string{"d", "a", "b", "c"}},
		{"start_count", "desc", []string{"b", "d", "c", "a"}},
		{"oom_count", "asc", []string{"a", "c", "d", "b"}},
		{"current_status", "asc", []string{"b", "a", "c", "d"}}, // crashing, running, running, stopped (tie by id)
		{"score", "desc", []string{"a", "b", "c", "d"}},         // NULLS LAST, ties by id
		{"score", "asc", []string{"b", "a", "c", "d"}},          // NULLS LAST even when ascending
		{"bogus; DROP TABLE containers", "sideways", []string{"c", "b", "a", "d"}},
	}
	for _, tc := range tests {
		t.Run(tc.field+"_"+tc.dir, func(t *testing.T) {
			page, total, err := r.FindPage(ctx, repository.ContainerFilter{}, 10, 0, tc.field, tc.dir)
			if err != nil {
				t.Fatal(err)
			}
			if total != 4 || !sameStrings(ids(page), tc.want) {
				t.Errorf("got %s total=%d, want %s total=4", joined(ids(page)), total, joined(tc.want))
			}
		})
	}

	t.Run("limit and offset keep total", func(t *testing.T) {
		page, total, err := r.FindPage(ctx, repository.ContainerFilter{}, 2, 1, "last_seen_at", "desc")
		if err != nil {
			t.Fatal(err)
		}
		if total != 4 || !sameStrings(ids(page), []string{"b", "a"}) {
			t.Errorf("got %s total=%d", joined(ids(page)), total)
		}
		page, total, _ = r.FindPage(ctx, repository.ContainerFilter{}, 2, 10, "last_seen_at", "desc")
		if total != 4 || len(page) != 0 {
			t.Errorf("past the end: %d rows total=%d", len(page), total)
		}
	})

	t.Run("latest score joined", func(t *testing.T) {
		a := mustFind(t, db, "a")
		if a.Score == nil || *a.Score != 90 {
			t.Errorf("a.Score = %v, want 90", a.Score)
		}
		if c := mustFind(t, db, "c"); c.Score != nil {
			t.Errorf("c.Score = %v, want nil", *c.Score)
		}
		all, err := r.FindAll(ctx, repository.ContainerFilter{Query: "betadb"})
		if err != nil || len(all) != 1 || all[0].Score == nil || *all[0].Score != 40 {
			t.Errorf("FindAll score join: %v %v", all, err)
		}
	})
}

func TestContainerFindByIDExcludesArchivedAndIgnored(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	for _, id := range []string{"live", "arch", "ign"} {
		mustUpsert(t, db, newContainer(id))
	}
	r := db.Containers()
	_ = r.SoftDelete(ctx, "arch")
	_ = r.Ignore(ctx, "ign")

	if got, err := r.FindByID(ctx, "live"); err != nil || got == nil {
		t.Errorf("live: %v %v", got, err)
	}
	for _, id := range []string{"arch", "ign", "missing"} {
		if got, err := r.FindByID(ctx, id); err != nil || got != nil {
			t.Errorf("%s: got %v err %v, want nil,nil", id, got, err)
		}
	}
}

func TestContainerArchiveIgnoreRestoreDelete(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	r := db.Containers()
	for _, id := range []string{"a", "b", "c", "d"} {
		c := newContainer(id)
		c.LastSeenAt = at(time.Duration(len(id)+int(id[0]-'a')) * time.Minute)
		mustUpsert(t, db, c)
	}

	// Soft delete: gone from active lists, present in FindDeleted, newest archive first.
	if err := r.SoftDelete(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := r.SoftDelete(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if active, _ := r.FindAll(ctx, repository.ContainerFilter{}); !sameStrings(ids(active), []string{"d", "c"}) {
		t.Errorf("active = %s", joined(ids(active)))
	}
	del, err := r.FindDeleted(ctx, "")
	if err != nil || !sameStrings(ids(del), []string{"b", "a"}) || del[0].DeletedAt == nil {
		t.Errorf("FindDeleted = %s err=%v", joined(ids(del)), err)
	}
	if del, _ := r.FindDeleted(ctx, "nope"); len(del) != 0 {
		t.Errorf("FindDeleted(other source) = %s", joined(ids(del)))
	}
	if del, _ := r.FindDeleted(ctx, "src1"); len(del) != 2 {
		t.Errorf("FindDeleted(src1) = %s", joined(ids(del)))
	}

	// Ignore: IsIgnored, FindIgnored (non-deleted only).
	if err := r.Ignore(ctx, "c"); err != nil {
		t.Fatal(err)
	}
	if err := r.Ignore(ctx, "b"); err != nil { // archived AND ignored
		t.Fatal(err)
	}
	if ig, err := r.IsIgnored(ctx, "c"); err != nil || !ig {
		t.Errorf("IsIgnored(c) = %v %v", ig, err)
	}
	if ig, err := r.IsIgnored(ctx, "d"); err != nil || ig {
		t.Errorf("IsIgnored(d) = %v %v", ig, err)
	}
	if ig, err := r.IsIgnored(ctx, "missing"); err != nil || ig {
		t.Errorf("IsIgnored(missing) = %v %v", ig, err)
	}
	ign, err := r.FindIgnored(ctx, "")
	if err != nil || !sameStrings(ids(ign), []string{"c"}) {
		t.Errorf("FindIgnored = %s err=%v", joined(ids(ign)), err)
	}
	if ign, _ := r.FindIgnored(ctx, "nope"); len(ign) != 0 {
		t.Errorf("FindIgnored(other source) = %s", joined(ids(ign)))
	}

	// Restore clears both flags.
	if err := r.Restore(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if err := r.Restore(ctx, "c"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"b", "c"} {
		if got := mustFind(t, db, id); got.DeletedAt != nil || got.Ignored {
			t.Errorf("%s not restored: %+v", id, got)
		}
	}

	// Hard delete / Delete remove the row.
	if err := r.HardDelete(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(ctx, "c"); err != nil {
		t.Fatal(err)
	}
	if err := r.HardDelete(ctx, "missing"); err != nil {
		t.Errorf("hard delete of missing id: %v", err)
	}
	if n := count(t, db, "containers", ""); n != 2 { // a (archived), d
		t.Errorf("remaining = %d, want 2", n)
	}
}

func TestContainerBulkHardDeleteAndDeleteBySource(t *testing.T) {
	ctx := context.Background()

	// seed: s1: del1, del2 (archived), ign1 (ignored), both (archived+ignored), live1 ; s2: del3, ign2, live2
	seed := func(t *testing.T) *DB {
		db := reset(t)
		for _, x := range []struct{ id, src string }{
			{"del1", "s1"}, {"del2", "s1"}, {"ign1", "s1"}, {"both", "s1"}, {"live1", "s1"},
			{"del3", "s2"}, {"ign2", "s2"}, {"live2", "s2"},
		} {
			c := newContainer(x.id)
			c.SourceName = x.src
			mustUpsert(t, db, c)
		}
		r := db.Containers()
		for _, id := range []string{"del1", "del2", "del3", "both"} {
			_ = r.SoftDelete(ctx, id)
		}
		for _, id := range []string{"ign1", "ign2", "both"} {
			_ = r.Ignore(ctx, id)
		}
		return db
	}
	remaining := func(t *testing.T, db *DB) []string {
		rows, err := db.pool.Query(ctx, `SELECT id FROM containers ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var id string
			_ = rows.Scan(&id)
			out = append(out, id)
		}
		return out
	}

	t.Run("HardDeleteDeleted all sources", func(t *testing.T) {
		db := seed(t)
		n, err := db.Containers().HardDeleteDeleted(ctx, "")
		if err != nil || n != 4 {
			t.Fatalf("n=%d err=%v, want 4", n, err)
		}
		if got := remaining(t, db); !sameStrings(got, []string{"ign1", "ign2", "live1", "live2"}) {
			t.Errorf("remaining = %s", joined(got))
		}
	})
	t.Run("HardDeleteDeleted one source", func(t *testing.T) {
		db := seed(t)
		n, err := db.Containers().HardDeleteDeleted(ctx, "s1")
		if err != nil || n != 3 {
			t.Fatalf("n=%d err=%v, want 3", n, err)
		}
		if count(t, db, "containers", "id = 'del3'") != 1 {
			t.Error("s2 archived container removed")
		}
	})
	t.Run("HardDeleteIgnored skips archived", func(t *testing.T) {
		db := seed(t)
		n, err := db.Containers().HardDeleteIgnored(ctx, "")
		if err != nil || n != 2 {
			t.Fatalf("n=%d err=%v, want 2 (ign1, ign2)", n, err)
		}
		if count(t, db, "containers", "id = 'both'") != 1 {
			t.Error("archived+ignored container must be left to HardDeleteDeleted")
		}
	})
	t.Run("HardDeleteIgnored one source", func(t *testing.T) {
		db := seed(t)
		n, err := db.Containers().HardDeleteIgnored(ctx, "s2")
		if err != nil || n != 1 {
			t.Fatalf("n=%d err=%v, want 1", n, err)
		}
	})
	t.Run("DeleteBySource", func(t *testing.T) {
		db := seed(t)
		n, err := db.Containers().DeleteBySource(ctx, "s1")
		if err != nil || n != 5 {
			t.Fatalf("n=%d err=%v, want 5", n, err)
		}
		if got := remaining(t, db); !sameStrings(got, []string{"del3", "ign2", "live2"}) {
			t.Errorf("remaining = %s", joined(got))
		}
		if n, err := db.Containers().DeleteBySource(ctx, "none"); err != nil || n != 0 {
			t.Errorf("unknown source: %d %v", n, err)
		}
	})
}

// seedChildren attaches one row of every dependent table to the container.
func seedChildren(t *testing.T, db *DB, id string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Events().Save(ctx, &model.LifecycleEvent{ContainerID: id, EventType: model.EventTypeStart, SourceName: "s1", OccurredAt: at(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Resources().Save(ctx, &model.ResourceSample{ContainerID: id, SourceName: "s1", SampledAt: at(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Scores().Save(ctx, &model.StabilityScore{ContainerID: id, SourceName: "s1", Score: 50, CalculatedAt: at(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := db.CrashLogs().Save(ctx, &model.CrashLog{ContainerID: id, SourceName: "s1", OccurredAt: at(time.Minute), Lines: "boom"}); err != nil {
		t.Fatal(err)
	}
}

func TestContainerDeletesCascade(t *testing.T) {
	ctx := context.Background()
	tables := []string{"lifecycle_events", "resource_samples", "stability_scores", "crash_logs"}

	run := func(t *testing.T, name string, del func(r repository.ContainerRepository) error) {
		t.Run(name, func(t *testing.T) {
			db := reset(t)
			for _, id := range []string{"gone", "kept"} {
				c := newContainer(id)
				c.SourceName = "s1"
				if id == "kept" {
					c.SourceName = "s2"
				}
				mustUpsert(t, db, c)
				seedChildren(t, db, id)
			}
			if err := del(db.Containers()); err != nil {
				t.Fatal(err)
			}
			for _, tbl := range tables {
				if n := count(t, db, tbl, "container_id = 'gone'"); n != 0 {
					t.Errorf("%s: %d orphan rows", tbl, n)
				}
				if n := count(t, db, tbl, "container_id = 'kept'"); n != 1 {
					t.Errorf("%s: kept container lost its rows (%d)", tbl, n)
				}
			}
		})
	}
	run(t, "DeleteBySource", func(r repository.ContainerRepository) error { _, err := r.DeleteBySource(ctx, "s1"); return err })
	run(t, "HardDelete", func(r repository.ContainerRepository) error { return r.HardDelete(ctx, "gone") })
	run(t, "Delete", func(r repository.ContainerRepository) error { return r.Delete(ctx, "gone") })
	run(t, "HardDeleteDeleted", func(r repository.ContainerRepository) error {
		if err := r.SoftDelete(ctx, "gone"); err != nil {
			return err
		}
		_, err := r.HardDeleteDeleted(ctx, "")
		return err
	})
	run(t, "HardDeleteIgnored", func(r repository.ContainerRepository) error {
		if err := r.Ignore(ctx, "gone"); err != nil {
			return err
		}
		_, err := r.HardDeleteIgnored(ctx, "")
		return err
	})
}
