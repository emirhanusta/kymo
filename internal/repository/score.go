package repository

import (
	"context"

	"github.com/emirhanusta/kymo/internal/model"
)

// ScoreRepository defines the persistence operations for StabilityScore records.
// Implementations are expected to be safe for concurrent use.
type ScoreRepository interface {
	// Save persists a new StabilityScore and returns it with the database-assigned ID.
	Save(ctx context.Context, s *model.StabilityScore) (*model.StabilityScore, error)

	// FindLatest returns the most recently calculated StabilityScore for the given
	// container. Returns (nil, nil) when no score has been computed yet.
	FindLatest(ctx context.Context, containerID string) (*model.StabilityScore, error)

	// FindHistory returns the n most recent StabilityScores for the given container,
	// ordered by CalculatedAt descending so the caller sees newest-first.
	FindHistory(ctx context.Context, containerID string, n int) ([]*model.StabilityScore, error)
}
