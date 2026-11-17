// Package stability implements the container stability scoring algorithm.
// A StabilityScore quantifies how reliably a container has been running over
// a configurable look-back window by accumulating weighted penalties for
// restarts, crashes, OOM kills, resource volatility, and availability gaps.
package stability

import (
	"context"
	"math"
	"time"

	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

// StabilityScorer computes a composite health score for a container by analysing
// its lifecycle events and resource samples over a caller-specified time window.
type StabilityScorer interface {
	// Score calculates and persists a StabilityScore for container c using event
	// and resource data from [windowStart, windowEnd]. Callers should clamp
	// windowStart to the container's first-seen time so that recently discovered
	// containers are not penalised for history that predates them.
	// The returned score is also written to the ScoreRepository.
	Score(ctx context.Context, c *model.Container, windowStart, windowEnd time.Time) (*model.StabilityScore, error)

	// Evaluate is Score that also returns the raw analysis, so callers can
	// aggregate several containers (e.g. all instances of a workload).
	Evaluate(ctx context.Context, c *model.Container, windowStart, windowEnd time.Time) (*model.StabilityScore, Analysis, error)
}

// Analysis is the raw, uncapped input of a container's score.
type Analysis struct {
	Restarts int
	Crashes  int
	OOMs     int
	// VolatilityPenalty and AvailabilityPenalty are already in penalty points.
	VolatilityPenalty   float64
	AvailabilityPenalty float64
	// Active is true when the container is running or crashing (i.e. it is
	// supposed to be up), which is when availability counts.
	Active bool
}

// Weights controls how much each failure mode affects the final score.
// All penalty weights are in [0, 100]; the sum of maximum possible penalties
// should not exceed 100 so that the score stays in [0, 100].
type Weights struct {
	// RestartPenaltyPerEvent is subtracted for each restart event.
	RestartPenaltyPerEvent float64
	// CrashPenaltyPerEvent is subtracted for each crash (non-zero exit) event.
	CrashPenaltyPerEvent float64
	// OOMPenaltyPerEvent is subtracted for each OOM kill event.
	OOMPenaltyPerEvent float64
	// VolatilityThreshold is the CPU-percent standard deviation above which
	// the VolatilityPenalty is applied.
	VolatilityThreshold float64
	// VolatilityPenaltyMax caps the volatility deduction.
	VolatilityPenaltyMax float64
	// AvailabilityPenaltyMax caps the availability deduction.
	AvailabilityPenaltyMax float64
}

// DefaultWeights returns a sensible baseline penalty configuration.
func DefaultWeights() Weights {
	return Weights{
		RestartPenaltyPerEvent: 2.0,
		CrashPenaltyPerEvent:   5.0,
		OOMPenaltyPerEvent:     10.0,
		VolatilityThreshold:    20.0,
		VolatilityPenaltyMax:   15.0,
		AvailabilityPenaltyMax: 20.0,
	}
}

// scorer is the concrete implementation of StabilityScorer.
type scorer struct {
	events    repository.EventRepository
	resources repository.ResourceRepository
	scores    repository.ScoreRepository
	weights   Weights
	log       *zap.Logger
}

// NewScorer creates a StabilityScorer backed by the provided repositories.
// It uses DefaultWeights; call NewScorerWithWeights to customise penalties.
func NewScorer(
	events repository.EventRepository,
	resources repository.ResourceRepository,
	scores repository.ScoreRepository,
	log *zap.Logger,
) StabilityScorer {
	return NewScorerWithWeights(events, resources, scores, DefaultWeights(), log)
}

// NewScorerWithWeights creates a StabilityScorer with custom penalty weights.
func NewScorerWithWeights(
	events repository.EventRepository,
	resources repository.ResourceRepository,
	scores repository.ScoreRepository,
	weights Weights,
	log *zap.Logger,
) StabilityScorer {
	return &scorer{
		events:    events,
		resources: resources,
		scores:    scores,
		weights:   weights,
		log:       log,
	}
}

// Score computes penalties for restarts, crashes, OOM kills, CPU volatility, and
// availability gaps, then persists and returns the resulting StabilityScore.
//
// Final score formula:
//
//	score = max(0, 100 − restartPenalty − crashPenalty − oomPenalty
//	                    − volatilityPenalty − availabilityPenalty)
func (s *scorer) Score(ctx context.Context, c *model.Container, windowStart, windowEnd time.Time) (*model.StabilityScore, error) {
	score, _, err := s.Evaluate(ctx, c, windowStart, windowEnd)
	return score, err
}

func (s *scorer) Evaluate(ctx context.Context, c *model.Container, windowStart, windowEnd time.Time) (*model.StabilityScore, Analysis, error) {
	lifecycleEvents, err := s.events.FindByContainerIDSince(ctx, c.ID, windowStart)
	if err != nil {
		return nil, Analysis{}, err
	}

	samples, err := s.resources.FindByContainerID(ctx, c.ID, windowStart, windowEnd, 0)
	if err != nil {
		return nil, Analysis{}, err
	}

	a := Analyze(c, lifecycleEvents, samples, windowStart, windowEnd, s.weights)
	result := scoreFrom(c, a, s.weights)

	saved, err := s.scores.Save(ctx, result)
	if err != nil {
		s.log.Warn("failed to persist stability score", zap.String("container_id", c.ID), zap.Error(err))
		return result, a, nil
	}

	return saved, a, nil
}

// Compute is the pure scoring function used by Score. It is exported so the
// algorithm can be unit-tested without a database.
func Compute(
	c *model.Container,
	events []*model.LifecycleEvent,
	samples []*model.ResourceSample,
	windowStart, windowEnd time.Time,
	w Weights,
) *model.StabilityScore {
	return scoreFrom(c, Analyze(c, events, samples, windowStart, windowEnd, w), w)
}

// Analyze extracts the raw scoring inputs for one container.
//
// Restarts: every start event in the window except the very first start of a
// container that was first seen inside the window (its initial boot).
// Crashes: crash events and die events with a non-zero exit code.
func Analyze(
	c *model.Container,
	events []*model.LifecycleEvent,
	samples []*model.ResourceSample,
	windowStart, windowEnd time.Time,
	w Weights,
) Analysis {
	var a Analysis
	starts := 0
	for _, e := range events {
		if e.OccurredAt.Before(windowStart) || e.OccurredAt.After(windowEnd) {
			continue
		}
		switch e.EventType {
		case model.EventTypeStart:
			starts++
		case model.EventTypeCrash, model.EventTypeDie:
			if e.ExitCode != 0 {
				a.Crashes++
			}
		case model.EventTypeOOM:
			a.OOMs++
		}
	}
	a.Restarts = starts
	if a.Restarts > 0 && !c.FirstSeenAt.Before(windowStart) {
		a.Restarts-- // the initial boot is not a restart
	}
	a.VolatilityPenalty = computeVolatilityPenalty(samples, w)

	// Availability only makes sense for containers that are supposed to be up.
	// A cleanly stopped container (or one we have no samples for, e.g. a
	// cluster without metrics-server) is not penalised.
	a.Active = c.CurrentStatus == model.StatusRunning || c.CurrentStatus == model.StatusCrashing
	if a.Active {
		a.AvailabilityPenalty = computeAvailabilityPenalty(samples, windowStart, windowEnd, w)
	}
	return a
}

// penalties turns raw counts into capped penalty points.
func penalties(restarts, crashes, ooms int, w Weights) (float64, float64, float64) {
	return math.Min(float64(restarts)*w.RestartPenaltyPerEvent, 30.0),
		math.Min(float64(crashes)*w.CrashPenaltyPerEvent, 30.0),
		math.Min(float64(ooms)*w.OOMPenaltyPerEvent, 25.0)
}

func scoreFrom(c *model.Container, a Analysis, w Weights) *model.StabilityScore {
	restartPenalty, crashPenalty, oomPenalty := penalties(a.Restarts, a.Crashes, a.OOMs, w)
	total := restartPenalty + crashPenalty + oomPenalty + a.VolatilityPenalty + a.AvailabilityPenalty
	return &model.StabilityScore{
		ContainerID:         c.ID,
		SourceName:          c.SourceName,
		Score:               math.Max(0, 100.0-total),
		RestartPenalty:      restartPenalty,
		CrashPenalty:        crashPenalty,
		OOMPenalty:          oomPenalty,
		VolatilityPenalty:   a.VolatilityPenalty,
		AvailabilityPenalty: a.AvailabilityPenalty,
		CalculatedAt:        time.Now().UTC(),
	}
}

// ComputeWorkload combines the analyses of every instance of a workload:
//   - restarts, crashes and OOM kills are summed across instances (a rollout's
//     fresh pods are not restarts, because each pod's first boot is excluded),
//     then capped like a single container's;
//   - volatility is the worst instance's;
//   - availability is the best active instance's — the workload is available
//     while at least one replica reports — and 0 when nothing is active.
func ComputeWorkload(workloadID, source string, members []Analysis, w Weights) *model.WorkloadScore {
	var (
		restarts, crashes, ooms int
		volatility              float64
		availability            = math.Inf(1)
	)
	for _, m := range members {
		restarts += m.Restarts
		crashes += m.Crashes
		ooms += m.OOMs
		volatility = math.Max(volatility, m.VolatilityPenalty)
		if m.Active {
			availability = math.Min(availability, m.AvailabilityPenalty)
		}
	}
	if math.IsInf(availability, 1) {
		availability = 0
	}
	restartPenalty, crashPenalty, oomPenalty := penalties(restarts, crashes, ooms, w)
	total := restartPenalty + crashPenalty + oomPenalty + volatility + availability
	return &model.WorkloadScore{
		WorkloadID:          workloadID,
		SourceName:          source,
		Score:               math.Max(0, 100.0-total),
		RestartPenalty:      restartPenalty,
		CrashPenalty:        crashPenalty,
		OOMPenalty:          oomPenalty,
		VolatilityPenalty:   volatility,
		AvailabilityPenalty: availability,
		Restarts:            restarts,
		Crashes:             crashes,
		OOMs:                ooms,
		CalculatedAt:        time.Now().UTC(),
	}
}

// computeVolatilityPenalty returns a penalty proportional to the standard deviation
// of CPU usage across the provided samples. High variance indicates an unstable workload.
func computeVolatilityPenalty(samples []*model.ResourceSample, w Weights) float64 {
	if len(samples) < 2 {
		return 0
	}

	var sum float64
	for _, sample := range samples {
		sum += sample.CPUPercent
	}
	mean := sum / float64(len(samples))

	var variance float64
	for _, sample := range samples {
		diff := sample.CPUPercent - mean
		variance += diff * diff
	}
	variance /= float64(len(samples))
	stddev := math.Sqrt(variance)

	if w.VolatilityThreshold <= 0 || stddev <= w.VolatilityThreshold {
		return 0
	}

	// Scale penalty linearly beyond the threshold, capped at VolatilityPenaltyMax.
	excess := stddev - w.VolatilityThreshold
	penalty := (excess / w.VolatilityThreshold) * w.VolatilityPenaltyMax
	return math.Min(penalty, w.VolatilityPenaltyMax)
}

// computeAvailabilityPenalty returns a penalty based on how much of the window
// has passed since the container last reported a resource sample: a running
// container that stopped reporting halfway through the window loses half the
// maximum. Time before the first sample is deliberately ignored — it usually
// means KYMO was not watching yet, not that the container was down.
// No samples means no data, not downtime.
func computeAvailabilityPenalty(samples []*model.ResourceSample, from, to time.Time, w Weights) float64 {
	if len(samples) == 0 {
		return 0
	}

	windowDuration := to.Sub(from).Seconds()
	if windowDuration <= 0 {
		return 0
	}

	last := samples[len(samples)-1].SampledAt
	// Allow one minute of slack for poll-interval jitter.
	trailingGap := math.Max(0, to.Sub(last).Seconds()-60)

	gap := math.Min(trailingGap/windowDuration, 1.0)
	return gap * w.AvailabilityPenaltyMax
}
