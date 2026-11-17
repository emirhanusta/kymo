package repository

import (
	"context"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
)

// K8sEventQuery filters persisted Kubernetes Events. Zero values mean "no filter".
type K8sEventQuery struct {
	Source     string
	Namespace  string
	Type       string
	Reason     string
	WorkloadID string
	// Query is a case-insensitive substring match over reason, message and name.
	Query string
	// Since returns only events with last_seen >= Since.
	Since time.Time
	// Before returns only events with last_seen < Before (cursor for "load older").
	Before time.Time
	Limit  int
}

// K8sEventRepository persists Kubernetes Events.
// Implementations are expected to be safe for concurrent use.
type K8sEventRepository interface {
	// Upsert stores an event. An existing (source_name, uid, first_seen) row has
	// its count, message and (when non-empty) workload_id updated and last_seen
	// moved forward only.
	Upsert(ctx context.Context, e *model.K8sEvent) error

	// List returns events matching q, newest last_seen first.
	List(ctx context.Context, q K8sEventQuery) ([]*model.K8sEvent, error)

	// Reasons aggregates (reason, type) pairs with their number of event rows
	// since the given time, most frequent first. source may be "".
	Reasons(ctx context.Context, source string, since time.Time) ([]model.K8sEventReason, error)

	// DeleteBySource removes every event recorded for the source.
	DeleteBySource(ctx context.Context, sourceName string) (int64, error)
}
