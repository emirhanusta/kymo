package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/collector"
	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/secret"
)

func newSyncManager(t *testing.T) (*SourceManager, *recorderService, map[string][]*fakeCollector) {
	t.Helper()
	r := NewRecorderService(nil, nil, nil, nil, nil, nil, ScoringConfig{Interval: time.Hour}, zap.NewNop()).(*recorderService)
	built := map[string][]*fakeCollector{}
	factory := func(spec model.SourceSpec) collector.Collector {
		f := newFake(0)
		built[spec.Name] = append(built[spec.Name], f)
		return f
	}
	box, _ := secret.New("")
	return NewSourceManager(nil, box, r, NewManagementService(zap.NewNop()), factory, zap.NewNop()), r, built
}

func cfgSpec(name string) model.SourceSpec {
	return model.SourceSpec{Name: name, Type: model.SourceTypeDocker, Enabled: true}
}

func TestSyncConfigDiff(t *testing.T) {
	m, r, built := newSyncManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = r.Run(ctx) }()

	res, err := m.SyncConfig(ctx, []model.SourceSpec{cfgSpec("a"), cfgSpec("b"), cfgSpec("c")})
	if err != nil || !reflect.DeepEqual(res.Added, []string{"a", "b", "c"}) {
		t.Fatalf("initial sync: %+v %v", res, err)
	}
	for _, n := range []string{"a", "b", "c"} {
		waitFor(t, func() bool { return healthOf(t, r, n).status == SourceConnected })
	}
	// A user source that the config will later collide with.
	r.AttachSource(model.SourceSpec{Name: "u", Type: "docker", Origin: model.SourceOriginUser, Enabled: true}, newFake(0), nil)

	changed := cfgSpec("b")
	changed.ReadOnly = true
	res, err = m.SyncConfig(ctx, []model.SourceSpec{cfgSpec("a"), changed, cfgSpec("d"), cfgSpec("u")})
	if err != nil {
		t.Fatal(err)
	}
	want := SyncResult{
		Added: []string{"d"}, Removed: []string{"c"}, Updated: []string{"b"},
		Unchanged: []string{"a"}, Conflicts: []string{"u"},
	}
	res.ReloadedAt = time.Time{}
	if !reflect.DeepEqual(res, want) {
		t.Fatalf("got %+v want %+v", res, want)
	}

	if len(built["a"]) != 1 || built["a"][0].stopped.Load() {
		t.Error("unchanged source must keep running")
	}
	if len(built["b"]) != 2 || !built["b"][0].stopped.Load() || !r.IsReadOnly("b") {
		t.Error("changed source must be re-attached")
	}
	if _, ok := r.Source("c"); ok || !built["c"][0].stopped.Load() {
		t.Error("removed source must be detached and stopped")
	}
	if u, ok := r.Source("u"); !ok || u.Origin != model.SourceOriginUser {
		t.Error("user source must be untouched")
	}
	if ls := m.LastConfigSync(); ls == nil || ls.Result == nil || ls.Error != "" {
		t.Errorf("last sync not recorded: %+v", ls)
	}
}

func TestReloadConfig(t *testing.T) {
	m, _, _ := newSyncManager(t)
	if _, err := m.ReloadConfig(context.Background()); !errors.Is(err, ErrNoConfigFile) {
		t.Fatalf("want ErrNoConfigFile, got %v", err)
	}
	fail := true
	m.SetConfigReloader("/x/config.yaml", func() ([]model.SourceSpec, error) {
		if fail {
			return nil, errors.New("bad yaml")
		}
		return []model.SourceSpec{cfgSpec("a")}, nil
	})
	_, err := m.ReloadConfig(context.Background())
	var ce *ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("want ConfigError, got %v", err)
	}
	if ls := m.LastConfigSync(); ls == nil || ls.Error != "bad yaml" {
		t.Fatalf("error not stored: %+v", ls)
	}
	fail = false
	res, err := m.ReloadConfig(context.Background())
	if err != nil || len(res.Added) != 1 || m.LastConfigSync().Error != "" {
		t.Fatalf("reload: %+v %v", res, err)
	}
}
