package timescale

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

type cronJobRepo struct{ db *DB }

// CronJobs returns the CronJobRepository backed by this DB.
func (db *DB) CronJobs() repository.CronJobRepository { return &cronJobRepo{db} }

const saveCronJobSQL = `
INSERT INTO cronjob_executions
  (source_name, namespace, cronjob_name, job_name, pod_name,
   started_at, completed_at, duration_seconds, succeeded, exit_code, sampled_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
ON CONFLICT (source_name, namespace, job_name, pod_name, sampled_at) DO NOTHING
RETURNING id`

// Save persists a CronJobExecution and populates its assigned ID. An execution
// that was already recorded is silently skipped (ID stays 0).
func (r *cronJobRepo) Save(ctx context.Context, exec *model.CronJobExecution) (*model.CronJobExecution, error) {
	err := r.db.pool.QueryRow(ctx, saveCronJobSQL,
		exec.SourceName, exec.Namespace, exec.CronJobName, exec.JobName, exec.PodName,
		exec.StartedAt, exec.CompletedAt, exec.DurationSeconds, exec.Succeeded, exec.ExitCode,
		exec.SampledAt,
	).Scan(&exec.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return exec, nil // duplicate
	}
	if err != nil {
		return nil, fmt.Errorf("timescale: cronjob save: %w", err)
	}
	return exec, nil
}

const findByCronJobSQL = `
SELECT id, source_name, namespace, cronjob_name, job_name, pod_name,
       started_at, completed_at, duration_seconds, succeeded, exit_code, sampled_at
FROM cronjob_executions
WHERE ($1 = '' OR source_name = $1)
  AND namespace = $2
  AND cronjob_name = $3
ORDER BY sampled_at DESC LIMIT $4`

// FindByCronJob returns up to limit executions for the given CronJob, newest first.
// Pass an empty sourceName to include results from all sources.
func (r *cronJobRepo) FindByCronJob(ctx context.Context, sourceName, namespace, cronJobName string, limit int) ([]*model.CronJobExecution, error) {
	rows, err := r.db.pool.Query(ctx, findByCronJobSQL, sourceName, namespace, cronJobName, limit)
	if err != nil {
		return nil, fmt.Errorf("timescale: cronjob find by cronjob: %w", err)
	}
	defer rows.Close()
	return scanCronJobRows(rows)
}

const findLatestPerCronJobSQL = `
SELECT DISTINCT ON (namespace, cronjob_name)
       id, source_name, namespace, cronjob_name, job_name, pod_name,
       started_at, completed_at, duration_seconds, succeeded, exit_code, sampled_at
FROM cronjob_executions
WHERE ($1 = '' OR source_name = $1)
  AND ($2 = '' OR namespace = $2)
ORDER BY namespace, cronjob_name, sampled_at DESC`

// FindLatestPerCronJob returns the most recent execution for each CronJob.
// Pass an empty sourceName to include all sources; pass an empty namespace to include all namespaces.
func (r *cronJobRepo) FindLatestPerCronJob(ctx context.Context, sourceName, namespace string) ([]*model.CronJobExecution, error) {
	rows, err := r.db.pool.Query(ctx, findLatestPerCronJobSQL, sourceName, namespace)
	if err != nil {
		return nil, fmt.Errorf("timescale: cronjob find latest per cronjob: %w", err)
	}
	defer rows.Close()
	return scanCronJobRows(rows)
}

// DeleteBySource removes every execution recorded for the source.
func (r *cronJobRepo) DeleteBySource(ctx context.Context, sourceName string) (int64, error) {
	tag, err := r.db.pool.Exec(ctx, `DELETE FROM cronjob_executions WHERE source_name = $1`, sourceName)
	if err != nil {
		return 0, fmt.Errorf("timescale: cronjob delete by source: %w", err)
	}
	return tag.RowsAffected(), nil
}

// scanCronJobRows is a shared helper that scans pgx rows into CronJobExecution slices.
func scanCronJobRows(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}) ([]*model.CronJobExecution, error) {
	var results []*model.CronJobExecution
	for rows.Next() {
		var e model.CronJobExecution
		if err := rows.Scan(
			&e.ID, &e.SourceName, &e.Namespace, &e.CronJobName, &e.JobName, &e.PodName,
			&e.StartedAt, &e.CompletedAt, &e.DurationSeconds, &e.Succeeded, &e.ExitCode,
			&e.SampledAt,
		); err != nil {
			return nil, fmt.Errorf("timescale: cronjob scan: %w", err)
		}
		results = append(results, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("timescale: cronjob rows: %w", err)
	}
	return results, nil
}
