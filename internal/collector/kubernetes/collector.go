// Package kubernetes implements the collector.Collector interface for Kubernetes clusters.
// It connects to the Kubernetes API server via client-go, watches Pod lifecycle events
// using a SharedInformerFactory, and polls per-pod resource usage from the metrics-server.
package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	"github.com/emirhanusta/kymo/internal/collector"
	"github.com/emirhanusta/kymo/internal/kube"
	"github.com/emirhanusta/kymo/internal/model"
)

const (
	// defaultPollInterval is used when New is called with a zero/negative value.
	defaultPollInterval = 30 * time.Second

	// statsTimeout caps a single metrics-server request.
	statsTimeout = 10 * time.Second

	// crashLogTimeout caps one crash-log fetch (including the Previous fallback).
	crashLogTimeout = 10 * time.Second

	// maxTrackedCrashLogs bounds the crash-log de-duplication set; it is
	// cleared entirely when exceeded.
	maxTrackedCrashLogs = 5000

	// maxConcurrentCrashLogFetches limits simultaneous log requests.
	maxConcurrentCrashLogFetches = 4

	// maxConcurrentStatsFetches limits simultaneous metrics-server calls.
	maxConcurrentStatsFetches = 8
)

// ---------------------------------------------------------------------------
// Internal state types
// ---------------------------------------------------------------------------

// containerSnapshot stores the last-seen ContainerStatus fields needed for
// change detection, plus per-collector-instance accumulated event counters.
// It is always accessed under the Collector's mu lock.
type containerSnapshot struct {
	restartCount int32
	state        corev1.ContainerState
	image        string
	// memLimit is the container's memory limit in bytes (0 = unlimited), used to
	// derive MemPercent for resource samples.
	memLimit int64

	// Accumulated counters since this collector instance started watching.
	startCount  int
	crashCount  int
	oomCount    int
	firstSeenAt time.Time
}

// podRecord groups the per-container snapshots for one Pod.
// Keyed in Collector.pods by "namespace/pod-name".
type podRecord struct {
	namespace  string
	podName    string
	nodeName   string
	containers map[string]*containerSnapshot // container name → snapshot
}

// ---------------------------------------------------------------------------
// Collector
// ---------------------------------------------------------------------------

// Collector watches a Kubernetes cluster and emits CollectorEvents for Pod
// lifecycle transitions and container resource samples.
type Collector struct {
	log            *zap.Logger
	sourceName     string
	kubeconfigPath string
	kubeconfigData []byte
	kubeContext    string
	pollInterval   time.Duration
	crashLogs      CrashLogOptions
	k8sEvents      K8sEventOptions
	k8sEventFilter k8sEventFilter

	// errHost / errMgr drive opt-in error-log capture (nil when off); openLog
	// replaces the real log stream in tests.
	errHost ErrorLogHost
	errMgr  *errorLogManager
	openLog logOpener

	clientset        kubernetes.Interface
	metricsAvailable bool // true when metrics-server is reachable

	factory informers.SharedInformerFactory
	// podLister reads the pod informer cache (set in Start; used to resolve
	// the workload of Pod events). nil outside a running informer.
	podLister corelisters.PodLister

	// pods tracks the live state of all observed pods for diffing.
	// Keyed by "namespace/pod-name".
	mu   sync.RWMutex
	pods map[string]*podRecord

	// workloadCache caches WorkloadInfo by pod key (namespace/pod-name) to avoid
	// repeated API calls on pod-update events.
	// Guarded by mu.
	workloadCache map[string]WorkloadInfo

	// owners caches ReplicaSet→Deployment and Job→CronJob resolution
	// (internally synchronised).
	owners *ownerResolver

	// logged de-duplicates crash-log fetches by container + termination time.
	// Guarded by logMu, which also guards stopped (set by Stop before it waits
	// on wg, so no goroutine is added once the wait has begun).
	logMu   sync.Mutex
	logged  map[string]struct{}
	stopped bool
	logSem  chan struct{}

	events chan collector.CollectorEvent
	// eventsMu guards closed: emit holds it for reading while sending, Stop takes
	// it for writing before closing events, so no send can hit a closed channel
	// even if an informer handler is still running.
	eventsMu sync.RWMutex
	closed   bool
	wg       sync.WaitGroup
	cancel   context.CancelFunc
}

// New creates a new Kubernetes Collector.
//   - sourceName: human-readable source identifier (e.g. "prod-k8s"); used to tag every event.
//   - kubeconfigPath: path to a kubeconfig file, or "" to use in-cluster credentials then ~/.kube/config.
//   - kubeconfigData: raw kubeconfig content (takes precedence over kubeconfigPath when non-empty).
//   - kubeContext: named context in the kubeconfig to use, or "" for the current-context.
//   - pollInterval: how often to query the metrics-server. Pass 0 for the default (30 s).
//   - opts: optional behaviour such as crash-log capture (zero value = defaults, capture off).
func New(log *zap.Logger, sourceName, kubeconfigPath string, kubeconfigData []byte, kubeContext string, pollInterval time.Duration, opts Options) *Collector {
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	return &Collector{
		crashLogs:      opts.CrashLogs.normalized(),
		k8sEvents:      opts.K8sEvents,
		k8sEventFilter: newK8sEventFilter(opts.K8sEvents),
		errHost:        opts.ErrorLogs,
		logged:         make(map[string]struct{}),
		logSem:         make(chan struct{}, maxConcurrentCrashLogFetches),
		log:            log,
		sourceName:     sourceName,
		kubeconfigPath: kubeconfigPath,
		kubeconfigData: kubeconfigData,
		kubeContext:    kubeContext,
		pollInterval:   pollInterval,
		pods:           make(map[string]*podRecord),
		workloadCache:  make(map[string]WorkloadInfo),
		owners:         newOwnerResolver(),
		events:         make(chan collector.CollectorEvent, 256),
	}
}

// Start builds the Kubernetes client, creates a SharedInformerFactory for Pod
// objects, waits for the initial cache sync, and launches the stats-poller goroutine.
// It returns an error if the API server is unreachable.
func (c *Collector) Start(ctx context.Context) error {
	c.log.Info("kubernetes collector starting", zap.String("source", c.sourceName))

	cfg, err := c.buildRestConfig()
	if err != nil {
		return fmt.Errorf("kubernetes: rest config: %w", err)
	}

	c.clientset, err = kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("kubernetes: create clientset: %w", err)
	}

	// Verify connectivity before launching goroutines.
	if _, err = c.clientset.Discovery().ServerVersion(); err != nil {
		return fmt.Errorf("kubernetes: server unreachable: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	if c.errHost != nil {
		// Created before the informers start: their handlers kick it.
		c.errMgr = newErrorLogManager(c, c.errHost)
	}

	// Build informer factory with no resync (0 = no periodic resync events).
	c.factory = informers.NewSharedInformerFactory(c.clientset, 0)
	podInformer := c.factory.Core().V1().Pods()

	if _, err = podInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerDetailedFuncs{
			AddFunc:    func(obj interface{}, isInInitialList bool) { c.onPodAdd(runCtx, obj, isInInitialList) },
			UpdateFunc: func(oldObj, newObj interface{}) { c.onPodUpdate(runCtx, oldObj, newObj) },
			DeleteFunc: func(obj interface{}) { c.onPodDelete(runCtx, obj) },
		},
	); err != nil {
		cancel()
		return fmt.Errorf("kubernetes: add event handler: %w", err)
	}

	if c.k8sEvents.Enabled {
		if err = c.registerK8sEventInformer(runCtx, podInformer.Lister()); err != nil {
			cancel()
			return fmt.Errorf("kubernetes: add k8s event handler: %w", err)
		}
	}

	c.podLister = podInformer.Lister()

	// Start all informers (non-blocking; goroutines are owned by the factory).
	c.factory.Start(runCtx.Done())

	// Block until the initial list is complete and the cache is warm.
	c.log.Info("kubernetes: waiting for cache sync")
	// Only the pod cache gates startup: the Events informer may lack RBAC and
	// must not block (or fail) the source.
	if !cache.WaitForCacheSync(runCtx.Done(), podInformer.Informer().HasSynced) {
		c.log.Warn("kubernetes: pod informer cache sync timed out")
	}

	// Reconcile: anything stored for this source that no longer exists in the
	// cluster is marked stopped. Emitted asynchronously because the recorder
	// only starts draining events once Start has returned.
	if pods, err := podInformer.Lister().List(labels.Everything()); err == nil {
		ids := make([]string, 0, len(pods))
		for _, pod := range pods {
			// Spec containers, not just reported statuses: a Pending pod has no
			// statuses yet but its containers exist (see emitPendingContainers).
			for _, ctr := range pod.Spec.Containers {
				ids = append(ids, containerID(pod.Namespace, pod.Name, ctr.Name))
			}
		}
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			c.emit(runCtx, collector.CollectorEvent{
				Kind:       collector.CollectorEventSync,
				SourceName: c.sourceName,
				SyncIDs:    ids,
			})
		}()
	}

	// Check whether the metrics-server is available (optional dependency).
	c.metricsAvailable = c.checkMetricsAvailable()
	if !c.metricsAvailable {
		c.log.Warn("kubernetes: metrics-server not found; resource sampling disabled")
	}

	if c.errMgr != nil {
		c.wg.Add(2)
		go c.errMgr.run(runCtx)
		go c.errMgr.flushLoop(runCtx)
	}

	c.wg.Add(1)
	go c.pollStats(runCtx)

	c.log.Info("kubernetes collector started",
		zap.String("source", c.sourceName),
		zap.Int("known_pods", len(c.pods)),
		zap.Bool("metrics_available", c.metricsAvailable),
		zap.Duration("poll_interval", c.pollInterval),
	)
	return nil
}

// Stop cancels the run context, waits for the stats-poller goroutine to exit,
// and closes the events channel. The informer factory goroutines stop when the
// context's done channel closes.
func (c *Collector) Stop() error {
	c.log.Info("kubernetes collector stopping")
	if c.cancel != nil {
		c.cancel()
	}
	c.logMu.Lock()
	c.stopped = true
	c.logMu.Unlock()
	c.wg.Wait()
	// Wait for informer goroutines (and their event handlers) to exit. Without
	// this, handlers delivering the initial pod list can still be running when
	// the source is restarted at runtime (e.g. config reload).
	if c.factory != nil {
		c.factory.Shutdown()
	}
	c.eventsMu.Lock()
	if !c.closed {
		c.closed = true
		close(c.events)
	}
	c.eventsMu.Unlock()
	return nil
}

// Events returns the read-only channel on which this collector publishes CollectorEvents.
// The channel is closed after Stop returns.
func (c *Collector) Events() <-chan collector.CollectorEvent {
	return c.events
}

// Stats fetches a single point-in-time ResourceSample for the container identified
// by containerID. containerID must be in the format "namespace/pod-name/container-name".
// Returns (nil, nil) when the metrics-server is unavailable or has no data yet.
func (c *Collector) Stats(ctx context.Context, containerID string) (*model.ResourceSample, error) {
	namespace, podName, containerName, err := parseContainerID(containerID)
	if err != nil {
		return nil, err
	}

	data, err := c.clientset.Discovery().RESTClient().Get().
		AbsPath("/apis/metrics.k8s.io/v1beta1").
		Namespace(namespace).
		Resource("pods").
		Name(podName).
		DoRaw(ctx)
	if err != nil {
		return nil, nil //nolint:nilerr // metrics-server absent or container not yet sampled
	}

	var resp podMetricsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, nil
	}

	now := time.Now().UTC()
	for _, cm := range resp.Containers {
		if cm.Name == containerName {
			return parseContainerMetrics(containerID, cm.Usage, now), nil
		}
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// Rest config
// ---------------------------------------------------------------------------

// buildRestConfig resolves the cluster connection: uploaded kubeconfig content,
// then an explicit kubeconfig path, then in-cluster credentials / default rules.
func (c *Collector) buildRestConfig() (*rest.Config, error) {
	return kube.RestConfig(c.kubeconfigPath, c.kubeconfigData, c.kubeContext)
}

// checkMetricsAvailable returns true if the metrics.k8s.io/v1beta1 API group
// is registered in the cluster (i.e. metrics-server is installed).
func (c *Collector) checkMetricsAvailable() bool {
	_, err := c.clientset.Discovery().ServerResourcesForGroupVersion("metrics.k8s.io/v1beta1")
	return err == nil
}

// ---------------------------------------------------------------------------
// Informer event handlers
// ---------------------------------------------------------------------------

// onPodAdd is called by the informer for every Pod – both during the initial
// list sync (isInInitialList=true) and when a new Pod is created later.
//
// For initial-list pods we seed the snapshot map and then emit a synthetic
// lifecycle event (with Metadata["seed"]=true) for each container so that the
// container row is upserted into the database on startup. The seed flag lets
// the recorder skip incrementing start/crash counters.
// For real new-pod events we emit Start for every container that is already Running.
func (c *Collector) onPodAdd(ctx context.Context, obj interface{}, isInInitialList bool) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return
	}

	now := time.Now().UTC()
	pk := podKey(pod)

	c.mu.Lock()
	rec, exists := c.pods[pk]
	if !exists {
		rec = &podRecord{
			namespace:  pod.Namespace,
			podName:    pod.Name,
			nodeName:   pod.Spec.NodeName,
			containers: make(map[string]*containerSnapshot),
		}
		c.pods[pk] = rec
	}
	rec.nodeName = pod.Spec.NodeName

	// Seed / refresh snapshots.
	for _, cs := range pod.Status.ContainerStatuses {
		snap, snapExists := rec.containers[cs.Name]
		if !snapExists {
			snap = &containerSnapshot{firstSeenAt: now}
			rec.containers[cs.Name] = snap
		}
		snap.restartCount = cs.RestartCount
		snap.state = cs.State
		snap.image = cs.Image
		snap.memLimit = memoryLimit(pod, cs.Name)
	}
	c.mu.Unlock()

	// Detect workload without holding the mutex (API calls are made inside).
	wl := c.owners.detect(ctx, c.clientset, pod)

	// Store the detected workload info in the cache.
	c.mu.Lock()
	c.workloadCache[pk] = wl
	c.mu.Unlock()

	c.emitPendingContainers(ctx, pod, wl)
	c.kickErrorLogs()

	if isInInitialList {
		// Startup seeding: record each container's current state without
		// inventing lifecycle events — KYMO did not observe a start,
		// so it must not count one (that would inflate restart counters and
		// the stability penalty on every service restart).
		for _, cs := range pod.Status.ContainerStatuses {
			c.mu.RLock()
			snap := rec.containers[cs.Name]
			var ct *model.Container
			if snap != nil {
				ct = buildContainerModel(c.sourceName, pod, cs.Name, cs.Image, snap, wl)
			}
			c.mu.RUnlock()
			if ct == nil {
				continue
			}
			if cs.State.Running != nil && !cs.State.Running.StartedAt.IsZero() {
				ct.FirstSeenAt = cs.State.Running.StartedAt.UTC()
			}
			c.emit(ctx, collector.CollectorEvent{
				Kind:       collector.CollectorEventState,
				SourceName: c.sourceName,
				Container:  ct,
			})

			// For already-terminated CronJob containers, back-fill the execution
			// record. Executions are de-duplicated in the database, so replaying
			// them on every restart is harmless.
			if wl.Kind == model.WorkloadCronJob && wl.CronJobName != "" && cs.State.Terminated != nil {
				c.emitCronJobExecution(ctx, pod, cs, cs.State.Terminated, wl)
			}
		}
		return
	}

	// Real pod creation: emit Start for containers that are already Running.
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Running == nil {
			continue
		}
		c.mu.Lock()
		snap := rec.containers[cs.Name]
		snap.startCount++
		ct := buildContainerModel(c.sourceName, pod, cs.Name, cs.Image, snap, wl)
		c.mu.Unlock()

		startedAt := cs.State.Running.StartedAt.UTC()
		c.emitLifecycle(ctx, ct, model.EventTypeStart, 0, pod.Spec.NodeName, nil, startedAt)
	}
}

// onPodUpdate is called by the informer whenever a Pod's API object changes.
// It diffs the old and new ContainerStatuses to detect container lifecycle
// transitions and emits the appropriate CollectorEvents.
func (c *Collector) onPodUpdate(ctx context.Context, oldObj, newObj interface{}) {
	oldPod, ok := oldObj.(*corev1.Pod)
	if !ok {
		return
	}
	newPod, ok := newObj.(*corev1.Pod)
	if !ok {
		return
	}

	// Skip resync no-ops (same resource version means nothing changed).
	if oldPod.ResourceVersion == newPod.ResourceVersion {
		return
	}

	// Index old container statuses by name for O(1) lookup during the diff.
	oldStatuses := make(map[string]corev1.ContainerStatus, len(oldPod.Status.ContainerStatuses))
	for _, cs := range oldPod.Status.ContainerStatuses {
		oldStatuses[cs.Name] = cs
	}

	pk := podKey(newPod)
	now := time.Now().UTC()

	c.mu.Lock()
	rec, exists := c.pods[pk]
	if !exists {
		rec = &podRecord{
			namespace:  newPod.Namespace,
			podName:    newPod.Name,
			nodeName:   newPod.Spec.NodeName,
			containers: make(map[string]*containerSnapshot),
		}
		c.pods[pk] = rec
	}
	rec.nodeName = newPod.Spec.NodeName
	wl, wlCached := c.workloadCache[pk]
	c.mu.Unlock()

	// If the workload info is not yet cached (e.g. we missed the add event),
	// detect it now and cache it for future updates.
	if !wlCached {
		wl = c.owners.detect(ctx, c.clientset, newPod)
		c.mu.Lock()
		c.workloadCache[pk] = wl
		c.mu.Unlock()
	}

	for _, newCS := range newPod.Status.ContainerStatuses {
		oldCS := oldStatuses[newCS.Name] // zero-value if not seen before

		c.mu.Lock()
		snap, snapExists := rec.containers[newCS.Name]
		if !snapExists {
			snap = &containerSnapshot{firstSeenAt: now}
			rec.containers[newCS.Name] = snap
		}
		snap.memLimit = memoryLimit(newPod, newCS.Name)
		c.mu.Unlock()

		c.processContainerChange(ctx, newPod, newCS, oldCS, snap, wl)
	}

	c.emitPendingContainers(ctx, newPod, wl)
	c.kickErrorLogs()
}

// emitPendingContainers records containers that the pod spec declares but the
// kubelet has not reported yet — typically a Pending pod that cannot be
// scheduled (no node, so no container statuses at all). Without this, a
// workload whose pods never start would be invisible in KYMO, which is exactly
// when it matters most. The rows are upserted with an unknown status and no
// lifecycle event; real statuses take over once the kubelet reports them.
func (c *Collector) emitPendingContainers(ctx context.Context, pod *corev1.Pod, wl WorkloadInfo) {
	if pod.DeletionTimestamp != nil || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		return
	}
	reported := make(map[string]bool, len(pod.Status.ContainerStatuses))
	for _, cs := range pod.Status.ContainerStatuses {
		reported[cs.Name] = true
	}
	for _, ctr := range pod.Spec.Containers {
		if reported[ctr.Name] {
			continue
		}
		snap := &containerSnapshot{firstSeenAt: pod.CreationTimestamp.UTC()}
		if snap.firstSeenAt.IsZero() {
			snap.firstSeenAt = time.Now().UTC()
		}
		ct := buildContainerModel(c.sourceName, pod, ctr.Name, ctr.Image, snap, wl)
		ct.CurrentStatus = model.StatusUnknown
		c.emit(ctx, collector.CollectorEvent{
			Kind:       collector.CollectorEventState,
			SourceName: c.sourceName,
			Container:  ct,
		})
	}
}

// stopUnreportedContainers emits an UpdateOnly "stopped" state for every spec
// container of a deleted pod that has no snapshot in rec.
func (c *Collector) stopUnreportedContainers(ctx context.Context, pod *corev1.Pod, rec *podRecord, wl WorkloadInfo) {
	for _, ctr := range pod.Spec.Containers {
		if rec != nil {
			if _, ok := rec.containers[ctr.Name]; ok {
				continue
			}
		}
		snap := &containerSnapshot{firstSeenAt: pod.CreationTimestamp.UTC()}
		ct := buildContainerModel(c.sourceName, pod, ctr.Name, ctr.Image, snap, wl)
		ct.CurrentStatus = model.StatusStopped
		c.emit(ctx, collector.CollectorEvent{
			Kind:       collector.CollectorEventState,
			SourceName: c.sourceName,
			Container:  ct,
			UpdateOnly: true,
		})
	}
}

// onPodDelete is called by the informer when a Pod is removed from the cluster.
// It emits EventTypeStop for every container that was last seen as Running and
// an UpdateOnly state event (status stopped) for all other containers, then
// removes the pod record from the state map.
func (c *Collector) onPodDelete(ctx context.Context, obj interface{}) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		// Handle the tombstone wrapping that occurs when an informer misses a delete.
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			return
		}
		pod, ok = tombstone.Obj.(*corev1.Pod)
		if !ok {
			return
		}
	}

	pk := podKey(pod)
	now := time.Now().UTC()
	defer c.kickErrorLogs()

	c.mu.Lock()
	rec := c.pods[pk]
	delete(c.pods, pk)
	wl := c.workloadCache[pk]
	delete(c.workloadCache, pk)
	c.mu.Unlock()

	// The workload may be missing from the cache (e.g. update raced with add);
	// resolve it so the emitted containers carry correct workload fields.
	if wl.Kind == "" {
		wl = c.owners.detect(ctx, c.clientset, pod)
	}

	// Spec containers that never reported a status (pod stuck Pending) were
	// recorded by emitPendingContainers; mark them stopped too, otherwise they
	// stay "unknown" and keep the workload looking active forever.
	defer c.stopUnreportedContainers(ctx, pod, rec, wl)

	if rec == nil {
		return
	}

	for containerName, snap := range rec.containers {
		ct := buildContainerModel(c.sourceName, pod, containerName, snap.image, snap, wl)
		ct.CurrentStatus = model.StatusStopped

		if snap.state.Running != nil {
			c.emitLifecycle(ctx, ct, model.EventTypeStop, 0, rec.nodeName, nil, now)
			continue
		}

		// Not running (Waiting / CrashLoopBackOff / Terminated): no lifecycle
		// event, but the stored status must not stay "crashing"/"unknown"
		// forever once the pod is gone.
		c.emit(ctx, collector.CollectorEvent{
			Kind:       collector.CollectorEventState,
			SourceName: c.sourceName,
			Container:  ct,
			UpdateOnly: true,
		})
	}
}

// ---------------------------------------------------------------------------
// Container change processing (called from onPodUpdate)
// ---------------------------------------------------------------------------

// processContainerChange detects lifecycle transitions between oldCS and newCS
// and emits the appropriate sequence of CollectorEvents.
//
// The typical sequences this function handles are:
//
//   - Running → Terminated (clean exit or crash, possibly with OOM)
//   - Waiting/Terminated → Running  (start after crash backoff or init)
//   - RestartCount increased         (crash+restart between two update events)
func (c *Collector) processContainerChange(
	ctx context.Context,
	pod *corev1.Pod,
	newCS, oldCS corev1.ContainerStatus,
	snap *containerSnapshot,
	wl WorkloadInfo,
) {
	restartDelta := newCS.RestartCount - oldCS.RestartCount

	if restartDelta > 0 {
		// The container crashed and was restarted (possibly multiple times)
		// between the two update events. Use LastTerminationState for the most
		// recent exit details; emit generic crashes for earlier restarts.
		c.handleRestartedContainer(ctx, pod, newCS, snap, restartDelta, wl)
		return
	}

	// No restart increment: detect state-transition events directly.

	// → Terminated: container exited without restarting. We check newCS only so
	// we also catch Waiting→Terminated (fast-running Job/CronJob containers that
	// never surfaced as Running between two update events).
	if newCS.State.Terminated != nil && oldCS.State.Terminated == nil {
		c.handleTermination(ctx, pod, newCS, newCS.State.Terminated, snap, wl)
		return
	}

	// Not-Running → Running: container (re-)started.
	if oldCS.State.Running == nil && newCS.State.Running != nil {
		finishedAt := newCS.State.Running.StartedAt.UTC()

		c.mu.Lock()
		snap.startCount++
		snap.state = newCS.State
		snap.image = newCS.Image
		ct := buildContainerModel(c.sourceName, pod, newCS.Name, newCS.Image, snap, wl)
		c.mu.Unlock()

		c.emitLifecycle(ctx, ct, model.EventTypeStart, 0, pod.Spec.NodeName, nil, finishedAt)
		return
	}

	// All other transitions (e.g. Waiting→Waiting): just refresh state.
	c.mu.Lock()
	snap.state = newCS.State
	snap.image = newCS.Image
	snap.restartCount = newCS.RestartCount
	c.mu.Unlock()
}

// handleRestartedContainer emits the lifecycle events implied by one or more
// container restarts since the last observed update.
//
// For the most recent restart it uses LastTerminationState.Terminated for accurate
// timing and reason. Earlier restarts (restartDelta > 1) are emitted as synthetic
// crashes because we don't have their individual details.
func (c *Collector) handleRestartedContainer(
	ctx context.Context,
	pod *corev1.Pod,
	cs corev1.ContainerStatus,
	snap *containerSnapshot,
	restartDelta int32,
	wl WorkloadInfo,
) {
	// Emit generic crash events for restarts we have no detail on.
	for i := int32(1); i < restartDelta; i++ {
		c.mu.Lock()
		snap.crashCount++
		ct := buildContainerModel(c.sourceName, pod, cs.Name, cs.Image, snap, wl)
		c.mu.Unlock()

		meta := map[string]any{"synthetic": true, "reason": "intermediate_restart"}
		c.emitLifecycle(ctx, ct, model.EventTypeCrash, 1, pod.Spec.NodeName, meta, time.Now().UTC())
	}

	// Most recent termination – use LastTerminationState if available.
	if t := cs.LastTerminationState.Terminated; t != nil {
		c.handleTermination(ctx, pod, cs, t, snap, wl)
	} else {
		// No detail available; emit a generic crash.
		c.mu.Lock()
		snap.crashCount++
		ct := buildContainerModel(c.sourceName, pod, cs.Name, cs.Image, snap, wl)
		c.mu.Unlock()
		c.emitLifecycle(ctx, ct, model.EventTypeCrash, 1, pod.Spec.NodeName, nil, time.Now().UTC())
	}

	// If the container is now running again, emit the start event.
	if cs.State.Running != nil {
		startedAt := cs.State.Running.StartedAt.UTC()

		c.mu.Lock()
		snap.startCount++
		snap.state = cs.State
		snap.image = cs.Image
		snap.restartCount = cs.RestartCount
		ct := buildContainerModel(c.sourceName, pod, cs.Name, cs.Image, snap, wl)
		c.mu.Unlock()

		c.emitLifecycle(ctx, ct, model.EventTypeStart, 0, pod.Spec.NodeName, nil, startedAt)
	} else {
		// Container is in Waiting or Terminated state (crash-backoff).
		c.mu.Lock()
		snap.state = cs.State
		snap.image = cs.Image
		snap.restartCount = cs.RestartCount
		c.mu.Unlock()
	}
}

// handleTermination emits OOM and/or Crash/Stop events for a container that
// has entered the Terminated state. When the container belongs to a CronJob it
// also emits a CollectorEventCronJob for execution tracking.
func (c *Collector) handleTermination(
	ctx context.Context,
	pod *corev1.Pod,
	cs corev1.ContainerStatus,
	t *corev1.ContainerStateTerminated,
	snap *containerSnapshot,
	wl WorkloadInfo,
) {
	finishedAt := t.FinishedAt.UTC()
	if finishedAt.IsZero() {
		finishedAt = time.Now().UTC()
	}

	meta := buildTerminatedMetadata(t)

	// OOMKilled is surfaced as a distinct event before the crash event.
	if t.Reason == "OOMKilled" {
		c.mu.Lock()
		snap.oomCount++
		ct := buildContainerModel(c.sourceName, pod, cs.Name, cs.Image, snap, wl)
		c.mu.Unlock()

		c.emitLifecycle(ctx, ct, model.EventTypeOOM, 0, pod.Spec.NodeName, meta, finishedAt)
	}

	exitCode := int(t.ExitCode)
	evtType := model.EventTypeDie
	// A container killed because its pod is being deleted (rollout, scale-down,
	// eviction, node drain) exits non-zero after SIGTERM/SIGKILL, but that is a
	// planned stop, not a crash. Counting it would add fake crashes to every
	// deploy. OOM kills are still reported above.
	podDeleting := pod.DeletionTimestamp != nil
	terminating := podDeleting && t.Reason != "OOMKilled"
	switch {
	case terminating:
		evtType = model.EventTypeStop
		if meta == nil {
			meta = map[string]any{}
		}
		meta["pod_terminating"] = true
	case exitCode != 0:
		evtType = model.EventTypeCrash
		c.mu.Lock()
		snap.crashCount++
		c.mu.Unlock()
	}

	c.mu.Lock()
	snap.state = corev1.ContainerState{Terminated: t}
	snap.image = cs.Image
	snap.restartCount = cs.RestartCount
	ct := buildContainerModel(c.sourceName, pod, cs.Name, cs.Image, snap, wl)
	c.mu.Unlock()
	if terminating {
		ct.CurrentStatus = model.StatusStopped
	}

	c.emitLifecycle(ctx, ct, evtType, exitCode, pod.Spec.NodeName, meta, finishedAt)

	// Snapshot the container's logs for crashes and OOM kills (never for
	// clean exits or when the pod is being deleted).
	if !podDeleting && (evtType == model.EventTypeCrash || t.Reason == "OOMKilled") {
		c.captureCrashLog(ctx, pod, cs, t, finishedAt)
	}

	// Emit a CronJob execution record when the container is owned by a CronJob.
	if wl.Kind == model.WorkloadCronJob && wl.CronJobName != "" {
		c.emitCronJobExecution(ctx, pod, cs, t, wl)
	}
}

// emitCronJobExecution constructs and emits a CollectorEventCronJob for a
// container that terminated as part of a CronJob-owned Job.
func (c *Collector) emitCronJobExecution(
	ctx context.Context,
	pod *corev1.Pod,
	cs corev1.ContainerStatus,
	t *corev1.ContainerStateTerminated,
	wl WorkloadInfo,
) {
	now := time.Now().UTC()
	exitCode := int(t.ExitCode)
	succeeded := exitCode == 0

	var startedAt *time.Time
	if pod.Status.StartTime != nil {
		st := pod.Status.StartTime.UTC()
		startedAt = &st
	}

	completedAt := t.FinishedAt.UTC()
	if completedAt.IsZero() {
		completedAt = now
	}

	var durationSecs *float64
	if startedAt != nil {
		d := completedAt.Sub(*startedAt).Seconds()
		durationSecs = &d
	}

	exec := &model.CronJobExecution{
		SourceName:      c.sourceName,
		Namespace:       pod.Namespace,
		CronJobName:     wl.CronJobName,
		JobName:         wl.JobName,
		PodName:         pod.Name,
		StartedAt:       startedAt,
		CompletedAt:     &completedAt,
		DurationSeconds: durationSecs,
		Succeeded:       &succeeded,
		ExitCode:        exitCode,
		// Keyed on the completion time (not "now") so the same execution observed
		// twice — e.g. replayed on startup — de-duplicates in the database.
		SampledAt: completedAt,
	}

	c.emit(ctx, collector.CollectorEvent{
		Kind:        collector.CollectorEventCronJob,
		SourceName:  c.sourceName,
		Container:   &model.Container{ID: containerID(pod.Namespace, pod.Name, cs.Name)},
		CronJobExec: exec,
	})
}

// ---------------------------------------------------------------------------
// Stats polling
// ---------------------------------------------------------------------------

// pollStats runs a ticker at c.pollInterval and, on each tick, queries the
// metrics-server for every pod that has at least one running container.
func (c *Collector) pollStats(ctx context.Context) {
	defer c.wg.Done()
	c.log.Debug("kubernetes stats poller goroutine started")

	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.log.Debug("kubernetes stats poller goroutine stopped")
			return
		case <-ticker.C:
			if c.metricsAvailable {
				c.collectAllPodStats(ctx)
			}
		}
	}
}

// collectAllPodStats snapshots the set of pods that have running containers
// and fetches their metrics concurrently.
func (c *Collector) collectAllPodStats(ctx context.Context) {
	type podRef struct {
		namespace string
		podName   string
		nodeName  string
	}

	c.mu.RLock()
	var targets []podRef
	for _, rec := range c.pods {
		hasRunning := false
		for _, snap := range rec.containers {
			if snap.state.Running != nil {
				hasRunning = true
				break
			}
		}
		if hasRunning {
			targets = append(targets, podRef{rec.namespace, rec.podName, rec.nodeName})
		}
	}
	c.mu.RUnlock()

	if len(targets) == 0 {
		return
	}

	sem := make(chan struct{}, maxConcurrentStatsFetches)
	var wg sync.WaitGroup

	for _, t := range targets {
		wg.Add(1)
		go func(namespace, podName, nodeName string) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			sCtx, cancel := context.WithTimeout(ctx, statsTimeout)
			defer cancel()

			c.collectPodStats(sCtx, namespace, podName, nodeName)
		}(t.namespace, t.podName, t.nodeName)
	}
	wg.Wait()
}

// collectPodStats fetches metrics for all containers in a single Pod from the
// metrics-server and emits a resource CollectorEvent for each container.
func (c *Collector) collectPodStats(ctx context.Context, namespace, podName, nodeName string) {
	data, err := c.clientset.Discovery().RESTClient().Get().
		AbsPath("/apis/metrics.k8s.io/v1beta1").
		Namespace(namespace).
		Resource("pods").
		Name(podName).
		DoRaw(ctx)
	if err != nil {
		c.log.Debug("kubernetes: pod metrics unavailable",
			zap.String("namespace", namespace),
			zap.String("pod", podName),
			zap.Error(err),
		)
		return
	}

	var resp podMetricsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		c.log.Debug("kubernetes: failed to decode pod metrics", zap.Error(err))
		return
	}

	now := time.Now().UTC()
	podKey := namespace + "/" + podName

	for _, cm := range resp.Containers {
		cID := containerID(namespace, podName, cm.Name)
		sample := parseContainerMetrics(cID, cm.Usage, now)

		c.mu.RLock()
		rec := c.pods[podKey]
		var snap *containerSnapshot
		if rec != nil {
			snap = rec.containers[cm.Name]
		}
		c.mu.RUnlock()

		if snap == nil {
			continue
		}

		// Build a lightweight container record for the event; avoid holding the lock
		// during the channel send.
		c.mu.RLock()
		wl := c.workloadCache[podKey]
		ct := &model.Container{
			ID:            cID,
			Runtime:       model.RuntimeKubernetes,
			SourceName:    c.sourceName,
			Name:          cm.Name,
			Image:         snap.image,
			Namespace:     namespace,
			NodeName:      nodeName,
			FirstSeenAt:   snap.firstSeenAt,
			LastSeenAt:    now,
			CurrentStatus: containerStateToStatus(snap.state),
			WorkloadKind:  wl.Kind,
			WorkloadName:  wl.Name,
			IsSystem:      wl.IsSystem,
		}
		if snap.memLimit > 0 {
			sample.MemPercent = float64(sample.MemBytes) / float64(snap.memLimit) * 100.0
		}
		c.mu.RUnlock()

		sample.SourceName = c.sourceName
		c.emit(ctx, collector.CollectorEvent{
			Kind:       collector.CollectorEventResource,
			SourceName: c.sourceName,
			Container:  ct,
			Sample:     sample,
		})
	}
}

// ---------------------------------------------------------------------------
// Helpers: emitting
// ---------------------------------------------------------------------------

// emitLifecycle constructs and emits a lifecycle CollectorEvent.
func (c *Collector) emitLifecycle(
	ctx context.Context,
	ct *model.Container,
	evtType model.EventType,
	exitCode int,
	nodeName string,
	metadata map[string]any,
	occurredAt time.Time,
) {
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	c.emit(ctx, collector.CollectorEvent{
		Kind:       collector.CollectorEventLifecycle,
		SourceName: c.sourceName,
		Container:  ct,
		Lifecycle: &model.LifecycleEvent{
			ContainerID: ct.ID,
			EventType:   evtType,
			ExitCode:    exitCode,
			SourceName:  c.sourceName,
			NodeName:    nodeName,
			Metadata:    metadata,
			OccurredAt:  occurredAt,
		},
	})
}

// emit sends an event onto the events channel, dropping it if ctx is cancelled.
func (c *Collector) emit(ctx context.Context, event collector.CollectorEvent) {
	c.eventsMu.RLock()
	defer c.eventsMu.RUnlock()
	if c.closed {
		return
	}
	select {
	case c.events <- event:
	case <-ctx.Done():
	}
}

// ---------------------------------------------------------------------------
// Helpers: model building
// ---------------------------------------------------------------------------

// buildContainerModel constructs a model.Container from a pod, container name,
// image, the current snapshot, and workload info. It must be called with c.mu
// held at least for read (since it reads snap fields), but since the caller
// always holds it, this is guaranteed.
func buildContainerModel(sourceName string, pod *corev1.Pod, containerName, image string, snap *containerSnapshot, wl WorkloadInfo) *model.Container {
	status := containerStateToStatus(snap.state)
	if status == model.StatusCrashing && (pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded) {
		// A pod in a terminal phase (typically a Job pod with restartPolicy
		// Never/OnFailure that gave up) will never restart: it is finished,
		// not crash looping. The failed run itself is reported by the Job
		// execution data.
		status = model.StatusStopped
	}
	return &model.Container{
		ID:            containerID(pod.Namespace, pod.Name, containerName),
		Runtime:       model.RuntimeKubernetes,
		SourceName:    sourceName,
		Name:          containerName,
		Image:         image,
		Namespace:     pod.Namespace,
		NodeName:      pod.Spec.NodeName,
		FirstSeenAt:   snap.firstSeenAt,
		LastSeenAt:    time.Now().UTC(),
		StartCount:    snap.startCount,
		CrashCount:    snap.crashCount,
		OOMCount:      snap.oomCount,
		CurrentStatus: status,
		WorkloadKind:  wl.Kind,
		WorkloadName:  wl.Name,
		IsSystem:      wl.IsSystem,
		Revision:      podRevision(pod, wl),
	}
}

// podRevision returns the rollout revision of a pod: pod-template-hash
// (Deployment/ReplicaSet), controller-revision-hash (StatefulSet/DaemonSet) or
// the Job name for Job/CronJob pods. Empty when unknown.
func podRevision(pod *corev1.Pod, wl WorkloadInfo) string {
	if v := pod.Labels["pod-template-hash"]; v != "" {
		return v
	}
	if v := pod.Labels["controller-revision-hash"]; v != "" {
		return v
	}
	if wl.Kind == model.WorkloadJob || wl.Kind == model.WorkloadCronJob {
		if wl.JobName != "" {
			return wl.JobName
		}
		if wl.Kind == model.WorkloadJob {
			return wl.Name
		}
	}
	return ""
}

// containerStateToStatus maps a Kubernetes ContainerState to our ContainerStatus.
func containerStateToStatus(state corev1.ContainerState) model.ContainerStatus {
	switch {
	case state.Running != nil:
		return model.StatusRunning
	case state.Terminated != nil:
		if state.Terminated.ExitCode != 0 {
			return model.StatusCrashing
		}
		return model.StatusStopped
	case state.Waiting != nil && state.Waiting.Reason == "CrashLoopBackOff":
		// Waiting between restarts of a crashing container.
		return model.StatusCrashing
	default: // Waiting (ContainerCreating, ImagePullBackOff, …) or not reported
		return model.StatusUnknown
	}
}

// memoryLimit returns the memory limit in bytes declared for the named
// container in the pod spec, or 0 when none is set.
func memoryLimit(pod *corev1.Pod, containerName string) int64 {
	for _, ctr := range pod.Spec.Containers {
		if ctr.Name != containerName {
			continue
		}
		if q, ok := ctr.Resources.Limits[corev1.ResourceMemory]; ok {
			return q.Value()
		}
		return 0
	}
	return 0
}

// containerID returns the logical container identifier used as the primary key
// in our domain model. Format: "namespace/pod-name/container-name".
func containerID(namespace, podName, containerName string) string {
	return namespace + "/" + podName + "/" + containerName
}

// podKey returns the map key for a Pod: "namespace/pod-name".
func podKey(pod *corev1.Pod) string {
	return pod.Namespace + "/" + pod.Name
}

// parseContainerID splits "namespace/pod-name/container-name" into its components.
func parseContainerID(id string) (namespace, podName, containerName string, err error) {
	parts := strings.SplitN(id, "/", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", fmt.Errorf("kubernetes: invalid container ID %q (want namespace/pod/container)", id)
	}
	return parts[0], parts[1], parts[2], nil
}

// buildTerminatedMetadata extracts context from a ContainerStateTerminated for
// storage in LifecycleEvent.Metadata.
func buildTerminatedMetadata(t *corev1.ContainerStateTerminated) map[string]any {
	if t == nil {
		return nil
	}
	meta := make(map[string]any, 4)
	if t.Reason != "" {
		meta["reason"] = t.Reason
	}
	if t.Message != "" {
		meta["message"] = t.Message
	}
	if t.Signal != 0 {
		meta["signal"] = t.Signal
	}
	if t.ContainerID != "" {
		meta["runtime_container_id"] = t.ContainerID
	}
	if len(meta) == 0 {
		return nil
	}
	return meta
}

// ---------------------------------------------------------------------------
// Helpers: metrics decoding
// ---------------------------------------------------------------------------

// podMetricsResponse is a minimal local representation of the
// metrics.k8s.io/v1beta1 PodMetrics response body. Using a local struct avoids
// adding k8s.io/metrics as a direct dependency.
type podMetricsResponse struct {
	Containers []struct {
		Name  string            `json:"name"`
		Usage map[string]string `json:"usage"` // "cpu": "125m", "memory": "52428800"
	} `json:"containers"`
}

// parseContainerMetrics converts a metrics-server usage map into a ResourceSample.
//
// CPU conversion: the metrics-server reports nanocores or milliCPU strings
// (e.g. "125m" = 125 milliCPU). MilliValue() / 10 gives CPUPercent where 100 %
// represents one full CPU core.
//
// Memory: Value() returns bytes directly.
//
// Network and block I/O are not available from the metrics-server API.
func parseContainerMetrics(cID string, usage map[string]string, sampledAt time.Time) *model.ResourceSample {
	sample := &model.ResourceSample{
		ContainerID: cID,
		SampledAt:   sampledAt,
	}

	if cpuStr, ok := usage["cpu"]; ok {
		if q, err := resource.ParseQuantity(cpuStr); err == nil {
			// 1000 milliCPU = 1 CPU = 100 % → divide by 10
			sample.CPUPercent = float64(q.MilliValue()) / 10.0
		}
	}

	if memStr, ok := usage["memory"]; ok {
		if q, err := resource.ParseQuantity(memStr); err == nil {
			sample.MemBytes = q.Value()
		}
	}

	return sample
}
