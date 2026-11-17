package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/collector"
	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/secret"
)

// fakeCollector fails to start failures times, then succeeds.
type fakeCollector struct {
	failures int32
	starts   atomic.Int32
	stopped  atomic.Bool
	events   chan collector.CollectorEvent
}

func newFake(failures int32) *fakeCollector {
	return &fakeCollector{failures: failures, events: make(chan collector.CollectorEvent)}
}

func (f *fakeCollector) Start(ctx context.Context) error {
	if f.starts.Add(1) <= f.failures {
		return errors.New("unreachable")
	}
	return nil
}
func (f *fakeCollector) Stop() error {
	if f.stopped.CompareAndSwap(false, true) {
		close(f.events)
	}
	return nil
}
func (f *fakeCollector) Events() <-chan collector.CollectorEvent { return f.events }
func (f *fakeCollector) Stats(context.Context, string) (*model.ResourceSample, error) {
	return nil, nil
}

func healthOf(t *testing.T, r *recorderService, name string) sourceHealth {
	t.Helper()
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[name]
	if !ok {
		t.Fatalf("source %q not registered", name)
	}
	return e.getHealth()
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRecorderAttachDetachWhileRunning(t *testing.T) {
	r := NewRecorderService(nil, nil, nil, nil, nil, nil, ScoringConfig{Interval: time.Hour}, zap.NewNop()).(*recorderService)

	before := newFake(0)
	r.AttachSource(model.SourceSpec{Name: "a", Type: "docker", Enabled: true}, before, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = r.Run(ctx); close(done) }()

	waitFor(t, func() bool { return healthOf(t, r, "a").status == SourceConnected })

	// Attached while running: starts immediately.
	during := newFake(0)
	r.AttachSource(model.SourceSpec{Name: "b", Type: "docker", Enabled: true, ReadOnly: true}, during, nil)
	waitFor(t, func() bool { return healthOf(t, r, "b").status == SourceConnected })
	if !r.IsReadOnly("b") || r.IsReadOnly("a") {
		t.Fatal("read-only flags not tracked per source")
	}

	// Disabled sources never start.
	off := newFake(0)
	r.AttachSource(model.SourceSpec{Name: "c", Type: "docker", Enabled: false}, off, nil)
	if h := healthOf(t, r, "c"); h.status != SourceDisabled || off.starts.Load() != 0 {
		t.Fatalf("disabled source started: %+v", h)
	}

	// Re-attaching replaces (and stops) the previous collector.
	replacement := newFake(0)
	r.AttachSource(model.SourceSpec{Name: "b", Type: "docker", Enabled: true}, replacement, nil)
	if !during.stopped.Load() {
		t.Fatal("replaced collector was not stopped")
	}

	r.DetachSource("a")
	if !before.stopped.Load() {
		t.Fatal("detached collector was not stopped")
	}
	if _, ok := r.Source("a"); ok {
		t.Fatal("detached source still registered")
	}

	cancel()
	<-done
	if !replacement.stopped.Load() {
		t.Fatal("collectors not stopped on shutdown")
	}
}

func TestRecorderRetriesFailingSource(t *testing.T) {
	r := NewRecorderService(nil, nil, nil, nil, nil, nil, ScoringConfig{Interval: time.Hour}, zap.NewNop()).(*recorderService)
	flaky := newFake(1)
	r.AttachSource(model.SourceSpec{Name: "k", Type: "kubernetes", Enabled: true}, flaky, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = r.Run(ctx) }()

	waitFor(t, func() bool { return healthOf(t, r, "k").status == SourceError })
	if h := healthOf(t, r, "k"); h.lastError != "unreachable" {
		t.Fatalf("last error not reported: %+v", h)
	}
	// Detaching during the back-off wait must not hang.
	r.DetachSource("k")
}

func TestBuildSpecValidation(t *testing.T) {
	noKey, _ := secret.New("")
	m := &SourceManager{box: noKey}

	cases := []struct {
		name string
		in   SourceInput
		ok   bool
	}{
		{"docker ok", SourceInput{Name: "local-docker", Type: "docker"}, true},
		{"bad name", SourceInput{Name: "Bad Name", Type: "docker"}, false},
		{"bad type", SourceInput{Name: "x", Type: "podman"}, false},
		{"poll too low", SourceInput{Name: "x", Type: "docker", PollIntervalSeconds: 1}, false},
		{"k8s path", SourceInput{Name: "dev", Type: "kubernetes", KubeconfigPath: "/k/config"}, true},
		{"upload without key", SourceInput{Name: "dev", Type: "kubernetes", Kubeconfig: "apiVersion: v1"}, false},
	}
	for _, tc := range cases {
		_, err := m.buildSpec(tc.in, nil)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v", tc.name, err)
		}
	}

	withKey, _ := secret.New("k")
	m = &SourceManager{box: withKey}
	if _, err := m.buildSpec(SourceInput{Name: "dev", Type: "kubernetes", Kubeconfig: "not: [valid"}, nil); err == nil {
		t.Error("invalid kubeconfig accepted")
	}

	existing := &model.SourceSpec{Name: "dev", Type: "kubernetes", Kubeconfig: []byte("kept")}
	spec, err := m.buildSpec(SourceInput{Type: "kubernetes", ReadOnly: true}, existing)
	if err != nil || string(spec.Kubeconfig) != "kept" || spec.Name != "dev" || !spec.ReadOnly {
		t.Errorf("update should keep the stored kubeconfig: %+v %v", spec, err)
	}
	spec, _ = m.buildSpec(SourceInput{Type: "kubernetes", KubeconfigPath: "/k"}, existing)
	if len(spec.Kubeconfig) != 0 {
		t.Error("switching to a path should drop the uploaded kubeconfig")
	}
	if _, err := m.buildSpec(SourceInput{Type: "docker"}, existing); err == nil {
		t.Error("type change accepted")
	}
}

// slowStopCollector blocks in Stop until released, like a Kubernetes collector
// draining its informer.
type slowStopCollector struct {
	*fakeCollector
	release chan struct{}
}

func (s *slowStopCollector) Stop() error {
	<-s.release
	return s.fakeCollector.Stop()
}

func TestRecorderReplaceKeepsSourceListed(t *testing.T) {
	r := NewRecorderService(nil, nil, nil, nil, nil, nil, ScoringConfig{Interval: time.Hour}, zap.NewNop()).(*recorderService)
	slow := &slowStopCollector{fakeCollector: newFake(0), release: make(chan struct{})}
	r.AttachSource(model.SourceSpec{Name: "k", Type: "kubernetes", Enabled: true}, slow, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = r.Run(ctx) }()
	waitFor(t, func() bool { return healthOf(t, r, "k").status == SourceConnected })

	replaced := make(chan struct{})
	next := newFake(0)
	go func() {
		r.AttachSource(model.SourceSpec{Name: "k", Type: "kubernetes", Enabled: true, ReadOnly: true}, next, nil)
		close(replaced)
	}()

	// While the old collector is still stopping, the source is listed with the
	// new settings and does not disappear.
	waitFor(t, func() bool { return r.IsReadOnly("k") })
	if h := healthOf(t, r, "k"); h.status != SourceConnecting {
		t.Fatalf("status while replacing = %s, want connecting", h.status)
	}
	if next.starts.Load() != 0 {
		t.Fatal("new collector started before the old one stopped")
	}

	close(slow.release)
	<-replaced
	waitFor(t, func() bool { return healthOf(t, r, "k").status == SourceConnected })
	if len(r.order) != 1 {
		t.Fatalf("order = %v, want one entry", r.order)
	}
}
