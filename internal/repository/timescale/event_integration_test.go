package timescale

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

func evIDs(es []*model.LifecycleEvent) []int64 {
	out := make([]int64, len(es))
	for i, e := range es {
		out[i] = e.ID
	}
	return out
}

func saveEvent(t *testing.T, db *DB, cid string, typ model.EventType, src string, occurred time.Time) *model.LifecycleEvent {
	t.Helper()
	e, err := db.Events().Save(context.Background(), &model.LifecycleEvent{
		ContainerID: cid, EventType: typ, SourceName: src, NodeName: "node-1", ExitCode: 1, OccurredAt: occurred,
	})
	if err != nil {
		t.Fatalf("save event: %v", err)
	}
	return e
}

func TestEventSaveAndMetadataRoundTrip(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	mustUpsert(t, db, newContainer("c1"))

	meta := map[string]any{"reason": "OOMKilled", "restart": float64(3), "nested": map[string]any{"a": []any{"x", float64(1)}}}
	in := &model.LifecycleEvent{ContainerID: "c1", EventType: model.EventTypeOOM, ExitCode: 137,
		NodeName: "node-9", SourceName: "src1", Metadata: meta, OccurredAt: at(time.Minute)}
	saved, err := db.Events().Save(ctx, in)
	if err != nil || saved.ID == 0 {
		t.Fatalf("save: id=%d err=%v", saved.ID, err)
	}
	// nil metadata must round-trip as nil.
	saveEvent(t, db, "c1", model.EventTypeStart, "src1", at(2*time.Minute))

	got, err := db.Events().FindByContainerID(ctx, "c1", 0)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %d events, err %v", len(got), err)
	}
	e := got[0]
	if e.ID != saved.ID || e.EventType != model.EventTypeOOM || e.ExitCode != 137 || e.NodeName != "node-9" ||
		e.SourceName != "src1" || !e.OccurredAt.Equal(at(time.Minute)) || e.HasLogs {
		t.Errorf("event = %+v", e)
	}
	if !reflect.DeepEqual(e.Metadata, meta) {
		t.Errorf("metadata = %#v, want %#v", e.Metadata, meta)
	}
	if got[1].Metadata != nil {
		t.Errorf("nil metadata came back as %#v", got[1].Metadata)
	}
}

func TestEventFindByContainer(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	mustUpsert(t, db, newContainer("c1"))
	mustUpsert(t, db, newContainer("c2"))
	var all []int64
	for i := 0; i < 5; i++ {
		all = append(all, saveEvent(t, db, "c1", model.EventTypeStart, "src1", at(time.Duration(i)*time.Minute)).ID)
	}
	saveEvent(t, db, "c2", model.EventTypeStart, "src1", at(time.Minute))

	t.Run("limit returns newest N ascending", func(t *testing.T) {
		got, err := db.Events().FindByContainerID(ctx, "c1", 3)
		if err != nil || !reflect.DeepEqual(evIDs(got), all[2:]) {
			t.Errorf("ids = %v err=%v, want %v", evIDs(got), err, all[2:])
		}
	})
	t.Run("limit 0 and negative return everything", func(t *testing.T) {
		for _, lim := range []int{0, -1} {
			got, err := db.Events().FindByContainerID(ctx, "c1", lim)
			if err != nil || !reflect.DeepEqual(evIDs(got), all) {
				t.Errorf("limit %d: ids = %v err=%v", lim, evIDs(got), err)
			}
		}
	})
	t.Run("unknown container", func(t *testing.T) {
		got, err := db.Events().FindByContainerID(ctx, "nope", 10)
		if err != nil || len(got) != 0 {
			t.Errorf("got %v err %v", got, err)
		}
	})
	t.Run("since is inclusive and ascending", func(t *testing.T) {
		got, err := db.Events().FindByContainerIDSince(ctx, "c1", at(2*time.Minute))
		if err != nil || !reflect.DeepEqual(evIDs(got), all[2:]) {
			t.Errorf("ids = %v err=%v, want %v", evIDs(got), err, all[2:])
		}
		got, _ = db.Events().FindByContainerIDSince(ctx, "c1", at(time.Hour))
		if len(got) != 0 {
			t.Errorf("future since returned %d", len(got))
		}
	})
}

func TestEventFindRecent(t *testing.T) {
	db := reset(t)
	ctx := context.Background()

	a := newContainer("ns/pa/app") // workload web in src1
	b := newContainer("ns/pb/app")
	b.WorkloadName = "api"
	o := newContainer("ns/po/app")
	o.SourceName = "src2"
	mustUpsert(t, db, a)
	mustUpsert(t, db, b)
	mustUpsert(t, db, o)

	e1 := saveEvent(t, db, a.ID, model.EventTypeStart, "src1", at(1*time.Minute))
	e2 := saveEvent(t, db, a.ID, model.EventTypeCrash, "src1", at(2*time.Minute))
	e3 := saveEvent(t, db, b.ID, model.EventTypeCrash, "src1", at(3*time.Minute))
	e4 := saveEvent(t, db, o.ID, model.EventTypeStart, "src2", at(4*time.Minute))

	webID := model.WorkloadID("src1", "ns", "Deployment", "web")
	tests := []struct {
		name string
		q    repository.EventQuery
		want []int64
	}{
		{"all newest first", repository.EventQuery{Limit: 10}, []int64{e4.ID, e3.ID, e2.ID, e1.ID}},
		{"limit", repository.EventQuery{Limit: 2}, []int64{e4.ID, e3.ID}},
		{"source", repository.EventQuery{Limit: 10, Source: "src1"}, []int64{e3.ID, e2.ID, e1.ID}},
		{"type", repository.EventQuery{Limit: 10, Type: model.EventTypeCrash}, []int64{e3.ID, e2.ID}},
		{"before is strict", repository.EventQuery{Limit: 10, Before: at(3 * time.Minute)}, []int64{e2.ID, e1.ID}},
		{"since is inclusive", repository.EventQuery{Limit: 10, Since: at(3 * time.Minute)}, []int64{e4.ID, e3.ID}},
		{"workload", repository.EventQuery{Limit: 10, WorkloadID: webID}, []int64{e2.ID, e1.ID}},
		{"combined", repository.EventQuery{Limit: 10, Source: "src1", Type: model.EventTypeCrash, WorkloadID: webID}, []int64{e2.ID}},
		{"no match", repository.EventQuery{Limit: 10, Source: "nope"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := db.Events().FindRecent(ctx, tc.q)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(evIDs(got), append([]int64{}, tc.want...)) && !(len(got) == 0 && len(tc.want) == 0) {
				t.Errorf("ids = %v, want %v", evIDs(got), tc.want)
			}
		})
	}

	got, _ := db.Events().FindRecent(ctx, repository.EventQuery{Limit: 10})
	if got[0].WorkloadID != model.WorkloadID("src2", "ns", "Deployment", "web") || got[2].WorkloadID != webID {
		t.Errorf("workload ids: %q %q", got[0].WorkloadID, got[2].WorkloadID)
	}
	if got[0].ContainerName != o.Name || got[0].Runtime != model.RuntimeKubernetes ||
		got[2].ContainerName != a.Name {
		t.Errorf("container join: %q/%q , %q", got[0].ContainerName, got[0].Runtime, got[2].ContainerName)
	}
}

func TestEventHasLogs(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	mustUpsert(t, db, newContainer("c1"))
	mustUpsert(t, db, newContainer("c2"))

	withLog := saveEvent(t, db, "c1", model.EventTypeCrash, "src1", at(time.Minute))
	noLog := saveEvent(t, db, "c1", model.EventTypeCrash, "src1", at(2*time.Minute))
	otherContainer := saveEvent(t, db, "c2", model.EventTypeCrash, "src1", at(time.Minute)) // same time, other container
	if err := db.CrashLogs().Save(ctx, &model.CrashLog{ContainerID: "c1", SourceName: "src1", OccurredAt: at(time.Minute), Lines: "x"}); err != nil {
		t.Fatal(err)
	}
	// A log 1s off must not count: has_logs requires the same occurred_at.
	if err := db.CrashLogs().Save(ctx, &model.CrashLog{ContainerID: "c1", SourceName: "src1", OccurredAt: at(2*time.Minute + time.Second), Lines: "x"}); err != nil {
		t.Fatal(err)
	}

	want := map[int64]bool{withLog.ID: true, noLog.ID: false, otherContainer.ID: false}
	byContainer, err := db.Events().FindByContainerID(ctx, "c1", 0)
	if err != nil {
		t.Fatal(err)
	}
	since, _ := db.Events().FindByContainerIDSince(ctx, "c1", base)
	recent, _ := db.Events().FindRecent(ctx, repository.EventQuery{Limit: 10})
	for name, list := range map[string][]*model.LifecycleEvent{"FindByContainerID": byContainer, "Since": since, "FindRecent": recent} {
		for _, e := range list {
			if e.HasLogs != want[e.ID] {
				t.Errorf("%s: event %d has_logs=%v, want %v", name, e.ID, e.HasLogs, want[e.ID])
			}
		}
	}
}
