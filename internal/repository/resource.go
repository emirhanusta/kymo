package repository

import (
	"context"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
)

// ResourceRepository defines the persistence operations for ResourceSample records.
// Implementations are expected to be safe for concurrent use.
type ResourceRepository interface {
	// Save persists a new ResourceSample and returns it with the database-assigned ID.
	Save(ctx context.Context, s *model.ResourceSample) (*model.ResourceSample, error)

	// FindByContainerID returns resource samples for the given container within the
	// closed interval [from, to], ordered by SampledAt ascending. When bucket > 0
	// samples are downsampled into time buckets of that width (averages for
	// gauges, maxima for cumulative counters).
	FindByContainerID(ctx context.Context, containerID string, from, to time.Time, bucket time.Duration) ([]*model.ResourceSample, error)

	// GetAggregated returns pre-computed aggregate metrics (averages, peaks) for the
	// given container within the specified time window. Implementations should
	// leverage TimescaleDB continuous aggregates or time_bucket() for efficiency.
	GetAggregated(ctx context.Context, containerID string, from, to time.Time) (*AggregatedResource, error)
}
