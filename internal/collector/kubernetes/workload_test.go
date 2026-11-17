package kubernetes

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/emirhanusta/kymo/internal/collector"
	"github.com/emirhanusta/kymo/internal/model"
)

func ownedPod(name, ownerKind, ownerName string, labels map[string]string) *corev1.Pod {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "app", Labels: labels}}
	if ownerKind != "" {
		p.OwnerReferences = []metav1.OwnerReference{{Kind: ownerKind, Name: ownerName}}
	}
	return p
}

func ownedBy(kind, name string) []metav1.OwnerReference {
	return []metav1.OwnerReference{{Kind: kind, Name: name}}
}

func countGets(cs *fake.Clientset, resource string) int {
	n := 0
	for _, a := range cs.Actions() {
		if a.GetVerb() == "get" && a.GetResource().Resource == resource {
			n++
		}
	}
	return n
}

func TestDetectWorkload(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewSimpleClientset(
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-abc", Namespace: "app", OwnerReferences: ownedBy("Deployment", "web")}},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "bare-rs", Namespace: "app"}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "nightly-1700", Namespace: "app", OwnerReferences: ownedBy("CronJob", "nightly")}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "migrate-7", Namespace: "app"}},
	)

	tests := []struct {
		name    string
		pod     *corev1.Pod
		kind    string
		wlName  string
		cronjob string
		jobName string
	}{
		{"deployment", ownedPod("p", "ReplicaSet", "web-abc", nil), model.WorkloadDeployment, "web", "", ""},
		{"bare replicaset", ownedPod("p", "ReplicaSet", "bare-rs", nil), "ReplicaSet", "bare-rs", "", ""},
		{"statefulset", ownedPod("p", "StatefulSet", "db", nil), model.WorkloadStatefulSet, "db", "", ""},
		{"daemonset", ownedPod("p", "DaemonSet", "agent", nil), model.WorkloadDaemonSet, "agent", "", ""},
		{"cronjob", ownedPod("p", "Job", "nightly-1700", nil), model.WorkloadCronJob, "nightly", "nightly", "nightly-1700"},
		{"plain job (digit suffix, API says none)", ownedPod("p", "Job", "migrate-7", nil), model.WorkloadJob, "migrate-7", "", "migrate-7"},
		{"bare pod", ownedPod("lonely", "", "", nil), model.WorkloadPod, "lonely", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectWorkload(ctx, cs, tt.pod)
			if got.Kind != tt.kind || got.Name != tt.wlName || got.CronJobName != tt.cronjob || got.JobName != tt.jobName {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func failingClient(err error) *fake.Clientset {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("get", "*", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, err
	})
	return cs
}

func TestDetectWorkloadFallbacks(t *testing.T) {
	ctx := context.Background()
	errs := map[string]error{
		"notfound":  apierrors.NewNotFound(schema.GroupResource{Resource: "x"}, "x"),
		"forbidden": apierrors.NewForbidden(schema.GroupResource{Resource: "x"}, "x", errors.New("no")),
	}
	for en, e := range errs {
		cs := failingClient(e)
		t.Run(en+"/rs with hash", func(t *testing.T) {
			got := DetectWorkload(ctx, cs, ownedPod("p", "ReplicaSet", "web-5d8f7", map[string]string{"pod-template-hash": "5d8f7"}))
			if got.Kind != model.WorkloadDeployment || got.Name != "web" {
				t.Fatalf("got %+v", got)
			}
		})
		t.Run(en+"/rs hash mismatch", func(t *testing.T) {
			got := DetectWorkload(ctx, cs, ownedPod("p", "ReplicaSet", "web-5d8f7", map[string]string{"pod-template-hash": "zzz"}))
			if got.Kind != "ReplicaSet" || got.Name != "web-5d8f7" {
				t.Fatalf("got %+v", got)
			}
		})
		t.Run(en+"/rs no label", func(t *testing.T) {
			got := DetectWorkload(ctx, cs, ownedPod("p", "ReplicaSet", "web-5d8f7", nil))
			if got.Kind != "ReplicaSet" {
				t.Fatalf("got %+v", got)
			}
		})
		t.Run(en+"/cronjob job", func(t *testing.T) {
			got := DetectWorkload(ctx, cs, ownedPod("p", "Job", "nightly-1700", nil))
			if got.Kind != model.WorkloadCronJob || got.Name != "nightly" || got.CronJobName != "nightly" || got.JobName != "nightly-1700" {
				t.Fatalf("got %+v", got)
			}
		})
		t.Run(en+"/plain job", func(t *testing.T) {
			got := DetectWorkload(ctx, cs, ownedPod("p", "Job", "migrate", nil))
			if got.Kind != model.WorkloadJob || got.Name != "migrate" {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestOwnerResolverCache(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewSimpleClientset(
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-abc", Namespace: "app", OwnerReferences: ownedBy("Deployment", "web")}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "nightly-1", Namespace: "app", OwnerReferences: ownedBy("CronJob", "nightly")}},
	)
	r := newOwnerResolver()
	for i := 0; i < 3; i++ {
		if got := r.detect(ctx, cs, ownedPod("p", "ReplicaSet", "web-abc", nil)); got.Name != "web" {
			t.Fatalf("got %+v", got)
		}
		if got := r.detect(ctx, cs, ownedPod("q", "Job", "nightly-1", nil)); got.Name != "nightly" {
			t.Fatalf("got %+v", got)
		}
	}
	if n := countGets(cs, "replicasets"); n != 1 {
		t.Fatalf("replicaset GETs = %d, want 1", n)
	}
	if n := countGets(cs, "jobs"); n != 1 {
		t.Fatalf("job GETs = %d, want 1", n)
	}
}

func newTestCollector(cs *fake.Clientset) *Collector {
	c := New(zap.NewNop(), "src", "", nil, "", 0)
	c.clientset = cs
	return c
}

func drain(c *Collector) []collector.CollectorEvent {
	var out []collector.CollectorEvent
	for {
		select {
		case e := <-c.Events():
			out = append(out, e)
		case <-time.After(50 * time.Millisecond):
			return out
		}
	}
}

func TestOnPodDeleteEmitsStopAndStoppedState(t *testing.T) {
	ctx := context.Background()
	c := newTestCollector(fake.NewSimpleClientset())
	pod := ownedPod("db-0", "StatefulSet", "db", nil)
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{Name: "run", Image: "img", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.Now()}}},
		{Name: "wait", Image: "img", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}},
	}
	c.onPodAdd(ctx, pod, true)
	drain(c)

	c.onPodDelete(ctx, pod)
	evs := drain(c)
	var stops, states int
	for _, e := range evs {
		switch e.Kind {
		case collector.CollectorEventLifecycle:
			stops++
			if e.Lifecycle.EventType != model.EventTypeStop || e.Container.Name != "run" {
				t.Errorf("unexpected lifecycle %+v", e)
			}
		case collector.CollectorEventState:
			states++
			ct := e.Container
			if !e.UpdateOnly || ct.Name != "wait" || ct.CurrentStatus != model.StatusStopped ||
				ct.SourceName != "src" || ct.WorkloadKind != model.WorkloadStatefulSet || ct.WorkloadName != "db" {
				t.Errorf("unexpected state event %+v / %+v", e, ct)
			}
		}
	}
	if stops != 1 || states != 1 || len(evs) != 2 {
		t.Fatalf("stops=%d states=%d total=%d", stops, states, len(evs))
	}
}

func TestOnPodAddStatefulSetEmitsState(t *testing.T) {
	ctx := context.Background()
	c := newTestCollector(fake.NewSimpleClientset())
	pod := ownedPod("db-0", "StatefulSet", "db", nil)
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{Name: "main", Image: "img", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.Now()}}},
	}
	c.onPodAdd(ctx, pod, true)
	evs := drain(c)
	if len(evs) != 1 || evs[0].Kind != collector.CollectorEventState {
		t.Fatalf("events: %+v", evs)
	}
	ct := evs[0].Container
	if ct.WorkloadKind != model.WorkloadStatefulSet || ct.WorkloadName != "db" {
		t.Fatalf("container: %+v", ct)
	}
}

func TestTerminationDuringPodDeletionIsAStop(t *testing.T) {
	c := New(zap.NewNop(), "src", "", nil, "", time.Minute)
	c.clientset = fake.NewSimpleClientset()

	now := metav1.Now()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "api-1", DeletionTimestamp: &now}}
	cs := corev1.ContainerStatus{Name: "api", Image: "img"}
	term := &corev1.ContainerStateTerminated{ExitCode: 137, Reason: "Error"}

	go c.handleTermination(context.Background(), pod, cs, term, &containerSnapshot{}, WorkloadInfo{Kind: "Deployment", Name: "api"})
	ev := <-c.Events()
	if ev.Lifecycle == nil || ev.Lifecycle.EventType != model.EventTypeStop {
		t.Fatalf("want a stop event for a terminating pod, got %+v", ev.Lifecycle)
	}
	if ev.Container.CurrentStatus != model.StatusStopped {
		t.Fatalf("status = %s, want stopped", ev.Container.CurrentStatus)
	}

	// Without a deletion timestamp the same exit is a crash.
	pod.DeletionTimestamp = nil
	go c.handleTermination(context.Background(), pod, cs, term, &containerSnapshot{}, WorkloadInfo{Kind: "Deployment", Name: "api"})
	if ev := <-c.Events(); ev.Lifecycle.EventType != model.EventTypeCrash {
		t.Fatalf("want crash, got %s", ev.Lifecycle.EventType)
	}
}

// A handler that runs after Stop (e.g. an informer still delivering the initial
// list while the source is restarted) must not panic with "send on closed channel".
func TestEmitAfterStopDoesNotPanic(t *testing.T) {
	c := New(zap.NewNop(), "src", "", nil, "", time.Minute)
	c.clientset = fake.NewSimpleClientset()
	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
	c.emit(context.Background(), collector.CollectorEvent{Kind: collector.CollectorEventState})
	if err := c.Stop(); err != nil { // idempotent
		t.Fatal(err)
	}
}
