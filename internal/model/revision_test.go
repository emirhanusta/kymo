package model

import (
	"testing"
	"time"
)

func TestBuildRevisions(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mk := func(id, rev, img string, st ContainerStatus, first time.Duration) *Container {
		return &Container{ID: id, Revision: rev, Image: img, CurrentStatus: st,
			FirstSeenAt: t0.Add(first), LastSeenAt: t0.Add(first + time.Minute)}
	}
	got := BuildRevisions([]*Container{
		mk("ns/a-1/app", "old", "img:1", StatusStopped, 0),
		mk("ns/a-1/side", "old", "side:1", StatusStopped, time.Second),
		mk("ns/a-2/app", "new", "img:2", StatusRunning, time.Hour),
		mk("ns/a-3/app", "new", "img:2", StatusRunning, 2*time.Hour),
		mk("ns/b-1/app", "", "img:0", StatusStopped, -time.Hour),
	})
	if len(got) != 3 {
		t.Fatalf("got %d revisions: %+v", len(got), got)
	}
	if got[0].Revision != "new" || !got[0].Current || got[0].Pods != 2 || got[0].ActivePods != 2 {
		t.Errorf("newest wrong: %+v", got[0])
	}
	if got[1].Revision != "old" || got[1].Current || got[1].Pods != 1 || got[1].ActivePods != 0 || len(got[1].Images) != 2 {
		t.Errorf("old wrong: %+v", got[1])
	}
	if got[2].Revision != "" {
		t.Errorf("unknown group should be last: %+v", got[2])
	}

	// Nothing active: newest is current.
	got = BuildRevisions([]*Container{
		mk("ns/a-1/app", "old", "i", StatusStopped, 0),
		mk("ns/a-2/app", "new", "i", StatusStopped, time.Hour),
	})
	if !got[0].Current || got[1].Current {
		t.Errorf("fallback current wrong: %+v", got)
	}
	if BuildRevisions(nil) == nil {
		t.Error("want empty non-nil slice")
	}
}
