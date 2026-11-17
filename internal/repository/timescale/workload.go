package timescale

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

type workloadRepo struct{ db *DB }

// Workloads returns the WorkloadRepository backed by this DB.
func (db *DB) Workloads() repository.WorkloadRepository { return &workloadRepo{db} }

// workloadSummarySQL aggregates active container rows by workload. An
// "instance" is a pod for Kubernetes (second segment of namespace/pod/container).
// %s is the extra WHERE clause.
const workloadSummarySQL = `
WITH members AS (
  SELECT c.*,
         CASE WHEN c.runtime = 'kubernetes' THEN split_part(c.id, '/', 2) ELSE c.id END AS instance
  FROM containers c
  WHERE c.deleted_at IS NULL AND c.ignored = false AND c.workload_id <> '' %s
), agg AS (
  SELECT workload_id,
         MAX(source_name)   AS source_name,
         MAX(runtime)       AS runtime,
         MAX(namespace)     AS namespace,
         MAX(workload_kind) AS kind,
         MAX(workload_name) AS name,
         bool_or(is_system) AS is_system,
         array_agg(DISTINCT name ORDER BY name) AS containers,
         COALESCE(array_agg(DISTINCT image ORDER BY image)
                  FILTER (WHERE current_status <> 'stopped'), '{}') AS images,
         COUNT(DISTINCT instance) AS instances,
         COUNT(DISTINCT instance) FILTER (WHERE current_status <> 'stopped') AS active,
         COUNT(DISTINCT instance) FILTER (WHERE current_status = 'running')  AS running,
         bool_or(current_status = 'crashing') AS any_crashing,
         SUM(start_count)::int AS starts,
         SUM(GREATEST(start_count - 1, 0))::int AS restarts,
         SUM(crash_count)::int AS crashes,
         SUM(oom_count)::int   AS ooms,
         MIN(first_seen_at) AS first_seen,
         MAX(last_seen_at)  AS last_seen
  FROM members
  GROUP BY workload_id
)
SELECT a.workload_id, a.source_name, a.runtime, a.namespace, a.kind, a.name, a.is_system,
       a.containers, a.images, a.instances, a.active, a.running, a.any_crashing,
       a.starts, a.restarts, a.crashes, a.ooms, a.first_seen, a.last_seen, ls.score
FROM agg a
LEFT JOIN LATERAL (
  SELECT s.score FROM workload_scores s
  WHERE s.workload_id = a.workload_id
  ORDER BY s.calculated_at DESC LIMIT 1
) ls ON TRUE
ORDER BY a.namespace, a.name, a.kind`

func (r *workloadRepo) query(ctx context.Context, where string, args []any) ([]*model.WorkloadSummary, error) {
	rows, err := r.db.pool.Query(ctx, fmt.Sprintf(workloadSummarySQL, where), args...)
	if err != nil {
		return nil, fmt.Errorf("timescale: workload list: %w", err)
	}
	defer rows.Close()

	var out []*model.WorkloadSummary
	for rows.Next() {
		var (
			w           model.WorkloadSummary
			anyCrashing bool
		)
		if err := rows.Scan(&w.ID, &w.SourceName, &w.Runtime, &w.Namespace, &w.Kind, &w.Name, &w.IsSystem,
			&w.Containers, &w.Images, &w.Instances, &w.ActiveInstances, &w.RunningInstances, &anyCrashing,
			&w.StartCount, &w.RestartCount, &w.CrashCount, &w.OOMCount, &w.FirstSeenAt, &w.LastSeenAt, &w.Score); err != nil {
			return nil, fmt.Errorf("timescale: workload scan: %w", err)
		}
		switch {
		case anyCrashing:
			w.Status = model.StatusCrashing
		case w.RunningInstances > 0:
			w.Status = model.StatusRunning
		case w.ActiveInstances > 0:
			w.Status = model.StatusUnknown
		default:
			w.Status = model.StatusStopped
		}
		if w.Images == nil {
			w.Images = []string{}
		}
		out = append(out, &w)
	}
	return out, rows.Err()
}

func (r *workloadRepo) List(ctx context.Context, f repository.WorkloadFilter) ([]*model.WorkloadSummary, error) {
	var (
		clauses []string
		args    []any
	)
	add := func(expr string, v any) {
		args = append(args, v)
		clauses = append(clauses, fmt.Sprintf(expr, len(args)))
	}
	if f.Source != "" {
		add("c.source_name = $%d", f.Source)
	}
	if f.Namespace != "" {
		add("c.namespace = $%d", f.Namespace)
	}
	if f.Kind != "" {
		add("c.workload_kind = $%d", f.Kind)
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		args = append(args, "%"+escapeLike(q)+"%")
		clauses = append(clauses, fmt.Sprintf("(c.workload_name ILIKE $%[1]d OR c.namespace ILIKE $%[1]d)", len(args)))
	}
	where := ""
	if len(clauses) > 0 {
		where = "AND " + strings.Join(clauses, " AND ")
	}
	return r.query(ctx, where, args)
}

func (r *workloadRepo) Get(ctx context.Context, id string) (*model.WorkloadSummary, error) {
	list, err := r.query(ctx, "AND c.workload_id = $1", []any{id})
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, repository.ErrNotFound
	}
	return list[0], nil
}

const workloadResourcesSQL = `
SELECT bucket, SUM(cpu), SUM(mem)::bigint, MAX(mem_pct), COUNT(*)
FROM (
  SELECT time_bucket($4::interval, rs.sampled_at) AS bucket, rs.container_id,
         AVG(rs.cpu_percent) AS cpu, AVG(rs.mem_bytes) AS mem, AVG(rs.mem_percent) AS mem_pct
  FROM resource_samples rs
  JOIN containers c ON c.id = rs.container_id
  WHERE c.workload_id = $1 AND rs.sampled_at BETWEEN $2 AND $3
  GROUP BY bucket, rs.container_id
) per_instance
GROUP BY bucket
ORDER BY bucket`

func (r *workloadRepo) Resources(ctx context.Context, id string, from, to time.Time, bucket time.Duration) ([]*model.ResourceSample, error) {
	if bucket <= 0 {
		bucket = 30 * time.Second
	}
	rows, err := r.db.pool.Query(ctx, workloadResourcesSQL, id, from, to, bucket)
	if err != nil {
		return nil, fmt.Errorf("timescale: workload resources: %w", err)
	}
	defer rows.Close()
	var out []*model.ResourceSample
	for rows.Next() {
		s := model.ResourceSample{ContainerID: id}
		var instances int
		if err := rows.Scan(&s.SampledAt, &s.CPUPercent, &s.MemBytes, &s.MemPercent, &instances); err != nil {
			return nil, fmt.Errorf("timescale: workload resources scan: %w", err)
		}
		out = append(out, &s)
	}
	return out, rows.Err()
}

const workloadScoreCols = `id, workload_id, source_name, score, restart_penalty, crash_penalty, oom_penalty,
  volatility_penalty, availability_penalty, restarts, crashes, ooms, calculated_at`

func scanWorkloadScore(row interface{ Scan(...any) error }) (*model.WorkloadScore, error) {
	var s model.WorkloadScore
	return &s, row.Scan(&s.ID, &s.WorkloadID, &s.SourceName, &s.Score, &s.RestartPenalty, &s.CrashPenalty,
		&s.OOMPenalty, &s.VolatilityPenalty, &s.AvailabilityPenalty, &s.Restarts, &s.Crashes, &s.OOMs, &s.CalculatedAt)
}

func (r *workloadRepo) SaveScore(ctx context.Context, s *model.WorkloadScore) error {
	err := r.db.pool.QueryRow(ctx, `INSERT INTO workload_scores
  (workload_id, source_name, score, restart_penalty, crash_penalty, oom_penalty,
   volatility_penalty, availability_penalty, restarts, crashes, ooms, calculated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id`,
		s.WorkloadID, s.SourceName, s.Score, s.RestartPenalty, s.CrashPenalty, s.OOMPenalty,
		s.VolatilityPenalty, s.AvailabilityPenalty, s.Restarts, s.Crashes, s.OOMs, s.CalculatedAt,
	).Scan(&s.ID)
	if err != nil {
		return fmt.Errorf("timescale: workload score save: %w", err)
	}
	return nil
}

func (r *workloadRepo) LatestScore(ctx context.Context, id string) (*model.WorkloadScore, error) {
	s, err := scanWorkloadScore(r.db.pool.QueryRow(ctx, `SELECT `+workloadScoreCols+`
FROM workload_scores WHERE workload_id = $1 ORDER BY calculated_at DESC LIMIT 1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("timescale: workload score latest: %w", err)
	}
	return s, nil
}

func (r *workloadRepo) ScoreHistory(ctx context.Context, id string, n int) ([]*model.WorkloadScore, error) {
	rows, err := r.db.pool.Query(ctx, `SELECT `+workloadScoreCols+`
FROM workload_scores WHERE workload_id = $1 ORDER BY calculated_at DESC LIMIT $2`, id, n)
	if err != nil {
		return nil, fmt.Errorf("timescale: workload score history: %w", err)
	}
	defer rows.Close()
	var out []*model.WorkloadScore
	for rows.Next() {
		s, err := scanWorkloadScore(rows)
		if err != nil {
			return nil, fmt.Errorf("timescale: workload score scan: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
