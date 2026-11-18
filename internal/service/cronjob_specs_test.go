package service

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func cj(ns, name, schedule string, suspend bool, tz string) *batchv1.CronJob {
	c := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       batchv1.CronJobSpec{Schedule: schedule, Suspend: &suspend},
	}
	if tz != "" {
		c.Spec.TimeZone = &tz
	}
	return c
}

// fakeMgmt returns a management service wired to a fake clientset (never a real cluster).
func fakeMgmt(t *testing.T, objs ...runtime.Object) (*managementService, *fake.Clientset, *int) {
	t.Helper()
	cs := fake.NewSimpleClientset(objs...)
	lists := 0
	cs.PrependReactor("list", "cronjobs", func(k8stesting.Action) (bool, runtime.Object, error) {
		lists++
		return false, nil, nil
	})
	s := NewManagementService(zap.NewNop()).(*managementService)
	s.k8sClients["prod"] = cs
	s.k8sOrder = []string{"prod"}
	return s, cs, &lists
}

func TestListCronJobSpecs(t *testing.T) {
	last := metav1.NewTime(time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC))
	withStatus := cj("app", "report", "*/5 * * * *", false, "")
	withStatus.Status.LastScheduleTime = &last
	s, _, _ := fakeMgmt(t,
		withStatus,
		cj("app", "paused", "0 * * * *", true, ""),
		cj("app", "broken", "not a schedule", false, ""),
		cj("app", "tokyo", "0 9 * * *", false, "Asia/Tokyo"),
		cj("app", "badzone", "0 9 * * *", false, "Mars/Base"),
		cj("app", "hourly", "@hourly", false, ""),
	)

	specs, err := s.ListCronJobSpecs(context.Background(), "prod")
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]CronJobSpec{}
	for _, sp := range specs {
		if sp.SourceName != "prod" {
			t.Errorf("%s: source = %q", sp.Name, sp.SourceName)
		}
		by[sp.Name] = sp
	}
	if len(by) != 6 {
		t.Fatalf("got %d specs", len(by))
	}
	if r := by["report"]; r.NextRun == nil || r.NextRun.Minute()%5 != 0 || !r.NextRun.After(time.Now()) ||
		r.LastScheduleTime == nil || !r.LastScheduleTime.Equal(last.Time) {
		t.Errorf("report: %+v", r)
	}
	if p := by["paused"]; !p.Suspend || p.NextRun != nil {
		t.Errorf("suspended must have no next run: %+v", p)
	}
	if b := by["broken"]; b.NextRun != nil || b.Schedule != "not a schedule" {
		t.Errorf("bad schedule: %+v", b)
	}
	if b := by["badzone"]; b.NextRun != nil {
		t.Errorf("bad timezone must yield no next run: %+v", b)
	}
	if h := by["hourly"]; h.NextRun == nil || h.NextRun.Minute() != 0 {
		t.Errorf("@hourly: %+v", h)
	}
	tk := by["tokyo"]
	if tk.Timezone != "Asia/Tokyo" || tk.NextRun == nil {
		t.Fatalf("tokyo: %+v", tk)
	}
	loc, _ := time.LoadLocation("Asia/Tokyo")
	if lt := tk.NextRun.In(loc); lt.Hour() != 9 || lt.Minute() != 0 {
		t.Errorf("tokyo next run %s is not 09:00 JST", lt)
	}
}

func TestListCronJobSpecs_UnknownSource(t *testing.T) {
	s, _, _ := fakeMgmt(t)
	if _, err := s.ListCronJobSpecs(context.Background(), "gone"); err == nil {
		t.Fatal("want error for a source without a client")
	}
}

func TestListCronJobSpecs_Cached(t *testing.T) {
	s, cs, lists := fakeMgmt(t, cj("app", "a", "* * * * *", false, ""))
	now := time.Now()
	s.cronSpecs.now = func() time.Time { return now }
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := s.ListCronJobSpecs(ctx, "prod"); err != nil {
			t.Fatal(err)
		}
	}
	if *lists != 1 {
		t.Fatalf("cluster listed %d times within the TTL, want 1", *lists)
	}

	// New objects are not visible until the TTL passes...
	if _, err := cs.BatchV1().CronJobs("app").Create(ctx, cj("app", "b", "* * * * *", false, ""), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListCronJobSpecs(ctx, "prod"); len(got) != 1 {
		t.Fatalf("cached result has %d items, want 1", len(got))
	}
	// ...then the cache expires...
	now = now.Add(cronSpecTTL + time.Second)
	if got, _ := s.ListCronJobSpecs(ctx, "prod"); len(got) != 2 || *lists != 2 {
		t.Fatalf("after TTL: %d items, %d lists", len(got), *lists)
	}
	// ...and unregistering a source drops its entry.
	s.UnregisterSource("prod")
	if _, ok := s.cronSpecs.get("prod"); ok {
		t.Fatal("cache entry survived UnregisterSource")
	}
}

func TestListCronJobSpecs_ErrorsAreNotCached(t *testing.T) {
	s, cs, lists := fakeMgmt(t)
	failing := true
	cs.PrependReactor("list", "cronjobs", func(k8stesting.Action) (bool, runtime.Object, error) {
		if failing {
			return true, nil, context.DeadlineExceeded
		}
		return false, nil, nil
	})
	_ = lists
	if _, err := s.ListCronJobSpecs(context.Background(), "prod"); err == nil {
		t.Fatal("want error")
	}
	failing = false
	if _, err := s.ListCronJobSpecs(context.Background(), "prod"); err != nil {
		t.Fatalf("retry after failure: %v", err)
	}
}

func TestNextCronRun(t *testing.T) {
	now := time.Date(2026, 3, 1, 10, 2, 0, 0, time.UTC)
	got, ok := NextCronRun("*/5 * * * *", "", now)
	if !ok || !got.Equal(time.Date(2026, 3, 1, 10, 5, 0, 0, time.UTC)) {
		t.Fatalf("*/5: %v %v", got, ok)
	}
	if _, ok := NextCronRun("", "", now); ok {
		t.Error("empty schedule must not parse")
	}
	if _, ok := NextCronRun("0 0 * * *", "Not/AZone", now); ok {
		t.Error("invalid timezone must not parse")
	}
}

func TestNextCronRunWithoutTimezoneUsesUTC(t *testing.T) {
	ist := time.FixedZone("TRT", 3*60*60)
	now := time.Date(2026, 11, 18, 13, 30, 0, 0, ist) // 10:30 UTC
	next, ok := NextCronRun("45 23 * * *", "", now)
	if !ok {
		t.Fatal("schedule should parse")
	}
	if want := time.Date(2026, 11, 18, 23, 45, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next.UTC(), want)
	}
}
