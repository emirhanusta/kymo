package timescale

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

func wlNames(ws []*model.WorkloadSummary) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.Name
	}
	return out
}

type wlSeed struct {
	id, src, ns, kind, name, image, cname string
	status                                model.ContainerStatus
	seen                                  time.Duration
	starts, crashes, ooms                 int
	runtime                               model.Runtime
}

func seedWL(t *testing.T, db *DB, s wlSeed) {
	t.Helper()
	c := newContainer(s.id)
	c.SourceName, c.Namespace, c.WorkloadKind, c.WorkloadName = s.src, s.ns, s.kind, s.name
	c.Image, c.Name, c.CurrentStatus = s.image, s.cname, s.status
	c.FirstSeenAt, c.LastSeenAt = at(s.seen-time.Hour), at(s.seen)
	if s.runtime != "" {
		c.Runtime = s.runtime
	}
	mustUpsert(t, db, c)
	if err := db.Containers().IncrementCounters(context.Background(), s.id, s.starts, s.crashes, s.ooms); err != nil {
		t.Fatal(err)
	}
}

func TestWorkloadListGetAggregation(t *testing.T) {
	db := reset(t)
	ctx := context.Background()

	// web: 3 pods. web-1 has two containers (app + sidecar), web-2 crashes, web-3 is stopped.
	seedWL(t, db, wlSeed{id: "prod/web-1/app", src: "s1", ns: "prod", kind: "Deployment", name: "web", image: "app:1", cname: "app", status: model.StatusRunning, seen: 10 * time.Minute, starts: 3, crashes: 1})
	seedWL(t, db, wlSeed{id: "prod/web-1/sidecar", src: "s1", ns: "prod", kind: "Deployment", name: "web", image: "side:1", cname: "sidecar", status: model.StatusRunning, seen: 20 * time.Minute, starts: 1})
	seedWL(t, db, wlSeed{id: "prod/web-2/app", src: "s1", ns: "prod", kind: "Deployment", name: "web", image: "app:2", cname: "app", status: model.StatusCrashing, seen: 15 * time.Minute, starts: 2, crashes: 4, ooms: 1})
	seedWL(t, db, wlSeed{id: "prod/web-3/app", src: "s1", ns: "prod", kind: "Deployment", name: "web", image: "app:0", cname: "app", status: model.StatusStopped, seen: 5 * time.Minute, starts: 1})
	// archived and ignored instances must not count.
	seedWL(t, db, wlSeed{id: "prod/web-4/app", src: "s1", ns: "prod", kind: "Deployment", name: "web", image: "arch:1", cname: "app", status: model.StatusRunning, seen: 50 * time.Minute, starts: 10})
	seedWL(t, db, wlSeed{id: "prod/web-5/app", src: "s1", ns: "prod", kind: "Deployment", name: "web", image: "ign:1", cname: "app", status: model.StatusRunning, seen: 50 * time.Minute, starts: 10})
	_ = db.Containers().SoftDelete(ctx, "prod/web-4/app")
	_ = db.Containers().Ignore(ctx, "prod/web-5/app")

	// status precedence: running (one running, one stopped), unknown (paused only), stopped (all stopped)
	seedWL(t, db, wlSeed{id: "prod/run-1/app", src: "s1", ns: "prod", kind: "StatefulSet", name: "run", image: "r:1", cname: "app", status: model.StatusRunning, seen: time.Minute})
	seedWL(t, db, wlSeed{id: "prod/run-2/app", src: "s1", ns: "prod", kind: "StatefulSet", name: "run", image: "r:2", cname: "app", status: model.StatusStopped, seen: time.Minute})
	seedWL(t, db, wlSeed{id: "prod/unk-1/app", src: "s1", ns: "prod", kind: "DaemonSet", name: "unk", image: "u:1", cname: "app", status: model.StatusPaused, seen: time.Minute})
	seedWL(t, db, wlSeed{id: "prod/unk-2/app", src: "s1", ns: "prod", kind: "DaemonSet", name: "unk", image: "u:1", cname: "app", status: model.StatusUnknown, seen: time.Minute})
	seedWL(t, db, wlSeed{id: "prod/stop-1/app", src: "s1", ns: "prod", kind: "Job", name: "stop", image: "s:1", cname: "app", status: model.StatusStopped, seen: time.Minute})
	// docker-style: instance == container id
	seedWL(t, db, wlSeed{id: "dock-a", src: "s2", ns: "", kind: "Pod", name: "dk", image: "d:1", cname: "a", status: model.StatusRunning, seen: time.Minute, runtime: "docker"})
	seedWL(t, db, wlSeed{id: "dock-b", src: "s2", ns: "", kind: "Pod", name: "dk", image: "d:1", cname: "b", status: model.StatusRunning, seen: time.Minute, runtime: "docker"})
	// no workload (kind unknown) never shows up.
	seedWL(t, db, wlSeed{id: "prod/loose/app", src: "s1", ns: "prod", kind: "unknown", name: "", image: "l:1", cname: "app", status: model.StatusRunning, seen: time.Minute})

	list, err := db.Workloads().List(ctx, repository.WorkloadFilter{})
	if err != nil {
		t.Fatal(err)
	}
	// ORDER BY namespace, name, kind
	if got, want := wlNames(list), []string{"dk", "run", "stop", "unk", "web"}; !sameStrings(got, want) {
		t.Fatalf("workloads = %s, want %s", joined(got), joined(want))
	}
	by := map[string]*model.WorkloadSummary{}
	for _, w := range list {
		by[w.Name] = w
	}

	web := by["web"]
	if web.ID != model.WorkloadID("s1", "prod", "Deployment", "web") || web.SourceName != "s1" || web.Runtime != model.RuntimeKubernetes ||
		web.Namespace != "prod" || web.Kind != "Deployment" {
		t.Errorf("web identity = %+v", web)
	}
	if web.Instances != 3 || web.ActiveInstances != 2 || web.RunningInstances != 1 {
		t.Errorf("web instances/active/running = %d/%d/%d, want 3/2/1", web.Instances, web.ActiveInstances, web.RunningInstances)
	}
	if web.Status != model.StatusCrashing {
		t.Errorf("web status = %s, want crashing", web.Status)
	}
	if web.StartCount != 7 || web.RestartCount != 3 || web.CrashCount != 5 || web.OOMCount != 1 {
		t.Errorf("web start/restart/crash/oom = %d/%d/%d/%d, want 7/3/5/1", web.StartCount, web.RestartCount, web.CrashCount, web.OOMCount)
	}
	if !sameStrings(web.Containers, []string{"app", "sidecar"}) {
		t.Errorf("web containers = %v", web.Containers)
	}
	if !sameStrings(web.Images, []string{"app:1", "app:2", "side:1"}) {
		t.Errorf("web images = %v (stopped/archived/ignored images must be excluded)", web.Images)
	}
	if !web.FirstSeenAt.Equal(at(5*time.Minute-time.Hour)) || !web.LastSeenAt.Equal(at(20*time.Minute)) {
		t.Errorf("web seen = %v .. %v", web.FirstSeenAt, web.LastSeenAt)
	}
	if web.Score != nil {
		t.Errorf("web score = %v, want nil", *web.Score)
	}

	if r := by["run"]; r.Status != model.StatusRunning || r.Instances != 2 || r.ActiveInstances != 1 || r.RunningInstances != 1 {
		t.Errorf("run = %+v", r)
	}
	if u := by["unk"]; u.Status != model.StatusUnknown || u.ActiveInstances != 2 || u.RunningInstances != 0 {
		t.Errorf("unk = %+v", u)
	}
	if s := by["stop"]; s.Status != model.StatusStopped || s.ActiveInstances != 0 || s.Images == nil || len(s.Images) != 0 {
		t.Errorf("stop = %+v (images must be an empty non-nil slice)", s)
	}
	if d := by["dk"]; d.Instances != 2 || d.Runtime != "docker" || d.Namespace != "" {
		t.Errorf("dk = %+v", d)
	}

	// Get
	got, err := db.Workloads().Get(ctx, web.ID)
	if err != nil || !reflect.DeepEqual(got, web) {
		t.Errorf("Get = %+v err=%v, want %+v", got, err, web)
	}
	if _, err := db.Workloads().Get(ctx, "missing"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("Get(missing) err = %v, want ErrNotFound", err)
	}

	// Fully archived workload disappears.
	_ = db.Containers().SoftDelete(ctx, "prod/stop-1/app")
	if _, err := db.Workloads().Get(ctx, by["stop"].ID); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("archived workload still listed: %v", err)
	}
}

func TestWorkloadListFilters(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	mk := func(id, src, ns, kind, name string) {
		seedWL(t, db, wlSeed{id: id, src: src, ns: ns, kind: kind, name: name, image: "i", cname: "c", status: model.StatusRunning, seen: time.Minute})
	}
	mk("prod/a-1/c", "s1", "prod", "Deployment", "alpha")
	mk("dev/b-1/c", "s1", "dev", "StatefulSet", "beta")
	mk("prod/c-1/c", "s2", "prod", "Deployment", "100%_cpu")
	mk("prod/d-1/c", "s2", "prod", "Deployment", "100xcpu")

	tests := []struct {
		name string
		f    repository.WorkloadFilter
		want []string
	}{
		{"none", repository.WorkloadFilter{}, []string{"beta", "100%_cpu", "100xcpu", "alpha"}}, // by namespace (dev, prod), name
		{"source", repository.WorkloadFilter{Source: "s2"}, []string{"100%_cpu", "100xcpu"}},
		{"namespace", repository.WorkloadFilter{Namespace: "dev"}, []string{"beta"}},
		{"kind", repository.WorkloadFilter{Kind: "StatefulSet"}, []string{"beta"}},
		{"query case-insensitive", repository.WorkloadFilter{Query: "ALP"}, []string{"alpha"}},
		{"query namespace", repository.WorkloadFilter{Query: "dev"}, []string{"beta"}},
		{"query percent literal", repository.WorkloadFilter{Query: "%"}, []string{"100%_cpu"}},
		{"query underscore literal", repository.WorkloadFilter{Query: "_"}, []string{"100%_cpu"}},
		{"combined", repository.WorkloadFilter{Source: "s1", Namespace: "prod", Kind: "Deployment", Query: "alp"}, []string{"alpha"}},
		{"no match", repository.WorkloadFilter{Source: "s1", Kind: "Job"}, []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := db.Workloads().List(ctx, tc.f)
			if err != nil {
				t.Fatal(err)
			}
			if !sameStrings(wlNames(got), tc.want) {
				t.Errorf("got %s, want %s", joined(wlNames(got)), joined(tc.want))
			}
		})
	}
}

func TestWorkloadScores(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	seedWL(t, db, wlSeed{id: "prod/a-1/c", src: "s1", ns: "prod", kind: "Deployment", name: "alpha", image: "i", cname: "c", status: model.StatusRunning, seen: time.Minute})
	id := model.WorkloadID("s1", "prod", "Deployment", "alpha")
	r := db.Workloads()

	if got, err := r.LatestScore(ctx, id); err != nil || got != nil {
		t.Fatalf("no scores: %v %v, want nil,nil", got, err)
	}
	if got, err := r.ScoreHistory(ctx, id, 5); err != nil || len(got) != 0 {
		t.Fatalf("no history: %v %v", got, err)
	}

	for i, sc := range []float64{50, 60, 70} {
		s := &model.WorkloadScore{WorkloadID: id, SourceName: "s1", Score: sc, RestartPenalty: 1, CrashPenalty: 2, OOMPenalty: 3,
			VolatilityPenalty: 4, AvailabilityPenalty: 5, Restarts: 6, Crashes: 7, OOMs: 8, CalculatedAt: at(time.Duration(i) * time.Minute)}
		if err := r.SaveScore(ctx, s); err != nil || s.ID == 0 {
			t.Fatalf("save: id=%d err=%v", s.ID, err)
		}
	}
	if err := r.SaveScore(ctx, &model.WorkloadScore{WorkloadID: "other", SourceName: "s1", Score: 1, CalculatedAt: at(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	latest, err := r.LatestScore(ctx, id)
	if err != nil || latest == nil {
		t.Fatal(latest, err)
	}
	want := model.WorkloadScore{ID: latest.ID, WorkloadID: id, SourceName: "s1", Score: 70, RestartPenalty: 1, CrashPenalty: 2, OOMPenalty: 3,
		VolatilityPenalty: 4, AvailabilityPenalty: 5, Restarts: 6, Crashes: 7, OOMs: 8, CalculatedAt: at(2 * time.Minute)}
	if !latest.CalculatedAt.Equal(want.CalculatedAt) {
		t.Errorf("calculated_at = %v", latest.CalculatedAt)
	}
	latest.CalculatedAt = want.CalculatedAt
	if !reflect.DeepEqual(*latest, want) {
		t.Errorf("latest = %+v, want %+v", *latest, want)
	}

	hist, err := r.ScoreHistory(ctx, id, 2)
	if err != nil || len(hist) != 2 || hist[0].Score != 70 || hist[1].Score != 60 {
		t.Errorf("history = %v err=%v", hist, err)
	}

	// The summary carries the latest workload score.
	w, err := r.Get(ctx, id)
	if err != nil || w.Score == nil || *w.Score != 70 {
		t.Errorf("summary score = %v err=%v, want 70", w, err)
	}
	list, _ := r.List(ctx, repository.WorkloadFilter{})
	if len(list) != 1 || list[0].Score == nil || *list[0].Score != 70 {
		t.Errorf("list score = %v", list)
	}
}

func TestWorkloadResources(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	seedWL(t, db, wlSeed{id: "prod/w-1/app", src: "s1", ns: "prod", kind: "Deployment", name: "w", image: "i", cname: "app", status: model.StatusRunning, seen: time.Minute})
	seedWL(t, db, wlSeed{id: "prod/w-2/app", src: "s1", ns: "prod", kind: "Deployment", name: "w", image: "i", cname: "app", status: model.StatusRunning, seen: time.Minute})
	seedWL(t, db, wlSeed{id: "prod/x-1/app", src: "s1", ns: "prod", kind: "Deployment", name: "x", image: "i", cname: "app", status: model.StatusRunning, seen: time.Minute})
	id := model.WorkloadID("s1", "prod", "Deployment", "w")

	// bucket [0,30s): w-1 avg cpu 20 (10,30) mem 200 (100,300) pct 2 ; w-2 cpu 5 mem 50 pct 7
	// bucket [30s,60s): w-1 cpu 4 mem 40 pct 1
	for _, s := range []model.ResourceSample{
		{ContainerID: "prod/w-1/app", CPUPercent: 10, MemBytes: 100, MemPercent: 1, SampledAt: at(0)},
		{ContainerID: "prod/w-1/app", CPUPercent: 30, MemBytes: 300, MemPercent: 3, SampledAt: at(10 * time.Second)},
		{ContainerID: "prod/w-2/app", CPUPercent: 5, MemBytes: 50, MemPercent: 7, SampledAt: at(5 * time.Second)},
		{ContainerID: "prod/w-1/app", CPUPercent: 4, MemBytes: 40, MemPercent: 1, SampledAt: at(40 * time.Second)},
		{ContainerID: "prod/x-1/app", CPUPercent: 99, MemBytes: 9999, MemPercent: 99, SampledAt: at(0)}, // other workload
	} {
		saveSample(t, db, s)
	}

	got, err := db.Workloads().Resources(ctx, id, at(0), at(time.Minute), 30*time.Second)
	if err != nil || len(got) != 2 {
		t.Fatalf("len=%d err=%v", len(got), err)
	}
	b1, b2 := got[0], got[1]
	if !b1.SampledAt.Equal(at(0)) || !b2.SampledAt.Equal(at(30*time.Second)) {
		t.Errorf("buckets = %v, %v", b1.SampledAt, b2.SampledAt)
	}
	if b1.ContainerID != id {
		t.Errorf("ContainerID = %q, want workload id", b1.ContainerID)
	}
	// CPU and memory summed across instances of per-instance averages; mem_percent is the max.
	if !near(b1.CPUPercent, 25) || b1.MemBytes != 250 || !near(b1.MemPercent, 7) {
		t.Errorf("bucket1 = %+v", b1)
	}
	if !near(b2.CPUPercent, 4) || b2.MemBytes != 40 || !near(b2.MemPercent, 1) {
		t.Errorf("bucket2 = %+v", b2)
	}

	// bucket <= 0 falls back to the 30s default.
	def, err := db.Workloads().Resources(ctx, id, at(0), at(time.Minute), 0)
	if err != nil || len(def) != 2 {
		t.Errorf("default bucket: len=%d err=%v", len(def), err)
	}
	// Empty window / unknown workload.
	if none, err := db.Workloads().Resources(ctx, "nope", at(0), at(time.Minute), time.Minute); err != nil || len(none) != 0 {
		t.Errorf("unknown workload: %v %v", none, err)
	}
}
