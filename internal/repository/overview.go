package repository

import (
	"context"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
)

// OverviewScope restricts overview aggregates to clusters. Source (exact name)
// wins; otherwise a non-nil Sources restricts to those names (an empty,
// non-nil slice matches nothing); nil means every recorded cluster.
type OverviewScope struct {
	Source  string
	Sources []string
}

// ActivityQuery asks for an hourly histogram of the last Hours hours ending at
// the hour containing Now, plus the crash count of the window before it.
type ActivityQuery struct {
	OverviewScope
	Now   time.Time
	Hours int
}

// ChangesQuery asks for the latest rollouts of new revisions.
type ChangesQuery struct {
	OverviewScope
	Limit int
}

// OverviewRepository serves the read-only aggregates behind the Overview page.
// Job and CronJob workloads and system workloads are left out of both.
type OverviewRepository interface {
	// Activity counts start, crash and OOM events per hour.
	Activity(ctx context.Context, q ActivityQuery) (*model.Activity, error)
	// Changes returns, newest first, the revisions that replaced an earlier
	// revision of the same workload (a workload's first recorded revision is
	// not a change).
	Changes(ctx context.Context, q ChangesQuery) ([]*model.Change, error)
}
