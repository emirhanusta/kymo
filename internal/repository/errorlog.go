package repository

import (
	"context"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
)

// ErrorQuery filters error signatures. Zero values mean "no filter".
type ErrorQuery struct {
	Source     string
	Namespace  string
	WorkloadID string
	// Since is the start of the window; signatures not seen since are skipped.
	Since time.Time
	// Until is the end of the window (default now); buckets are spread over [Since, Until].
	Until time.Time
	Limit int
}

// ErrorLogRepository persists log-capture targets, error signatures, their
// counts and the newest occurrences. Implementations are safe for concurrent use.
type ErrorLogRepository interface {
	// ListTargets returns every target, oldest first.
	ListTargets(ctx context.Context) ([]*model.LogCaptureTarget, error)
	// CreateTarget inserts a target and sets ID and CreatedAt. ErrConflict when
	// a target for the same (source, namespace, workload) exists.
	CreateTarget(ctx context.Context, t *model.LogCaptureTarget) error
	// GetTarget returns the target or ErrNotFound.
	GetTarget(ctx context.Context, id int64) (*model.LogCaptureTarget, error)
	// DeleteTarget removes a target; ErrNotFound when it does not exist.
	DeleteTarget(ctx context.Context, id int64) error
	// SyncConfigTargets makes the origin='config' targets equal to declared:
	// missing ones are added (a UI target of the same scope is promoted),
	// undeclared ones are removed.
	SyncConfigTargets(ctx context.Context, declared []*model.LogCaptureTarget) error

	// SaveBatch upserts signatures, adds the per-minute counts and occurrences
	// and trims each touched signature to its newest keep occurrences.
	SaveBatch(ctx context.Context, b *model.ErrorBatch, keep int) error

	// ListSignatures returns signatures seen in the window, newest last_seen
	// first, with window counts, distinct pods and 24 buckets.
	ListSignatures(ctx context.Context, q ErrorQuery) ([]*model.ErrorSignature, error)
	// GetSignature returns one signature for the window or ErrNotFound.
	GetSignature(ctx context.Context, id string, since, until time.Time) (*model.ErrorSignature, error)
	// ListOccurrences returns occurrences strictly older than before (all when
	// zero), newest first.
	ListOccurrences(ctx context.Context, signatureID string, before time.Time, limit int) ([]*model.ErrorOccurrence, error)

	// WorkloadErrorRates sums captured error entries per workload for buckets
	// at or after since, with each workload's most frequent signature.
	WorkloadErrorRates(ctx context.Context, since time.Time) ([]model.WorkloadErrorRate, error)

	// DeleteStale removes signatures (cascading counts and occurrences) last
	// seen before the cutoff.
	DeleteStale(ctx context.Context, before time.Time) (int64, error)
	// SetRetention re-applies the retention policy of counts and occurrences.
	SetRetention(ctx context.Context, d time.Duration) error
}
