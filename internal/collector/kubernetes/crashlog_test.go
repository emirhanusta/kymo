package kubernetes

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/emirhanusta/kymo/internal/collector"
	"github.com/emirhanusta/kymo/internal/model"
)

func logCollector(enabled bool) (*Collector, *fake.Clientset) {
	cs := fake.NewSimpleClientset()
	c := New(zap.NewNop(), "src", "", nil, "", 0, Options{CrashLogs: CrashLogOptions{Enabled: enabled, TailLines: 50, MaxBytes: 4096}})
	c.clientset = cs
	return c, cs
}

func crashPod(rv string, restarts int32, state corev1.ContainerState, last *corev1.ContainerStateTerminated) *corev1.Pod {
	cs := corev1.ContainerStatus{Name: "app", Image: "img:1", RestartCount: restarts, State: state}
	if last != nil {
		cs.LastTerminationState = corev1.ContainerState{Terminated: last}
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "web-abc", Namespace: "ns", ResourceVersion: rv,
			Labels: map[string]string{"pod-template-hash": "abc"},
		},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{cs}},
	}
}

func logActions(cs *fake.Clientset) []*corev1.PodLogOptions {
	var out []*corev1.PodLogOptions
	for _, a := range cs.Actions() {
		if a.GetSubresource() == "log" {
			out = append(out, a.(interface{ GetValue() any }).GetValue().(*corev1.PodLogOptions))
		}
	}
	return out
}

func crashLogEvents(evs []collector.CollectorEvent) []*model.CrashLog {
	var out []*model.CrashLog
	for _, e := range evs {
		if e.Kind == collector.CollectorEventCrashLog {
			out = append(out, e.CrashLog)
		}
	}
	return out
}

func running() corev1.ContainerState {
	return corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.Now()}}
}

func TestCrashFetchesPreviousLogs(t *testing.T) {
	ctx := context.Background()
	c, cs := logCollector(true)
	finished := time.Date(2026, 3, 1, 12, 0, 0, 123, time.UTC)

	oldPod := crashPod("1", 0, running(), nil)
	c.onPodAdd(ctx, oldPod, true)
	drain(c)

	newPod := crashPod("2", 1, running(), &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error", FinishedAt: metav1.NewTime(finished)})
	c.onPodUpdate(ctx, oldPod, newPod)
	c.wg.Wait()
	evs := drain(c)

	acts := logActions(cs)
	if len(acts) != 1 || !acts[0].Previous || acts[0].Container != "app" ||
		acts[0].TailLines == nil || *acts[0].TailLines != 50 ||
		acts[0].LimitBytes != nil { // LimitBytes would cut the newest lines; the tail is trimmed client-side

		t.Fatalf("log requests = %+v", acts)
	}
	logs := crashLogEvents(evs)
	if len(logs) != 1 {
		t.Fatalf("crash log events = %d", len(logs))
	}
	l := logs[0]
	if !l.OccurredAt.Equal(finished) || l.ContainerID != "ns/web-abc/app" || !l.Previous ||
		l.Lines != "fake logs" || l.LineCount != 1 || l.Reason != "Error" || l.ExitCode != 1 || l.Truncated {
		t.Errorf("unexpected crash log %+v", l)
	}
	for _, e := range evs {
		if e.Kind == collector.CollectorEventLifecycle && e.Lifecycle.EventType == model.EventTypeCrash &&
			!e.Lifecycle.OccurredAt.Equal(l.OccurredAt) {
			t.Errorf("crash event time %v != log time %v", e.Lifecycle.OccurredAt, l.OccurredAt)
		}
		if e.Container != nil && e.Container.Revision != "" && e.Container.Revision != "abc" {
			t.Errorf("revision = %q", e.Container.Revision)
		}
	}
}

func TestStillTerminatedUsesCurrentLogs(t *testing.T) {
	ctx := context.Background()
	c, cs := logCollector(true)
	oldPod := crashPod("1", 0, running(), nil)
	c.onPodAdd(ctx, oldPod, true)
	drain(c)

	term := &corev1.ContainerStateTerminated{ExitCode: 2, Reason: "Error", FinishedAt: metav1.Now()}
	newPod := crashPod("2", 0, corev1.ContainerState{Terminated: term}, nil)
	c.onPodUpdate(ctx, oldPod, newPod)
	c.wg.Wait()

	acts := logActions(cs)
	if len(acts) != 1 || acts[0].Previous {
		t.Fatalf("want one non-previous request, got %+v", acts)
	}
	if got := crashLogEvents(drain(c)); len(got) != 1 || got[0].Previous {
		t.Fatalf("crash logs = %+v", got)
	}
}

func TestOOMAndCrashFetchOnce(t *testing.T) {
	ctx := context.Background()
	c, cs := logCollector(true)
	oldPod := crashPod("1", 0, running(), nil)
	c.onPodAdd(ctx, oldPod, true)
	drain(c)

	last := &corev1.ContainerStateTerminated{ExitCode: 137, Reason: "OOMKilled", FinishedAt: metav1.Now()}
	newPod := crashPod("2", 1, running(), last)
	c.onPodUpdate(ctx, oldPod, newPod)
	// The same termination seen again on a later pod update.
	again := crashPod("3", 1, running(), last)
	c.onPodUpdate(ctx, newPod, again)
	c.wg.Wait()
	evs := drain(c)

	var oom, crash int
	for _, e := range evs {
		if e.Kind != collector.CollectorEventLifecycle {
			continue
		}
		switch e.Lifecycle.EventType {
		case model.EventTypeOOM:
			oom++
		case model.EventTypeCrash:
			crash++
		}
	}
	if oom < 1 || crash < 1 {
		t.Fatalf("expected OOM and crash events, got oom=%d crash=%d", oom, crash)
	}
	if n := len(logActions(cs)); n != 1 {
		t.Errorf("log fetches = %d, want 1", n)
	}
	if got := crashLogEvents(evs); len(got) != 1 || got[0].Reason != "OOMKilled" {
		t.Errorf("crash log events = %+v", got)
	}
}

func TestTerminatingPodDoesNotFetchLogs(t *testing.T) {
	ctx := context.Background()
	c, cs := logCollector(true)
	oldPod := crashPod("1", 0, running(), nil)
	c.onPodAdd(ctx, oldPod, true)
	drain(c)

	now := metav1.Now()
	term := &corev1.ContainerStateTerminated{ExitCode: 143, Reason: "Error", FinishedAt: metav1.Now()}
	newPod := crashPod("2", 0, corev1.ContainerState{Terminated: term}, nil)
	newPod.DeletionTimestamp = &now
	c.onPodUpdate(ctx, oldPod, newPod)
	c.wg.Wait()

	if n := len(logActions(cs)); n != 0 {
		t.Errorf("log fetches = %d, want 0", n)
	}
	if got := crashLogEvents(drain(c)); len(got) != 0 {
		t.Errorf("unexpected crash logs %+v", got)
	}
}

func TestCleanExitDoesNotFetchLogs(t *testing.T) {
	ctx := context.Background()
	c, cs := logCollector(true)
	oldPod := crashPod("1", 0, running(), nil)
	c.onPodAdd(ctx, oldPod, true)
	drain(c)

	term := &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed", FinishedAt: metav1.Now()}
	c.onPodUpdate(ctx, oldPod, crashPod("2", 0, corev1.ContainerState{Terminated: term}, nil))
	c.wg.Wait()
	if n := len(logActions(cs)); n != 0 {
		t.Errorf("log fetches = %d, want 0", n)
	}
}

func TestCrashLogsDisabled(t *testing.T) {
	ctx := context.Background()
	c, cs := logCollector(false)
	oldPod := crashPod("1", 0, running(), nil)
	c.onPodAdd(ctx, oldPod, true)
	drain(c)

	last := &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error", FinishedAt: metav1.Now()}
	c.onPodUpdate(ctx, oldPod, crashPod("2", 1, running(), last))
	c.wg.Wait()
	if n := len(logActions(cs)); n != 0 {
		t.Errorf("log fetches = %d, want 0", n)
	}
}

func TestPodRevision(t *testing.T) {
	mk := func(l map[string]string) *corev1.Pod { return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: l}} }
	cases := []struct {
		pod  *corev1.Pod
		wl   WorkloadInfo
		want string
	}{
		{mk(map[string]string{"pod-template-hash": "h1"}), WorkloadInfo{Kind: model.WorkloadDeployment}, "h1"},
		{mk(map[string]string{"controller-revision-hash": "web-77"}), WorkloadInfo{Kind: model.WorkloadStatefulSet}, "web-77"},
		{mk(nil), WorkloadInfo{Kind: model.WorkloadCronJob, Name: "cj", CronJobName: "cj", JobName: "cj-123"}, "cj-123"},
		{mk(nil), WorkloadInfo{Kind: model.WorkloadJob, Name: "migrate", JobName: "migrate"}, "migrate"},
		{mk(nil), WorkloadInfo{Kind: model.WorkloadPod}, ""},
	}
	for i, tc := range cases {
		if got := podRevision(tc.pod, tc.wl); got != tc.want {
			t.Errorf("case %d: got %q want %q", i, got, tc.want)
		}
	}
}

func TestCountLines(t *testing.T) {
	for in, want := range map[string]int{"": 0, "a": 1, "a\n": 1, "a\nb": 2, "a\nb\n": 2} {
		if got := countLines(in); got != want {
			t.Errorf("countLines(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestKeepTailKeepsNewestLines(t *testing.T) {
	data := []byte("old line 1\nold line 2\nnewest line\n")
	got := string(keepTail(data, 16))
	if got != "newest line\n" {
		t.Fatalf("keepTail = %q, want the newest complete line", got)
	}
	if string(keepTail(data, 1000)) != string(data) {
		t.Fatal("data under the limit must be returned unchanged")
	}
}
