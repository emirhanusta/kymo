package repository

import (
	"context"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
)

// IssueQuery filters issue listings. Zero values mean "no filter".
type IssueQuery struct {
	// Status is "open" (default), "resolved" or "all".
	Status     string
	Source     string
	Severity   string
	Type       string
	WorkloadID string
	// Before is a paging cursor: for "resolved" it matches resolved_at < Before,
	// otherwise first_seen < Before.
	Before time.Time
	Limit  int
}

// IssueRepository persists issues. Implementations are safe for concurrent use.
type IssueRepository interface {
	// Create inserts an open issue and sets its ID. ErrConflict is returned when
	// an open issue of the same (workload, type) already exists.
	Create(ctx context.Context, i *model.Issue) error
	// UpdateOpen refreshes severity, title, detail and last_seen of an open issue.
	UpdateOpen(ctx context.Context, i *model.Issue) error
	// Resolve marks an open issue resolved at the given time.
	Resolve(ctx context.Context, id int64, at time.Time) error
	// Reopen turns a resolved issue open again (resolved_at cleared, flaps+1)
	// with the given severity, title, detail and last_seen.
	Reopen(ctx context.Context, i *model.Issue) error

	// ListActive returns every open issue plus resolved ones whose resolved_at
	// is at or after resolvedSince (newest resolved first).
	ListActive(ctx context.Context, resolvedSince time.Time) ([]*model.Issue, error)
	// List returns issues matching q (see IssueQuery for ordering).
	List(ctx context.Context, q IssueQuery) ([]*model.Issue, error)
	// Get returns one issue or ErrNotFound.
	Get(ctx context.Context, id int64) (*model.Issue, error)
	// Summary counts the open issues.
	Summary(ctx context.Context) (*model.IssueSummary, error)

	// MarkNotified records that a notification was dispatched for the issue.
	MarkNotified(ctx context.Context, id int64, at time.Time) error
	// DeleteResolvedBefore removes resolved issues with resolved_at < t.
	DeleteResolvedBefore(ctx context.Context, t time.Time) (int64, error)
}
