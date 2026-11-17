// Package errorlogs manages opt-in error-log capture: the live settings and
// targets the collectors read, ingestion of aggregated batches, the targets
// API and the queries behind the error endpoints.
package errorlogs

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/config"
	"github.com/emirhanusta/kymo/internal/errorlog"
	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

// Errors reported by the service.
var (
	// ErrTargetNotFound: no such target.
	ErrTargetNotFound = errors.New("log capture target not found")
	// ErrTargetLocked: the target is declared in config.yaml.
	ErrTargetLocked = errors.New("this target is declared in config.yaml and cannot be changed here")
	// ErrDuplicate: the target already exists.
	ErrDuplicate = errors.New("a log capture target for this scope already exists")
	// ErrBadWorkload: the workload id is malformed.
	ErrBadWorkload = errors.New("malformed workload id")
	// ErrSignatureNotFound: no such signature.
	ErrSignatureNotFound = errors.New("error signature not found")
)

// ValidationError is a rejected input (HTTP 400).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// EnabledViaError is returned when capture cannot be turned off for a
// workload because it is enabled through a namespace target, an annotation or
// config.yaml (HTTP 409).
type EnabledViaError struct{ Via string }

func (e *EnabledViaError) Error() string {
	return "log capture is enabled via " + e.Via + " and cannot be turned off here"
}

// SourceLookup resolves registered sources (the recorder implements it).
type SourceLookup interface {
	Source(name string) (model.SourceSpec, bool)
}

// Service implements capture settings, targets and queries.
type Service struct {
	repo    repository.ErrorLogRepository
	sources SourceLookup
	log     *zap.Logger

	settings atomic.Pointer[errorlog.Settings]
	targets  atomic.Pointer[[]model.LogCaptureTarget]
	enabled  atomic.Bool

	retention time.Duration

	mu        sync.Mutex
	providers map[string]errorlog.StatusProvider
	subs      map[int]chan struct{}
	nextSub   int
}

// New builds the service from the initial configuration. sources may be nil.
func New(repo repository.ErrorLogRepository, cfg config.LogCaptureConfig, sources SourceLookup, log *zap.Logger) (*Service, error) {
	if log == nil {
		log = zap.NewNop()
	}
	s := &Service{
		repo: repo, sources: sources, log: log,
		providers: map[string]errorlog.StatusProvider{}, subs: map[int]chan struct{}{},
	}
	if err := s.applySettings(cfg); err != nil {
		return nil, err
	}
	empty := []model.LogCaptureTarget{}
	s.targets.Store(&empty)
	return s, nil
}

func settingsFrom(cfg config.LogCaptureConfig) (errorlog.Settings, error) {
	a, err := errorlog.NewAnalyzer(cfg.ExtraPatterns, cfg.IgnorePatterns)
	if err != nil {
		return errorlog.Settings{}, err
	}
	return errorlog.Settings{
		Enabled: cfg.Enabled, MaxStreams: cfg.MaxStreamsPerSource, MaxLinesPerMinute: cfg.MaxLinesPerMinute,
		SamplesPerSignature: cfg.SamplesPerSignature, Analyzer: a,
	}, nil
}

func (s *Service) applySettings(cfg config.LogCaptureConfig) error {
	st, err := settingsFrom(cfg)
	if err != nil {
		return err
	}
	s.settings.Store(&st)
	s.enabled.Store(cfg.Enabled)
	if d, err := cfg.RetentionDuration(); err == nil {
		s.retention = d
	}
	return nil
}

// ApplyConfig applies a (re)loaded log_capture section live: settings,
// patterns and the config.yaml targets. Retention needs a restart.
func (s *Service) ApplyConfig(ctx context.Context, cfg config.LogCaptureConfig) error {
	if err := s.applySettings(cfg); err != nil {
		return err
	}
	if err := s.repo.SyncConfigTargets(ctx, cfg.TargetSpecs()); err != nil {
		return err
	}
	if err := s.Refresh(ctx); err != nil {
		return err
	}
	s.notify()
	return nil
}

// Refresh reloads the targets snapshot from the database and wakes subscribers
// when it changed.
func (s *Service) Refresh(ctx context.Context) error {
	list, err := s.repo.ListTargets(ctx)
	if err != nil {
		return err
	}
	snap := make([]model.LogCaptureTarget, len(list))
	for i, t := range list {
		snap[i] = *t
	}
	old := s.targets.Load()
	s.targets.Store(&snap)
	if old == nil || !sameTargets(*old, snap) {
		s.notify()
	}
	return nil
}

func sameTargets(a, b []model.LogCaptureTarget) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Origin != b[i].Origin {
			return false
		}
	}
	return true
}

// Run applies the retention policy, then refreshes the targets and removes
// stale signatures until ctx ends.
func (s *Service) Run(ctx context.Context) {
	if s.retention > 0 {
		rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		if err := s.repo.SetRetention(rctx, s.retention); err != nil {
			s.log.Warn("applying log_capture.retention failed; the default (7d) stays", zap.Error(err))
		}
		cancel()
	}
	refresh := time.NewTicker(30 * time.Second)
	defer refresh.Stop()
	cleanup := time.NewTicker(time.Hour)
	defer cleanup.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-refresh.C:
			if err := s.Refresh(ctx); err != nil && ctx.Err() == nil {
				s.log.Warn("refreshing log capture targets failed", zap.Error(err))
			}
		case <-cleanup.C:
			ret := s.retention
			if ret <= 0 {
				ret = 7 * 24 * time.Hour
			}
			n, err := s.repo.DeleteStale(ctx, time.Now().UTC().Add(-ret))
			if err != nil && ctx.Err() == nil {
				s.log.Warn("deleting stale error signatures failed", zap.Error(err))
			} else if n > 0 {
				s.log.Info("stale error signatures deleted", zap.Int64("count", n))
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Host side (what the collectors use)
// ---------------------------------------------------------------------------

// Settings returns the current capture settings.
func (s *Service) Settings() errorlog.Settings { return *s.settings.Load() }

// Targets returns the current targets snapshot.
func (s *Service) Targets() []model.LogCaptureTarget { return *s.targets.Load() }

// Subscribe returns a channel that receives a signal whenever settings or
// targets change, and a function to cancel the subscription.
func (s *Service) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	id := s.nextSub
	s.nextSub++
	s.subs[id] = ch
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.subs, id)
		s.mu.Unlock()
	}
}

func (s *Service) notify() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Ingest stores one aggregated batch.
func (s *Service) Ingest(ctx context.Context, b *model.ErrorBatch) error {
	return s.repo.SaveBatch(ctx, b, s.Settings().SamplesPerSignature)
}

// Register makes a running source visible to the status endpoints.
func (s *Service) Register(source string, p errorlog.StatusProvider) (unregister func()) {
	s.mu.Lock()
	s.providers[source] = p
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		if s.providers[source] == p {
			delete(s.providers, source)
		}
		s.mu.Unlock()
	}
}

func (s *Service) provider(source string) errorlog.StatusProvider {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.providers[source]
}

// Status reports the live state of every running source.
func (s *Service) Status() model.LogCaptureStatus {
	s.mu.Lock()
	provs := make([]errorlog.StatusProvider, 0, len(s.providers))
	for _, p := range s.providers {
		provs = append(provs, p)
	}
	s.mu.Unlock()
	out := model.LogCaptureStatus{Enabled: s.enabled.Load(), Sources: make([]model.LogCaptureSourceStatus, 0, len(provs))}
	for _, p := range provs {
		out.Sources = append(out.Sources, p.LogCaptureStatus())
	}
	sort.Slice(out.Sources, func(i, j int) bool { return out.Sources[i].SourceName < out.Sources[j].SourceName })
	return out
}

// ---------------------------------------------------------------------------
// Targets API
// ---------------------------------------------------------------------------

// TargetInput is the body of POST /log-capture/targets.
type TargetInput struct {
	SourceName string `json:"source_name"`
	Namespace  string `json:"namespace"`
	WorkloadID string `json:"workload_id"`
}

// ListTargets returns every target.
func (s *Service) ListTargets(ctx context.Context) ([]*model.LogCaptureTarget, error) {
	return s.repo.ListTargets(ctx)
}

func invalid(format string, args ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

func (s *Service) checkSource(name string) error {
	if s.sources == nil {
		return nil
	}
	spec, ok := s.sources.Source(name)
	if !ok {
		return invalid("unknown source %q", name)
	}
	if spec.Type != model.SourceTypeKubernetes {
		return invalid("source %q is not a kubernetes source", name)
	}
	return nil
}

// CreateTarget opts a namespace or workload in.
func (s *Service) CreateTarget(ctx context.Context, in TargetInput) (*model.LogCaptureTarget, error) {
	if in.SourceName == "" {
		return nil, invalid("source_name is required")
	}
	if err := s.checkSource(in.SourceName); err != nil {
		return nil, err
	}
	if in.WorkloadID != "" {
		src, ns, _, _, ok := model.SplitWorkloadID(in.WorkloadID)
		if !ok {
			return nil, invalid("workload_id %q is malformed (want source:namespace:Kind:name)", in.WorkloadID)
		}
		if src != in.SourceName {
			return nil, invalid("workload_id belongs to source %q, not %q", src, in.SourceName)
		}
		if in.Namespace == "" {
			in.Namespace = ns
		} else if ns != in.Namespace {
			return nil, invalid("workload_id belongs to namespace %q, not %q", ns, in.Namespace)
		}
	}
	if in.Namespace == "" {
		return nil, invalid("namespace is required")
	}
	t := &model.LogCaptureTarget{SourceName: in.SourceName, Namespace: in.Namespace, WorkloadID: in.WorkloadID, Origin: model.TargetOriginUI}
	if err := s.repo.CreateTarget(ctx, t); err != nil {
		if errors.Is(err, repository.ErrConflict) {
			return nil, ErrDuplicate
		}
		return nil, err
	}
	_ = s.Refresh(ctx)
	return t, nil
}

// DeleteTarget removes a UI target.
func (s *Service) DeleteTarget(ctx context.Context, id int64) error {
	t, err := s.repo.GetTarget(ctx, id)
	if errors.Is(err, repository.ErrNotFound) {
		return ErrTargetNotFound
	}
	if err != nil {
		return err
	}
	if t.Origin == model.TargetOriginConfig {
		return ErrTargetLocked
	}
	if err := s.repo.DeleteTarget(ctx, id); err != nil && !errors.Is(err, repository.ErrNotFound) {
		return err
	}
	_ = s.Refresh(ctx)
	return nil
}

// WorkloadCapture is the capture state of one workload.
type WorkloadCapture struct {
	Enabled       bool   `json:"enabled"`
	Via           any    `json:"via"` // "workload" | "namespace" | "annotation" | "config" | null
	TargetID      *int64 `json:"target_id"`
	Streams       int    `json:"streams"`
	GlobalEnabled bool   `json:"global_enabled"`
}

// WorkloadCapture reports whether a workload is opted in and how. Enabled
// reflects the opt-in; GlobalEnabled tells whether the kill switch allows it.
func (s *Service) WorkloadCapture(ctx context.Context, workloadID string) (*WorkloadCapture, error) {
	src, ns, _, _, ok := model.SplitWorkloadID(workloadID)
	if !ok {
		return nil, ErrBadWorkload
	}
	targets, err := s.repo.ListTargets(ctx)
	if err != nil {
		return nil, err
	}
	out := &WorkloadCapture{GlobalEnabled: s.enabled.Load()}
	var wt, nt, ct *model.LogCaptureTarget
	for _, t := range targets {
		if t.SourceName != src || t.Namespace != ns || (t.WorkloadID != "" && t.WorkloadID != workloadID) {
			continue
		}
		switch {
		case t.Origin == model.TargetOriginConfig:
			if ct == nil || t.WorkloadID != "" {
				ct = t
			}
		case t.WorkloadID != "":
			wt = t
		default:
			nt = t
		}
	}
	annotated := false
	if p := s.provider(src); p != nil {
		out.Streams, annotated = p.WorkloadCapture(workloadID)
	}
	switch {
	case wt != nil:
		out.Via, out.TargetID = "workload", &wt.ID
	case nt != nil:
		out.Via, out.TargetID = "namespace", &nt.ID
	case annotated:
		out.Via = "annotation"
	case ct != nil:
		out.Via, out.TargetID = "config", &ct.ID
	}
	out.Enabled = out.Via != nil
	return out, nil
}

// SetWorkloadCapture turns the workload's own target on or off.
func (s *Service) SetWorkloadCapture(ctx context.Context, workloadID string, enabled bool) (*WorkloadCapture, error) {
	src, ns, _, _, ok := model.SplitWorkloadID(workloadID)
	if !ok {
		return nil, ErrBadWorkload
	}
	cur, err := s.WorkloadCapture(ctx, workloadID)
	if err != nil {
		return nil, err
	}
	if enabled {
		if cur.Via == "workload" {
			return cur, nil
		}
		if err := s.checkSource(src); err != nil {
			return nil, err
		}
		_, err := s.CreateTarget(ctx, TargetInput{SourceName: src, Namespace: ns, WorkloadID: workloadID})
		if err != nil && !errors.Is(err, ErrDuplicate) {
			return nil, err
		}
		return s.WorkloadCapture(ctx, workloadID)
	}
	if cur.Via == "workload" && cur.TargetID != nil {
		if err := s.DeleteTarget(ctx, *cur.TargetID); err != nil && !errors.Is(err, ErrTargetNotFound) {
			return nil, err
		}
		return s.WorkloadCapture(ctx, workloadID)
	}
	if cur.Via != nil {
		return nil, &EnabledViaError{Via: cur.Via.(string)}
	}
	return cur, nil
}

// ---------------------------------------------------------------------------
// Queries
// ---------------------------------------------------------------------------

// Query limits.
const (
	DefaultLimit = 100
	MaxLimit     = 500
	// DefaultWindow is the window of signature listings.
	DefaultWindow = 24 * time.Hour
)

// ListSignatures returns signatures seen within the last window.
func (s *Service) ListSignatures(ctx context.Context, q repository.ErrorQuery, window time.Duration) ([]*model.ErrorSignature, error) {
	if window <= 0 {
		window = DefaultWindow
	}
	now := time.Now().UTC()
	q.Until, q.Since = now, now.Add(-window)
	if q.Limit <= 0 {
		q.Limit = DefaultLimit
	}
	if q.Limit > MaxLimit {
		q.Limit = MaxLimit
	}
	return s.repo.ListSignatures(ctx, q)
}

// GetSignature returns one signature with 24h window counts. A non-empty
// workloadID must match the signature's workload.
func (s *Service) GetSignature(ctx context.Context, id, workloadID string) (*model.ErrorSignature, error) {
	now := time.Now().UTC()
	sig, err := s.repo.GetSignature(ctx, id, now.Add(-DefaultWindow), now)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, ErrSignatureNotFound
	}
	if err != nil {
		return nil, err
	}
	if workloadID != "" && sig.WorkloadID != workloadID {
		return nil, ErrSignatureNotFound
	}
	return sig, nil
}

// ListOccurrences returns the stored occurrences strictly older than before.
func (s *Service) ListOccurrences(ctx context.Context, id string, before time.Time, limit int) ([]*model.ErrorOccurrence, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	return s.repo.ListOccurrences(ctx, id, before, limit)
}
