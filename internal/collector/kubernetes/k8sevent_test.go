package kubernetes

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/emirhanusta/kymo/internal/collector"
	"github.com/emirhanusta/kymo/internal/model"
)

func eventCollector(objs ...runtime.Object) *Collector {
	c := New(zap.NewNop(), "src", "", nil, "", 0, Options{K8sEvents: K8sEventOptions{
		Enabled:           true,
		NormalReasons:     []string{"ScalingReplicaSet", "Killing"},
		ExcludeNamespaces: []string{"skipme"},
	}})
	c.clientset = fake.NewSimpleClientset(objs...)
	return c
}

func k8sEvents(evs []collector.CollectorEvent) []*model.K8sEvent {
	var out []*model.K8sEvent
	for _, e := range evs {
		if e.Kind == collector.CollectorEventK8sEvent {
			out = append(out, e.K8sEvent)
		}
	}
	return out
}

func mkEvent(ns, typ, reason, kind, name string) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Namespace: ns, Name: reason + "." + name, UID: types.UID("uid-" + reason + name)},
		InvolvedObject: corev1.ObjectReference{Kind: kind, Name: name, Namespace: ns},
		Type:           typ, Reason: reason, Message: "msg",
		FirstTimestamp: metav1.NewTime(time.Unix(1000, 0)),
		Source:         corev1.EventSource{Component: "kubelet"},
	}
}

func TestK8sEventFiltering(t *testing.T) {
	ctx := context.Background()
	c := eventCollector()
	c.onK8sEvent(ctx, mkEvent("ns", "Warning", "BackOff", "Pod", "p"))
	c.onK8sEvent(ctx, mkEvent("ns", "Normal", "Pulled", "Pod", "p"))       // filtered
	c.onK8sEvent(ctx, mkEvent("ns", "Normal", "Killing", "Pod", "p"))      // allowed Normal
	c.onK8sEvent(ctx, mkEvent("skipme", "Warning", "BackOff", "Pod", "p")) // excluded namespace
	c.onK8sEvent(ctx, "not an event")                                      // wrong type
	got := k8sEvents(drain(c))
	if len(got) != 2 || got[0].Reason != "BackOff" || got[1].Reason != "Killing" {
		t.Fatalf("got %+v", got)
	}
	if got[0].Component != "kubelet" || got[0].SourceName != "src" || got[0].Type != "Warning" || got[0].Kind != "Pod" {
		t.Fatalf("fields: %+v", got[0])
	}
}

func TestK8sEventPodWorkloadFromCache(t *testing.T) {
	c := eventCollector()
	c.workloadCache["ns/web-abc-12345"] = WorkloadInfo{Kind: model.WorkloadDeployment, Name: "web"}
	c.onK8sEvent(context.Background(), mkEvent("ns", "Warning", "BackOff", "Pod", "web-abc-12345"))
	got := k8sEvents(drain(c))
	if len(got) != 1 || got[0].WorkloadID != "src:ns:Deployment:web" {
		t.Fatalf("got %+v", got)
	}
}

func TestK8sEventPodWorkloadFromLister(t *testing.T) {
	pod := ownedPod("db-0", "StatefulSet", "db", nil)
	pod.Namespace = "ns"
	idx := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	_ = idx.Add(pod)
	c := eventCollector()
	c.podLister = corelisters.NewPodLister(idx)
	c.onK8sEvent(context.Background(), mkEvent("ns", "Warning", "Unhealthy", "Pod", "db-0"))
	c.onK8sEvent(context.Background(), mkEvent("ns", "Warning", "Unhealthy", "Pod", "gone-pod"))
	got := k8sEvents(drain(c))
	if len(got) != 2 || got[0].WorkloadID != "src:ns:StatefulSet:db" || got[1].WorkloadID != "" {
		t.Fatalf("got %+v", got)
	}
}

func TestK8sEventOwnerResolution(t *testing.T) {
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "web-abc",
		OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "web"}}}}
	orphan := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "lonely"}}
	c := eventCollector(rs, orphan)
	ctx := context.Background()
	c.onK8sEvent(ctx, mkEvent("ns", "Normal", "ScalingReplicaSet", "ReplicaSet", "web-abc"))
	c.onK8sEvent(ctx, mkEvent("ns", "Normal", "ScalingReplicaSet", "ReplicaSet", "lonely"))
	c.onK8sEvent(ctx, mkEvent("ns", "Normal", "ScalingReplicaSet", "Deployment", "web"))
	c.onK8sEvent(ctx, mkEvent("ns", "Warning", "Failed", "Node", "node-1"))
	got := k8sEvents(drain(c))
	want := []string{"src:ns:Deployment:web", "src:ns:ReplicaSet:lonely", "src:ns:Deployment:web", ""}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i, w := range want {
		if got[i].WorkloadID != w {
			t.Errorf("event %d workload = %q, want %q", i, got[i].WorkloadID, w)
		}
	}
}

func TestK8sEventTimestampsAndCount(t *testing.T) {
	t0, t1, t2 := time.Unix(1000, 0), time.Unix(2000, 0), time.Unix(3000, 0)
	mt := func(x time.Time) metav1.MicroTime { return metav1.NewMicroTime(x) }

	// firstTimestamp / lastTimestamp / count.
	e := buildK8sEvent("s", &corev1.Event{FirstTimestamp: metav1.NewTime(t0), LastTimestamp: metav1.NewTime(t1), Count: 4})
	if !e.FirstSeen.Equal(t0) || !e.LastSeen.Equal(t1) || e.Count != 4 {
		t.Errorf("legacy: %+v", e)
	}
	// series wins for last seen and count; eventTime is the first-seen fallback.
	e = buildK8sEvent("s", &corev1.Event{
		EventTime: mt(t0), LastTimestamp: metav1.NewTime(t1), Count: 2,
		Series: &corev1.EventSeries{Count: 9, LastObservedTime: mt(t2)},
	})
	if !e.FirstSeen.Equal(t0) || !e.LastSeen.Equal(t2) || e.Count != 9 {
		t.Errorf("series: %+v", e)
	}
	// only eventTime: last seen = eventTime; count min 1; reportingController as component.
	e = buildK8sEvent("s", &corev1.Event{EventTime: mt(t1), ReportingController: "kube-scheduler"})
	if !e.FirstSeen.Equal(t1) || !e.LastSeen.Equal(t1) || e.Count != 1 || e.Component != "kube-scheduler" {
		t.Errorf("eventTime: %+v", e)
	}
	// only creationTimestamp.
	e = buildK8sEvent("s", &corev1.Event{ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(t2)}})
	if !e.FirstSeen.Equal(t2) || !e.LastSeen.Equal(t2) || e.Type != "Normal" {
		t.Errorf("creation: %+v", e)
	}
}

func TestK8sEventAfterStopDoesNotPanic(t *testing.T) {
	c := eventCollector()
	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
	c.onK8sEvent(context.Background(), mkEvent("ns", "Warning", "BackOff", "Pod", "p"))
}

// A pod that can't be scheduled has no container statuses; its containers must
// still be recorded so the workload shows up (with an unknown status).
func TestPendingPodContainersAreRecorded(t *testing.T) {
	c := New(zap.NewNop(), "src", "", nil, "", time.Minute, Options{})
	c.clientset = fake.NewSimpleClientset()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "bigmem-1", CreationTimestamp: metav1.Now()},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "bigmem", Image: "busybox:1.36"}}},
		Status:     corev1.PodStatus{Phase: corev1.PodPending},
	}
	go c.onPodAdd(context.Background(), pod, false)
	ev := <-c.Events()
	if ev.Kind != collector.CollectorEventState || ev.Container == nil || ev.UpdateOnly {
		t.Fatalf("want an upsert state event, got %+v", ev)
	}
	if ev.Container.ID != "shop/bigmem-1/bigmem" || ev.Container.CurrentStatus != model.StatusUnknown || ev.Container.Image != "busybox:1.36" {
		t.Fatalf("unexpected container: %+v", ev.Container)
	}

	// Deleting the still-pending pod must mark the container stopped, otherwise
	// the workload keeps counting it as an active instance forever.
	go c.onPodDelete(context.Background(), pod)
	ev = <-c.Events()
	if ev.Container == nil || !ev.UpdateOnly || ev.Container.ID != "shop/bigmem-1/bigmem" || ev.Container.CurrentStatus != model.StatusStopped {
		t.Fatalf("want an update-only stopped event, got %+v", ev)
	}
}

func TestCrashLoopBackOffIsCrashing(t *testing.T) {
	cases := map[string]model.ContainerStatus{
		"CrashLoopBackOff":  model.StatusCrashing,
		"ImagePullBackOff":  model.StatusUnknown,
		"ContainerCreating": model.StatusUnknown,
	}
	for reason, want := range cases {
		got := containerStateToStatus(corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason}})
		if got != want {
			t.Errorf("%s -> %s, want %s", reason, got, want)
		}
	}
}
