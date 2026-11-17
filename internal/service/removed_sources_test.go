package service

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/collector"
	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
	"github.com/emirhanusta/kymo/internal/secret"
)

// historyContainers is an in-memory ContainerRepository for source history.
type historyContainers struct {
	repository.ContainerRepository
	history map[string]model.SourceHistory
	purged  []string
}

func (f *historyContainers) SourceHistory(context.Context) ([]model.SourceHistory, error) {
	out := []model.SourceHistory{}
	for _, h := range f.history {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeenAt.After(out[j].LastSeenAt) })
	return out, nil
}

func (f *historyContainers) PurgeSource(_ context.Context, name string) (int64, error) {
	n := int64(f.history[name].Containers)
	delete(f.history, name)
	f.purged = append(f.purged, name)
	return n, nil
}

// memSources is an in-memory SourceRepository.
type memSources struct {
	repository.SourceRepository
	rows map[string]*model.SourceSpec
}

func (m *memSources) Create(_ context.Context, s *model.SourceSpec) error {
	if _, ok := m.rows[s.Name]; ok {
		return repository.ErrConflict
	}
	cp := *s
	m.rows[s.Name] = &cp
	return nil
}

func (m *memSources) Delete(_ context.Context, name string) error {
	if _, ok := m.rows[name]; !ok {
		return repository.ErrNotFound
	}
	delete(m.rows, name)
	return nil
}

func newRemovedManager(t *testing.T, hist map[string]model.SourceHistory) (*SourceManager, *historyContainers) {
	t.Helper()
	fc := &historyContainers{history: hist}
	r := NewRecorderService(fc, nil, nil, nil, nil, nil, nil, ScoringConfig{Interval: time.Hour}, zap.NewNop())
	factory := func(model.SourceSpec) collector.Collector { return newFake(0) }
	box, _ := secret.New("")
	m := NewSourceManager(&memSources{rows: map[string]*model.SourceSpec{}}, box, r,
		NewManagementService(zap.NewNop()), factory, zap.NewNop())
	return m, fc
}

func names(hs []model.SourceHistory) []string {
	out := []string{}
	for _, h := range hs {
		out = append(out, h.Name)
	}
	return out
}

func TestRemovedSourcesExcludesRegistered(t *testing.T) {
	now := time.Now()
	m, _ := newRemovedManager(t, map[string]model.SourceHistory{
		"live":     {Name: "live", Containers: 1, LastSeenAt: now},
		"disabled": {Name: "disabled", Containers: 1, LastSeenAt: now},
		"gone-old": {Name: "gone-old", Containers: 2, LastSeenAt: now.Add(-2 * time.Hour)},
		"gone-new": {Name: "gone-new", CronJobRuns: 3, LastSeenAt: now.Add(-time.Hour)},
	})
	m.recorder.AttachSource(model.SourceSpec{Name: "live", Type: model.SourceTypeKubernetes, Enabled: true}, newFake(0), nil)
	m.recorder.AttachSource(model.SourceSpec{Name: "disabled", Type: model.SourceTypeKubernetes}, newFake(0), nil)

	got, err := m.RemovedSources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n := names(got); len(n) != 2 || n[0] != "gone-new" || n[1] != "gone-old" {
		t.Fatalf("removed = %v", n)
	}
}

func TestRemovedSourcesEmptyIsNonNil(t *testing.T) {
	m, _ := newRemovedManager(t, map[string]model.SourceHistory{})
	got, err := m.RemovedSources(context.Background())
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestPurgeRemovedSource(t *testing.T) {
	m, fc := newRemovedManager(t, map[string]model.SourceHistory{
		"live": {Name: "live", Containers: 1}, "gone": {Name: "gone", Containers: 4},
	})
	m.recorder.AttachSource(model.SourceSpec{Name: "live", Type: model.SourceTypeKubernetes, Enabled: true}, newFake(0), nil)
	ctx := context.Background()

	if _, err := m.PurgeRemovedSource(ctx, "live"); !errors.Is(err, ErrSourceRegistered) {
		t.Fatalf("registered: %v", err)
	}
	if _, err := m.PurgeRemovedSource(ctx, "nope"); !errors.Is(err, ErrNoHistory) {
		t.Fatalf("unknown: %v", err)
	}
	if len(fc.purged) != 0 {
		t.Fatalf("refused purges deleted data: %v", fc.purged)
	}
	n, err := m.PurgeRemovedSource(ctx, "gone")
	if err != nil || n != 4 {
		t.Fatalf("purge gone = %d, %v", n, err)
	}
	if _, ok := fc.history["live"]; !ok {
		t.Fatal("live history touched")
	}
}

// Removing a source keeps its history; re-adding the same name reattaches it.
func TestReAddSourceReattachesHistory(t *testing.T) {
	m, fc := newRemovedManager(t, map[string]model.SourceHistory{
		"edge": {Name: "edge", Containers: 2, LastSeenAt: time.Now()},
	})
	ctx := context.Background()
	in := SourceInput{Name: "edge", Type: model.SourceTypeKubernetes}

	if _, err := m.Create(ctx, in); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.RemovedSources(ctx); len(got) != 0 {
		t.Fatalf("registered source listed as removed: %v", names(got))
	}
	if err := m.Delete(ctx, "edge", false); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.RemovedSources(ctx); len(got) != 1 || got[0].Name != "edge" || got[0].Containers != 2 {
		t.Fatalf("history not kept after delete: %v", got)
	}
	if _, err := m.Create(ctx, in); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.RemovedSources(ctx); len(got) != 0 {
		t.Fatalf("re-added source still listed as removed: %v", names(got))
	}
	if h := fc.history["edge"]; h.Containers != 2 {
		t.Fatalf("history lost: %+v", h)
	}
}
