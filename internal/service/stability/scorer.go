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
	lifecycleEvents, err := s.events.FindByContainerIDSince(ctx, c.ID, windowStart)
	if err != nil {
		return nil, err
	}

	samples, err := s.resources.FindByContainerID(ctx, c.ID, windowStart, windowEnd, 0)
	if err != nil {
		return nil, err
	}

	result := Compute(c, lifecycleEvents, samples, windowStart, windowEnd, s.weights)

	saved, err := s.scores.Save(ctx, result)
	if err != nil {
		s.log.Warn("failed to persist stability score", zap.String("container_id", c.ID), zap.Error(err))
		return result, nil
	}

	return saved, nil
}

// Compute is the pure scoring function used by Score. It is exported so the
// algorithm can be unit-tested without a database.
//
// Restarts: every start event in the window except the very first start of a
// container that was first seen inside the window (its initial boot).
// Crashes: crash events and die events with a non-zero exit code.
func Compute(
	c *model.Container,
	events []*model.LifecycleEvent,
	samples []*model.ResourceSample,
	windowStart, windowEnd time.Time,
	w Weights,
) *model.StabilityScore {
	var starts, crashes, ooms int
	for _, e := range events {
		if e.OccurredAt.Before(windowStart) || e.OccurredAt.After(windowEnd) {
			continue
		}
		switch e.EventType {
		case model.EventTypeStart:
			starts++
		case model.EventTypeCrash, model.EventTypeDie:
			if e.ExitCode != 0 {
				crashes++
			}
		case model.EventTypeOOM:
			ooms++
		}
	}
	restarts := starts
	if restarts > 0 && !c.FirstSeenAt.Before(windowStart) {
		restarts-- // the initial boot is not a restart
	}

	restartPenalty := math.Min(float64(restarts)*w.RestartPenaltyPerEvent, 30.0)
	crashPenalty := math.Min(float64(crashes)*w.CrashPenaltyPerEvent, 30.0)
	oomPenalty := math.Min(float64(ooms)*w.OOMPenaltyPerEvent, 25.0)
	volatilityPenalty := computeVolatilityPenalty(samples, w)

	// Availability only makes sense for containers that are supposed to be up.
	// A cleanly stopped container (or one we have no samples for, e.g. a
	// cluster without metrics-server) is not penalised.
	availabilityPenalty := 0.0
	if c.CurrentStatus == model.StatusRunning || c.CurrentStatus == model.StatusCrashing {
		availabilityPenalty = computeAvailabilityPenalty(samples, windowStart, windowEnd, w)
	}

	total := restartPenalty + crashPenalty + oomPenalty + volatilityPenalty + availabilityPenalty

	return &model.StabilityScore{
		ContainerID:         c.ID,
		SourceName:          c.SourceName,
		Score:               math.Max(0, 100.0-total),
		RestartPenalty:      restartPenalty,
		CrashPenalty:        crashPenalty,
		OOMPenalty:          oomPenalty,
		VolatilityPenalty:   volatilityPenalty,
		AvailabilityPenalty: availabilityPenalty,
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
