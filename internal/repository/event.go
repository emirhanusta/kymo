package repository

import (
	"context"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
)

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

	// FindRecent returns the n most recent lifecycle events across all containers,
	// ordered by OccurredAt descending, with ContainerName and Runtime populated.
	// Pass a non-empty sourceName to restrict the feed to one source.
	FindRecent(ctx context.Context, n int, sourceName string) ([]*model.LifecycleEvent, error)
}
