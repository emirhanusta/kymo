package stability

import (
	"testing"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
)

func ev(t model.EventType, exit int, at time.Time) *model.LifecycleEvent {
	return &model.LifecycleEvent{EventType: t, ExitCode: exit, OccurredAt: at}
}

func TestCompute_InitialBootIsNotARestart(t *testing.T) {
	now := time.Now()
	from := now.Add(-time.Hour)
	c := &model.Container{ID: "a", FirstSeenAt: from, CurrentStatus: model.StatusStopped}

	got := Compute(c, []*model.LifecycleEvent{ev(model.EventTypeStart, 0, from.Add(time.Second))}, nil, from, now, DefaultWeights())
	if got.RestartPenalty != 0 || got.Score != 100 {
		t.Fatalf("first boot should not be penalised, got restart=%v score=%v", got.RestartPenalty, got.Score)
	}
}

func TestCompute_RestartsCrashesAndOOMs(t *testing.T) {
	now := time.Now()
	from := now.Add(-24 * time.Hour)
	// Container predates the window, so every start inside it is a restart.
	c := &model.Container{ID: "a", FirstSeenAt: from.Add(-time.Hour), CurrentStatus: model.StatusStopped}
	w := DefaultWeights()

	events := []*model.LifecycleEvent{
		ev(model.EventTypeStart, 0, from.Add(time.Hour)),
		ev(model.EventTypeStart, 0, from.Add(2*time.Hour)),
		ev(model.EventTypeCrash, 1, from.Add(3*time.Hour)),
		ev(model.EventTypeDie, 0, from.Add(4*time.Hour)),   // clean exit: not a crash
		ev(model.EventTypeDie, 137, from.Add(5*time.Hour)), // non-zero die: crash
		ev(model.EventTypeOOM, 0, from.Add(6*time.Hour)),
		ev(model.EventTypeCrash, 1, from.Add(-time.Minute)), // outside window
	}
	got := Compute(c, events, nil, from, now, w)

	if want := 2 * w.RestartPenaltyPerEvent; got.RestartPenalty != want {
		t.Errorf("restart penalty = %v, want %v", got.RestartPenalty, want)
	}
	if want := 2 * w.CrashPenaltyPerEvent; got.CrashPenalty != want {
		t.Errorf("crash penalty = %v, want %v", got.CrashPenalty, want)
	}
	if want := w.OOMPenaltyPerEvent; got.OOMPenalty != want {
		t.Errorf("oom penalty = %v, want %v", got.OOMPenalty, want)
	}
	if got.Score < 0 || got.Score > 100 {
		t.Errorf("score out of range: %v", got.Score)
	}
}

func TestCompute_PenaltiesAreCapped(t *testing.T) {
	now := time.Now()
	from := now.Add(-time.Hour)
	c := &model.Container{ID: "a", FirstSeenAt: from.Add(-time.Hour), CurrentStatus: model.StatusCrashing}
	var events []*model.LifecycleEvent
	for i := 0; i < 100; i++ {
		at := from.Add(time.Duration(i) * time.Second)
		events = append(events, ev(model.EventTypeStart, 0, at), ev(model.EventTypeCrash, 1, at), ev(model.EventTypeOOM, 0, at))
	}
	got := Compute(c, events, nil, from, now, DefaultWeights())
	if got.RestartPenalty != 30 || got.CrashPenalty != 30 || got.OOMPenalty != 25 {
		t.Fatalf("caps not applied: %+v", got)
	}
	if got.Score != 15 {
		t.Fatalf("score = %v, want 15", got.Score)
	}
}

func TestCompute_AvailabilityOnlyForRunningContainers(t *testing.T) {
	now := time.Now()
	from := now.Add(-10 * time.Hour)
	// Samples only cover the first half of the window.
	samples := []*model.ResourceSample{
		{SampledAt: from},
		{SampledAt: from.Add(5 * time.Hour)},
	}
	w := DefaultWeights()

	running := &model.Container{ID: "a", FirstSeenAt: from, CurrentStatus: model.StatusRunning}
	late := []*model.ResourceSample{{SampledAt: now.Add(-time.Minute)}}
	if got := Compute(running, nil, late, from, now, w); got.AvailabilityPenalty != 0 {
		t.Errorf("time before the first sample must not count as downtime, got %v", got.AvailabilityPenalty)
	}
	got := Compute(running, nil, samples, from, now, w)
	if got.AvailabilityPenalty < w.AvailabilityPenaltyMax*0.45 || got.AvailabilityPenalty > w.AvailabilityPenaltyMax*0.55 {
		t.Errorf("running container with half coverage: availability penalty = %v", got.AvailabilityPenalty)
	}

	stopped := &model.Container{ID: "b", FirstSeenAt: from, CurrentStatus: model.StatusStopped}
	if got := Compute(stopped, nil, samples, from, now, w); got.AvailabilityPenalty != 0 {
		t.Errorf("stopped container should not get an availability penalty, got %v", got.AvailabilityPenalty)
	}

	if got := Compute(running, nil, nil, from, now, w); got.AvailabilityPenalty != 0 {
		t.Errorf("no samples means no data, not downtime; got %v", got.AvailabilityPenalty)
	}
}

func TestComputeVolatilityPenalty(t *testing.T) {
	w := DefaultWeights()
	flat := []*model.ResourceSample{{CPUPercent: 10}, {CPUPercent: 10}, {CPUPercent: 10}}
	if p := computeVolatilityPenalty(flat, w); p != 0 {
		t.Errorf("flat CPU should have no penalty, got %v", p)
	}
	spiky := []*model.ResourceSample{{CPUPercent: 0}, {CPUPercent: 200}, {CPUPercent: 0}, {CPUPercent: 200}}
	if p := computeVolatilityPenalty(spiky, w); p != w.VolatilityPenaltyMax {
		t.Errorf("very spiky CPU should hit the cap, got %v", p)
	}
}
