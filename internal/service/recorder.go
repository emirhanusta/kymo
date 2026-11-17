// Package service contains the core application services that orchestrate
// collectors, repositories, and domain logic for the KYMO.
package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/collector"
	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
	"github.com/emirhanusta/kymo/internal/service/stability"
)

// RecorderService is the primary application service. It coordinates all active
// collectors, fans in their event streams, persists data via repositories, and
// exposes query methods consumed by HTTP handlers.
type RecorderService interface {
	// Run starts the event processing loop. It starts all registered collectors,
	// fans in their event channels, and persists every incoming event. Run blocks
	// until ctx is cancelled, then shuts down all collectors and returns.
	Run(ctx context.Context) error

	// GetContainer returns the current persisted state for the container identified by id.
	// Returns (nil, nil) when the container is not found.
	GetContainer(ctx context.Context, id string) (*model.Container, error)

	// ListContainers returns all active containers matching the filter.
	ListContainers(ctx context.Context, f repository.ContainerFilter) ([]*model.Container, error)

	// ListContainersPaged returns one page of containers matching the filter and
	// the total count. page is 1-based; limit is clamped to [1, 200].
	// sortField and sortDir are forwarded to the repository (validated there).
	ListContainersPaged(ctx context.Context, f repository.ContainerFilter, page, limit int, sortField, sortDir string) ([]*model.Container, int64, error)

	// ListSources returns the list of configured collector sources with live
	// container counts. It is used to populate the source selector in the frontend.
	ListSources(ctx context.Context) ([]SourceStatus, error)

	// GetStabilityScore returns the most recently computed stability score for the
	// given containerID. Returns (nil, nil) when no score exists yet.
	GetStabilityScore(ctx context.Context, containerID string) (*model.StabilityScore, error)

	// GetScoreHistory returns the n most recent stability scores for the given
	// containerID, ordered by CalculatedAt descending.
	GetScoreHistory(ctx context.Context, containerID string, n int) ([]*model.StabilityScore, error)

	// GetLifecycleEvents returns the most recent limit lifecycle events for the
	// given containerID, ordered by OccurredAt ascending.
	GetLifecycleEvents(ctx context.Context, containerID string, limit int) ([]*model.LifecycleEvent, error)

	// GetResourceSamples returns resource samples for the given containerID within
	// the closed interval [from, to], ordered by SampledAt ascending. Ranges longer
	// than a few hours are downsampled to roughly maxResourcePoints points.
	GetResourceSamples(ctx context.Context, containerID string, from, to time.Time) ([]*model.ResourceSample, error)

	// SoftDeleteContainer archives a container by setting its deleted_at timestamp.
	// The container is excluded from active listings but all history is preserved.
	// If a collector observes the container again it will be automatically restored.
	SoftDeleteContainer(ctx context.Context, id string) error

	// IgnoreContainer permanently suppresses tracking for a container. The container
	// is excluded from all active listings and the in-memory ignore list is updated
	// so new events from collectors are silently dropped without a DB round-trip.
	IgnoreContainer(ctx context.Context, id string) error

	// RestoreContainer clears both deleted_at and ignored, making the container
	// active again and removing it from the in-memory ignore list.
	RestoreContainer(ctx context.Context, id string) error

	// HardDeleteContainer permanently removes the container and all related records
	// (events, samples, scores via CASCADE). Also removes the entry from the
	// in-memory ignore list.
	HardDeleteContainer(ctx context.Context, id string) error

	// GetDeletedContainers returns soft-deleted containers, optionally filtered by sourceName.
	GetDeletedContainers(ctx context.Context, sourceName string) ([]*model.Container, error)

	// GetIgnoredContainers returns ignored containers, optionally filtered by sourceName.
	GetIgnoredContainers(ctx context.Context, sourceName string) ([]*model.Container, error)

	// PurgeArchivedContainers permanently deletes all soft-deleted containers and their
	// history. Optionally filtered by sourceName. Returns the number of containers removed.
	PurgeArchivedContainers(ctx context.Context, sourceName string) (int64, error)

	// PurgeIgnoredContainers permanently deletes all ignored containers and their
	// history, and removes them from the in-memory ignore list.
	// Optionally filtered by sourceName. Returns the number of containers removed.
	PurgeIgnoredContainers(ctx context.Context, sourceName string) (int64, error)

	// GetCronJobExecutions returns up to limit executions for the given CronJob,
	// ordered by sampled_at descending (most recent first).
	GetCronJobExecutions(ctx context.Context, sourceName, namespace, cronJobName string, limit int) ([]*model.CronJobExecution, error)

	// ListCronJobs returns the most recent execution for each CronJob in the
	// given source and namespace.
	ListCronJobs(ctx context.Context, sourceName, namespace string) ([]*model.CronJobExecution, error)

	// GetRecentEvents returns the most recent lifecycle events matching q,
	// ordered by occurred_at descending. q.Limit is clamped to [1, 200].
	GetRecentEvents(ctx context.Context, q repository.EventQuery) ([]*model.LifecycleEvent, error)

	// --- Workloads ---

	// ListWorkloads returns one summary per workload (all instances aggregated).
	ListWorkloads(ctx context.Context, f repository.WorkloadFilter) ([]*model.WorkloadSummary, error)
	// GetWorkload returns a workload summary, or (nil, nil) when unknown.
	GetWorkload(ctx context.Context, id string) (*model.WorkloadSummary, error)
	// GetWorkloadScore returns the latest workload score, or (nil, nil).
	GetWorkloadScore(ctx context.Context, id string) (*model.WorkloadScore, error)
	// GetWorkloadScoreHistory returns the n most recent workload scores, newest first.
	GetWorkloadScoreHistory(ctx context.Context, id string, n int) ([]*model.WorkloadScore, error)
	// GetWorkloadResources returns usage summed over all instances, bucketed.
	GetWorkloadResources(ctx context.Context, id string, from, to time.Time) ([]*model.ResourceSample, error)

	// --- Source registry ---

	// AttachSource registers a source and its collector. c may be nil when the
	// collector could not be built (initErr explains why). If Run is already
	// active an enabled source starts immediately; otherwise it starts with Run.
	// An existing source with the same name is detached first.
	AttachSource(spec model.SourceSpec, c collector.Collector, initErr error)
	// DetachSource stops a source's collector and removes it from the registry.
	// Recorded data is kept. It is a no-op for unknown names.
	DetachSource(name string)
	// Source returns the registered spec for name.
	Source(name string) (model.SourceSpec, bool)
	// IsReadOnly reports whether management actions are disabled for a source.
	IsReadOnly(name string) bool
	// PurgeSource permanently deletes every container, event, sample, score and
	// CronJob execution recorded for a source.
	PurgeSource(ctx context.Context, name string) error
}

// SourceSettings are the non-secret connection settings of a source.
type SourceSettings struct {
	Socket              string `json:"socket,omitempty"`
	KubeconfigPath      string `json:"kubeconfig_path,omitempty"`
	KubeContext         string `json:"kube_context,omitempty"`
	KubeconfigUploaded  bool   `json:"kubeconfig_uploaded"`
	PollIntervalSeconds int    `json:"poll_interval_seconds,omitempty"`
}

// SourceStatus holds the configuration and live status of a collector source.
type SourceStatus struct {
	// Name is the human-readable source identifier (e.g. "local-docker").
	Name string `json:"name"`
	// Type is the runtime type: "docker" or "kubernetes".
	Type string `json:"type"`
	// Origin is "config" (config.yaml, locked) or "user" (created via the API).
	Origin string `json:"origin"`
	// Enabled is false when the source is paused.
	Enabled bool `json:"enabled"`
	// ReadOnly disables management actions (kill, delete pod, scale, …).
	ReadOnly bool `json:"read_only"`
	// ContainerCount is the total number of known containers from this source.
	ContainerCount int `json:"container_count"`
	// RunningCount is the number of running containers from this source.
	RunningCount int `json:"running_count"`
	// Status is "connecting", "connected", "error" or "disabled".
	Status string `json:"status"`
	// LastError is the most recent collector start error, if any.
	LastError string `json:"last_error,omitempty"`
	// ConnectedAt is when the collector last started successfully.
	ConnectedAt *time.Time `json:"connected_at,omitempty"`
	// Settings are the connection settings (never includes kubeconfig content).
	Settings SourceSettings `json:"settings"`
}

// Source connection states reported by ListSources.
const (
	SourceConnecting = "connecting"
	SourceConnected  = "connected"
	SourceError      = "error"
	SourceDisabled   = "disabled"
)

// ScoringConfig controls the periodic stability scoring loop.
type ScoringConfig struct {
	// Interval is how often every active container is re-scored.
	Interval time.Duration
	// Window is the look-back period considered by the scorer.
	Window time.Duration
}

// maxResourcePoints is the approximate number of points returned for long
// resource-sample ranges; shorter ranges return raw samples.
const maxResourcePoints = 600

// rawResourceRange is the longest range returned without downsampling.
const rawResourceRange = 3 * time.Hour

// recorderService is the concrete implementation of RecorderService.
type recorderService struct {
	containers repository.ContainerRepository
	events     repository.EventRepository
	resources  repository.ResourceRepository
	scores     repository.ScoreRepository
	cronJobs   repository.CronJobRepository
	workloads  repository.WorkloadRepository
	scorer     stability.StabilityScorer
	scoring    ScoringConfig
	log        *zap.Logger
	// ignoreList is an in-memory set of container IDs that should be silently
	// dropped on arrival. It is populated lazily on first sight and updated
	// synchronously by IgnoreContainer / RestoreContainer / HardDeleteContainer.
	ignoreList sync.Map // map[string]struct{}

	// merged carries events from every running collector to the dispatch loop.
	merged chan collector.CollectorEvent

	// mu guards entries, order and runCtx.
	mu      sync.RWMutex
	entries map[string]*sourceEntry
	order   []string
	runCtx  context.Context // non-nil while Run is active
}

// sourceEntry is one registered source and the lifecycle of its collector.
type sourceEntry struct {
	spec      model.SourceSpec
	collector collector.Collector // nil when it could not be built
	initErr   error

	cancel context.CancelFunc // non-nil while started
	done   chan struct{}      // closed when the start/retry goroutine exits

	hmu    sync.Mutex
	health sourceHealth
}

type sourceHealth struct {
	status      string
	lastError   string
	connectedAt *time.Time
}

func (e *sourceEntry) setHealth(h sourceHealth) {
	e.hmu.Lock()
	e.health = h
	e.hmu.Unlock()
}

func (e *sourceEntry) getHealth() sourceHealth {
	e.hmu.Lock()
	defer e.hmu.Unlock()
	return e.health
}

// NewRecorderService constructs a RecorderService. Sources are registered with
// AttachSource. Zero values in scoring fall back to a 60s interval and a 24h window.
func NewRecorderService(
	containers repository.ContainerRepository,
	events repository.EventRepository,
	resources repository.ResourceRepository,
	scores repository.ScoreRepository,
	cronJobs repository.CronJobRepository,
	workloads repository.WorkloadRepository,
	scoring ScoringConfig,
	log *zap.Logger,
) RecorderService {
	if scoring.Interval <= 0 {
		scoring.Interval = 60 * time.Second
	}
	if scoring.Window <= 0 {
		scoring.Window = 24 * time.Hour
	}
	return &recorderService{
		containers: containers,
		events:     events,
		resources:  resources,
		scores:     scores,
		cronJobs:   cronJobs,
		workloads:  workloads,
		scorer:     stability.NewScorer(events, resources, scores, log),
		scoring:    scoring,
		log:        log,
		merged:     make(chan collector.CollectorEvent, 512),
		entries:    make(map[string]*sourceEntry),
	}
}

// Run starts every registered source, then dispatches collector events until
// ctx is cancelled. Sources attached while Run is active start immediately.
//
// A collector that fails to start (e.g. an unreachable cluster) does not stop
// the others: it is retried in the background with exponential back-off and
// its state is reported through ListSources.
func (s *recorderService) Run(ctx context.Context) error {
	s.mu.Lock()
	s.runCtx = ctx
	for _, name := range s.order {
		s.startEntry(s.entries[name])
	}
	n := len(s.order)
	s.mu.Unlock()

	s.log.Info("recorder starting",
		zap.Int("sources", n),
		zap.Duration("scoring_interval", s.scoring.Interval),
		zap.Duration("scoring_window", s.scoring.Window),
	)

	defer func() {
		s.mu.Lock()
		entries := make([]*sourceEntry, 0, len(s.order))
		for _, name := range s.order {
			entries = append(entries, s.entries[name])
		}
		s.runCtx = nil
		s.mu.Unlock()
		for _, e := range entries {
			s.stopEntry(e)
		}
	}()

	go s.scoringLoop(ctx)

	for {
		select {
		case <-ctx.Done():
			s.log.Info("recorder stopping")
			return ctx.Err()
		case event := <-s.merged:
			s.dispatch(ctx, event)
		}
	}
}

// AttachSource registers (or replaces) a source; see RecorderService.
func (s *recorderService) AttachSource(spec model.SourceSpec, c collector.Collector, initErr error) {
	e := &sourceEntry{spec: spec, collector: c, initErr: initErr}
	switch {
	case !spec.Enabled:
		e.health = sourceHealth{status: SourceDisabled}
	case c == nil:
		msg := "collector unavailable"
		if initErr != nil {
			msg = initErr.Error()
		}
		e.health = sourceHealth{status: SourceError, lastError: msg}
	default:
		e.health = sourceHealth{status: SourceConnecting}
	}

	// Swap the new entry in first so the source stays listed (as connecting)
	// while a replaced collector shuts down, which can take a while for a
	// Kubernetes source still delivering its initial pod list.
	s.mu.Lock()
	old := s.entries[spec.Name]
	s.entries[spec.Name] = e
	if old == nil {
		s.order = append(s.order, spec.Name)
	}
	s.mu.Unlock()

	if old != nil {
		s.stopEntry(old)
	}

	s.mu.Lock()
	if s.runCtx != nil && s.entries[spec.Name] == e {
		s.startEntry(e)
	}
	s.mu.Unlock()
	s.log.Info("source attached", zap.String("source", spec.Name), zap.String("type", spec.Type),
		zap.String("origin", spec.Origin), zap.Bool("enabled", spec.Enabled), zap.Bool("read_only", spec.ReadOnly))
}

// DetachSource stops and unregisters a source; see RecorderService.
func (s *recorderService) DetachSource(name string) {
	s.mu.Lock()
	e, ok := s.entries[name]
	if ok {
		delete(s.entries, name)
		s.order = without(s.order, name)
	}
	s.mu.Unlock()
	if ok {
		s.stopEntry(e)
		s.log.Info("source detached", zap.String("source", name))
	}
}

// Source returns a copy of the registered spec.
func (s *recorderService) Source(name string) (model.SourceSpec, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if e, ok := s.entries[name]; ok {
		return e.spec, true
	}
	return model.SourceSpec{}, false
}

// IsReadOnly reports whether management actions are disabled for a source.
func (s *recorderService) IsReadOnly(name string) bool {
	spec, ok := s.Source(name)
	return ok && spec.ReadOnly
}

// PurgeSource deletes everything recorded for a source.
func (s *recorderService) PurgeSource(ctx context.Context, name string) error {
	n, err := s.containers.DeleteBySource(ctx, name)
	if err != nil {
		return err
	}
	if _, err := s.cronJobs.DeleteBySource(ctx, name); err != nil {
		return err
	}
	s.log.Info("source history purged", zap.String("source", name), zap.Int64("containers", n))
	return nil
}

// startEntry launches an enabled source's collector. Caller holds s.mu and
// s.runCtx is non-nil.
func (s *recorderService) startEntry(e *sourceEntry) {
	if !e.spec.Enabled || e.collector == nil || e.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(s.runCtx)
	e.cancel = cancel
	e.done = make(chan struct{})

	go s.forward(ctx, e.collector.Events())
	go func() {
		defer close(e.done)
		s.startWithRetry(ctx, e)
	}()
}

// stopEntry cancels a source's start/retry loop, waits for it, and stops the
// collector (which closes its events channel and ends forwarding).
func (s *recorderService) stopEntry(e *sourceEntry) {
	if e.cancel == nil {
		return
	}
	e.cancel()
	<-e.done
	if err := e.collector.Stop(); err != nil {
		s.log.Warn("error stopping collector", zap.String("source", e.spec.Name), zap.Error(err))
	}
	e.cancel = nil
}

// forward copies a collector's events onto the merged channel until the
// collector closes its channel.
func (s *recorderService) forward(ctx context.Context, ch <-chan collector.CollectorEvent) {
	for event := range ch {
		select {
		case s.merged <- event:
		case <-ctx.Done():
			// Drain so the collector never blocks while shutting down.
			for range ch {
			}
			return
		}
	}
}

// startWithRetry starts a collector, retrying with exponential back-off (5s up
// to 5m) until it succeeds or ctx is cancelled.
func (s *recorderService) startWithRetry(ctx context.Context, e *sourceEntry) {
	const (
		initialDelay = 5 * time.Second
		maxDelay     = 5 * time.Minute
	)
	delay := initialDelay
	for {
		err := e.collector.Start(ctx)
		if err == nil {
			now := time.Now().UTC()
			e.setHealth(sourceHealth{status: SourceConnected, connectedAt: &now})
			return
		}
		if ctx.Err() != nil {
			return
		}
		e.setHealth(sourceHealth{status: SourceError, lastError: err.Error()})
		s.log.Warn("collector failed to start; will retry",
			zap.String("source", e.spec.Name),
			zap.String("type", e.spec.Type),
			zap.Duration("retry_in", delay),
			zap.Error(err),
		)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
		}
	}
}

func without(list []string, name string) []string {
	out := make([]string, 0, len(list))
	for _, n := range list {
		if n != name {
			out = append(out, n)
		}
	}
	return out
}

// dispatch routes a collector event to the correct handler. Events for ignored
// containers are silently dropped before any DB work is done.
func (s *recorderService) dispatch(ctx context.Context, event collector.CollectorEvent) {
	switch event.Kind {
	case collector.CollectorEventCronJob:
		// CronJob events carry only a minimal Container stub (ID only); they go
		// straight to their own table without touching the containers row.
		s.handleCronJob(ctx, event)
		return
	case collector.CollectorEventSync:
		s.handleSync(ctx, event)
		return
	}

	if event.Container == nil {
		return
	}
	if s.isIgnored(ctx, event.Container.ID) {
		return
	}

	switch event.Kind {
	case collector.CollectorEventLifecycle:
		s.handleLifecycle(ctx, event)
	case collector.CollectorEventResource:
		s.handleResource(ctx, event)
	case collector.CollectorEventState:
		s.handleState(ctx, event)
	}
}

// isIgnored checks the in-memory ignore list, falling back to the database on
// first sight of a container since service start.
func (s *recorderService) isIgnored(ctx context.Context, id string) bool {
	if _, ok := s.ignoreList.Load(id); ok {
		return true
	}
	if ignored, err := s.containers.IsIgnored(ctx, id); err == nil && ignored {
		s.ignoreList.Store(id, struct{}{})
		return true
	}
	return false
}

// handleLifecycle upserts the container (restoring it if archived — a fresh
// lifecycle event means it is alive again), records the event, and advances the
// lifetime counters by exactly the amount this event implies. Counters are kept
// in the database rather than taken from the collector so they survive restarts.
func (s *recorderService) handleLifecycle(ctx context.Context, event collector.CollectorEvent) {
	ev := event.Lifecycle
	if ev == nil {
		return
	}
	if !ev.OccurredAt.IsZero() && ev.OccurredAt.After(event.Container.LastSeenAt) {
		event.Container.LastSeenAt = ev.OccurredAt
	}
	if _, err := s.containers.Upsert(ctx, event.Container, true); err != nil {
		s.log.Error("failed to upsert container", zap.String("id", event.Container.ID), zap.Error(err))
		return
	}

	if _, err := s.events.Save(ctx, ev); err != nil {
		s.log.Error("failed to save lifecycle event",
			zap.String("container_id", event.Container.ID),
			zap.String("event_type", string(ev.EventType)),
			zap.Error(err))
		return
	}

	var starts, crashes, ooms int
	switch ev.EventType {
	case model.EventTypeStart:
		starts = 1
	case model.EventTypeCrash:
		crashes = 1
	case model.EventTypeDie:
		if ev.ExitCode != 0 {
			crashes = 1
		}
	case model.EventTypeOOM:
		ooms = 1
	}
	if err := s.containers.IncrementCounters(ctx, event.Container.ID, starts, crashes, ooms); err != nil {
		s.log.Error("failed to increment container counters", zap.String("id", event.Container.ID), zap.Error(err))
	}

	s.log.Debug("lifecycle event persisted",
		zap.String("container_id", event.Container.ID),
		zap.String("event_type", string(ev.EventType)),
		zap.Int("exit_code", ev.ExitCode),
	)
}

// handleState applies a state snapshot without recording a lifecycle event.
func (s *recorderService) handleState(ctx context.Context, event collector.CollectorEvent) {
	if event.UpdateOnly {
		if _, err := s.containers.UpdateState(ctx, event.Container); err != nil {
			s.log.Error("failed to update container state", zap.String("id", event.Container.ID), zap.Error(err))
		}
		return
	}
	if _, err := s.containers.Upsert(ctx, event.Container, false); err != nil {
		s.log.Error("failed to upsert container state", zap.String("id", event.Container.ID), zap.Error(err))
	}
}

// handleSync marks containers that disappeared while the service was not
// watching (or whose deletion event was missed) as stopped.
func (s *recorderService) handleSync(ctx context.Context, event collector.CollectorEvent) {
	n, err := s.containers.MarkMissingStopped(ctx, event.SourceName, event.SyncIDs)
	if err != nil {
		s.log.Error("failed to reconcile containers", zap.String("source", event.SourceName), zap.Error(err))
		return
	}
	if n > 0 {
		s.log.Info("reconciled vanished containers as stopped",
			zap.String("source", event.SourceName), zap.Int64("count", n))
	}
}

// handleResource stores a resource sample and bumps the container's last-seen
// time. It deliberately does not upsert the full container: sample events carry
// only partial identity data and must not clobber stored fields or un-archive
// a container. Unknown containers are inserted once so the sample's foreign
// key is satisfied.
func (s *recorderService) handleResource(ctx context.Context, event collector.CollectorEvent) {
	if event.Sample == nil {
		return
	}
	found, err := s.containers.Touch(ctx, event.Container.ID, event.Sample.SampledAt)
	if err != nil {
		s.log.Error("failed to touch container", zap.String("id", event.Container.ID), zap.Error(err))
		return
	}
	if !found {
		if _, err := s.containers.Upsert(ctx, event.Container, false); err != nil {
			s.log.Error("failed to insert container for sample", zap.String("id", event.Container.ID), zap.Error(err))
			return
		}
	}

	if _, err := s.resources.Save(ctx, event.Sample); err != nil {
		s.log.Error("failed to save resource sample",
			zap.String("container_id", event.Container.ID),
			zap.Error(err))
		return
	}

	s.log.Debug("resource sample persisted",
		zap.String("container_id", event.Container.ID),
		zap.Float64("cpu_percent", event.Sample.CPUPercent),
		zap.Int64("mem_bytes", event.Sample.MemBytes),
	)
}

func (s *recorderService) handleCronJob(ctx context.Context, event collector.CollectorEvent) {
	if event.CronJobExec == nil {
		return
	}
	if _, err := s.cronJobs.Save(ctx, event.CronJobExec); err != nil {
		s.log.Error("failed to save cronjob execution",
			zap.String("cronjob", event.CronJobExec.CronJobName),
			zap.Error(err))
		return
	}
	s.log.Debug("cronjob execution persisted",
		zap.String("cronjob", event.CronJobExec.CronJobName),
		zap.String("namespace", event.CronJobExec.Namespace),
	)
}

// scoringLoop scores every active container shortly after startup and then on
// every scoring interval until ctx is cancelled.
func (s *recorderService) scoringLoop(ctx context.Context) {
	// Give collectors a moment to seed state before the first pass.
	select {
	case <-ctx.Done():
		return
	case <-time.After(15 * time.Second):
	}
	s.scoreAllContainers(ctx)

	ticker := time.NewTicker(s.scoring.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scoreAllContainers(ctx)
		}
	}
}

func (s *recorderService) scoreAllContainers(ctx context.Context) {
	now := time.Now().UTC()
	windowStart := now.Add(-s.scoring.Window)

	// Containers not seen during the window (e.g. pods replaced long ago) have
	// nothing new to score; skipping them keeps the pass proportional to the
	// live estate rather than to all history.
	containers, err := s.containers.FindAll(ctx, repository.ContainerFilter{SeenSince: windowStart})
	if err != nil {
		s.log.Error("scoring loop: failed to list containers", zap.Error(err))
		return
	}

	type workloadAcc struct {
		source  string
		members []stability.Analysis
	}
	byWorkload := make(map[string]*workloadAcc)

	for _, c := range containers {
		if ctx.Err() != nil {
			return
		}
		from := windowStart
		if c.FirstSeenAt.After(from) {
			from = c.FirstSeenAt
		}
		_, analysis, err := s.scorer.Evaluate(ctx, c, from, now)
		if err != nil {
			s.log.Error("scoring loop: failed to score container",
				zap.String("container_id", c.ID),
				zap.Error(err))
			continue
		}
		if c.WorkloadID == "" {
			continue
		}
		acc := byWorkload[c.WorkloadID]
		if acc == nil {
			acc = &workloadAcc{source: c.SourceName}
			byWorkload[c.WorkloadID] = acc
		}
		acc.members = append(acc.members, analysis)
	}

	for id, acc := range byWorkload {
		score := stability.ComputeWorkload(id, acc.source, acc.members, stability.DefaultWeights())
		if err := s.workloads.SaveScore(ctx, score); err != nil {
			s.log.Error("scoring loop: failed to save workload score", zap.String("workload_id", id), zap.Error(err))
		}
	}
	s.log.Debug("scoring pass complete",
		zap.Int("containers", len(containers)), zap.Int("workloads", len(byWorkload)))
}

// ---------------------------------------------------------------------------
// Workloads
// ---------------------------------------------------------------------------

func (s *recorderService) ListWorkloads(ctx context.Context, f repository.WorkloadFilter) ([]*model.WorkloadSummary, error) {
	return s.workloads.List(ctx, f)
}

func (s *recorderService) GetWorkload(ctx context.Context, id string) (*model.WorkloadSummary, error) {
	w, err := s.workloads.Get(ctx, id)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, nil
	}
	return w, err
}

func (s *recorderService) GetWorkloadScore(ctx context.Context, id string) (*model.WorkloadScore, error) {
	return s.workloads.LatestScore(ctx, id)
}

func (s *recorderService) GetWorkloadScoreHistory(ctx context.Context, id string, n int) ([]*model.WorkloadScore, error) {
	if n <= 0 || n > 1440 {
		n = 60
	}
	return s.workloads.ScoreHistory(ctx, id, n)
}

func (s *recorderService) GetWorkloadResources(ctx context.Context, id string, from, to time.Time) ([]*model.ResourceSample, error) {
	bucket := 30 * time.Second
	if span := to.Sub(from); span > rawResourceRange {
		bucket = (span / maxResourcePoints).Truncate(time.Second)
	}
	return s.workloads.Resources(ctx, id, from, to, bucket)
}

func (s *recorderService) GetContainer(ctx context.Context, id string) (*model.Container, error) {
	return s.containers.FindByID(ctx, id)
}

func (s *recorderService) ListContainers(ctx context.Context, f repository.ContainerFilter) ([]*model.Container, error) {
	return s.containers.FindAll(ctx, f)
}

func (s *recorderService) ListContainersPaged(ctx context.Context, f repository.ContainerFilter, page, limit int, sortField, sortDir string) ([]*model.Container, int64, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 200 {
		limit = 20
	}
	offset := (page - 1) * limit
	return s.containers.FindPage(ctx, f, limit, offset, sortField, sortDir)
}

func (s *recorderService) ListSources(ctx context.Context) ([]SourceStatus, error) {
	all, err := s.containers.FindAll(ctx, repository.ContainerFilter{})
	if err != nil {
		return nil, err
	}
	totalCounts := make(map[string]int)
	runningCounts := make(map[string]int)
	for _, c := range all {
		totalCounts[c.SourceName]++
		if c.CurrentStatus == model.StatusRunning {
			runningCounts[c.SourceName]++
		}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]SourceStatus, 0, len(s.order))
	for _, name := range s.order {
		e := s.entries[name]
		h := e.getHealth()
		result = append(result, SourceStatus{
			Name:           e.spec.Name,
			Type:           e.spec.Type,
			Origin:         e.spec.Origin,
			Enabled:        e.spec.Enabled,
			ReadOnly:       e.spec.ReadOnly,
			ContainerCount: totalCounts[name],
			RunningCount:   runningCounts[name],
			Status:         h.status,
			LastError:      h.lastError,
			ConnectedAt:    h.connectedAt,
			Settings: SourceSettings{
				Socket:              e.spec.Socket,
				KubeconfigPath:      e.spec.KubeconfigPath,
				KubeContext:         e.spec.KubeContext,
				KubeconfigUploaded:  len(e.spec.Kubeconfig) > 0 || len(e.spec.KubeconfigEnc) > 0,
				PollIntervalSeconds: int(e.spec.PollInterval / time.Second),
			},
		})
	}
	return result, nil
}

func (s *recorderService) GetStabilityScore(ctx context.Context, containerID string) (*model.StabilityScore, error) {
	return s.scores.FindLatest(ctx, containerID)
}

func (s *recorderService) GetScoreHistory(ctx context.Context, containerID string, n int) ([]*model.StabilityScore, error) {
	return s.scores.FindHistory(ctx, containerID, n)
}

func (s *recorderService) GetLifecycleEvents(ctx context.Context, containerID string, limit int) ([]*model.LifecycleEvent, error) {
	return s.events.FindByContainerID(ctx, containerID, limit)
}

func (s *recorderService) GetResourceSamples(ctx context.Context, containerID string, from, to time.Time) ([]*model.ResourceSample, error) {
	var bucket time.Duration
	if span := to.Sub(from); span > rawResourceRange {
		bucket = (span / maxResourcePoints).Truncate(time.Second)
		if bucket < 10*time.Second {
			bucket = 10 * time.Second
		}
	}
	return s.resources.FindByContainerID(ctx, containerID, from, to, bucket)
}

func (s *recorderService) SoftDeleteContainer(ctx context.Context, id string) error {
	return s.containers.SoftDelete(ctx, id)
}

func (s *recorderService) IgnoreContainer(ctx context.Context, id string) error {
	if err := s.containers.Ignore(ctx, id); err != nil {
		return err
	}
	s.ignoreList.Store(id, struct{}{})
	return nil
}

func (s *recorderService) RestoreContainer(ctx context.Context, id string) error {
	if err := s.containers.Restore(ctx, id); err != nil {
		return err
	}
	s.ignoreList.Delete(id)
	return nil
}

func (s *recorderService) HardDeleteContainer(ctx context.Context, id string) error {
	if err := s.containers.HardDelete(ctx, id); err != nil {
		return err
	}
	s.ignoreList.Delete(id)
	return nil
}

func (s *recorderService) GetDeletedContainers(ctx context.Context, sourceName string) ([]*model.Container, error) {
	return s.containers.FindDeleted(ctx, sourceName)
}

func (s *recorderService) GetIgnoredContainers(ctx context.Context, sourceName string) ([]*model.Container, error) {
	return s.containers.FindIgnored(ctx, sourceName)
}

func (s *recorderService) PurgeArchivedContainers(ctx context.Context, sourceName string) (int64, error) {
	return s.containers.HardDeleteDeleted(ctx, sourceName)
}

func (s *recorderService) PurgeIgnoredContainers(ctx context.Context, sourceName string) (int64, error) {
	// Collect IDs first so we can clean the in-memory ignore list.
	ignored, err := s.containers.FindIgnored(ctx, sourceName)
	if err != nil {
		return 0, err
	}
	count, err := s.containers.HardDeleteIgnored(ctx, sourceName)
	if err != nil {
		return 0, err
	}
	for _, c := range ignored {
		s.ignoreList.Delete(c.ID)
	}
	return count, nil
}

func (s *recorderService) GetCronJobExecutions(ctx context.Context, sourceName, namespace, cronJobName string, limit int) ([]*model.CronJobExecution, error) {
	return s.cronJobs.FindByCronJob(ctx, sourceName, namespace, cronJobName, limit)
}

func (s *recorderService) ListCronJobs(ctx context.Context, sourceName, namespace string) ([]*model.CronJobExecution, error) {
	return s.cronJobs.FindLatestPerCronJob(ctx, sourceName, namespace)
}

func (s *recorderService) GetRecentEvents(ctx context.Context, q repository.EventQuery) ([]*model.LifecycleEvent, error) {
	if q.Limit <= 0 || q.Limit > 200 {
		q.Limit = 20
	}
	return s.events.FindRecent(ctx, q)
}
