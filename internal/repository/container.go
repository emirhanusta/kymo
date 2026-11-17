package repository

import (
	"context"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
)

// ContainerRepository defines the persistence operations for Container records.
// Implementations are expected to be safe for concurrent use.
// ContainerFilter narrows container listings. Zero values mean "no filter".
type ContainerFilter struct {
	// Runtime restricts results to a single orchestrator.
	Runtime model.Runtime
	// Source restricts results to a named collector source.
	Source string
	// Query is a case-insensitive substring matched against name, image,
	// namespace and workload name.
	Query string
	// Status restricts results to a single current_status value.
	Status model.ContainerStatus
}

// ContainerRepository defines the persistence operations for Container records.
// Implementations are expected to be safe for concurrent use.
type ContainerRepository interface {
	// Upsert inserts a new Container or updates the existing record that shares
	// the same ID. Lifetime counters (start/crash/OOM) are never overwritten by an
	// upsert — use IncrementCounters. Empty identity fields on c do not clobber
	// stored values. When restore is true an archived (soft-deleted) container is
	// made active again. It returns the persisted state after the operation.
	Upsert(ctx context.Context, c *model.Container, restore bool) (*model.Container, error)

	// UpdateState applies the status and last-seen time of c to an existing row.
	// It returns false when no row with c.ID exists.
	UpdateState(ctx context.Context, c *model.Container) (bool, error)

	// IncrementCounters atomically adds the given deltas to a container's
	// lifetime start/crash/OOM counters.
	IncrementCounters(ctx context.Context, id string, starts, crashes, ooms int) error

	// Touch advances last_seen_at for the container. It returns false when no
	// row with that ID exists.
	Touch(ctx context.Context, id string, seenAt time.Time) (bool, error)

	// MarkMissingStopped marks every active (running/paused/crashing/unknown)
	// container of the given source whose ID is not in presentIDs as stopped.
	// It returns the number of rows changed.
	MarkMissingStopped(ctx context.Context, sourceName string, presentIDs []string) (int64, error)

	// FindByID retrieves an active Container by its runtime-specific identifier.
	// Returns (nil, nil) when no matching record exists.
	FindByID(ctx context.Context, id string) (*model.Container, error)

	// FindAll returns every active Container matching the filter.
	FindAll(ctx context.Context, f ContainerFilter) ([]*model.Container, error)

	// FindPage returns one page of active containers matching the filter and the
	// total matching row count. sortField and sortDir are validated against a
	// whitelist internally.
	FindPage(ctx context.Context, f ContainerFilter, limit, offset int, sortField, sortDir string) ([]*model.Container, int64, error)

	// Delete permanently removes the Container with the given ID along with all
	// cascade-deletable related records (events, samples, scores).
	Delete(ctx context.Context, id string) error

	// SoftDelete marks a container as archived by setting deleted_at = NOW().
	// The container is excluded from active listings but all history is preserved.
	// If the container is observed by a collector again it will be auto-restored.
	SoftDelete(ctx context.Context, id string) error

	// Ignore permanently suppresses tracking for a container by setting ignored = true.
	// The container is excluded from all active listings and will not be re-added
	// even if the collector observes it again.
	Ignore(ctx context.Context, id string) error

	// Restore clears both deleted_at and ignored, making the container active again.
	Restore(ctx context.Context, id string) error

	// HardDelete permanently removes the Container and all related records.
	HardDelete(ctx context.Context, id string) error

	// FindDeleted returns all soft-deleted containers, optionally filtered by sourceName.
	// Results are ordered by deleted_at descending (most recently archived first).
	FindDeleted(ctx context.Context, sourceName string) ([]*model.Container, error)

	// FindIgnored returns all ignored containers that have not been soft-deleted,
	// optionally filtered by sourceName. Results are ordered by last_seen_at descending.
	FindIgnored(ctx context.Context, sourceName string) ([]*model.Container, error)

	// IsIgnored returns true when the container with the given ID has ignored = true.
	// Returns false (not an error) when the container does not exist.
	IsIgnored(ctx context.Context, id string) (bool, error)

	// HardDeleteDeleted permanently removes all soft-deleted containers (and all
	// their related records via CASCADE), optionally filtered by sourceName.
	// Returns the number of rows deleted.
	HardDeleteDeleted(ctx context.Context, sourceName string) (int64, error)

	// HardDeleteIgnored permanently removes all ignored (non-deleted) containers
	// (and all their related records via CASCADE), optionally filtered by sourceName.
	// Returns the number of rows deleted.
	HardDeleteIgnored(ctx context.Context, sourceName string) (int64, error)
}

// CronJobRepository defines the persistence operations for CronJobExecution records.
// Implementations are expected to be safe for concurrent use.
type CronJobRepository interface {
	// Save persists a CronJobExecution and populates its assigned ID.
	Save(ctx context.Context, exec *model.CronJobExecution) (*model.CronJobExecution, error)

	// FindByCronJob returns up to limit executions for the given CronJob,
	// ordered by sampled_at descending (most recent first).
	FindByCronJob(ctx context.Context, sourceName, namespace, cronJobName string, limit int) ([]*model.CronJobExecution, error)

	// FindLatestPerCronJob returns the single most recent execution for each
	// distinct CronJob within the given source and namespace.
	FindLatestPerCronJob(ctx context.Context, sourceName, namespace string) ([]*model.CronJobExecution, error)
}
