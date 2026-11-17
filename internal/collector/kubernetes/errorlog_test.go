package kubernetes

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/emirhanusta/kymo/internal/errorlog"
	"github.com/emirhanusta/kymo/internal/model"
)

// fakeHost is an ErrorLogHost for tests.
type fakeHost struct {
	mu       sync.Mutex
	settings errorlog.Settings
	targets  []model.LogCaptureTarget
	batches  []*model.ErrorBatch
	status   errorlog.StatusProvider
	subs     []chan struct{}
}

func newFakeHost() *fakeHost {
	a, _ := errorlog.NewAnalyzer(nil, nil)
	return &fakeHost{settings: errorlog.Settings{Enabled: true, MaxStreams: 50, MaxLinesPerMinute: 600, SamplesPerSignature: 50, Analyzer: a}}
}

func (h *fakeHost) Settings() errorlog.Settings {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.settings
}
func (h *fakeHost) set(f func(s *errorlog.Settings, t *[]model.LogCaptureTarget)) {
	h.mu.Lock()
	f(&h.settings, &h.targets)
	h.mu.Unlock()
}
func (h *fakeHost) Targets() []model.LogCaptureTarget {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]model.LogCaptureTarget(nil), h.targets...)
}
func (h *fakeHost) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	return ch, func() {}
}
func (h *fakeHost) Ingest(_ context.Context, b *model.ErrorBatch) error {
	h.mu.Lock()
	h.batches = append(h.batches, b)
	h.mu.Unlock()
	return nil
}
func (h *fakeHost) Register(_ string, p errorlog.StatusProvider) func() {
	h.mu.Lock()
	h.status = p
	h.mu.Unlock()
	return func() {}
}

// all merges every ingested batch by signature id.
func (h *fakeHost) all() map[string]model.ErrorSignatureDelta {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]model.ErrorSignatureDelta{}
	for _, b := range h.batches {
		for _, s := range b.Signatures {
			cur, ok := out[s.ID]
			if !ok {
				out[s.ID] = s
				continue
			}
			cur.Total += s.Total
			cur.Occurrences = append(cur.Occurrences, s.Occurrences...)
			out[s.ID] = cur
		}
	}
	return out
}

// logRig wires a Collector with fake pods, a fake host and injectable streams.
type logRig struct {
	c    *Collector
	host *fakeHost
	m    *errorLogManager
	inf  informers.SharedInformerFactory

	mu     sync.Mutex
	opened map[string][]*corev1.PodLogOptions
	pipes  map[string]*io.PipeWriter
	ctxs   map[string]context.Context
}

func newLogRig(t *testing.T) *logRig {
	t.Helper()
	cs := fake.NewSimpleClientset()
	host := newFakeHost()
	c := New(zap.NewNop(), "src", "", nil, "", 0, Options{ErrorLogs: host})
	c.clientset = cs
	inf := informers.NewSharedInformerFactory(cs, 0)
	c.podLister = inf.Core().V1().Pods().Lister()
	r := &logRig{c: c, host: host, inf: inf,
		opened: map[string][]*corev1.PodLogOptions{}, pipes: map[string]*io.PipeWriter{}, ctxs: map[string]context.Context{}}
	c.openLog = func(ctx context.Context, ns, pod string, opts *corev1.PodLogOptions) (io.ReadCloser, error) {
		id := containerID(ns, pod, opts.Container)
		pr, pw := io.Pipe()
		r.mu.Lock()
		r.opened[id] = append(r.opened[id], opts)
		r.pipes[id] = pw
		r.ctxs[id] = ctx
		r.mu.Unlock()
		go func() { <-ctx.Done(); _ = pw.Close() }()
		return pr, nil
	}
	r.m = newErrorLogManager(c, host)
	c.errMgr = r.m
	return r
}

func (r *logRig) addPod(ns, name, container string, started time.Time, annotated bool) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Annotations: map[string]string{}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name: container, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(started)}},
		}}},
	}
	if annotated {
		pod.Annotations[model.AnnotationCaptureErrorLogs] = "true"
	}
	_ = r.inf.Core().V1().Pods().Informer().GetIndexer().Add(pod)
	r.c.mu.Lock()
	r.c.workloadCache[podKey(pod)] = WorkloadInfo{Kind: model.WorkloadDeployment, Name: "web"}
	r.c.mu.Unlock()
}

func (r *logRig) openedIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []string
	for id := range r.opened {
		ids = append(ids, id)
	}
	return ids
}

func (r *logRig) write(t *testing.T, id string, lines ...string) {
	t.Helper()
	r.mu.Lock()
	pw := r.pipes[id]
	r.mu.Unlock()
	if pw == nil {
		t.Fatalf("no stream for %s", id)
	}
	for _, l := range lines {
		if _, err := pw.Write([]byte(l + "\n")); err != nil {
			t.Fatal(err)
		}
	}
}

func (r *logRig) streamCount() int { return r.m.LogCaptureStatus().Streams }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (r *logRig) shutdownWithin(t *testing.T) {
	t.Helper()
	done := make(chan struct{})
	go func() { r.m.shutdown(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not finish: goroutine leak or deadlock")
	}
}

func (a *aggregator) totalEntries() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, s := range a.sigs {
		n += s.delta.Total
	}
	return n
}

var longAgo = time.Now().Add(-time.Hour)

func targetNS(ns string) model.LogCaptureTarget {
	return model.LogCaptureTarget{SourceName: "src", Namespace: ns, Origin: model.TargetOriginUI}
}

func TestErrorLogOnlyOptedInContainersStream(t *testing.T) {
	r := newLogRig(t)
	r.addPod("shop", "web-1", "app", longAgo, false)  // namespace target
	r.addPod("other", "web-2", "app", longAgo, false) // not opted in
	r.addPod("other", "web-3", "app", longAgo, true)  // annotation
	r.host.set(func(_ *errorlog.Settings, t *[]model.LogCaptureTarget) {
		*t = []model.LogCaptureTarget{targetNS("shop")}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.m.reconcile(ctx)
	waitFor(t, "two streams", func() bool { return len(r.openedIDs()) == 2 })
	ids := strings.Join(r.openedIDs(), ",")
	if !strings.Contains(ids, "shop/web-1/app") || !strings.Contains(ids, "other/web-3/app") || strings.Contains(ids, "web-2") {
		t.Errorf("opened: %s", ids)
	}
	// Containers that were already running get only new lines.
	for id, opts := range r.opened {
		if len(opts) != 1 || !opts[0].Follow || !opts[0].Timestamps || opts[0].SinceTime == nil {
			t.Errorf("%s options: %+v", id, opts)
		}
	}
	if n, annotated := r.m.WorkloadCapture("src:other:Deployment:web"); n != 1 || !annotated {
		t.Errorf("WorkloadCapture = %d %v", n, annotated)
	}
	if n, annotated := r.m.WorkloadCapture("src:shop:Deployment:web"); n != 1 || annotated {
		t.Errorf("WorkloadCapture shop = %d %v", n, annotated)
	}
	cancel()
	r.shutdownWithin(t)
	if r.streamCount() != 0 {
		t.Error("streams left after shutdown")
	}
}

func TestErrorLogCapturesAndAggregates(t *testing.T) {
	r := newLogRig(t)
	r.addPod("shop", "web-1", "app", longAgo, false)
	r.addPod("shop", "web-2", "app", longAgo, false)
	r.host.set(func(_ *errorlog.Settings, t *[]model.LogCaptureTarget) {
		*t = []model.LogCaptureTarget{targetNS("shop")}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.m.reconcile(ctx)
	waitFor(t, "streams", func() bool { return len(r.openedIDs()) == 2 })

	r.write(t, "shop/web-1/app",
		`{"level":"info","msg":"there was an error earlier but all fine"}`,
		`{"level":"error","msg":"order 17 failed password=hunter2"}`,
		"2024-01-02 10:00:00 ERROR Failed to charge card 4111111111111111",
		"java.lang.IllegalStateException: boom",
		"\tat a.B.c(B.java:1)",
		"INFO back to normal",
	)
	r.write(t, "shop/web-2/app", `{"level":"error","msg":"order 99 failed password=other"}`, "GET / 200")
	waitFor(t, "3 entries", func() bool { return r.m.agg.totalEntries() == 3 })
	cancel()
	r.shutdownWithin(t)

	sigs := r.host.all()
	if len(sigs) != 2 {
		t.Fatalf("signatures: %+v", sigs)
	}
	wl := "src:shop:Deployment:web"
	order := sigs[errorlog.SignatureID(wl, "order <n> failed password=***")]
	if order.Total != 2 || order.Level != "error" || order.WorkloadID != wl || order.Namespace != "shop" || order.Kind != "Deployment" || order.Name != "web" || order.SourceName != "src" {
		t.Fatalf("order signature: %+v", order)
	}
	pods := map[string]bool{}
	cnt := 0
	for _, b := range order.Buckets {
		cnt += b.Count
		for _, p := range b.Pods {
			pods[p] = true
		}
	}
	if cnt != 2 || !pods["web-1"] || !pods["web-2"] || len(order.Occurrences) != 2 {
		t.Errorf("order buckets/pods/occurrences: %+v", order)
	}
	for _, o := range order.Occurrences {
		if strings.Contains(o.Message, "hunter2") || strings.Contains(o.Message, "other") || o.Container != "app" || !strings.HasPrefix(o.ContainerID, "shop/web-") {
			t.Errorf("occurrence: %+v", o)
		}
	}
	card := sigs[errorlog.SignatureID(wl, "Failed to charge card ***")]
	if card.Total != 1 || !strings.Contains(card.Occurrences[0].Message, "IllegalStateException") || strings.Contains(card.Occurrences[0].Message, "4111") {
		t.Errorf("stack trace entry: %+v", card)
	}
}

func TestErrorLogMaxStreamsOldestFirst(t *testing.T) {
	r := newLogRig(t)
	for i, age := range []time.Duration{3 * time.Hour, time.Hour, 2 * time.Hour} {
		r.addPod("shop", fmt.Sprintf("web-%d", i), "app", time.Now().Add(-age), false)
	}
	r.host.set(func(s *errorlog.Settings, t *[]model.LogCaptureTarget) {
		s.MaxStreams = 2
		*t = []model.LogCaptureTarget{targetNS("shop")}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.m.reconcile(ctx)
	waitFor(t, "streams", func() bool { return len(r.openedIDs()) == 2 })
	ids := strings.Join(r.openedIDs(), ",")
	if strings.Contains(ids, "web-1") { // web-1 is the newest container
		t.Errorf("newest container should be left out: %s", ids)
	}
	if st := r.m.LogCaptureStatus(); st.Streams != 2 || st.MaxStreams != 2 {
		t.Errorf("status: %+v", st)
	}
	cancel()
	r.shutdownWithin(t)
}

func TestErrorLogRateLimitCountsDrops(t *testing.T) {
	r := newLogRig(t)
	r.addPod("shop", "web-1", "app", longAgo, false)
	r.host.set(func(s *errorlog.Settings, t *[]model.LogCaptureTarget) {
		s.MaxLinesPerMinute = 3
		*t = []model.LogCaptureTarget{targetNS("shop")}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.m.reconcile(ctx)
	waitFor(t, "stream", func() bool { return len(r.openedIDs()) == 1 })
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, fmt.Sprintf(`{"level":"error","msg":"fail %d"}`, i))
	}
	r.write(t, "shop/web-1/app", lines...)
	waitFor(t, "drops", func() bool { return r.m.dropped.Load() == 7 })
	if r.m.agg.totalEntries() != 3 {
		t.Errorf("kept %d entries, want 3", r.m.agg.totalEntries())
	}
	if st := r.m.LogCaptureStatus(); st.DroppedLines != 7 {
		t.Errorf("status dropped = %d", st.DroppedLines)
	}
	cancel()
	r.shutdownWithin(t)
}

func TestErrorLogTargetsAndKillSwitchApplyLive(t *testing.T) {
	r := newLogRig(t)
	r.addPod("shop", "web-1", "app", longAgo, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r.m.reconcile(ctx)
	if len(r.openedIDs()) != 0 {
		t.Fatal("nothing is opted in yet")
	}
	r.host.set(func(_ *errorlog.Settings, t *[]model.LogCaptureTarget) {
		*t = []model.LogCaptureTarget{targetNS("shop")}
	})
	r.m.reconcile(ctx)
	waitFor(t, "stream", func() bool { return len(r.openedIDs()) == 1 })

	r.mu.Lock()
	sctx := r.ctxs["shop/web-1/app"]
	r.mu.Unlock()
	r.host.set(func(_ *errorlog.Settings, t *[]model.LogCaptureTarget) { *t = nil })
	r.m.reconcile(ctx)
	waitFor(t, "stream cancelled", func() bool { return sctx.Err() != nil })
	waitFor(t, "stream gone", func() bool { return r.streamCount() == 0 })

	// Re-enabled, then the global kill switch stops it again.
	r.host.set(func(_ *errorlog.Settings, t *[]model.LogCaptureTarget) {
		*t = []model.LogCaptureTarget{targetNS("shop")}
	})
	r.m.resume["shop/web-1/app"].retryAt = time.Time{}
	r.m.reconcile(ctx)
	waitFor(t, "stream again", func() bool { return r.streamCount() == 1 })
	r.host.set(func(s *errorlog.Settings, _ *[]model.LogCaptureTarget) { s.Enabled = false })
	r.m.reconcile(ctx)
	waitFor(t, "kill switch", func() bool { return r.streamCount() == 0 })
	cancel()
	r.shutdownWithin(t)
}

func TestErrorLogReconnectSkipsSeenLines(t *testing.T) {
	r := newLogRig(t)
	r.addPod("shop", "web-1", "app", longAgo, false)
	r.host.set(func(_ *errorlog.Settings, t *[]model.LogCaptureTarget) {
		*t = []model.LogCaptureTarget{targetNS("shop")}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	base := time.Now().Add(time.Second).UTC().Truncate(time.Second)
	ts := func(d time.Duration) string { return base.Add(d).Format(time.RFC3339Nano) }
	r.m.reconcile(ctx)
	waitFor(t, "stream", func() bool { return len(r.openedIDs()) == 1 })
	r.write(t, "shop/web-1/app",
		ts(0)+` {"level":"error","msg":"first"}`,
		ts(time.Second)+` {"level":"error","msg":"second"}`,
		ts(time.Second)+` {"level":"error","msg":"second b"}`,
	)
	waitFor(t, "entries", func() bool { return r.m.agg.totalEntries() >= 2 })
	// Stream ends (connection dropped).
	r.mu.Lock()
	pw := r.pipes["shop/web-1/app"]
	r.mu.Unlock()
	_ = pw.Close()
	waitFor(t, "stream ended", func() bool { return r.streamCount() == 0 })
	waitFor(t, "all three recorded", func() bool { return r.m.agg.totalEntries() == 3 })

	r.m.resume["shop/web-1/app"].retryAt = time.Time{}
	r.m.reconcile(ctx)
	waitFor(t, "reconnected", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.opened["shop/web-1/app"]) == 2
	})
	r.mu.Lock()
	second := r.opened["shop/web-1/app"][1]
	r.mu.Unlock()
	if second.SinceTime == nil || !second.SinceTime.Time.Equal(base.Add(time.Second)) {
		t.Fatalf("reconnect since = %+v, want %v", second.SinceTime, base.Add(time.Second))
	}
	// The server replays from the truncated second: old lines must be skipped.
	r.write(t, "shop/web-1/app",
		ts(0)+` {"level":"error","msg":"first"}`,
		ts(time.Second)+` {"level":"error","msg":"second"}`,
		ts(time.Second)+` {"level":"error","msg":"second b"}`,
		ts(2*time.Second)+` {"level":"error","msg":"third"}`,
	)
	waitFor(t, "fourth entry", func() bool { return r.m.agg.totalEntries() == 4 })
	time.Sleep(100 * time.Millisecond)
	if n := r.m.agg.totalEntries(); n != 4 {
		t.Errorf("entries = %d, duplicates were not skipped", n)
	}
	cancel()
	r.shutdownWithin(t)
}

func TestErrorLogNewContainerInstanceReadsFromStart(t *testing.T) {
	r := newLogRig(t)
	r.addPod("shop", "web-1", "app", longAgo, false)
	r.host.set(func(_ *errorlog.Settings, t *[]model.LogCaptureTarget) {
		*t = []model.LogCaptureTarget{targetNS("shop")}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.m.reconcile(ctx)
	waitFor(t, "stream", func() bool { return len(r.openedIDs()) == 1 })

	// The container restarts: new StartedAt, same name.
	r.addPod("shop", "web-1", "app", time.Now(), false)
	r.m.reconcile(ctx)
	waitFor(t, "old stream stopped", func() bool { return r.streamCount() == 0 })
	r.m.reconcile(ctx)
	waitFor(t, "new stream", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.opened["shop/web-1/app"]) == 2
	})
	r.mu.Lock()
	second := r.opened["shop/web-1/app"][1]
	r.mu.Unlock()
	if second.SinceTime != nil {
		t.Errorf("a new instance must be read from its first line, got since %v", second.SinceTime)
	}
	cancel()
	r.shutdownWithin(t)
}

func TestErrorLogDefaultStreamUsesClientset(t *testing.T) {
	r := newLogRig(t)
	r.c.openLog = nil // fake clientset: GetLogs returns "fake logs"
	r.addPod("shop", "web-1", "app", longAgo, false)
	r.host.set(func(_ *errorlog.Settings, t *[]model.LogCaptureTarget) {
		*t = []model.LogCaptureTarget{targetNS("shop")}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.m.reconcile(ctx)
	waitFor(t, "stream to end", func() bool { return r.streamCount() == 0 })
	cancel()
	r.shutdownWithin(t)
	if len(r.host.all()) != 0 {
		t.Error("\"fake logs\" is not an error")
	}
	var logCalls int
	for _, a := range r.c.clientset.(*fake.Clientset).Actions() {
		if a.GetSubresource() == "log" {
			logCalls++
		}
	}
	if logCalls == 0 {
		t.Error("expected a GetLogs call")
	}
}

func TestErrorLogCollectorStopIsClean(t *testing.T) {
	r := newLogRig(t)
	r.addPod("shop", "web-1", "app", longAgo, false)
	r.host.set(func(_ *errorlog.Settings, t *[]model.LogCaptureTarget) {
		*t = []model.LogCaptureTarget{targetNS("shop")}
	})
	ctx, cancel := context.WithCancel(context.Background())
	r.c.cancel = cancel
	r.c.wg.Add(2)
	go r.m.run(ctx)
	go r.m.flushLoop(ctx)
	waitFor(t, "stream via run loop", func() bool { return r.streamCount() == 1 })
	r.write(t, "shop/web-1/app", `{"level":"error","msg":"last words"}`)
	waitFor(t, "entry", func() bool { return r.m.agg.totalEntries() == 1 })

	done := make(chan struct{})
	go func() { _ = r.c.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop hung")
	}
	if len(r.host.all()) != 1 {
		t.Error("the pending batch must be flushed on stop")
	}
	// Events channel is closed and kicks after Stop are harmless.
	if _, ok := <-r.c.Events(); ok {
		t.Error("events channel should be closed")
	}
	r.c.kickErrorLogs()
}

func TestReadLineAndTimestamp(t *testing.T) {
	long := strings.Repeat("x", 20000)
	br := bufio.NewReaderSize(strings.NewReader("short\n"+long+"\nlast"), 4096)
	l1, err := readLine(br, errorlog.MaxLineLen)
	if l1 != "short" || err != nil {
		t.Fatalf("l1 = %q %v", l1, err)
	}
	l2, err := readLine(br, errorlog.MaxLineLen)
	if len(l2) != errorlog.MaxLineLen || err != nil {
		t.Fatalf("long line: %d %v", len(l2), err)
	}
	l3, err := readLine(br, errorlog.MaxLineLen)
	if l3 != "last" || err != io.EOF {
		t.Fatalf("l3 = %q %v", l3, err)
	}

	ts, rest := splitTimestamp("2024-05-01T10:00:00.123456789Z hello world")
	if ts.IsZero() || rest != "hello world" || ts.Nanosecond() != 123456789 {
		t.Errorf("split = %v %q", ts, rest)
	}
	if ts, rest := splitTimestamp("fake logs"); !ts.IsZero() || rest != "fake logs" {
		t.Errorf("no timestamp: %v %q", ts, rest)
	}
}

func TestAggregatorCapsOccurrencesAndSignatures(t *testing.T) {
	a := &aggregator{sigs: map[string]*sigAgg{}, max: 2}
	s := &logStream{workloadID: "src:ns:Deployment:web", pod: "p", container: "c", id: "ns/p/c", kind: "Deployment", workload: "web"}
	now := time.Now().UTC()
	for i := 0; i < 10; i++ {
		a.add(s, errorlog.Entry{Level: "error", Template: "t1", Sample: fmt.Sprint(i), Message: fmt.Sprint(i)}, now.Add(time.Duration(i)*time.Second), 3)
	}
	a.add(s, errorlog.Entry{Level: "fatal", Template: "t2"}, now, 3)
	a.add(s, errorlog.Entry{Level: "error", Template: "t3"}, now, 3) // over the cap
	b := a.drain()
	if len(b.Signatures) != 2 || a.droppedCount() != 1 {
		t.Fatalf("signatures = %d dropped = %d", len(b.Signatures), a.droppedCount())
	}
	for _, sg := range b.Signatures {
		if sg.Template == "t1" {
			if sg.Total != 10 || len(sg.Occurrences) != 3 || sg.Occurrences[2].Message != "9" || sg.Sample != "9" {
				t.Errorf("t1: %+v", sg)
			}
		} else if sg.Level != "fatal" {
			t.Errorf("t2: %+v", sg)
		}
	}
	if a.drain() != nil {
		t.Error("drain must reset")
	}
}
