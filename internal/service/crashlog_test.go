package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/collector"
	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

type fakeContainers struct {
	repository.ContainerRepository
	known   map[string]*model.Container
	ignored map[string]bool
}

func (f *fakeContainers) FindByID(_ context.Context, id string) (*model.Container, error) {
	return f.known[id], nil
}
func (f *fakeContainers) IsIgnored(_ context.Context, id string) (bool, error) {
	return f.ignored[id], nil
}

// fakeCrashLogs mimics the repository's ON CONFLICT DO NOTHING behaviour.
type fakeCrashLogs struct {
	repository.CrashLogRepository
	mu   sync.Mutex
	rows map[string]*model.CrashLog
}

func (f *fakeCrashLogs) Save(_ context.Context, l *model.CrashLog) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := l.ContainerID + "@" + l.OccurredAt.Format(time.RFC3339Nano)
	if _, ok := f.rows[k]; !ok {
		f.rows[k] = l
	}
	return nil
}

func TestRecorderSavesCrashLogs(t *testing.T) {
	cl := &fakeCrashLogs{rows: map[string]*model.CrashLog{}}
	fc := &fakeContainers{
		known:   map[string]*model.Container{"ns/p/app": {ID: "ns/p/app"}, "ns/p/ign": {ID: "ns/p/ign"}},
		ignored: map[string]bool{"ns/p/ign": true},
	}
	r := NewRecorderService(fc, nil, nil, nil, nil, nil, cl, ScoringConfig{Interval: time.Hour}, zap.NewNop()).(*recorderService)

	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	ev := func(id string) collector.CollectorEvent {
		return collector.CollectorEvent{
			Kind: collector.CollectorEventCrashLog, SourceName: "src",
			Container: &model.Container{ID: id},
			CrashLog:  &model.CrashLog{ContainerID: id, OccurredAt: at, Reason: "OOMKilled", ExitCode: 137, Lines: "boom\n", LineCount: 1, Previous: true},
		}
	}
	ctx := context.Background()
	r.dispatch(ctx, ev("ns/p/app"))
	r.dispatch(ctx, ev("ns/p/app")) // same termination again: de-duplicated
	r.dispatch(ctx, ev("ns/p/ign")) // ignored container
	r.dispatch(ctx, ev("ns/p/new")) // unknown container row: dropped

	if len(cl.rows) != 1 {
		t.Fatalf("saved %d snapshots, want 1: %+v", len(cl.rows), cl.rows)
	}
	for _, l := range cl.rows {
		if l.ContainerID != "ns/p/app" || l.SourceName != "src" || l.Reason != "OOMKilled" ||
			l.ExitCode != 137 || !l.Previous || l.Lines != "boom\n" || !l.OccurredAt.Equal(at) {
			t.Errorf("mapped payload wrong: %+v", l)
		}
	}
}

func TestClampLimit(t *testing.T) {
	for _, tc := range []struct{ in, want int }{{0, 10}, {-1, 10}, {5, 5}, {500, 50}} {
		if got := clampLimit(tc.in, 10, 50); got != tc.want {
			t.Errorf("clampLimit(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
