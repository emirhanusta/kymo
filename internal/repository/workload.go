package repository

import (
	"context"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
)

// WorkloadFilter narrows workload listings. Zero values mean "no filter".
type WorkloadFilter struct {
	Source    string
	Namespace string
	Kind      string
	// Query is a case-insensitive substring matched against name and namespace.
	Query string
}

// WorkloadRepository aggregates container instances into workloads and stores
// workload-level stability scores.
type WorkloadRepository interface {
	// List returns one summary per workload with at least one active
	// (non-archived, non-ignored) instance.
	List(ctx context.Context, f WorkloadFilter) ([]*model.WorkloadSummary, error)
	// Get returns a single workload summary, or ErrNotFound.
	Get(ctx context.Context, id string) (*model.WorkloadSummary, error)
	// Resources returns usage aggregated over all instances per time bucket:
	// CPU and memory are summed across instances (per-instance averages within
	// the bucket), so the series shows the workload's total footprint.
	Resources(ctx context.Context, id string, from, to time.Time, bucket time.Duration) ([]*model.ResourceSample, error)

	SaveScore(ctx context.Context, s *model.WorkloadScore) error
	LatestScore(ctx context.Context, id string) (*model.WorkloadScore, error)
	ScoreHistory(ctx context.Context, id string, n int) ([]*model.WorkloadScore, error)
}
