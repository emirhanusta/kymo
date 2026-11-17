package errorlogs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/emirhanusta/kymo/internal/config"
	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

type memRepo struct {
	repository.ErrorLogRepository
	mu      sync.Mutex
	next    int64
	targets []*model.LogCaptureTarget
	saved   []*model.ErrorBatch
	keep    int
}

func (r *memRepo) ListTargets(context.Context) ([]*model.LogCaptureTarget, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*model.LogCaptureTarget, len(r.targets))
	for i, t := range r.targets {
		c := *t
		out[i] = &c
	}
	return out, nil
}

func (r *memRepo) CreateTarget(_ context.Context, t *model.LogCaptureTarget) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range r.targets {
		if x.SourceName == t.SourceName && x.Namespace == t.Namespace && x.WorkloadID == t.WorkloadID {
			return repository.ErrConflict
		}
	}
	r.next++
	t.ID, t.CreatedAt = r.next, time.Now()
	c := *t
	r.targets = append(r.targets, &c)
	return nil
}

func (r *memRepo) GetTarget(_ context.Context, id int64) (*model.LogCaptureTarget, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range r.targets {
		if x.ID == id {
			c := *x
			return &c, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (r *memRepo) DeleteTarget(_ context.Context, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, x := range r.targets {
		if x.ID == id {
			r.targets = append(r.targets[:i], r.targets[i+1:]...)
			return nil
		}
	}
	return repository.ErrNotFound
}

func (r *memRepo) SyncConfigTargets(_ context.Context, declared []*model.LogCaptureTarget) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var keep []*model.LogCaptureTarget
	for _, x := range r.targets {
		if x.Origin != model.TargetOriginConfig {
			keep = append(keep, x)
		}
	}
	r.targets = keep
	for _, d := range declared {
		r.next++
		c := *d
		c.ID = r.next
		r.targets = append(r.targets, &c)
	}
	return nil
}

func (r *memRepo) SaveBatch(_ context.Context, b *model.ErrorBatch, keep int) error {
	r.mu.Lock()
	r.saved, r.keep = append(r.saved, b), keep
	r.mu.Unlock()
	return nil
}

type srcs map[string]string // name -> type

func (s srcs) Source(n string) (model.SourceSpec, bool) {
	t, ok := s[n]
	return model.SourceSpec{Name: n, Type: t}, ok
}

type fakeProvider struct {
	name      string
	streams   int
	annotated bool
}

func (p fakeProvider) LogCaptureStatus() model.LogCaptureSourceStatus {
	return model.LogCaptureSourceStatus{SourceName: p.name, Streams: p.streams}
}
func (p fakeProvider) WorkloadCapture(string) (int, bool) { return p.streams, p.annotated }

func defaultCfg() config.LogCaptureConfig {
	return config.LogCaptureConfig{Enabled: true, MaxStreamsPerSource: 10, MaxLinesPerMinute: 100, SamplesPerSignature: 7, Retention: "7d"}
}

func newSvc(t *testing.T) (*Service, *memRepo) {
	t.Helper()
	repo := &memRepo{}
	s, err := New(repo, defaultCfg(), srcs{"prod": "kubernetes", "other": "docker"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s, repo
}

const wl = "prod:shop:Deployment:web"

func TestCreateTargetValidation(t *testing.T) {
	s, _ := newSvc(t)
	ctx := context.Background()
	bad := []TargetInput{
		{Namespace: "shop"},
		{SourceName: "nope", Namespace: "shop"},
		{SourceName: "other", Namespace: "shop"},
		{SourceName: "prod"},
		{SourceName: "prod", Namespace: "shop", WorkloadID: "garbage"},
		{SourceName: "prod", Namespace: "shop", WorkloadID: "x:shop:Deployment:web"},
		{SourceName: "prod", Namespace: "other-ns", WorkloadID: wl},
	}
	for _, in := range bad {
		var ve *ValidationError
		if _, err := s.CreateTarget(ctx, in); !errors.As(err, &ve) {
			t.Errorf("%+v: want validation error, got %v", in, err)
		}
	}
	tg, err := s.CreateTarget(ctx, TargetInput{SourceName: "prod", WorkloadID: wl}) // namespace inferred
	if err != nil || tg.Namespace != "shop" || tg.Origin != model.TargetOriginUI {
		t.Fatalf("create: %+v %v", tg, err)
	}
	if _, err := s.CreateTarget(ctx, TargetInput{SourceName: "prod", Namespace: "shop", WorkloadID: wl}); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate: %v", err)
	}
	if got := s.Targets(); len(got) != 1 || got[0].ID != tg.ID {
		t.Errorf("snapshot not refreshed: %+v", got)
	}
	if err := s.DeleteTarget(ctx, tg.ID); err != nil {
		t.Fatal(err)
	}
	if len(s.Targets()) != 0 {
		t.Error("snapshot not refreshed after delete")
	}
	if err := s.DeleteTarget(ctx, tg.ID); !errors.Is(err, ErrTargetNotFound) {
		t.Errorf("second delete: %v", err)
	}
}

func TestConfigTargetsAreLocked(t *testing.T) {
	s, repo := newSvc(t)
	ctx := context.Background()
	cfg := defaultCfg()
	cfg.Targets = []config.LogCaptureTarget{{Source: "prod", Namespace: "shop", Workload: "Deployment/worker"}, {Source: "prod", Namespace: "ops"}}
	if err := s.ApplyConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if len(repo.targets) != 2 || repo.targets[0].WorkloadID != "prod:shop:Deployment:worker" || repo.targets[1].WorkloadID != "" ||
		repo.targets[0].Origin != model.TargetOriginConfig {
		t.Fatalf("targets: %+v", repo.targets[0])
	}
	if err := s.DeleteTarget(ctx, repo.targets[0].ID); !errors.Is(err, ErrTargetLocked) {
		t.Errorf("delete config target: %v", err)
	}
	if !model.MatchesTarget(s.Targets(), "prod", "ops", "prod:ops:Deployment:anything") {
		t.Error("namespace target should match any workload")
	}
}

func TestWorkloadCaptureViaAndToggle(t *testing.T) {
	s, repo := newSvc(t)
	ctx := context.Background()
	unreg := s.Register("prod", fakeProvider{streams: 2})
	defer unreg()

	st, err := s.WorkloadCapture(ctx, wl)
	if err != nil || st.Enabled || st.Via != nil || st.TargetID != nil || st.Streams != 2 || !st.GlobalEnabled {
		t.Fatalf("initial: %+v %v", st, err)
	}
	if _, err := s.WorkloadCapture(ctx, "garbage"); !errors.Is(err, ErrBadWorkload) {
		t.Errorf("malformed id: %v", err)
	}
	// Turning off something that is off is a no-op.
	if st, err := s.SetWorkloadCapture(ctx, wl, false); err != nil || st.Enabled {
		t.Errorf("off->off: %+v %v", st, err)
	}

	st, err = s.SetWorkloadCapture(ctx, wl, true)
	if err != nil || !st.Enabled || st.Via != "workload" || st.TargetID == nil {
		t.Fatalf("enable: %+v %v", st, err)
	}
	if again, err := s.SetWorkloadCapture(ctx, wl, true); err != nil || *again.TargetID != *st.TargetID || len(repo.targets) != 1 {
		t.Errorf("enable twice: %+v %v", again, err)
	}
	// A namespace target is reported only when there is no workload target.
	if _, err := s.CreateTarget(ctx, TargetInput{SourceName: "prod", Namespace: "shop"}); err != nil {
		t.Fatal(err)
	}
	st, _ = s.WorkloadCapture(ctx, wl)
	if st.Via != "workload" {
		t.Errorf("workload target wins: %+v", st)
	}
	// Disabling removes the workload target; the namespace target keeps it enabled.
	st, err = s.SetWorkloadCapture(ctx, wl, false)
	if err != nil || !st.Enabled || st.Via != "namespace" {
		t.Fatalf("disable with namespace target: %+v %v", st, err)
	}
	var ev *EnabledViaError
	if _, err := s.SetWorkloadCapture(ctx, wl, false); !errors.As(err, &ev) || ev.Via != "namespace" {
		t.Errorf("want 409 via namespace, got %v", err)
	}
	// Other workloads of another namespace are unaffected.
	if st, _ := s.WorkloadCapture(ctx, "prod:ops:Deployment:web"); st.Enabled {
		t.Errorf("leaked into other namespace: %+v", st)
	}

	// Annotation and config enablement.
	s.Register("prod", fakeProvider{annotated: true})
	if err := s.DeleteTarget(ctx, repo.targets[0].ID); err != nil {
		t.Fatal(err)
	}
	st, _ = s.WorkloadCapture(ctx, wl)
	if st.Via != "annotation" || st.TargetID != nil {
		t.Errorf("annotation: %+v", st)
	}
	if _, err := s.SetWorkloadCapture(ctx, wl, false); !errors.As(err, &ev) || ev.Via != "annotation" {
		t.Errorf("annotation off: %v", err)
	}
	s.Register("prod", fakeProvider{})
	cfg := defaultCfg()
	cfg.Targets = []config.LogCaptureTarget{{Source: "prod", Namespace: "shop", Workload: "Deployment/web"}}
	if err := s.ApplyConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	st, _ = s.WorkloadCapture(ctx, wl)
	if st.Via != "config" || st.TargetID == nil {
		t.Errorf("config: %+v", st)
	}
	if _, err := s.SetWorkloadCapture(ctx, wl, false); !errors.As(err, &ev) || ev.Via != "config" {
		t.Errorf("config off: %v", err)
	}
	if st, err := s.SetWorkloadCapture(ctx, wl, true); err != nil || st.Via != "config" {
		t.Errorf("enable when config covers it: %+v %v", st, err)
	}

	// Kill switch is reported separately from the opt-in.
	cfg.Enabled = false
	if err := s.ApplyConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.WorkloadCapture(ctx, wl); !st.Enabled || st.GlobalEnabled {
		t.Errorf("kill switch: %+v", st)
	}
}

func TestApplyConfigLiveAndNotifies(t *testing.T) {
	s, _ := newSvc(t)
	ctx := context.Background()
	ch, cancel := s.Subscribe()
	defer cancel()

	if s.Settings().Analyzer.IsHead("INFO all good") {
		t.Fatal("INFO must not be an error by default")
	}
	cfg := defaultCfg()
	cfg.ExtraPatterns = []string{"all good"}
	cfg.IgnorePatterns = []string{"noise"}
	cfg.MaxLinesPerMinute = 5
	if err := s.ApplyConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("subscriber not notified")
	}
	st := s.Settings()
	if !st.Analyzer.IsHead("INFO all good") || st.Analyzer.IsHead("ERROR noise") || st.MaxLinesPerMinute != 5 {
		t.Errorf("settings not applied live: %+v", st)
	}
	bad := defaultCfg()
	bad.ExtraPatterns = []string{"("}
	if err := s.ApplyConfig(ctx, bad); err == nil {
		t.Fatal("invalid pattern accepted")
	}
	if !s.Settings().Analyzer.IsHead("INFO all good") {
		t.Error("previous settings must stay after a failed reload")
	}
	if _, err := New(&memRepo{}, bad, nil, nil); err == nil {
		t.Error("New must reject invalid patterns")
	}
}

func TestIngestUsesSamplesPerSignatureAndStatus(t *testing.T) {
	s, repo := newSvc(t)
	if err := s.Ingest(context.Background(), &model.ErrorBatch{}); err != nil || repo.keep != 7 {
		t.Fatalf("ingest: keep=%d %v", repo.keep, err)
	}
	s.Register("b-src", fakeProvider{name: "b-src", streams: 1})
	unreg := s.Register("a-src", fakeProvider{name: "a-src", streams: 3})
	st := s.Status()
	if !st.Enabled || len(st.Sources) != 2 || st.Sources[0].SourceName != "a-src" || st.Sources[0].Streams != 3 {
		t.Errorf("status: %+v", st)
	}
	unreg()
	if len(s.Status().Sources) != 1 {
		t.Error("unregister must remove the source")
	}
}
