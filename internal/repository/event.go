package repository

import (
	"context"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
)

// EventQuery filters the global event feed. Zero values mean "no filter".
type EventQuery struct {
	Limit  int
	Source string
	Type   model.EventType
	// Before returns only events that occurred strictly before this time
	// (cursor for "load older").
	Before time.Time
}

// EventRepository defines the persistence operations for LifecycleEvent records.
// Implementations are expected to be safe for concurrent use.
type EventRepository interface {
	// Save persists a new LifecycleEvent and returns it with the database-assigned ID.
	Save(ctx context.Context, e *model.LifecycleEvent) (*model.LifecycleEvent, error)

	// FindByContainerID returns the most recent limit lifecycle events for the
	// given container, ordered by OccurredAt ascending (chronological history).
	// A limit <= 0 returns every event.
	FindByContainerID(ctx context.Context, containerID string, limit int) ([]*model.LifecycleEvent, error)

	// FindByContainerIDSince returns the container's events that occurred at or
	// after since, ordered by OccurredAt ascending.
	FindByContainerIDSince(ctx context.Context, containerID string, since time.Time) ([]*model.LifecycleEvent, error)

	// FindRecent returns the most recent lifecycle events across all containers
	// matching q, ordered by OccurredAt descending, with ContainerName and
	// Runtime populated.
	FindRecent(ctx context.Context, q EventQuery) ([]*model.LifecycleEvent, error)
}
