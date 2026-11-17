// Package docker implements the collector.Collector interface for the Docker daemon.
// It connects to the local Docker socket, streams container lifecycle events via the
// Docker Events API, and polls per-container resource stats on a configurable interval.
package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	apitypes "github.com/docker/docker/api/types"
	containertypes "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	dockerclient "github.com/docker/docker/client"
	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/collector"
	"github.com/emirhanusta/kymo/internal/model"
)

const (
	// defaultPollInterval is used when New is called with a zero/negative interval.
	defaultPollInterval = 10 * time.Second

	// statsTimeout caps the time spent fetching one container's resource stats.
	statsTimeout = 5 * time.Second

	// maxConcurrentStatsFetches limits simultaneous ContainerStats calls.
	maxConcurrentStatsFetches = 16

	// reconnect back-off bounds.
	initialReconnectDelay = time.Second
	maxReconnectDelay     = 30 * time.Second
)

// Collector watches the local Docker daemon and emits CollectorEvents for
// container lifecycle changes and periodic resource samples.
type Collector struct {
	log          *zap.Logger
	sourceName   string
	socketPath   string
	pollInterval time.Duration

	client *dockerclient.Client

	// running is the set of containers currently active on the host.
	// Entries are added on "start" and removed on "die"/"destroy".
	// Guarded by mu.
	mu      sync.RWMutex
	running map[string]*model.Container

	events chan collector.CollectorEvent
	wg     sync.WaitGroup
	cancel context.CancelFunc
}

// New creates a Docker Collector.
//   - sourceName: human-readable source identifier (e.g. "local-docker"); used to tag every event.
//   - socketPath: Docker daemon socket (e.g. "unix:///var/run/docker.sock"), or "" to use DOCKER_HOST.
//   - pollInterval: how often to sample container resource stats. Pass 0 for the default (10 s).
func New(log *zap.Logger, sourceName, socketPath string, pollInterval time.Duration) *Collector {
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	return &Collector{
		log:          log,
		sourceName:   sourceName,
		socketPath:   socketPath,
		pollInterval: pollInterval,
		running:      make(map[string]*model.Container),
		events:       make(chan collector.CollectorEvent, 256),
	}
}

// Start connects to the Docker daemon, seeds state from currently running containers,
// and launches the event-stream and stats-poller goroutines.
// It returns an error if the initial connection or ping fails.
func (c *Collector) Start(ctx context.Context) error {
	c.log.Info("docker collector starting", zap.String("source", c.sourceName))

	opts := []dockerclient.Opt{
		dockerclient.FromEnv,
		dockerclient.WithAPIVersionNegotiation(),
	}
	if c.socketPath != "" {
		opts = append(opts, dockerclient.WithHost(c.socketPath))
	}

	var err error
	c.client, err = dockerclient.NewClientWithOpts(opts...)
	if err != nil {
		return err
	}

	if _, err = c.client.Ping(ctx); err != nil {
		_ = c.client.Close()
		c.client = nil
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel

	// Populate the running set with containers that are already alive so that
	// stats polling starts immediately, and reconcile the stored state of every
	// container with what the daemon reports now.
	if err = c.resync(runCtx); err != nil {
		c.log.Warn("docker: failed to sync container state", zap.Error(err))
		// non-fatal: we will still receive future events correctly
	}

	c.wg.Add(2)
	go c.streamEvents(runCtx)
	go c.pollStats(runCtx)

	c.log.Info("docker collector started",
		zap.String("source", c.sourceName),
		zap.Int("seeded_containers", len(c.running)),
		zap.Duration("poll_interval", c.pollInterval),
	)
	return nil
}

// Stop cancels the run context, waits for all background goroutines to exit,
// closes the events channel, and releases the Docker client.
func (c *Collector) Stop() error {
	c.log.Info("docker collector stopping")
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
	if c.client != nil {
		_ = c.client.Close()
	}
	close(c.events)
	return nil
}

// Events returns the read-only channel on which this collector publishes CollectorEvents.
// The channel is closed after Stop returns.
func (c *Collector) Events() <-chan collector.CollectorEvent {
	return c.events
}

// Stats fetches a single point-in-time ResourceSample for containerID.
//
// It uses the non-streaming stats call *without* one-shot mode: the daemon then
// takes two readings about a second apart and fills PreCPUStats, which is
// required for a correct CPU percentage. (One-shot mode leaves PreCPUStats
// zeroed, which turns the "delta" into lifetime usage.)
func (c *Collector) Stats(ctx context.Context, containerID string) (*model.ResourceSample, error) {
	if c.client == nil {
		return nil, errors.New("docker: collector not started")
	}
	resp, err := c.client.ContainerStats(ctx, containerID, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var s containertypes.StatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, err
	}

	return statsToSample(containerID, &s), nil
}

// ---------------------------------------------------------------------------
// Internal: seeding
// ---------------------------------------------------------------------------

// resync lists every container known to the daemon, refreshes the running set
// used for stats polling, and emits state events so the stored status matches
// reality (e.g. containers that stopped or were removed while KYMO was
// down). Running containers are upserted; stopped ones only update existing
// rows so old one-off containers are not imported. Emission happens in a
// goroutine so Start never blocks on a full events channel.
func (c *Collector) resync(ctx context.Context) error {
	list, err := c.client.ContainerList(ctx, containertypes.ListOptions{All: true})
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	states := make([]collector.CollectorEvent, 0, len(list))
	ids := make([]string, 0, len(list))

	c.mu.Lock()
	running := make(map[string]*model.Container, len(list))
	for i := range list {
		ct := containerFromListItem(&list[i], now)
		ct.SourceName = c.sourceName
		ids = append(ids, ct.ID)
		isActive := ct.CurrentStatus == model.StatusRunning || ct.CurrentStatus == model.StatusPaused
		if isActive {
			if prev := c.running[ct.ID]; prev != nil {
				ct.FirstSeenAt = prev.FirstSeenAt
			}
			running[ct.ID] = ct
		}
		cp := *ct
		states = append(states, collector.CollectorEvent{
			Kind:       collector.CollectorEventState,
			SourceName: c.sourceName,
			Container:  &cp,
			UpdateOnly: !isActive,
		})
	}
	c.running = running
	c.mu.Unlock()

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		for _, ev := range states {
			c.emit(ctx, ev)
		}
		c.emit(ctx, collector.CollectorEvent{
			Kind:       collector.CollectorEventSync,
			SourceName: c.sourceName,
			SyncIDs:    ids,
		})
	}()
	return nil
}

// ---------------------------------------------------------------------------
// Internal: event streaming
// ---------------------------------------------------------------------------

// streamEvents subscribes to the Docker event stream and forwards container
// lifecycle events to handleContainerEvent. It reconnects with exponential
// back-off on transient errors and exits when ctx is cancelled.
func (c *Collector) streamEvents(ctx context.Context) {
	defer c.wg.Done()
	c.log.Debug("docker event stream goroutine started")

	filterArgs := filters.NewArgs(
		filters.Arg("type", string(events.ContainerEventType)),
	)

	delay := initialReconnectDelay

	for {
		msgCh, errCh := c.client.Events(ctx, events.ListOptions{Filters: filterArgs})

	inner:
		for {
			select {
			case <-ctx.Done():
				c.log.Debug("docker event stream goroutine stopped")
				return

			case err, ok := <-errCh:
				if !ok || errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
					// Channel closed normally or context was cancelled.
					if ctx.Err() != nil {
						return
					}
					// Daemon closed the connection (e.g. daemon restart); reconnect.
					break inner
				}
				c.log.Warn("docker event stream error",
					zap.Error(err),
					zap.Duration("reconnect_in", delay),
				)
				break inner

			case msg := <-msgCh:
				c.handleContainerEvent(ctx, msg)
				delay = initialReconnectDelay // reset back-off on successful message
			}
		}

		// Wait before reconnecting, honouring context cancellation.
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = time.Duration(math.Min(float64(delay*2), float64(maxReconnectDelay)))
		c.log.Info("docker event stream reconnecting")
	}
}

// handleContainerEvent translates a Docker event message into our domain model
// and emits a CollectorEvent onto the events channel.
func (c *Collector) handleContainerEvent(ctx context.Context, msg events.Message) {
	containerID := msg.Actor.ID
	if containerID == "" {
		return
	}

	attrs := msg.Actor.Attributes

	// Prefer nanosecond precision; fall back to seconds.
	var eventTime time.Time
	if msg.TimeNano != 0 {
		eventTime = time.Unix(0, msg.TimeNano).UTC()
	} else {
		eventTime = time.Unix(msg.Time, 0).UTC()
	}

	switch msg.Action {

	// --- destroy: evict from state, no lifecycle event ---
	case events.ActionDestroy:
		c.mu.Lock()
		delete(c.running, containerID)
		c.mu.Unlock()
		return

	// --- stop / kill: precede or accompany "die"; handled via "die" ---
	case events.ActionStop, events.ActionKill:
		c.mu.Lock()
		if ct := c.running[containerID]; ct != nil {
			ct.LastSeenAt = eventTime
		}
		c.mu.Unlock()
		return

	// --- ignore all other non-lifecycle actions ---
	case events.ActionStart,
		events.ActionDie,
		events.ActionOOM,
		events.ActionPause,
		events.ActionUnPause:
		// fall through to handling below

	default:
		return
	}

	// Build or retrieve the container record.
	c.mu.Lock()

	ct, exists := c.running[containerID]
	if !exists {
		ct = &model.Container{
			ID:          containerID,
			Runtime:     model.RuntimeDocker,
			SourceName:  c.sourceName,
			Name:        strings.TrimPrefix(attrs["name"], "/"),
			Image:       attrs["image"],
			FirstSeenAt: eventTime,
		}
		ct.WorkloadKind, ct.WorkloadName = composeWorkload(attrs, ct.Name)
		// Only persist in the running set for "start"; other events for unknown
		// containers may arrive out of order (e.g. after a collector restart).
		if msg.Action == events.ActionStart {
			c.running[containerID] = ct
		}
	} else {
		// Refresh name/image in case they were not populated at seed time.
		if n := strings.TrimPrefix(attrs["name"], "/"); n != "" && ct.Name == "" {
			ct.Name = n
			if ct.WorkloadKind == "" {
				ct.WorkloadKind, ct.WorkloadName = composeWorkload(attrs, n)
			}
		}
	}
	ct.LastSeenAt = eventTime

	// Translate Docker action → domain EventType and update counters.
	var (
		evtType  model.EventType
		exitCode int
	)

	switch msg.Action {

	case events.ActionStart:
		evtType = model.EventTypeStart
		ct.CurrentStatus = model.StatusRunning
		ct.StartCount++

	case events.ActionDie:
		exitCodeStr := attrs["exitCode"]
		exitCode, _ = strconv.Atoi(exitCodeStr)
		if exitCode != 0 {
			evtType = model.EventTypeCrash
			ct.CurrentStatus = model.StatusCrashing
			ct.CrashCount++
		} else {
			evtType = model.EventTypeDie
			ct.CurrentStatus = model.StatusStopped
		}
		// Container is no longer running; remove from polling set.
		// If the daemon will restart it (restart policy), we'll receive a new "start".
		delete(c.running, containerID)

	case events.ActionOOM:
		// OOM fires before the subsequent "die". Increment the counter now;
		// the crash event is emitted on the following "die" with exitCode 137.
		evtType = model.EventTypeOOM
		ct.OOMCount++

	case events.ActionPause:
		evtType = model.EventTypePause
		ct.CurrentStatus = model.StatusPaused

	case events.ActionUnPause:
		evtType = model.EventTypeResume
		ct.CurrentStatus = model.StatusRunning
	}

	ctCopy := *ct
	c.mu.Unlock()

	c.emit(ctx, collector.CollectorEvent{
		Kind:       collector.CollectorEventLifecycle,
		SourceName: c.sourceName,
		Container:  &ctCopy,
		Lifecycle: &model.LifecycleEvent{
			ContainerID: containerID,
			EventType:   evtType,
			ExitCode:    exitCode,
			SourceName:  c.sourceName,
			Metadata:    buildMetadata(attrs),
			OccurredAt:  eventTime,
		},
	})
}

// ---------------------------------------------------------------------------
// Internal: stats polling
// ---------------------------------------------------------------------------

// pollStats runs a ticker at c.pollInterval and collects resource samples
// for every container in the running set on each tick.
func (c *Collector) pollStats(ctx context.Context) {
	defer c.wg.Done()
	c.log.Debug("docker stats poller goroutine started")

	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.log.Debug("docker stats poller goroutine stopped")
			return
		case <-ticker.C:
			c.collectAllStats(ctx)
		}
	}
}

// collectAllStats takes a snapshot of the running container set and fetches
// resource stats concurrently (up to maxConcurrentStatsFetches in parallel).
func (c *Collector) collectAllStats(ctx context.Context) {
	// Snapshot the running map under a read lock to minimise lock contention.
	c.mu.RLock()
	snapshot := make([]*model.Container, 0, len(c.running))
	for _, ct := range c.running {
		cp := *ct
		snapshot = append(snapshot, &cp)
	}
	c.mu.RUnlock()

	if len(snapshot) == 0 {
		return
	}

	sem := make(chan struct{}, maxConcurrentStatsFetches)
	var wg sync.WaitGroup

	for _, ct := range snapshot {
		wg.Add(1)
		go func(ct *model.Container) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			sCtx, cancel := context.WithTimeout(ctx, statsTimeout)
			defer cancel()

			sample, err := c.Stats(sCtx, ct.ID)
			if err != nil {
				// Container may have stopped between snapshot and fetch; log at debug.
				c.log.Debug("docker stats fetch failed",
					zap.String("container_id", ct.ID),
					zap.Error(err),
				)
				return
			}
			if sample == nil {
				return
			}

			sample.SourceName = c.sourceName
			c.emit(ctx, collector.CollectorEvent{
				Kind:       collector.CollectorEventResource,
				SourceName: c.sourceName,
				Container:  ct,
				Sample:     sample,
			})
		}(ct)
	}

	wg.Wait()
}

// ---------------------------------------------------------------------------
// Internal: helpers
// ---------------------------------------------------------------------------

// emit sends event onto the events channel, dropping it if ctx is done.
func (c *Collector) emit(ctx context.Context, event collector.CollectorEvent) {
	select {
	case c.events <- event:
	case <-ctx.Done():
	}
}

// containerFromListItem converts a types.Container (from ContainerList) to
// model.Container. It is used for the initial seeding of the running set.
func containerFromListItem(ct *apitypes.Container, now time.Time) *model.Container {
	name := ""
	if len(ct.Names) > 0 {
		name = strings.TrimPrefix(ct.Names[0], "/")
	}
	kind, project := composeWorkload(ct.Labels, name)
	return &model.Container{
		ID:            ct.ID,
		Runtime:       model.RuntimeDocker,
		Name:          name,
		Image:         ct.Image,
		FirstSeenAt:   time.Unix(ct.Created, 0).UTC(),
		LastSeenAt:    now,
		CurrentStatus: listItemStatus(ct),
		WorkloadKind:  kind,
		WorkloadName:  project,
	}
}

// composeLabel is the label Docker Compose sets on every container it creates.
const composeLabel = "com.docker.compose.project"

// composeWorkload maps a container's labels (or event attributes, which carry
// the labels) to its workload: the Compose project when the label is present,
// otherwise a standalone "Container" workload named after the container
// (leading "/" tolerated). Both are empty only when the container name is
// unknown too.
func composeWorkload(labels map[string]string, containerName string) (kind, name string) {
	if p := labels[composeLabel]; p != "" {
		return model.WorkloadCompose, p
	}
	if n := strings.TrimPrefix(containerName, "/"); n != "" {
		return model.WorkloadContainer, n
	}
	return "", ""
}

// listItemStatus maps a container list entry to our status, treating a
// container that exited with a non-zero code as crashing (the list API only
// exposes the exit code inside the human-readable Status, e.g. "Exited (3) 2m ago").
func listItemStatus(ct *apitypes.Container) model.ContainerStatus {
	st := dockerStateToStatus(ct.State)
	if ct.State == "exited" {
		var code int
		if _, err := fmt.Sscanf(ct.Status, "Exited (%d)", &code); err == nil && code != 0 {
			return model.StatusCrashing
		}
	}
	return st
}

// dockerStateToStatus maps a Docker container state string to our ContainerStatus.
func dockerStateToStatus(state string) model.ContainerStatus {
	switch state {
	case "running":
		return model.StatusRunning
	case "paused":
		return model.StatusPaused
	case "restarting":
		return model.StatusCrashing
	case "exited", "dead", "removing":
		return model.StatusStopped
	default:
		return model.StatusUnknown
	}
}

// buildMetadata converts Docker event actor attributes into the Metadata map
// stored on a LifecycleEvent. It retains the kill signal and all container
// labels, discarding internal bookkeeping fields (name, image, exitCode).
func buildMetadata(attrs map[string]string) map[string]any {
	skip := map[string]bool{
		"name":     true,
		"image":    true,
		"exitCode": true,
	}
	meta := make(map[string]any, len(attrs))
	for k, v := range attrs {
		if !skip[k] {
			meta[k] = v
		}
	}
	if len(meta) == 0 {
		return nil
	}
	return meta
}

// ---------------------------------------------------------------------------
// Internal: stats parsing
// ---------------------------------------------------------------------------

// statsToSample converts a Docker StatsResponse into a model.ResourceSample.
//
// CPU calculation (Linux):
//
//	cpuDelta    = CPUStats.TotalUsage    − PreCPUStats.TotalUsage
//	systemDelta = CPUStats.SystemUsage  − PreCPUStats.SystemUsage
//	cpuPercent  = (cpuDelta / systemDelta) × numCPUs × 100
//
// Memory: MemoryStats.Usage minus the page cache (cgroup v1 only).
// Network: sum of RxBytes/TxBytes across all interfaces.
// Block I/O: sum of IoServiceBytesRecursive "Read" and "Write" entries.
func statsToSample(containerID string, s *containertypes.StatsResponse) *model.ResourceSample {
	sampledAt := s.Read.UTC()
	if sampledAt.IsZero() {
		sampledAt = time.Now().UTC()
	}

	sample := &model.ResourceSample{
		ContainerID: containerID,
		SampledAt:   sampledAt,
	}

	// --- CPU ---
	cpuDelta := float64(s.CPUStats.CPUUsage.TotalUsage) -
		float64(s.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(s.CPUStats.SystemUsage) -
		float64(s.PreCPUStats.SystemUsage)
	numCPUs := float64(s.CPUStats.OnlineCPUs)
	if numCPUs == 0 {
		// Older daemon versions omit OnlineCPUs; fall back to per-CPU slice length.
		numCPUs = float64(len(s.CPUStats.CPUUsage.PercpuUsage))
	}
	if systemDelta > 0 && cpuDelta >= 0 && numCPUs > 0 {
		sample.CPUPercent = (cpuDelta / systemDelta) * numCPUs * 100.0
	}

	// --- Memory ---
	// Match the Docker CLI: subtract inactive page cache from usage
	// (cgroup v1: total_inactive_file, cgroup v2: inactive_file).
	memUsage := s.MemoryStats.Usage
	if v, ok := s.MemoryStats.Stats["total_inactive_file"]; ok && v < memUsage {
		memUsage -= v
	} else if v, ok := s.MemoryStats.Stats["inactive_file"]; ok && v < memUsage {
		memUsage -= v
	}
	sample.MemBytes = int64(memUsage)
	if s.MemoryStats.Limit > 0 {
		sample.MemPercent = float64(memUsage) / float64(s.MemoryStats.Limit) * 100.0
	}

	// --- Network (sum all interfaces) ---
	var rxBytes, txBytes uint64
	for _, iface := range s.Networks {
		rxBytes += iface.RxBytes
		txBytes += iface.TxBytes
	}
	sample.NetRxBytes = int64(rxBytes)
	sample.NetTxBytes = int64(txBytes)

	// --- Block I/O ---
	for _, entry := range s.BlkioStats.IoServiceBytesRecursive {
		switch strings.ToLower(entry.Op) {
		case "read":
			sample.BlockReadBytes += int64(entry.Value)
		case "write":
			sample.BlockWriteBytes += int64(entry.Value)
		}
	}

	return sample
}
