package timescale

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

// CrashLogs returns the CrashLogRepository backed by this DB.
func (db *DB) CrashLogs() repository.CrashLogRepository { return &crashLogRepo{db} }

type crashLogRepo struct{ db *DB }

const crashLogCols = `cl.id, cl.container_id, cl.source_name, cl.occurred_at, cl.reason, cl.exit_code,
  cl.previous, cl.lines, cl.line_count, cl.truncated, cl.captured_at`

const saveCrashLogSQL = `
INSERT INTO crash_logs
  (container_id, source_name, occurred_at, reason, exit_code, previous, lines, line_count, truncated, captured_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
ON CONFLICT (container_id, occurred_at) DO NOTHING`

// Save stores a snapshot, ignoring duplicates for the same termination.
func (r *crashLogRepo) Save(ctx context.Context, l *model.CrashLog) error {
	captured := l.CapturedAt
	if captured.IsZero() {
		captured = time.Now().UTC()
	}
	// Postgres text cannot hold NUL bytes.
	lines := stripNUL(l.Lines)
	if _, err := r.db.pool.Exec(ctx, saveCrashLogSQL,
		l.ContainerID, l.SourceName, l.OccurredAt, l.Reason, l.ExitCode, l.Previous,
		lines, l.LineCount, l.Truncated, captured,
	); err != nil {
		return fmt.Errorf("timescale: crash log save: %w", err)
	}
	return nil
}

func stripNUL(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			out := make([]byte, 0, len(s))
			for j := 0; j < len(s); j++ {
				if s[j] != 0 {
					out = append(out, s[j])
				}
			}
			return string(out)
		}
	}
	return s
}

// FindByContainer returns the container's snapshots, newest first.
func (r *crashLogRepo) FindByContainer(ctx context.Context, containerID string, limit int) ([]*model.CrashLog, error) {
	rows, err := r.db.pool.Query(ctx, `SELECT `+crashLogCols+` FROM crash_logs cl
WHERE cl.container_id = $1 ORDER BY cl.occurred_at DESC LIMIT $2`, containerID, limit)
	if err != nil {
		return nil, fmt.Errorf("timescale: crash log find by container: %w", err)
	}
	defer rows.Close()
	return scanCrashLogs(rows)
}

// FindByContainerAt returns the snapshot closest to at within tolerance.
func (r *crashLogRepo) FindByContainerAt(ctx context.Context, containerID string, at time.Time, tolerance time.Duration) (*model.CrashLog, error) {
	rows, err := r.db.pool.Query(ctx, `SELECT `+crashLogCols+` FROM crash_logs cl
WHERE cl.container_id = $1 AND cl.occurred_at BETWEEN $2 AND $3
ORDER BY abs(extract(epoch FROM (cl.occurred_at - $4::timestamptz))) ASC LIMIT 1`,
		containerID, at.Add(-tolerance), at.Add(tolerance), at)
	if err != nil {
		return nil, fmt.Errorf("timescale: crash log find at: %w", err)
	}
	defer rows.Close()
	list, err := scanCrashLogs(rows)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, nil
	}
	return list[0], nil
}

// FindByWorkload returns snapshots of every instance of the workload, newest first.
func (r *crashLogRepo) FindByWorkload(ctx context.Context, workloadID string, limit int) ([]*model.CrashLog, error) {
	rows, err := r.db.pool.Query(ctx, `SELECT `+crashLogCols+` FROM crash_logs cl
JOIN containers c ON c.id = cl.container_id
WHERE c.workload_id = $1 ORDER BY cl.occurred_at DESC LIMIT $2`, workloadID, limit)
	if err != nil {
		return nil, fmt.Errorf("timescale: crash log find by workload: %w", err)
	}
	defer rows.Close()
	return scanCrashLogs(rows)
}

func scanCrashLogs(rows pgx.Rows) ([]*model.CrashLog, error) {
	out := []*model.CrashLog{}
	for rows.Next() {
		var l model.CrashLog
		if err := rows.Scan(&l.ID, &l.ContainerID, &l.SourceName, &l.OccurredAt, &l.Reason, &l.ExitCode,
			&l.Previous, &l.Lines, &l.LineCount, &l.Truncated, &l.CapturedAt); err != nil {
			return nil, fmt.Errorf("scan crash log: %w", err)
		}
		out = append(out, &l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("crash log rows: %w", err)
	}
	return out, nil
}
