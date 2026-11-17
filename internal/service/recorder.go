// Package service contains the core application services that orchestrate
// collectors, repositories, and domain logic for the KYMO.
package service

import (
	"context"
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

	// GetRecentEvents returns the n most recent lifecycle events across all
	// containers (optionally one source), ordered by occurred_at descending.
	// Limit is clamped to [1, 200].
	GetRecentEvents(ctx context.Context, limit int, sourceName string) ([]*model.LifecycleEvent, error)
}

// SourceStatus holds the live status of a single collector source.
// It is returned by ListSources and consumed by the frontend's source selector.
type SourceStatus struct {
	// Name is the human-readable source identifier (e.g. "local-docker").
	Name string `json:"name"`
	// Type is the runtime type: "docker" or "kubernetes".
	Type string `json:"type"`
	// ContainerCount is the total number of known containers from this source.
	ContainerCount int `json:"container_count"`
	// RunningCount is the number of running containers from this source.
	RunningCount int `json:"running_count"`
	// Status is the collector connection state: "connecting", "connected" or "error".
	Status string `json:"status"`
	// LastError is the most recent collector start error, if any.
	LastError string `json:"last_error,omitempty"`
	// ConnectedAt is when the collector last started successfully.
	ConnectedAt *time.Time `json:"connected_at,omitempty"`
}

// Source connection states reported by ListSources.
const (
	SourceConnecting = "connecting"
	SourceConnected  = "connected"
	SourceError      = "error"
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
	collectors []collector.Collector
	sources    []SourceStatus // static list built from config at construction time
	containers repository.ContainerRepository
	events     repository.EventRepository
	resources  repository.ResourceRepository
	scores     repository.ScoreRepository
	cronJobs   repository.CronJobRepository
	scorer     stability.StabilityScorer
	scoring    ScoringConfig
	log        *zap.Logger
	// ignoreList is an in-memory set of container IDs that should be silently
	// dropped on arrival. It is populated lazily on first sight and updated
	// synchronously by IgnoreContainer / RestoreContainer / HardDeleteContainer.
	ignoreList sync.Map // map[string]struct{}

	// health tracks the connection state of each collector, indexed like sources.
	healthMu sync.RWMutex
	health   []sourceHealth
}

type sourceHealth struct {
	status      string
	lastError   string
	connectedAt *time.Time
}

// CollectorSource groups a collector with its configured source metadata so the
// recorder can tag live counts against the right source.
type CollectorSource struct {
	Collector collector.Collector
	Name      string
	Type      string
}

// NewRecorderService constructs a RecorderService wired to the provided collectors
// and repository implementations. All dependencies are required; zero values in
// scoring fall back to a 60s interval and a 24h window.
func NewRecorderService(
	sources []CollectorSource,
	containers repository.ContainerRepository,
	events repository.EventRepository,
	resources repository.ResourceRepository,
	scores repository.ScoreRepository,
	cronJobs repository.CronJobRepository,
	scoring ScoringConfig,
	log *zap.Logger,
) RecorderService {
	collectors := make([]collector.Collector, len(sources))
	staticSources := make([]SourceStatus, len(sources))
	health := make([]sourceHealth, len(sources))
	for i, s := range sources {
		collectors[i] = s.Collector
		staticSources[i] = SourceStatus{Name: s.Name, Type: s.Type}
		health[i] = sourceHealth{status: SourceConnecting}
	}
	if scoring.Interval <= 0 {
		scoring.Interval = 60 * time.Second
	}
	if scoring.Window <= 0 {
		scoring.Window = 24 * time.Hour
	}
	return &recorderService{
		collectors: collectors,
		sources:    staticSources,
		containers: containers,
		events:     events,
		resources:  resources,
		scores:     scores,
		cronJobs:   cronJobs,
		scorer:     stability.NewScorer(events, resources, scores, log),
		scoring:    scoring,
		log:        log,
		health:     health,
	}
}

// Run starts every registered collector, merges their event channels via a fan-in
// goroutine, and dispatches each CollectorEvent to the appropriate handler.
//
// A collector that fails to start (e.g. an unreachable cluster) does not stop the
// others: it is retried in the background with exponential back-off and its
// state is reported through ListSources. Run returns when ctx is cancelled.
func (s *recorderService) Run(ctx context.Context) error {
	s.log.Info("recorder starting",
		zap.Int("collectors", len(s.collectors)),
		zap.Duration("scoring_interval", s.scoring.Interval),
		zap.Duration("scoring_window", s.scoring.Window),
	)

	var startWG sync.WaitGroup
	for i := range s.collectors {
		startWG.Add(1)
		go func(i int) {
			defer startWG.Done()
			s.startWithRetry(ctx, i)
		}(i)
	}

	defer func() {
		// Wait for in-flight start attempts to observe cancellation before
		// stopping, so Stop never races with Start.
		startWG.Wait()
		for _, c := range s.collectors {
			if err := c.Stop(); err != nil {
				s.log.Warn("error stopping collector", zap.Error(err))
			}
		}
	}()

	go s.scoringLoop(ctx)

	merged := s.fanIn(ctx)
	for {
		select {
		case <-ctx.Done():
			s.log.Info("recorder stopping")
			return ctx.Err()
		case event, ok := <-merged:
			if !ok {
				return nil
			}
			s.dispatch(ctx, event)
		}
	}
}

// startWithRetry starts collector i, retrying with exponential back-off (5s up
// to 5m) until it succeeds or ctx is cancelled.
func (s *recorderService) startWithRetry(ctx context.Context, i int) {
	const (
		initialDelay = 5 * time.Second
		maxDelay     = 5 * time.Minute
	)
	src := s.sources[i]
	delay := initialDelay
	for {
		err := s.collectors[i].Start(ctx)
		if err == nil {
			now := time.Now().UTC()
			s.setHealth(i, sourceHealth{status: SourceConnected, connectedAt: &now})
			return
		}
		if ctx.Err() != nil {
			return
		}
		s.setHealth(i, sourceHealth{status: SourceError, lastError: err.Error()})
		s.log.Warn("collector failed to start; will retry",
			zap.String("source", src.Name),
			zap.String("type", src.Type),
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

func (s *recorderService) setHealth(i int, h sourceHealth) {
	s.healthMu.Lock()
	s.health[i] = h
	s.healthMu.Unlock()
}

// fanIn merges all collector event channels into a single channel using one
// goroutine per collector.
func (s *recorderService) fanIn(ctx context.Context) <-chan collector.CollectorEvent {
	merged := make(chan collector.CollectorEvent, 512)

	for _, c := range s.collectors {
		go func(ch <-chan collector.CollectorEvent) {
			for event := range ch {
				select {
				case merged <- event:
				case <-ctx.Done():
					return
				}
			}
		}(c.Events())
	}

	return merged
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
	containers, err := s.containers.FindAll(ctx, repository.ContainerFilter{})
	if err != nil {
		s.log.Error("scoring loop: failed to list containers", zap.Error(err))
		return
	}

	now := time.Now().UTC()
	for _, c := range containers {
		if ctx.Err() != nil {
			return
		}
		from := now.Add(-s.scoring.Window)
		if c.FirstSeenAt.After(from) {
			from = c.FirstSeenAt
		}
		if _, err := s.scorer.Score(ctx, c, from, now); err != nil {
			s.log.Error("scoring loop: failed to score container",
				zap.String("container_id", c.ID),
				zap.Error(err))
		}
	}
	s.log.Debug("scoring pass complete", zap.Int("containers", len(containers)))
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
	// Get all containers to compute live counts per source.
	all, err := s.containers.FindAll(ctx, repository.ContainerFilter{})
	if err != nil {
		return nil, err
	}

	// Build count maps.
	totalCounts := make(map[string]int, len(s.sources))
	runningCounts := make(map[string]int, len(s.sources))
	for _, c := range all {
		totalCounts[c.SourceName]++
		if c.CurrentStatus == model.StatusRunning {
			runningCounts[c.SourceName]++
		}
	}

	s.healthMu.RLock()
	defer s.healthMu.RUnlock()
	result := make([]SourceStatus, len(s.sources))
	for i, src := range s.sources {
		h := s.health[i]
		result[i] = SourceStatus{
			Name:           src.Name,
			Type:           src.Type,
			ContainerCount: totalCounts[src.Name],
			RunningCount:   runningCounts[src.Name],
			Status:         h.status,
			LastError:      h.lastError,
			ConnectedAt:    h.connectedAt,
		}
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

func (s *recorderService) GetRecentEvents(ctx context.Context, limit int, sourceName string) ([]*model.LifecycleEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	return s.events.FindRecent(ctx, limit, sourceName)
}
