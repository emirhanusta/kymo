package issues

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

// ---- fakes -----------------------------------------------------------------

type fakeIssues struct {
	repository.IssueRepository
	mu   sync.Mutex
	next int64
	rows map[int64]*model.Issue
}

func newFakeIssues() *fakeIssues { return &fakeIssues{rows: map[int64]*model.Issue{}} }

func (f *fakeIssues) Create(_ context.Context, i *model.Issue) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rows {
		if r.WorkloadID == i.WorkloadID && r.Type == i.Type && r.Status == model.IssueOpen {
			return repository.ErrConflict
		}
	}
	f.next++
	i.ID = f.next
	i.Status = model.IssueOpen
	c := *i
	f.rows[i.ID] = &c
	return nil
}

func (f *fakeIssues) UpdateOpen(_ context.Context, i *model.Issue) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.rows[i.ID]
	if r == nil || r.Status != model.IssueOpen {
		return repository.ErrNotFound
	}
	r.Severity, r.Title, r.Detail, r.LastSeen = i.Severity, i.Title, i.Detail, i.LastSeen
	return nil
}

func (f *fakeIssues) Resolve(_ context.Context, id int64, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.rows[id]
	if r == nil || r.Status != model.IssueOpen {
		return repository.ErrNotFound
	}
	r.Status, r.ResolvedAt = model.IssueResolved, &at
	return nil
}

func (f *fakeIssues) Reopen(_ context.Context, i *model.Issue) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.rows[i.ID]
	if r == nil || r.Status != model.IssueResolved {
		return repository.ErrNotFound
	}
	r.Status, r.ResolvedAt, r.Flaps = model.IssueOpen, nil, r.Flaps+1
	r.Severity, r.Title, r.Detail, r.LastSeen = i.Severity, i.Title, i.Detail, i.LastSeen
	i.Flaps = r.Flaps
	return nil
}

func (f *fakeIssues) ListActive(_ context.Context, since time.Time) ([]*model.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*model.Issue
	for _, r := range f.rows {
		if r.Status == model.IssueOpen || (r.ResolvedAt != nil && !r.ResolvedAt.Before(since)) {
			c := *r
			out = append(out, &c)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID > out[b].ID })
	return out, nil
}

func (f *fakeIssues) DeleteResolvedBefore(context.Context, time.Time) (int64, error) { return 0, nil }

func (f *fakeIssues) byType(wid string, t model.IssueType) []*model.Issue {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*model.Issue
	for _, r := range f.rows {
		if r.WorkloadID == wid && r.Type == t {
			c := *r
			out = append(out, &c)
		}
	}
	return out
}

type fakeWorkloads struct {
	repository.WorkloadRepository
	list []*model.WorkloadSummary
	err  error
}

func (f *fakeWorkloads) List(context.Context, repository.WorkloadFilter) ([]*model.WorkloadSummary, error) {
	return f.list, f.err
}

type fakeEvents struct {
	repository.EventRepository
	list []*model.LifecycleEvent
}

func (f *fakeEvents) FindRecent(_ context.Context, q repository.EventQuery) ([]*model.LifecycleEvent, error) {
	var out []*model.LifecycleEvent
	for _, e := range f.list {
		if (q.Type == "" || e.EventType == q.Type) && (q.Since.IsZero() || !e.OccurredAt.Before(q.Since)) {
			out = append(out, e)
		}
	}
	return out, nil
}

type fakeK8s struct {
	repository.K8sEventRepository
	list []*model.K8sEvent
}

func (f *fakeK8s) List(_ context.Context, q repository.K8sEventQuery) ([]*model.K8sEvent, error) {
	var out []*model.K8sEvent
	for _, e := range f.list {
		if (q.Type == "" || e.Type == q.Type) && (q.Since.IsZero() || !e.LastSeen.Before(q.Since)) {
			out = append(out, e)
		}
	}
	return out, nil
}

type fakeCron struct {
	repository.CronJobRepository
	list []*model.CronJobExecution // any order
}

func (f *fakeCron) FindLatestPerCronJob(context.Context, string, string) ([]*model.CronJobExecution, error) {
	best := map[string]*model.CronJobExecution{}
	for _, x := range f.list {
		k := x.SourceName + "/" + x.Namespace + "/" + x.CronJobName
		if b := best[k]; b == nil || x.SampledAt.After(b.SampledAt) {
			best[k] = x
		}
	}
	var out []*model.CronJobExecution
	for _, x := range best {
		out = append(out, x)
	}
	return out, nil
}

func (f *fakeCron) FindByCronJob(_ context.Context, src, ns, name string, limit int) ([]*model.CronJobExecution, error) {
	var out []*model.CronJobExecution
	for _, x := range f.list {
		if x.SourceName == src && x.Namespace == ns && x.CronJobName == name {
			out = append(out, x)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].SampledAt.After(out[b].SampledAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

type fakeSources map[string]model.SourceSpec

func (f fakeSources) Source(n string) (model.SourceSpec, bool) { s, ok := f[n]; return s, ok }

type fakeNotifier struct {
	mu      sync.Mutex
	events  []model.IssueEvent
	reminds [][]model.Issue
}

func (f *fakeNotifier) Emit(_ context.Context, ev model.IssueEvent) {
	f.mu.Lock()
	f.events = append(f.events, ev)
	f.mu.Unlock()
}
func (f *fakeNotifier) Remind(_ context.Context, open []model.Issue) {
	f.mu.Lock()
	f.reminds = append(f.reminds, open)
	f.mu.Unlock()
}
func (f *fakeNotifier) kinds() []model.IssueEventKind {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []model.IssueEventKind
	for _, e := range f.events {
		out = append(out, e.Kind)
	}
	return out
}

// ---- harness ---------------------------------------------------------------

type env struct {
	t         *testing.T
	now       time.Time
	issues    *fakeIssues
	workloads *fakeWorkloads
	events    *fakeEvents
	k8s       *fakeK8s
	cron      *fakeCron
	sources   fakeSources
	notifier  *fakeNotifier
	engine    *Engine
}

func newEnv(t *testing.T, cfg Config) *env {
	t.Helper()
	e := &env{
		t: t, now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		issues: newFakeIssues(), workloads: &fakeWorkloads{}, events: &fakeEvents{}, k8s: &fakeK8s{}, cron: &fakeCron{},
		sources:  fakeSources{"prod": {Name: "prod", Enabled: true}},
		notifier: &fakeNotifier{},
	}
	e.engine = New(cfg, Deps{
		Workloads: e.workloads, Events: e.events, K8sEvents: e.k8s, CronJobs: e.cron, Issues: e.issues,
		Sources: e.sources, Notifier: e.notifier, Now: func() time.Time { return e.now },
	})
	return e
}

func (e *env) eval() {
	e.t.Helper()
	if err := e.engine.Evaluate(context.Background()); err != nil {
		e.t.Fatalf("evaluate: %v", err)
	}
}

func (e *env) advance(d time.Duration) { e.now = e.now.Add(d) }

func workload(name, kind string, status model.ContainerStatus, active, running int) *model.WorkloadSummary {
	return &model.WorkloadSummary{
		ID: model.WorkloadID("prod", "default", kind, name), SourceName: "prod", Namespace: "default",
		Kind: kind, Name: name, Status: status, ActiveInstances: active, RunningInstances: running,
	}
}

func (e *env) lifecycle(w *model.WorkloadSummary, typ model.EventType, exit int, ago time.Duration) {
	e.events.list = append(e.events.list, &model.LifecycleEvent{
		EventType: typ, ExitCode: exit, WorkloadID: w.ID, SourceName: "prod", OccurredAt: e.now.Add(-ago),
	})
}

func (e *env) warn(w *model.WorkloadSummary, reason, msg string, count int) {
	e.k8s.list = append(e.k8s.list, &model.K8sEvent{
		SourceName: "prod", Namespace: w.Namespace, Kind: "Pod", Name: w.Name + "-x", WorkloadID: w.ID,
		Type: "Warning", Reason: reason, Message: msg, Count: count,
		FirstSeen: e.now.Add(-5 * time.Minute), LastSeen: e.now.Add(-time.Minute),
	})
}

func (e *env) one(w *model.WorkloadSummary, t model.IssueType) *model.Issue {
	e.t.Helper()
	rows := e.issues.byType(w.ID, t)
	if len(rows) != 1 {
		e.t.Fatalf("%s/%s: %d rows, want 1", w.Name, t, len(rows))
	}
	return rows[0]
}

func (e *env) none(w *model.WorkloadSummary, t model.IssueType) {
	e.t.Helper()
	if rows := e.issues.byType(w.ID, t); len(rows) != 0 {
		e.t.Fatalf("%s/%s: unexpected issue %+v", w.Name, t, rows[0])
	}
}

// ---- tests -----------------------------------------------------------------

func TestEveryIssueTypeOpens(t *testing.T) {
	e := newEnv(t, Config{})

	crash := workload("crashy", "Deployment", model.StatusCrashing, 2, 1)
	e.lifecycle(crash, model.EventTypeCrash, 1, time.Hour)
	e.lifecycle(crash, model.EventTypeCrash, 137, 10*time.Minute) // newest wins
	oom := workload("hungry", "Deployment", model.StatusCrashing, 1, 0)
	e.lifecycle(oom, model.EventTypeOOM, 137, time.Hour)
	e.lifecycle(oom, model.EventTypeOOM, 137, 2*time.Hour)
	e.lifecycle(oom, model.EventTypeOOM, 137, 30*time.Hour) // outside 24h
	e.lifecycle(oom, model.EventTypeCrash, 137, time.Hour)
	job := workload("nightly", "CronJob", model.StatusRunning, 1, 1)
	other := workload("scheduler", "Deployment", model.StatusRunning, 1, 1)
	e.workloads.list = []*model.WorkloadSummary{crash, oom, job, other}

	exec := func(ok bool, code int, ago time.Duration) *model.CronJobExecution {
		done := e.now.Add(-ago)
		return &model.CronJobExecution{SourceName: "prod", Namespace: "default", CronJobName: "nightly",
			CompletedAt: &done, Succeeded: &ok, ExitCode: code, SampledAt: done}
	}
	e.cron.list = []*model.CronJobExecution{exec(true, 0, 3*time.Hour), exec(true, 0, 2*time.Hour), exec(false, 2, time.Hour)}

	e.warn(other, "FailedScheduling", "0/3 nodes are available: 3 Insufficient cpu.\nsecond line", 1)
	e.warn(other, "ImagePullBackOff", "Back-off pulling image", 4)
	e.warn(other, "Failed", "Failed to pull image \"x\"", 1)  // also image_pull
	e.warn(other, "Failed", "container create failed", 1)     // not reported
	e.warn(other, "BackOff", "Back-off restarting failed", 9) // crash loop, not reported
	e.warn(other, "Evicted", "The node was low on resource", 2)
	e.warn(other, "Evicted", "The node was low on resource", 1)
	e.warn(other, "FailedMount", "MountVolume.SetUp failed", 1)
	e.warn(other, "FailedAttachVolume", "attach failed", 1)
	e.warn(other, "Unhealthy", "Readiness probe failed", 3)
	e.k8s.list = append(e.k8s.list, &model.K8sEvent{ // too old
		WorkloadID: other.ID, Type: "Warning", Reason: "FailedScheduling", FirstSeen: e.now.Add(-3 * time.Hour), LastSeen: e.now.Add(-2 * time.Hour),
	}, &model.K8sEvent{ // Normal
		WorkloadID: other.ID, Type: "Normal", Reason: "Evicted", FirstSeen: e.now, LastSeen: e.now,
	})

	e.eval()

	check := func(w *model.WorkloadSummary, typ model.IssueType, sev model.IssueSeverity, title string) {
		t.Helper()
		i := e.one(w, typ)
		if i.Severity != sev || i.Title != title || i.Status != model.IssueOpen || i.SourceName != "prod" ||
			i.Namespace != "default" || i.Kind != w.Kind || i.Name != w.Name {
			t.Errorf("%s: %+v", typ, i)
		}
	}
	check(crash, model.IssueCrashLoop, model.SeverityCritical, "Crash loop · exit 137")
	check(oom, model.IssueOOM, model.SeverityCritical, "OOMKilled ×2 in 24h")
	e.none(oom, model.IssueCrashLoop) // explained by OOM
	check(job, model.IssueJobFailed, model.SeverityCritical, "Last run failed (exit 2)")
	check(other, model.IssueCantSchedule, model.SeverityCritical, "Can't schedule — 0/3 nodes are available: 3 Insufficient cpu.")
	check(other, model.IssueImagePull, model.SeverityCritical, "Image pull failing")
	check(other, model.IssueEvicted, model.SeverityCritical, "Evicted ×3")
	check(other, model.IssueVolumeMount, model.SeverityCritical, "Volume mount failing")
	check(other, model.IssueProbeFailing, model.SeverityWarning, "Probe failing ×3")
	if got := e.one(job, model.IssueJobFailed).Detail; got != "2/3 recent runs succeeded" {
		t.Errorf("job detail %q", got)
	}
	if got := e.one(crash, model.IssueCrashLoop).Detail; got != "1/2 pods ready" {
		t.Errorf("crash detail %q", got)
	}

	kinds := e.notifier.kinds()
	if len(kinds) != 8 {
		t.Fatalf("events: %v", kinds)
	}
	for _, k := range kinds {
		if k != model.EventOpened {
			t.Errorf("unexpected event %s", k)
		}
	}
	if len(e.notifier.reminds) != 1 || len(e.notifier.reminds[0]) != 8 {
		t.Errorf("Remind should receive the 8 open issues: %v", e.notifier.reminds)
	}

	// A second pass changes nothing and announces nothing.
	e.advance(30 * time.Second)
	e.eval()
	if len(e.notifier.kinds()) != 8 || len(e.issues.rows) != 8 {
		t.Fatalf("second pass: %d events, %d rows", len(e.notifier.kinds()), len(e.issues.rows))
	}
}

func TestCronJobSucceedingOrRunningLatest(t *testing.T) {
	e := newEnv(t, Config{})
	job := workload("nightly", "CronJob", model.StatusRunning, 1, 1)
	e.workloads.list = []*model.WorkloadSummary{job}
	mk := func(ok bool, ago time.Duration, running bool) *model.CronJobExecution {
		at := e.now.Add(-ago)
		x := &model.CronJobExecution{SourceName: "prod", Namespace: "default", CronJobName: "nightly", SampledAt: at, StartedAt: &at, Succeeded: &ok}
		if running {
			x.Succeeded = nil
		} else {
			x.CompletedAt = &at
		}
		if !ok && !running {
			x.ExitCode = 1
		}
		return x
	}
	// Newest run still running, last finished one failed -> failing.
	e.cron.list = []*model.CronJobExecution{mk(false, 2*time.Hour, false), mk(true, time.Minute, true)}
	e.eval()
	e.one(job, model.IssueJobFailed)

	// Newest finished run succeeded -> condition ends and resolves after resolve_after.
	e.cron.list = []*model.CronJobExecution{mk(false, 2*time.Hour, false), mk(true, time.Minute, false)}
	e.eval()
	e.advance(DefaultResolveAfter)
	e.eval()
	if e.one(job, model.IssueJobFailed).Status != model.IssueResolved {
		t.Fatal("job_failed should resolve once the latest run succeeded")
	}
}

func TestResolveOnlyAfterResolveAfter(t *testing.T) {
	e := newEnv(t, Config{ResolveAfter: 5 * time.Minute})
	w := workload("api", "Deployment", model.StatusCrashing, 1, 0)
	e.workloads.list = []*model.WorkloadSummary{w}
	e.eval()
	id := e.one(w, model.IssueCrashLoop).ID

	// Condition clears: the clock starts at the first pass that sees it false.
	w.Status = model.StatusRunning
	w.RunningInstances = 1
	e.advance(30 * time.Second)
	e.eval()
	e.advance(4*time.Minute + 29*time.Second)
	e.eval() // 4m29s since first false pass
	if i := e.one(w, model.IssueCrashLoop); i.Status != model.IssueOpen {
		t.Fatalf("resolved too early: %+v", i)
	}

	// It flickers back: the timer restarts.
	w.Status = model.StatusCrashing
	e.advance(10 * time.Second)
	e.eval()
	w.Status = model.StatusRunning
	e.advance(10 * time.Second)
	e.eval() // false again, timer restarts here
	e.advance(4*time.Minute + 59*time.Second)
	e.eval()
	if e.one(w, model.IssueCrashLoop).Status != model.IssueOpen {
		t.Fatal("resolved before 5 minutes of continuous absence")
	}
	e.advance(time.Second)
	e.eval()
	i := e.one(w, model.IssueCrashLoop)
	if i.Status != model.IssueResolved || i.ResolvedAt == nil || !i.ResolvedAt.Equal(e.now) || i.ID != id {
		t.Fatalf("not resolved: %+v", i)
	}
	if got := e.notifier.kinds(); len(got) != 2 || got[0] != model.EventOpened || got[1] != model.EventResolved {
		t.Fatalf("events: %v", got)
	}
	if ev := e.notifier.events[1]; ev.Issue.Status != model.IssueResolved || ev.Issue.ResolvedAt == nil {
		t.Errorf("resolved event payload: %+v", ev.Issue)
	}
}

func TestReopenWithinWindowIsAFlap(t *testing.T) {
	e := newEnv(t, Config{ResolveAfter: 5 * time.Minute, ReopenWindow: 15 * time.Minute})
	w := workload("api", "Deployment", model.StatusCrashing, 1, 0)
	e.workloads.list = []*model.WorkloadSummary{w}
	e.eval()
	first := e.one(w, model.IssueCrashLoop)

	resolve := func() {
		w.Status, w.RunningInstances = model.StatusRunning, 1
		e.eval()
		e.advance(5 * time.Minute)
		e.eval()
	}
	resolve()
	if e.one(w, model.IssueCrashLoop).Status != model.IssueResolved {
		t.Fatal("expected resolved")
	}

	// Back 10 minutes later: same row, no second "opened".
	e.advance(10 * time.Minute)
	w.Status, w.RunningInstances = model.StatusCrashing, 0
	e.eval()
	i := e.one(w, model.IssueCrashLoop)
	if i.ID != first.ID || i.Status != model.IssueOpen || i.ResolvedAt != nil || i.Flaps != 1 {
		t.Fatalf("reopen: %+v", i)
	}
	if got := e.notifier.kinds(); len(got) != 2 || got[0] != model.EventOpened || got[1] != model.EventResolved {
		t.Fatalf("flap must not notify opened again: %v", got)
	}

	// Resolve again, then come back after the window: a brand-new issue + notification.
	resolve()
	e.advance(16 * time.Minute)
	w.Status, w.RunningInstances = model.StatusCrashing, 0
	e.eval()
	rows := e.issues.byType(w.ID, model.IssueCrashLoop)
	if len(rows) != 2 {
		t.Fatalf("want a new row after the window, got %d", len(rows))
	}
	if got := e.notifier.kinds(); got[len(got)-1] != model.EventOpened || len(got) != 4 {
		t.Fatalf("events: %v", got)
	}
}

func TestDegradedNeedsDegradedAfter(t *testing.T) {
	e := newEnv(t, Config{DegradedAfter: 3 * time.Minute})
	w := workload("web", "Deployment", model.StatusRunning, 3, 2)
	e.workloads.list = []*model.WorkloadSummary{w}

	e.eval()
	e.none(w, model.IssueDegraded)
	e.advance(2*time.Minute + 59*time.Second)
	e.eval()
	e.none(w, model.IssueDegraded)

	// Recovers briefly: the gate restarts.
	w.RunningInstances = 3
	e.advance(time.Second)
	e.eval()
	w.RunningInstances = 2
	e.advance(time.Second)
	e.eval()
	e.advance(2*time.Minute + 59*time.Second)
	e.eval()
	e.none(w, model.IssueDegraded)

	e.advance(time.Second)
	e.eval()
	i := e.one(w, model.IssueDegraded)
	if i.Severity != model.SeverityWarning || i.Title != "Degraded (2/3 ready)" {
		t.Fatalf("%+v", i)
	}
	if got := e.notifier.kinds(); len(got) != 1 || got[0] != model.EventOpened {
		t.Fatalf("events: %v", got)
	}

	// An open degraded issue survives a restart without waiting for the gate again.
	e.engine = New(Config{DegradedAfter: 3 * time.Minute}, Deps{
		Workloads: e.workloads, Events: e.events, K8sEvents: e.k8s, CronJobs: e.cron, Issues: e.issues,
		Sources: e.sources, Notifier: e.notifier, Now: func() time.Time { return e.now },
	})
	e.advance(30 * time.Second)
	e.eval()
	if e.one(w, model.IssueDegraded).Status != model.IssueOpen || len(e.notifier.kinds()) != 1 {
		t.Fatalf("restart must keep open issues silently: %v", e.notifier.kinds())
	}
	e.advance(10 * time.Minute)
	e.eval()
	if e.one(w, model.IssueDegraded).Status != model.IssueOpen {
		t.Fatal("still degraded, must stay open")
	}
}

func TestStoppedWorkloadIsNotDegraded(t *testing.T) {
	e := newEnv(t, Config{DegradedAfter: time.Second})
	w := workload("off", "Deployment", model.StatusStopped, 0, 0)
	e.workloads.list = []*model.WorkloadSummary{w}
	e.eval()
	e.advance(time.Minute)
	e.eval()
	if len(e.issues.rows) != 0 {
		t.Fatalf("unexpected issues: %v", e.issues.rows)
	}
}

func TestSystemAndRemovedSourcesIgnored(t *testing.T) {
	sys := workload("coredns", "Deployment", model.StatusCrashing, 1, 0)
	sys.Namespace, sys.IsSystem = "kube-system", true
	sys.ID = model.WorkloadID("prod", "kube-system", "Deployment", "coredns")
	gone := workload("old", "Deployment", model.StatusCrashing, 1, 0)
	gone.SourceName = "removed"
	gone.ID = model.WorkloadID("removed", "default", "Deployment", "old")
	live := workload("app", "Deployment", model.StatusCrashing, 1, 0)

	e := newEnv(t, Config{})
	e.workloads.list = []*model.WorkloadSummary{sys, gone, live}
	e.k8s.list = []*model.K8sEvent{
		{WorkloadID: sys.ID, Type: "Warning", Reason: "Unhealthy", Count: 1, FirstSeen: e.now, LastSeen: e.now},
		{WorkloadID: gone.ID, Type: "Warning", Reason: "Unhealthy", Count: 1, FirstSeen: e.now, LastSeen: e.now},
		// Workload without recorded containers: still reported, but system namespaces / removed sources are not.
		{WorkloadID: "prod:default:Deployment:new", Type: "Warning", Reason: "FailedScheduling", Message: "no nodes", Count: 1, FirstSeen: e.now, LastSeen: e.now},
		{WorkloadID: "prod:kube-system:DaemonSet:cni", Type: "Warning", Reason: "FailedScheduling", Count: 1, FirstSeen: e.now, LastSeen: e.now},
		{WorkloadID: "removed:default:Deployment:x", Type: "Warning", Reason: "FailedScheduling", Count: 1, FirstSeen: e.now, LastSeen: e.now},
	}
	e.eval()
	var got []string
	for _, r := range e.issues.rows {
		got = append(got, r.WorkloadID+"|"+string(r.Type))
	}
	sort.Strings(got)
	want := []string{live.ID + "|crash_loop", "prod:default:Deployment:new|cant_schedule"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v want %v", got, want)
	}
	n := e.one(&model.WorkloadSummary{ID: "prod:default:Deployment:new", Name: "new"}, model.IssueCantSchedule)
	if n.Kind != "Deployment" || n.Name != "new" || n.Namespace != "default" || n.SourceName != "prod" {
		t.Errorf("identity parsed from workload id: %+v", n)
	}

	// include_system turns the system workloads on.
	e2 := newEnv(t, Config{IncludeSystem: true})
	e2.workloads.list = []*model.WorkloadSummary{sys, gone}
	e2.eval()
	if len(e2.issues.rows) != 1 || e2.one(sys, model.IssueCrashLoop) == nil {
		t.Fatalf("include_system: %v", e2.issues.rows)
	}
}

func TestRemovedSourceIssuesResolveAndPausedOnesStay(t *testing.T) {
	e := newEnv(t, Config{})
	w := workload("app", "Deployment", model.StatusCrashing, 1, 0)
	e.workloads.list = []*model.WorkloadSummary{w}
	e.eval()

	// Paused source: its (frozen) issues are neither updated nor resolved.
	e.sources["prod"] = model.SourceSpec{Name: "prod", Enabled: false}
	e.advance(time.Hour)
	e.eval()
	if e.one(w, model.IssueCrashLoop).Status != model.IssueOpen {
		t.Fatal("paused source must keep its issues")
	}

	// Removed source: the issue resolves after the usual delay.
	delete(e.sources, "prod")
	e.eval()
	e.advance(DefaultResolveAfter)
	e.eval()
	if e.one(w, model.IssueCrashLoop).Status != model.IssueResolved {
		t.Fatal("issues of a removed source should resolve")
	}
}

func TestReadErrorDoesNotResolveAnything(t *testing.T) {
	e := newEnv(t, Config{})
	w := workload("app", "Deployment", model.StatusCrashing, 1, 0)
	e.workloads.list = []*model.WorkloadSummary{w}
	e.eval()

	e.workloads.err = errors.New("db down")
	e.advance(time.Hour)
	if err := e.engine.Evaluate(context.Background()); err == nil {
		t.Fatal("want error")
	}
	e.workloads.err = nil
	e.eval()
	if e.one(w, model.IssueCrashLoop).Status != model.IssueOpen {
		t.Fatal("a failed pass must not resolve issues")
	}
}

func TestPreexistingOpenIssueIsKeptSilently(t *testing.T) {
	e := newEnv(t, Config{})
	w := workload("app", "Deployment", model.StatusCrashing, 1, 0)
	e.workloads.list = []*model.WorkloadSummary{w}
	pre := &model.Issue{SourceName: "prod", WorkloadID: w.ID, Namespace: "default", Kind: "Deployment", Name: "app",
		Type: model.IssueCrashLoop, Severity: model.SeverityCritical, Title: "old title",
		FirstSeen: e.now.Add(-time.Hour), LastSeen: e.now.Add(-time.Minute)}
	if err := e.issues.Create(context.Background(), pre); err != nil {
		t.Fatal(err)
	}
	e.eval()
	i := e.one(w, model.IssueCrashLoop)
	if i.ID != pre.ID || i.Title != "Crash loop" || !i.LastSeen.Equal(e.now) || !i.FirstSeen.Equal(pre.FirstSeen) {
		t.Fatalf("%+v", i)
	}
	if len(e.notifier.kinds()) != 0 {
		t.Fatalf("existing issue must not be re-announced: %v", e.notifier.kinds())
	}
}

func TestSeverityAndTitleUpdateInPlace(t *testing.T) {
	e := newEnv(t, Config{})
	w := workload("app", "Deployment", model.StatusRunning, 1, 1)
	e.workloads.list = []*model.WorkloadSummary{w}
	e.lifecycle(w, model.EventTypeOOM, 137, time.Minute)
	e.eval()
	e.lifecycle(w, model.EventTypeOOM, 137, 2*time.Minute)
	e.advance(time.Minute)
	e.eval()
	if i := e.one(w, model.IssueOOM); i.Title != "OOMKilled ×2 in 24h" {
		t.Fatalf("%+v", i)
	}
	if len(e.notifier.kinds()) != 1 {
		t.Fatalf("title change must not re-notify: %v", e.notifier.kinds())
	}
}

func TestWarningsOfWorkloadWithoutPodsAreIgnored(t *testing.T) {
	e := newEnv(t, Config{})
	gone := workload("bigmem", "Deployment", model.StatusStopped, 0, 0)
	e.workloads.list = []*model.WorkloadSummary{gone}
	e.warn(gone, "FailedScheduling", "0/1 nodes are available: 1 Insufficient memory.", 3)
	e.eval()
	e.none(gone, model.IssueCantSchedule)
}
