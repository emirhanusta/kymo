package repository

import (
	"context"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
)

// CrashLogRepository persists crash-time log snapshots.
// Implementations are expected to be safe for concurrent use.
type CrashLogRepository interface {
	// Save stores a snapshot. A snapshot for the same (container_id,
	// occurred_at) that already exists is left untouched (no error).
	Save(ctx context.Context, l *model.CrashLog) error

	// FindByContainer returns up to limit snapshots of a container, newest first.
	FindByContainer(ctx context.Context, containerID string, limit int) ([]*model.CrashLog, error)

	// FindByContainerAt returns the snapshot whose occurred_at is within
	// tolerance of at, or (nil, nil) when none exists.
	FindByContainerAt(ctx context.Context, containerID string, at time.Time, tolerance time.Duration) (*model.CrashLog, error)

	// FindByWorkload returns up to limit snapshots across every instance of a
	// workload, newest first.
	FindByWorkload(ctx context.Context, workloadID string, limit int) ([]*model.CrashLog, error)
}
