package kubernetes

import (
	"context"
	"time"

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/emirhanusta/kymo/internal/collector"
	"github.com/emirhanusta/kymo/internal/model"
)

// eventResolveTimeout caps the owner lookups made while resolving an event's workload.
const eventResolveTimeout = 5 * time.Second

// K8sEventOptions configures Kubernetes Event collection.
type K8sEventOptions struct {
	// Enabled turns the Events informer on.
	Enabled bool
	// NormalReasons lists the Normal-type reasons to keep. Warning events are
	// always kept.
	NormalReasons []string
	// ExcludeNamespaces drops events from these namespaces.
	ExcludeNamespaces []string
}

// k8sEventFilter is the precomputed form of K8sEventOptions.
type k8sEventFilter struct {
	normalReasons map[string]struct{}
	excludeNS     map[string]struct{}
}

func newK8sEventFilter(o K8sEventOptions) k8sEventFilter {
	f := k8sEventFilter{
		normalReasons: make(map[string]struct{}, len(o.NormalReasons)),
		excludeNS:     make(map[string]struct{}, len(o.ExcludeNamespaces)),
	}
	for _, r := range o.NormalReasons {
		f.normalReasons[r] = struct{}{}
	}
	for _, ns := range o.ExcludeNamespaces {
		f.excludeNS[ns] = struct{}{}
	}
	return f
}

// keep reports whether the event passes the Warning / Normal-reason / namespace filters.
func (f k8sEventFilter) keep(ev *corev1.Event) bool {
	if _, skip := f.excludeNS[ev.Namespace]; skip {
		return false
	}
	if ev.Type == corev1.EventTypeWarning {
		return true
	}
	_, ok := f.normalReasons[ev.Reason]
	return ok
}

// registerK8sEventInformer adds the cluster-wide Events informer.
func (c *Collector) registerK8sEventInformer(ctx context.Context, pods corelisters.PodLister) error {
	c.podLister = pods
	inf := c.factory.Core().V1().Events().Informer()
	// A missing RBAC grant would otherwise only show up in klog output.
	_ = inf.SetWatchErrorHandlerWithContext(func(ctx context.Context, r *cache.Reflector, err error) {
		if apierrors.IsForbidden(err) {
			c.log.Warn("kubernetes: not allowed to list/watch events; Kubernetes Event history disabled until RBAC allows it",
				zap.String("source", c.sourceName), zap.Error(err))
			return
		}
		cache.DefaultWatchErrorHandler(ctx, r, err)
	})
	_, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.onK8sEvent(ctx, obj) },
		UpdateFunc: func(_, obj interface{}) { c.onK8sEvent(ctx, obj) },
	})
	return err
}

// onK8sEvent filters, converts and emits one Kubernetes Event. Persistence is
// idempotent, so the replay of the initial list on startup is harmless.
func (c *Collector) onK8sEvent(ctx context.Context, obj interface{}) {
	ev, ok := obj.(*corev1.Event)
	if !ok || !c.k8sEventFilter.keep(ev) {
		return
	}
	e := buildK8sEvent(c.sourceName, ev)
	e.WorkloadID = c.eventWorkloadID(ctx, ev)
	c.emit(ctx, collector.CollectorEvent{
		Kind:       collector.CollectorEventK8sEvent,
		SourceName: c.sourceName,
		K8sEvent:   e,
	})
}

// buildK8sEvent converts a core/v1 Event (WorkloadID is left empty).
func buildK8sEvent(source string, ev *corev1.Event) *model.K8sEvent {
	first := ev.FirstTimestamp.Time
	if first.IsZero() {
		first = ev.EventTime.Time
	}
	if first.IsZero() {
		first = ev.CreationTimestamp.Time
	}
	if first.IsZero() {
		first = time.Now()
	}
	var last time.Time
	switch {
	case ev.Series != nil && !ev.Series.LastObservedTime.IsZero():
		last = ev.Series.LastObservedTime.Time
	case !ev.LastTimestamp.IsZero():
		last = ev.LastTimestamp.Time
	case !ev.EventTime.IsZero():
		last = ev.EventTime.Time
	default:
		last = first
	}
	count := int(ev.Count)
	if ev.Series != nil && ev.Series.Count > 0 {
		count = int(ev.Series.Count)
	}
	if count < 1 {
		count = 1
	}
	component := ev.Source.Component
	if component == "" {
		component = ev.ReportingController
	}
	typ := ev.Type
	if typ == "" {
		typ = corev1.EventTypeNormal
	}
	uid := string(ev.UID)
	if uid == "" {
		uid = ev.Namespace + "/" + ev.Name
	}
	return &model.K8sEvent{
		SourceName: source,
		UID:        uid,
		Namespace:  ev.Namespace,
		Kind:       ev.InvolvedObject.Kind,
		Name:       ev.InvolvedObject.Name,
		Type:       typ,
		Reason:     ev.Reason,
		Message:    ev.Message,
		Count:      count,
		FirstSeen:  first.UTC(),
		LastSeen:   last.UTC(),
		Component:  component,
	}
}

// eventWorkloadID resolves the workload that owns the event's involved object,
// or "" when it cannot be determined cheaply.
func (c *Collector) eventWorkloadID(ctx context.Context, ev *corev1.Event) string {
	ns := ev.InvolvedObject.Namespace
	if ns == "" {
		ns = ev.Namespace
	}
	name := ev.InvolvedObject.Name
	if name == "" {
		return ""
	}
	id := func(kind, n string) string { return model.WorkloadID(c.sourceName, ns, kind, n) }

	switch ev.InvolvedObject.Kind {
	case "Pod":
		c.mu.RLock()
		wl, ok := c.workloadCache[ns+"/"+name]
		c.mu.RUnlock()
		if ok {
			return id(wl.Kind, wl.Name)
		}
		if c.podLister == nil || c.clientset == nil {
			return ""
		}
		pod, err := c.podLister.Pods(ns).Get(name)
		if err != nil || pod == nil {
			return ""
		}
		rctx, cancel := context.WithTimeout(ctx, eventResolveTimeout)
		defer cancel()
		wl = c.owners.detect(rctx, c.clientset, pod)
		return id(wl.Kind, wl.Name)
	case "ReplicaSet":
		if c.clientset != nil {
			rctx, cancel := context.WithTimeout(ctx, eventResolveTimeout)
			defer cancel()
			stub := &corev1.Pod{}
			stub.Namespace = ns
			if dep := c.owners.deploymentFor(rctx, c.clientset, stub, name); dep != "" {
				return id(model.WorkloadDeployment, dep)
			}
		}
		return id("ReplicaSet", name)
	case model.WorkloadDeployment, model.WorkloadStatefulSet, model.WorkloadDaemonSet, model.WorkloadCronJob:
		return id(ev.InvolvedObject.Kind, name)
	case model.WorkloadJob:
		if c.clientset != nil {
			rctx, cancel := context.WithTimeout(ctx, eventResolveTimeout)
			defer cancel()
			if cj := c.owners.cronJobFor(rctx, c.clientset, ns, name); cj != "" {
				return id(model.WorkloadCronJob, cj)
			}
		}
		return id(model.WorkloadJob, name)
	}
	return ""
}
