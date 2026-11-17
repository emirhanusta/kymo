package kubernetes

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/emirhanusta/kymo/internal/errorlog"
	"github.com/emirhanusta/kymo/internal/model"
)

// Tunables of error-log streaming.
const (
	errFlushEvery      = 5 * time.Second
	errTick            = time.Second
	errFullReconcile   = 10 // ticks between reconciles when nothing kicked
	errRetryAfterEnd   = 3 * time.Second
	errRetryAfterFail  = 15 * time.Second
	errLineChan        = 256
	errMaxAggregated   = 5000
	errLastErrorMaxAge = 10 * time.Minute
	errIngestTimeout   = 10 * time.Second
)

// ErrorLogHost is what the collector needs from the error-log service:
// live settings and targets, a change feed, ingestion and status registration.
type ErrorLogHost interface {
	Settings() errorlog.Settings
	Targets() []model.LogCaptureTarget
	// Subscribe signals changes of settings / targets.
	Subscribe() (<-chan struct{}, func())
	// Ingest stores one aggregated batch.
	Ingest(ctx context.Context, b *model.ErrorBatch) error
	// Register exposes the source's live status; the returned func undoes it.
	Register(source string, p errorlog.StatusProvider) func()
}

// logOpener opens a follow stream for one container (replaceable in tests).
type logOpener func(ctx context.Context, namespace, pod string, opts *corev1.PodLogOptions) (io.ReadCloser, error)

// kickErrorLogs asks the error-log manager to re-evaluate its streams soon.
func (c *Collector) kickErrorLogs() {
	if m := c.errMgr; m != nil {
		m.kickNow()
	}
}

// ---------------------------------------------------------------------------
// Manager
// ---------------------------------------------------------------------------

type resumeState struct {
	attached   bool
	startedAt  time.Time // container instance the position refers to
	from       time.Time // lines older than this were seen already
	keysAtFrom map[string]struct{}
	retryAt    time.Time
}

type logStream struct {
	id, namespace, pod, container string
	workloadID, kind, workload    string
	startedAt                     time.Time
	cancel                        context.CancelFunc
	done                          chan struct{}

	rlMinute int64
	rlCount  int
}

type errorLogManager struct {
	c       *Collector
	host    ErrorLogHost
	created time.Time
	kick    chan struct{}
	agg     *aggregator

	mu        sync.Mutex
	streams   map[string]*logStream
	resume    map[string]*resumeState
	lastErr   string
	lastErrAt time.Time

	dropped atomic.Int64
	swg     sync.WaitGroup
}

func newErrorLogManager(c *Collector, host ErrorLogHost) *errorLogManager {
	return &errorLogManager{
		c: c, host: host, created: time.Now(),
		kick:    make(chan struct{}, 1),
		agg:     &aggregator{sigs: map[string]*sigAgg{}, max: errMaxAggregated},
		streams: map[string]*logStream{},
		resume:  map[string]*resumeState{},
	}
}

func (m *errorLogManager) kickNow() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

func (m *errorLogManager) setErr(format string, args ...any) {
	m.mu.Lock()
	m.lastErr, m.lastErrAt = fmt.Sprintf(format, args...), time.Now()
	m.mu.Unlock()
}

// run reconciles streams until ctx ends, then stops them and flushes.
func (m *errorLogManager) run(ctx context.Context) {
	defer m.c.wg.Done()
	sub, unsub := m.host.Subscribe()
	defer unsub()
	unreg := m.host.Register(m.c.sourceName, m)
	defer unreg()

	tick := time.NewTicker(errTick)
	defer tick.Stop()
	pending, ticks := true, 0
	for {
		select {
		case <-ctx.Done():
			m.shutdown()
			return
		case <-m.kick:
			pending = true
		case <-sub:
			pending = true
		case <-tick.C:
			ticks++
			if pending || ticks%errFullReconcile == 0 {
				pending = false
				m.reconcile(ctx)
			}
		}
	}
}

func (m *errorLogManager) flushLoop(ctx context.Context) {
	defer m.c.wg.Done()
	t := time.NewTicker(errFlushEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.flush(ctx)
		}
	}
}

func (m *errorLogManager) shutdown() {
	m.mu.Lock()
	for _, s := range m.streams {
		s.cancel()
	}
	m.mu.Unlock()
	m.swg.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), errIngestTimeout)
	defer cancel()
	m.flush(ctx)
}

func (m *errorLogManager) flush(ctx context.Context) {
	b := m.agg.drain()
	if b == nil {
		return
	}
	ictx, cancel := context.WithTimeout(ctx, errIngestTimeout)
	defer cancel()
	if err := m.host.Ingest(ictx, b); err != nil {
		m.c.log.Warn("kubernetes: storing error logs failed; batch dropped",
			zap.String("source", m.c.sourceName), zap.Int("signatures", len(b.Signatures)), zap.Error(err))
		m.setErr("storing error logs: %v", err)
	}
}

type wantStream struct {
	id, namespace, pod, container string
	workloadID, kind, workload    string
	startedAt                     time.Time
}

// reconcile starts and stops streams so that they match the opted-in running
// containers (bounded by max_streams_per_source, oldest container first).
func (m *errorLogManager) reconcile(ctx context.Context) {
	c := m.c
	if c.podLister == nil || ctx.Err() != nil {
		return
	}
	st := m.host.Settings()
	targets := m.host.Targets()
	pods, err := c.podLister.List(labels.Everything())
	if err != nil {
		return
	}

	var wants []wantStream
	known := map[string]struct{}{}
	for _, pod := range pods {
		for _, cs := range pod.Status.ContainerStatuses {
			known[containerID(pod.Namespace, pod.Name, cs.Name)] = struct{}{}
		}
		if !st.Enabled || pod.DeletionTimestamp != nil {
			continue
		}
		c.mu.RLock()
		wl, ok := c.workloadCache[podKey(pod)]
		c.mu.RUnlock()
		if !ok {
			continue
		}
		wid := model.WorkloadID(c.sourceName, pod.Namespace, wl.Kind, wl.Name)
		if wid == "" {
			continue
		}
		if pod.Annotations[model.AnnotationCaptureErrorLogs] != "true" && !model.MatchesTarget(targets, c.sourceName, pod.Namespace, wid) {
			continue
		}
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Running == nil {
				continue
			}
			wants = append(wants, wantStream{
				id: containerID(pod.Namespace, pod.Name, cs.Name), namespace: pod.Namespace, pod: pod.Name, container: cs.Name,
				workloadID: wid, kind: wl.Kind, workload: wl.Name, startedAt: cs.State.Running.StartedAt.Time,
			})
		}
	}
	sort.Slice(wants, func(i, j int) bool {
		if !wants[i].startedAt.Equal(wants[j].startedAt) {
			return wants[i].startedAt.Before(wants[j].startedAt)
		}
		return wants[i].id < wants[j].id
	})
	if st.MaxStreams > 0 && len(wants) > st.MaxStreams {
		wants = wants[:st.MaxStreams]
	}
	wantByID := make(map[string]wantStream, len(wants))
	for _, w := range wants {
		wantByID[w.id] = w
	}

	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, s := range m.streams {
		select {
		case <-s.done:
			delete(m.streams, id)
			continue
		default:
		}
		if w, ok := wantByID[id]; !ok || !w.startedAt.Equal(s.startedAt) {
			s.cancel()
		}
	}
	for id := range m.resume {
		if _, ok := known[id]; !ok && m.streams[id] == nil {
			delete(m.resume, id)
		}
	}
	for _, w := range wants {
		if _, running := m.streams[w.id]; running {
			continue
		}
		r := m.resume[w.id]
		if r == nil {
			r = &resumeState{}
			m.resume[w.id] = r
		}
		if now.Before(r.retryAt) && r.startedAt.Equal(w.startedAt) { // a new instance starts at once
			continue
		}
		sctx, cancel := context.WithCancel(ctx)
		s := &logStream{
			id: w.id, namespace: w.namespace, pod: w.pod, container: w.container,
			workloadID: w.workloadID, kind: w.kind, workload: w.workload, startedAt: w.startedAt,
			cancel: cancel, done: make(chan struct{}),
		}
		m.streams[w.id] = s
		m.swg.Add(1)
		go m.runStream(sctx, s, r)
	}
}

// ---------------------------------------------------------------------------
// One container stream
// ---------------------------------------------------------------------------

type rawLine struct {
	text string
	err  error
}

func (m *errorLogManager) runStream(ctx context.Context, s *logStream, r *resumeState) {
	defer m.swg.Done()
	defer m.kickNow()
	defer close(s.done)
	defer s.cancel()
	retry := errRetryAfterEnd
	defer func() { r.retryAt = time.Now().Add(retry) }()

	opts := &corev1.PodLogOptions{Container: s.container, Follow: true, Timestamps: true}
	switch {
	case !r.attached:
		r.attached, r.startedAt = true, s.startedAt
		if !s.startedAt.After(m.created) {
			// Already running when capture began: only new lines.
			r.from, r.keysAtFrom = time.Now(), nil
		}
	case !r.startedAt.Equal(s.startedAt):
		// New container instance: read it from its first line.
		r.startedAt, r.from, r.keysAtFrom = s.startedAt, time.Time{}, nil
	}
	if !r.from.IsZero() {
		t := metav1.NewTime(r.from)
		opts.SinceTime = &t
	}

	body, err := m.c.openLogStream(ctx, s.namespace, s.pod, opts)
	if err != nil {
		if ctx.Err() == nil {
			m.c.log.Debug("kubernetes: cannot follow container logs",
				zap.String("container", s.id), zap.Error(err))
			m.setErr("%s: %v", s.id, err)
		}
		retry = errRetryAfterFail
		return
	}
	defer body.Close()

	lines := make(chan rawLine, errLineChan)
	m.swg.Add(1)
	go func() {
		defer m.swg.Done()
		defer close(lines)
		br := bufio.NewReaderSize(body, 16*1024)
		for {
			text, err := readLine(br, errorlog.MaxLineLen)
			if text != "" || err == nil {
				select {
				case lines <- rawLine{text: text}:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				select {
				case lines <- rawLine{err: err}:
				case <-ctx.Done():
				}
				return
			}
		}
	}()

	g := errorlog.NewGrouper(0, 0)
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()

	for {
		var timerC <-chan time.Time
		if d, ok := g.NextDeadline(time.Now()); ok {
			timer.Reset(d)
			timerC = timer.C
		}
		select {
		case <-ctx.Done():
			body.Close()
			m.recordGroups(s, g.Flush())
			return
		case rl, ok := <-lines:
			if !ok {
				m.recordGroups(s, g.Flush())
				return
			}
			if rl.err != nil {
				m.recordGroups(s, g.Flush())
				if !errors.Is(rl.err, io.EOF) && ctx.Err() == nil {
					m.setErr("%s: %v", s.id, rl.err)
					retry = errRetryAfterFail
				}
				return
			}
			m.handleLine(s, r, g, rl.text)
		case <-timerC:
			m.recordGroups(s, g.Idle(time.Now()))
		}
	}
}

func (m *errorLogManager) handleLine(s *logStream, r *resumeState, g *errorlog.Grouper, raw string) {
	ts, text := splitTimestamp(raw)
	if !ts.IsZero() {
		key := ts.Format(time.RFC3339Nano) + "\x00" + text
		switch {
		case ts.Before(r.from):
			return
		case ts.Equal(r.from):
			if _, dup := r.keysAtFrom[key]; dup {
				return
			}
			r.keysAtFrom[key] = struct{}{}
		default:
			r.from, r.keysAtFrom = ts, map[string]struct{}{key: {}}
		}
	}
	st := m.host.Settings()
	m.recordGroups(s, g.Add(st.Analyzer, text, ts, time.Now()))
}

func (m *errorLogManager) recordGroups(s *logStream, groups []errorlog.Group) {
	if len(groups) == 0 {
		return
	}
	st := m.host.Settings()
	for _, grp := range groups {
		entry, ok := st.Analyzer.Analyze(grp.Lines)
		if !ok {
			continue
		}
		now := time.Now()
		if minute := now.Unix() / 60; minute != s.rlMinute {
			s.rlMinute, s.rlCount = minute, 0
		}
		s.rlCount++
		if st.MaxLinesPerMinute > 0 && s.rlCount > st.MaxLinesPerMinute {
			m.dropped.Add(1)
			continue
		}
		ts := grp.Time
		if ts.IsZero() {
			ts = now
		}
		if !m.agg.add(s, entry, ts.UTC(), st.SamplesPerSignature) {
			m.dropped.Add(1)
		}
	}
}

// readLine reads one line of at most max bytes (the rest of a longer line is
// discarded). It returns io.EOF together with a trailing unterminated line.
func readLine(br *bufio.Reader, max int) (string, error) {
	var buf []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if room := max - len(buf); room > 0 {
			if len(chunk) > room {
				chunk = chunk[:room]
			}
			buf = append(buf, chunk...)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		text := strings.TrimRight(string(buf), "\r\n")
		return strings.ToValidUTF8(text, "�"), err
	}
}

// splitTimestamp separates the RFC3339Nano prefix added by Timestamps=true.
// Lines without a parsable prefix are returned as they are.
func splitTimestamp(line string) (time.Time, string) {
	i := strings.IndexByte(line, ' ')
	if i < 20 || i > 40 || line[4] != '-' {
		return time.Time{}, line
	}
	t, err := time.Parse(time.RFC3339Nano, line[:i])
	if err != nil {
		return time.Time{}, line
	}
	return t.UTC(), line[i+1:]
}

// ---------------------------------------------------------------------------
// Opening streams
// ---------------------------------------------------------------------------

func (c *Collector) openLogStream(ctx context.Context, namespace, pod string, opts *corev1.PodLogOptions) (io.ReadCloser, error) {
	if c.openLog != nil {
		return c.openLog(ctx, namespace, pod, opts)
	}
	if c.clientset == nil {
		return nil, errors.New("kubernetes: no client")
	}
	return c.clientset.CoreV1().Pods(namespace).GetLogs(pod, opts).Stream(ctx)
}

// ---------------------------------------------------------------------------
// Status (errorlog.StatusProvider)
// ---------------------------------------------------------------------------

// LogCaptureStatus reports the live capture state of this source.
func (m *errorLogManager) LogCaptureStatus() model.LogCaptureSourceStatus {
	m.mu.Lock()
	live := 0
	for _, s := range m.streams {
		select {
		case <-s.done:
		default:
			live++
		}
	}
	lastErr := ""
	if time.Since(m.lastErrAt) < errLastErrorMaxAge {
		lastErr = m.lastErr
	}
	m.mu.Unlock()
	return model.LogCaptureSourceStatus{
		SourceName: m.c.sourceName, Streams: live, MaxStreams: m.host.Settings().MaxStreams,
		DroppedLines: m.dropped.Load() + m.agg.droppedCount(), LastError: lastErr,
	}
}

// WorkloadCapture reports the streams of a workload and whether one of its
// pods carries the opt-in annotation.
func (m *errorLogManager) WorkloadCapture(workloadID string) (int, bool) {
	m.mu.Lock()
	n := 0
	for _, s := range m.streams {
		select {
		case <-s.done:
		default:
			if s.workloadID == workloadID {
				n++
			}
		}
	}
	m.mu.Unlock()

	annotated := false
	_, ns, _, _, ok := model.SplitWorkloadID(workloadID)
	if ok && m.c.podLister != nil {
		pods, err := m.c.podLister.Pods(ns).List(labels.Everything())
		if err == nil {
			m.c.mu.RLock()
			for _, pod := range pods {
				if pod.Annotations[model.AnnotationCaptureErrorLogs] != "true" {
					continue
				}
				wl, known := m.c.workloadCache[podKey(pod)]
				if known && model.WorkloadID(m.c.sourceName, ns, wl.Kind, wl.Name) == workloadID {
					annotated = true
					break
				}
			}
			m.c.mu.RUnlock()
		}
	}
	return n, annotated
}

// ---------------------------------------------------------------------------
// Aggregation
// ---------------------------------------------------------------------------

type bucketAgg struct {
	count int
	pods  map[string]struct{}
}

type sigAgg struct {
	delta   model.ErrorSignatureDelta
	buckets map[int64]*bucketAgg
}

type aggregator struct {
	mu      sync.Mutex
	sigs    map[string]*sigAgg
	max     int
	dropped int64
}

func (a *aggregator) droppedCount() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.dropped
}

// add records one entry; false when the signature table is full.
func (a *aggregator) add(s *logStream, e errorlog.Entry, ts time.Time, keep int) bool {
	id := errorlog.SignatureID(s.workloadID, e.Template)
	a.mu.Lock()
	defer a.mu.Unlock()
	sg := a.sigs[id]
	if sg == nil {
		if len(a.sigs) >= a.max {
			a.dropped++
			return true // counted by the aggregator itself
		}
		src, ns, _, _, _ := model.SplitWorkloadID(s.workloadID)
		sg = &sigAgg{buckets: map[int64]*bucketAgg{}, delta: model.ErrorSignatureDelta{
			ID: id, SourceName: src, WorkloadID: s.workloadID, Namespace: ns, Kind: s.kind, Name: s.workload,
			Level: e.Level, Template: e.Template, Sample: e.Sample, FirstSeen: ts, LastSeen: ts,
		}}
		a.sigs[id] = sg
	}
	d := &sg.delta
	d.Total++
	if ts.Before(d.FirstSeen) {
		d.FirstSeen = ts
	}
	if !ts.Before(d.LastSeen) {
		d.LastSeen, d.Sample = ts, e.Sample
	}
	if e.Level == model.ErrorLevelFatal {
		d.Level = model.ErrorLevelFatal
	}
	mk := ts.Truncate(time.Minute).Unix()
	b := sg.buckets[mk]
	if b == nil {
		b = &bucketAgg{pods: map[string]struct{}{}}
		sg.buckets[mk] = b
	}
	b.count++
	b.pods[s.pod] = struct{}{}

	d.Occurrences = append(d.Occurrences, model.ErrorOccurrence{
		Time: ts, ContainerID: s.id, Pod: s.pod, Container: s.container, Level: e.Level, Message: e.Message,
	})
	if keep > 0 && len(d.Occurrences) > keep {
		d.Occurrences = append([]model.ErrorOccurrence(nil), d.Occurrences[len(d.Occurrences)-keep:]...)
	}
	return true
}

// drain returns everything aggregated so far (nil when empty) and resets.
func (a *aggregator) drain() *model.ErrorBatch {
	a.mu.Lock()
	sigs := a.sigs
	a.sigs = map[string]*sigAgg{}
	a.mu.Unlock()
	if len(sigs) == 0 {
		return nil
	}
	b := &model.ErrorBatch{Signatures: make([]model.ErrorSignatureDelta, 0, len(sigs))}
	for _, sg := range sigs {
		d := sg.delta
		keys := make([]int64, 0, len(sg.buckets))
		for k := range sg.buckets {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		for _, k := range keys {
			bk := sg.buckets[k]
			pods := make([]string, 0, len(bk.pods))
			for p := range bk.pods {
				pods = append(pods, p)
			}
			sort.Strings(pods)
			d.Buckets = append(d.Buckets, model.ErrorBucketDelta{Bucket: time.Unix(k, 0).UTC(), Count: bk.count, Pods: pods})
		}
		b.Signatures = append(b.Signatures, d)
	}
	sort.Slice(b.Signatures, func(i, j int) bool { return b.Signatures[i].ID < b.Signatures[j].ID })
	return b
}
