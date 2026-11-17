package timescale

import (
	"context"
	"testing"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

func k8sEv(uid, typ, reason string, last time.Duration) *model.K8sEvent {
	return &model.K8sEvent{
		SourceName: "src1", UID: uid, Namespace: "ns", Kind: "Pod", Name: "web-1",
		Type: typ, Reason: reason, Message: "msg " + reason, Count: 1,
		FirstSeen: at(0), LastSeen: at(last), Component: "kubelet",
	}
}

func TestK8sEventUpsertDedupe(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	repo := db.K8sEvents()

	e := k8sEv("u1", "Warning", "BackOff", time.Minute)
	if err := repo.Upsert(ctx, e); err != nil {
		t.Fatal(err)
	}
	upd := *e
	upd.Count, upd.LastSeen, upd.Message, upd.WorkloadID = 5, at(10*time.Minute), "newer", "src1:ns:Deployment:web"
	if err := repo.Upsert(ctx, &upd); err != nil {
		t.Fatal(err)
	}
	// Stale replay (count 1, older last_seen, no workload).
	stale := *e
	stale.LastSeen, stale.WorkloadID = at(2*time.Minute), ""
	if err := repo.Upsert(ctx, &stale); err != nil {
		t.Fatal(err)
	}
	got, err := repo.List(ctx, repository.K8sEventQuery{})
	if err != nil || len(got) != 1 {
		t.Fatalf("list: %v %+v", err, got)
	}
	g := got[0]
	// A stale replay must not lower count or last_seen nor clear workload_id.
	if g.Count != 5 {
		t.Fatalf("count %d", g.Count)
	}
	if !g.LastSeen.Equal(at(10*time.Minute)) || g.WorkloadID != "src1:ns:Deployment:web" || g.Component != "kubelet" || g.ID == 0 {
		t.Fatalf("row: %+v", g)
	}
}

func TestK8sEventListFilters(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	repo := db.K8sEvents()
	mk := func(uid, typ, reason string, last time.Duration, mod func(*model.K8sEvent)) {
		e := k8sEv(uid, typ, reason, last)
		if mod != nil {
			mod(e)
		}
		if err := repo.Upsert(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	mk("a", "Warning", "BackOff", 1*time.Minute, func(e *model.K8sEvent) { e.WorkloadID = "w1" })
	mk("b", "Normal", "Killing", 2*time.Minute, func(e *model.K8sEvent) { e.WorkloadID = "w1" })
	mk("c", "Warning", "FailedScheduling", 3*time.Minute, func(e *model.K8sEvent) {
		e.SourceName, e.Namespace = "src2", "other"
		e.Message = "0/3 nodes 100% full"
	})
	mk("d", "Warning", "BackOff", 4*time.Minute, nil)

	uids := func(q repository.K8sEventQuery) string {
		got, err := repo.List(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		s := ""
		for _, e := range got {
			s += e.UID
		}
		return s
	}
	cases := []struct {
		name string
		q    repository.K8sEventQuery
		want string
	}{
		{"all newest first", repository.K8sEventQuery{}, "dcba"},
		{"source", repository.K8sEventQuery{Source: "src2"}, "c"},
		{"namespace", repository.K8sEventQuery{Namespace: "other"}, "c"},
		{"type", repository.K8sEventQuery{Type: "Normal"}, "b"},
		{"reason", repository.K8sEventQuery{Reason: "BackOff"}, "da"},
		{"workload", repository.K8sEventQuery{WorkloadID: "w1"}, "ba"},
		{"q reason ci", repository.K8sEventQuery{Query: "killing"}, "b"},
		{"q message", repository.K8sEventQuery{Query: "nodes"}, "c"},
		{"q name", repository.K8sEventQuery{Query: "WEB-1"}, "dcba"},
		{"q wildcard literal", repository.K8sEventQuery{Query: "100%"}, "c"},
		{"q underscore literal", repository.K8sEventQuery{Query: "_"}, ""},
		{"since", repository.K8sEventQuery{Since: at(3 * time.Minute)}, "dc"},
		{"before cursor", repository.K8sEventQuery{Before: at(3 * time.Minute)}, "ba"},
		{"limit", repository.K8sEventQuery{Limit: 2}, "dc"},
		{"limit+before", repository.K8sEventQuery{Limit: 1, Before: at(3 * time.Minute)}, "b"},
	}
	for _, tc := range cases {
		if got := uids(tc.q); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestK8sEventReasons(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	repo := db.K8sEvents()
	for i, e := range []*model.K8sEvent{
		k8sEv("1", "Warning", "BackOff", time.Minute),
		k8sEv("2", "Warning", "BackOff", 2*time.Minute),
		k8sEv("3", "Warning", "BackOff", 3*time.Minute),
		k8sEv("4", "Normal", "Killing", 4*time.Minute),
		k8sEv("5", "Warning", "Old", -time.Hour),
	} {
		e.Count = 100 + i // count must not feed the aggregate
		if err := repo.Upsert(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	other := k8sEv("6", "Warning", "Other", time.Minute)
	other.SourceName = "src2"
	if err := repo.Upsert(ctx, other); err != nil {
		t.Fatal(err)
	}

	got, err := repo.Reasons(ctx, "src1", at(0))
	if err != nil {
		t.Fatal(err)
	}
	want := []model.K8sEventReason{{Reason: "BackOff", Type: "Warning", Count: 3}, {Reason: "Killing", Type: "Normal", Count: 1}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %+v", got)
	}
	all, err := repo.Reasons(ctx, "", at(0))
	if err != nil || len(all) != 3 {
		t.Fatalf("all sources: %v %+v", err, all)
	}
}

func TestK8sEventPurgeAndHistory(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	repo := db.K8sEvents()
	mustUpsert(t, db, newContainer("c1")) // src1 has a container too
	for _, e := range []*model.K8sEvent{
		k8sEv("1", "Warning", "BackOff", time.Minute),
		k8sEv("2", "Warning", "BackOff", time.Hour),
	} {
		if err := repo.Upsert(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	only := k8sEv("3", "Warning", "BackOff", 2*time.Minute)
	only.SourceName = "events-only"
	if err := repo.Upsert(ctx, only); err != nil {
		t.Fatal(err)
	}

	hist, err := db.Containers().SourceHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]model.SourceHistory{}
	for _, h := range hist {
		counts[h.Name] = h
	}
	if counts["src1"].K8sEvents != 2 || counts["src1"].Containers != 1 ||
		counts["events-only"].K8sEvents != 1 || counts["events-only"].Containers != 0 ||
		!counts["events-only"].LastSeenAt.Equal(at(2*time.Minute)) {
		t.Fatalf("history: %+v", counts)
	}

	if _, err := db.Containers().PurgeSource(ctx, "src1"); err != nil {
		t.Fatal(err)
	}
	left, _ := repo.List(ctx, repository.K8sEventQuery{})
	if len(left) != 1 || left[0].SourceName != "events-only" {
		t.Fatalf("after purge: %+v", left)
	}
	n, err := repo.DeleteBySource(ctx, "events-only")
	if err != nil || n != 1 {
		t.Fatalf("delete by source: %d %v", n, err)
	}
}
