// Package timescale provides TimescaleDB-backed implementations of all repository
// interfaces defined in the repository package. Each concrete type wraps a shared
// pgx connection pool and is safe for concurrent use.
package timescale

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

// DB wraps a pgxpool.Pool and acts as a factory for all repository implementations.
// Obtain concrete repository instances via the typed accessor methods.
type DB struct {
	pool *pgxpool.Pool
	log  *zap.Logger
}

// New opens a pgx connection pool targeting the TimescaleDB instance at dsn.
// It validates the connection with a Ping before returning.
// maxConns overrides the pool size when > 0.
func New(ctx context.Context, dsn string, maxConns int32, log *zap.Logger) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("timescale: parse dsn: %w", err)
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("timescale: open pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("timescale: ping failed: %w", err)
	}

	log.Info("timescaledb connected", zap.String("host", cfg.ConnConfig.Host))
	return &DB{pool: pool, log: log}, nil
}

// Ping verifies the database is reachable.
func (db *DB) Ping(ctx context.Context) error { return db.pool.Ping(ctx) }

// Close releases all connections held by the pool. Should be called during
// graceful shutdown after all in-flight queries have completed.
func (db *DB) Close() {
	db.pool.Close()
}

// Containers returns the ContainerRepository backed by this DB.
func (db *DB) Containers() repository.ContainerRepository { return &containerRepo{db} }

// Events returns the EventRepository backed by this DB.
func (db *DB) Events() repository.EventRepository { return &eventRepo{db} }

// Resources returns the ResourceRepository backed by this DB.
func (db *DB) Resources() repository.ResourceRepository { return &resourceRepo{db} }

// Scores returns the ScoreRepository backed by this DB.
func (db *DB) Scores() repository.ScoreRepository { return &scoreRepo{db} }

// ---------------------------------------------------------------------------
// ContainerRepository implementation
// ---------------------------------------------------------------------------

type containerRepo struct{ db *DB }

// containerCols is the column list shared by every container read query. The
// trailing score column comes from the latest-score lateral join below; queries
// alias the containers table as "c".
const containerCols = `c.id, c.runtime, c.source_name, c.name, c.image, c.namespace, c.node_name,
  c.first_seen_at, c.last_seen_at, c.start_count, c.crash_count, c.oom_count, c.current_status,
  c.deleted_at, c.ignored, c.workload_kind, c.workload_name, c.is_system, c.workload_id, c.revision, ls.score`

// latestScoreJoin attaches the most recent stability score to each container row.
const latestScoreJoin = `LEFT JOIN LATERAL (
  SELECT s.score FROM stability_scores s
  WHERE s.container_id = c.id
  ORDER BY s.calculated_at DESC LIMIT 1
) ls ON TRUE`

// activeWhere restricts results to containers that are neither archived nor ignored.
const activeWhere = `c.deleted_at IS NULL AND c.ignored = false`

// upsertContainerSQL inserts a container or refreshes its identity and status.
// Lifetime counters are only set on insert (always 0 — they are advanced by
// IncrementCounters) so that collector restarts can never reset them. Empty
// identity fields never overwrite stored values, and last_seen_at only moves
// forward. $14 controls whether an archived container is restored.
const upsertContainerSQL = `
INSERT INTO containers AS c (id, runtime, source_name, name, image, namespace, node_name,
  first_seen_at, last_seen_at, start_count, crash_count, oom_count, current_status,
  workload_kind, workload_name, is_system, workload_id, revision)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,0,0,0,$10,
  COALESCE(NULLIF($11,''),'unknown'),$12,$13,$15,$16)
ON CONFLICT (id) DO UPDATE SET
  name           = COALESCE(NULLIF(EXCLUDED.name,''), c.name),
  image          = COALESCE(NULLIF(EXCLUDED.image,''), c.image),
  source_name    = COALESCE(NULLIF(EXCLUDED.source_name,''), c.source_name),
  namespace      = COALESCE(NULLIF(EXCLUDED.namespace,''), c.namespace),
  node_name      = COALESCE(NULLIF(EXCLUDED.node_name,''), c.node_name),
  last_seen_at   = GREATEST(c.last_seen_at, EXCLUDED.last_seen_at),
  current_status = EXCLUDED.current_status,
  workload_kind  = CASE WHEN $11 = '' THEN c.workload_kind ELSE EXCLUDED.workload_kind END,
  workload_name  = COALESCE(NULLIF(EXCLUDED.workload_name,''), c.workload_name),
  is_system      = EXCLUDED.is_system OR c.is_system,
  workload_id    = COALESCE(NULLIF(EXCLUDED.workload_id,''), c.workload_id),
  revision       = COALESCE(NULLIF(EXCLUDED.revision,''), c.revision),
  deleted_at     = CASE WHEN $14 THEN NULL ELSE c.deleted_at END
RETURNING c.id, c.runtime, c.source_name, c.name, c.image, c.namespace, c.node_name,
  c.first_seen_at, c.last_seen_at, c.start_count, c.crash_count, c.oom_count, c.current_status,
  c.deleted_at, c.ignored, c.workload_kind, c.workload_name, c.is_system, c.workload_id, c.revision, NULL::double precision`

// Upsert inserts or updates a Container row. See upsertContainerSQL for the
// field-level merge rules.
func (r *containerRepo) Upsert(ctx context.Context, c *model.Container, restore bool) (*model.Container, error) {
	firstSeen := c.FirstSeenAt
	if firstSeen.IsZero() {
		firstSeen = time.Now().UTC()
	}
	lastSeen := c.LastSeenAt
	if lastSeen.IsZero() {
		lastSeen = time.Now().UTC()
	}
	status := c.CurrentStatus
	if status == "" {
		status = model.StatusUnknown
	}
	row := r.db.pool.QueryRow(ctx, upsertContainerSQL,
		c.ID, c.Runtime, c.SourceName, c.Name, c.Image, c.Namespace, c.NodeName,
		firstSeen, lastSeen, status,
		c.WorkloadKind, c.WorkloadName, c.IsSystem, restore,
		model.WorkloadID(c.SourceName, c.Namespace, c.WorkloadKind, c.WorkloadName),
		c.Revision,
	)
	out, err := scanContainer(row)
	if err != nil {
		return nil, fmt.Errorf("timescale: container upsert: %w", err)
	}
	return out, nil
}

const updateStateSQL = `
UPDATE containers SET
  current_status = $2,
  last_seen_at   = GREATEST(last_seen_at, $3)
WHERE id = $1`

// UpdateState applies status and last-seen time to an existing row only.
func (r *containerRepo) UpdateState(ctx context.Context, c *model.Container) (bool, error) {
	seen := c.LastSeenAt
	if seen.IsZero() {
		seen = time.Now().UTC()
	}
	tag, err := r.db.pool.Exec(ctx, updateStateSQL, c.ID, c.CurrentStatus, seen)
	if err != nil {
		return false, fmt.Errorf("timescale: container update state: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

const incrementCountersSQL = `
UPDATE containers SET
  start_count = start_count + $2,
  crash_count = crash_count + $3,
  oom_count   = oom_count + $4
WHERE id = $1`

// IncrementCounters atomically advances a container's lifetime counters.
func (r *containerRepo) IncrementCounters(ctx context.Context, id string, starts, crashes, ooms int) error {
	if starts == 0 && crashes == 0 && ooms == 0 {
		return nil
	}
	if _, err := r.db.pool.Exec(ctx, incrementCountersSQL, id, starts, crashes, ooms); err != nil {
		return fmt.Errorf("timescale: container increment counters: %w", err)
	}
	return nil
}

const touchSQL = `UPDATE containers SET last_seen_at = GREATEST(last_seen_at, $2) WHERE id = $1`

// Touch advances last_seen_at; returns false when the container does not exist.
func (r *containerRepo) Touch(ctx context.Context, id string, seenAt time.Time) (bool, error) {
	tag, err := r.db.pool.Exec(ctx, touchSQL, id, seenAt)
	if err != nil {
		return false, fmt.Errorf("timescale: container touch: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

const markMissingStoppedSQL = `
UPDATE containers SET current_status = 'stopped'
WHERE source_name = $1
  AND current_status IN ('running', 'paused', 'crashing', 'unknown')
  AND NOT (id = ANY($2))`

// MarkMissingStopped marks active containers of a source that the runtime no
// longer reports as stopped.
func (r *containerRepo) MarkMissingStopped(ctx context.Context, sourceName string, presentIDs []string) (int64, error) {
	if presentIDs == nil {
		presentIDs = []string{}
	}
	tag, err := r.db.pool.Exec(ctx, markMissingStoppedSQL, sourceName, presentIDs)
	if err != nil {
		return 0, fmt.Errorf("timescale: container mark missing stopped: %w", err)
	}
	return tag.RowsAffected(), nil
}

// FindByID retrieves an active Container by its runtime ID.
// Returns nil, nil when no row is found.
func (r *containerRepo) FindByID(ctx context.Context, id string) (*model.Container, error) {
	sql := `SELECT ` + containerCols + ` FROM containers c ` + latestScoreJoin +
		` WHERE c.id = $1 AND ` + activeWhere
	c, err := scanContainer(r.db.pool.QueryRow(ctx, sql, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("timescale: container find by id: %w", err)
	}
	return c, nil
}

// buildFilter turns a ContainerFilter into a WHERE clause (always restricted to
// active containers) and its positional arguments.
func buildFilter(f repository.ContainerFilter) (string, []any) {
	clauses := []string{activeWhere}
	var args []any
	if f.Runtime != "" {
		args = append(args, f.Runtime)
		clauses = append(clauses, fmt.Sprintf("c.runtime = $%d", len(args)))
	}
	if f.Source != "" {
		args = append(args, f.Source)
		clauses = append(clauses, fmt.Sprintf("c.source_name = $%d", len(args)))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		clauses = append(clauses, fmt.Sprintf("c.current_status = $%d", len(args)))
	}
	if f.WorkloadKind != "" {
		args = append(args, f.WorkloadKind)
		clauses = append(clauses, fmt.Sprintf("c.workload_kind = $%d", len(args)))
	}
	if f.Workload != "" {
		args = append(args, f.Workload)
		clauses = append(clauses, fmt.Sprintf("c.workload_name = $%d", len(args)))
	}
	if f.WorkloadID != "" {
		args = append(args, f.WorkloadID)
		clauses = append(clauses, fmt.Sprintf("c.workload_id = $%d", len(args)))
	}
	if !f.SeenSince.IsZero() {
		args = append(args, f.SeenSince)
		clauses = append(clauses, fmt.Sprintf("c.last_seen_at >= $%d", len(args)))
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		args = append(args, "%"+escapeLike(q)+"%")
		n := len(args)
		clauses = append(clauses, fmt.Sprintf(
			"(c.name ILIKE $%[1]d OR c.image ILIKE $%[1]d OR c.namespace ILIKE $%[1]d OR c.workload_name ILIKE $%[1]d)", n))
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

// escapeLike escapes LIKE wildcards so user input is matched literally.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// FindAll returns all active containers matching the filter, most recently seen first.
func (r *containerRepo) FindAll(ctx context.Context, f repository.ContainerFilter) ([]*model.Container, error) {
	where, args := buildFilter(f)
	sql := `SELECT ` + containerCols + ` FROM containers c ` + latestScoreJoin + ` ` + where +
		` ORDER BY c.last_seen_at DESC`
	return r.queryContainers(ctx, sql, args, "find all")
}

// validContainerSortFields maps the public sort keys to safe ORDER BY expressions.
var validContainerSortFields = map[string]string{
	"last_seen_at":   "c.last_seen_at",
	"current_status": "c.current_status",
	"crash_count":    "c.crash_count",
	"oom_count":      "c.oom_count",
	"start_count":    "c.start_count",
	"name":           "c.name",
	"score":          "ls.score",
}

// sanitizeContainerSort validates and normalises sort parameters so they can be
// safely interpolated into a SQL ORDER BY clause.
func sanitizeContainerSort(field, dir string) (string, string) {
	expr, ok := validContainerSortFields[field]
	if !ok {
		expr = "c.last_seen_at"
	}
	if strings.EqualFold(dir, "asc") {
		dir = "ASC"
	} else {
		dir = "DESC"
	}
	return expr, dir
}

// FindPage returns one page of active containers matching the filter and the
// total matching count.
func (r *containerRepo) FindPage(ctx context.Context, f repository.ContainerFilter, limit, offset int, sortField, sortDir string) ([]*model.Container, int64, error) {
	sortExpr, sortDir := sanitizeContainerSort(sortField, sortDir)
	where, args := buildFilter(f)

	var total int64
	if err := r.db.pool.QueryRow(ctx, `SELECT COUNT(*) FROM containers c `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("timescale: container count: %w", err)
	}

	args = append(args, limit, offset)
	sql := fmt.Sprintf(`SELECT %s FROM containers c %s %s
ORDER BY %s %s NULLS LAST, c.id ASC LIMIT $%d OFFSET $%d`,
		containerCols, latestScoreJoin, where, sortExpr, sortDir, len(args)-1, len(args))

	containers, err := r.queryContainers(ctx, sql, args, "find page")
	if err != nil {
		return nil, 0, err
	}
	return containers, total, nil
}

const deleteContainerSQL = `DELETE FROM containers WHERE id = $1`

// Delete removes a Container row. Cascading deletes on related tables are handled
// by the database schema (ON DELETE CASCADE on foreign keys).
func (r *containerRepo) Delete(ctx context.Context, id string) error {
	_, err := r.db.pool.Exec(ctx, deleteContainerSQL, id)
	if err != nil {
		return fmt.Errorf("timescale: container delete: %w", err)
	}
	return nil
}

const softDeleteSQL = `UPDATE containers SET deleted_at = NOW() WHERE id = $1`

// SoftDelete archives a container by setting deleted_at = NOW().
func (r *containerRepo) SoftDelete(ctx context.Context, id string) error {
	_, err := r.db.pool.Exec(ctx, softDeleteSQL, id)
	if err != nil {
		return fmt.Errorf("timescale: container soft delete: %w", err)
	}
	return nil
}

const ignoreSQL = `UPDATE containers SET ignored = true WHERE id = $1`

// Ignore suppresses a container by setting ignored = true.
func (r *containerRepo) Ignore(ctx context.Context, id string) error {
	_, err := r.db.pool.Exec(ctx, ignoreSQL, id)
	if err != nil {
		return fmt.Errorf("timescale: container ignore: %w", err)
	}
	return nil
}

const restoreSQL = `UPDATE containers SET deleted_at = NULL, ignored = false WHERE id = $1`

// Restore clears both deleted_at and ignored, making the container active again.
func (r *containerRepo) Restore(ctx context.Context, id string) error {
	_, err := r.db.pool.Exec(ctx, restoreSQL, id)
	if err != nil {
		return fmt.Errorf("timescale: container restore: %w", err)
	}
	return nil
}

const hardDeleteSQL = `DELETE FROM containers WHERE id = $1`

// HardDelete permanently removes the Container and all related records via CASCADE.
func (r *containerRepo) HardDelete(ctx context.Context, id string) error {
	_, err := r.db.pool.Exec(ctx, hardDeleteSQL, id)
	if err != nil {
		return fmt.Errorf("timescale: container hard delete: %w", err)
	}
	return nil
}

const isIgnoredSQL = `SELECT ignored FROM containers WHERE id = $1`

// IsIgnored returns true when the container has ignored = true.
// Returns false, nil when the container does not exist.
func (r *containerRepo) IsIgnored(ctx context.Context, id string) (bool, error) {
	var ignored bool
	err := r.db.pool.QueryRow(ctx, isIgnoredSQL, id).Scan(&ignored)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("timescale: container is ignored: %w", err)
	}
	return ignored, nil
}

// findArchiveSelectCols selects all container columns for deleted or ignored queries.
var findArchiveSelectCols = `SELECT ` + containerCols + ` FROM containers c ` + latestScoreJoin

// HardDeleteDeleted permanently removes all soft-deleted containers and their
// related records (via CASCADE). Optionally filtered by sourceName.
func (r *containerRepo) HardDeleteDeleted(ctx context.Context, sourceName string) (int64, error) {
	sql := `DELETE FROM containers WHERE deleted_at IS NOT NULL`
	var args []any
	if sourceName != "" {
		args = append(args, sourceName)
		sql += fmt.Sprintf(" AND source_name = $%d", len(args))
	}
	tag, err := r.db.pool.Exec(ctx, sql, args...)
	if err != nil {
		return 0, fmt.Errorf("timescale: hard delete all deleted: %w", err)
	}
	return tag.RowsAffected(), nil
}

// DeleteBySource permanently removes every container of a source (CASCADE).
func (r *containerRepo) DeleteBySource(ctx context.Context, sourceName string) (int64, error) {
	tag, err := r.db.pool.Exec(ctx, `DELETE FROM containers WHERE source_name = $1`, sourceName)
	if err != nil {
		return 0, fmt.Errorf("timescale: container delete by source: %w", err)
	}
	return tag.RowsAffected(), nil
}

// HardDeleteIgnored permanently removes all ignored (non-deleted) containers and
// their related records (via CASCADE). Optionally filtered by sourceName.
func (r *containerRepo) HardDeleteIgnored(ctx context.Context, sourceName string) (int64, error) {
	sql := `DELETE FROM containers WHERE ignored = true AND deleted_at IS NULL`
	var args []any
	if sourceName != "" {
		args = append(args, sourceName)
		sql += fmt.Sprintf(" AND source_name = $%d", len(args))
	}
	tag, err := r.db.pool.Exec(ctx, sql, args...)
	if err != nil {
		return 0, fmt.Errorf("timescale: hard delete all ignored: %w", err)
	}
	return tag.RowsAffected(), nil
}

// FindDeleted returns soft-deleted containers, optionally filtered by sourceName.
func (r *containerRepo) FindDeleted(ctx context.Context, sourceName string) ([]*model.Container, error) {
	sql := findArchiveSelectCols + ` WHERE c.deleted_at IS NOT NULL`
	var args []any
	if sourceName != "" {
		args = append(args, sourceName)
		sql += fmt.Sprintf(" AND c.source_name = $%d", len(args))
	}
	sql += " ORDER BY c.deleted_at DESC"
	return r.queryContainers(ctx, sql, args, "find deleted")
}

// FindIgnored returns ignored (non-deleted) containers, optionally filtered by sourceName.
func (r *containerRepo) FindIgnored(ctx context.Context, sourceName string) ([]*model.Container, error) {
	sql := findArchiveSelectCols + ` WHERE c.ignored = true AND c.deleted_at IS NULL`
	var args []any
	if sourceName != "" {
		args = append(args, sourceName)
		sql += fmt.Sprintf(" AND c.source_name = $%d", len(args))
	}
	sql += " ORDER BY c.last_seen_at DESC"
	return r.queryContainers(ctx, sql, args, "find ignored")
}

// queryContainers is a shared helper that executes a container SELECT and scans results.
func (r *containerRepo) queryContainers(ctx context.Context, sql string, args []any, op string) ([]*model.Container, error) {
	rows, err := r.db.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("timescale: container %s: %w", op, err)
	}
	defer rows.Close()

	var containers []*model.Container
	for rows.Next() {
		c, err := scanContainer(rows)
		if err != nil {
			return nil, fmt.Errorf("timescale: container %s scan: %w", op, err)
		}
		containers = append(containers, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("timescale: container %s rows: %w", op, err)
	}
	return containers, nil
}

// scanContainer reads a single Container from any pgx row-like value.
// It accepts both pgx.Row (QueryRow) and pgx.Rows (Query iteration).
// Expects the columns listed in containerCols (including the trailing score).
func scanContainer(row interface {
	Scan(dest ...any) error
}) (*model.Container, error) {
	var c model.Container
	return &c, row.Scan(
		&c.ID, &c.Runtime, &c.SourceName, &c.Name, &c.Image, &c.Namespace, &c.NodeName,
		&c.FirstSeenAt, &c.LastSeenAt, &c.StartCount, &c.CrashCount, &c.OOMCount, &c.CurrentStatus,
		&c.DeletedAt, &c.Ignored,
		&c.WorkloadKind, &c.WorkloadName, &c.IsSystem, &c.WorkloadID, &c.Revision, &c.Score,
	)
}

// ---------------------------------------------------------------------------
// EventRepository implementation
// ---------------------------------------------------------------------------

type eventRepo struct{ db *DB }

const saveEventSQL = `
INSERT INTO lifecycle_events
  (container_id, event_type, exit_code, node_name, source_name, metadata, occurred_at)
VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`

// Save persists a LifecycleEvent and populates its assigned ID.
func (r *eventRepo) Save(ctx context.Context, e *model.LifecycleEvent) (*model.LifecycleEvent, error) {
	metaBytes, err := json.Marshal(e.Metadata)
	if err != nil {
		return nil, fmt.Errorf("timescale: event save marshal metadata: %w", err)
	}
	err = r.db.pool.QueryRow(ctx, saveEventSQL,
		e.ContainerID, e.EventType, e.ExitCode, e.NodeName, e.SourceName, metaBytes, e.OccurredAt,
	).Scan(&e.ID)
	if err != nil {
		return nil, fmt.Errorf("timescale: event save: %w", err)
	}
	return e, nil
}

const eventCols = `e.id, e.container_id, e.event_type, e.exit_code, e.node_name, e.metadata, e.occurred_at, e.source_name,
  EXISTS (SELECT 1 FROM crash_logs cl WHERE cl.container_id = e.container_id AND cl.occurred_at = e.occurred_at) AS has_logs`

// FindByContainerID returns the most recent limit events for the container in
// ascending time order. limit <= 0 returns every event.
func (r *eventRepo) FindByContainerID(ctx context.Context, containerID string, limit int) ([]*model.LifecycleEvent, error) {
	var (
		rows pgx.Rows
		err  error
	)
	if limit > 0 {
		rows, err = r.db.pool.Query(ctx, `SELECT * FROM (
  SELECT `+eventCols+` FROM lifecycle_events e WHERE e.container_id = $1
  ORDER BY e.occurred_at DESC LIMIT $2
) t ORDER BY t.occurred_at ASC`, containerID, limit)
	} else {
		rows, err = r.db.pool.Query(ctx, `SELECT `+eventCols+` FROM lifecycle_events e
WHERE e.container_id = $1 ORDER BY e.occurred_at ASC`, containerID)
	}
	if err != nil {
		return nil, fmt.Errorf("timescale: event find by container: %w", err)
	}
	defer rows.Close()
	return scanEvents(rows, false)
}

// FindByContainerIDSince returns events at or after since in ascending order.
func (r *eventRepo) FindByContainerIDSince(ctx context.Context, containerID string, since time.Time) ([]*model.LifecycleEvent, error) {
	rows, err := r.db.pool.Query(ctx, `SELECT `+eventCols+` FROM lifecycle_events e
WHERE e.container_id = $1 AND e.occurred_at >= $2 ORDER BY e.occurred_at ASC`, containerID, since)
	if err != nil {
		return nil, fmt.Errorf("timescale: event find since: %w", err)
	}
	defer rows.Close()
	return scanEvents(rows, false)
}

// FindRecent returns the most recent events matching q across all containers,
// joined with the owning container's name and runtime.
func (r *eventRepo) FindRecent(ctx context.Context, q repository.EventQuery) ([]*model.LifecycleEvent, error) {
	var (
		clauses []string
		args    []any
	)
	if q.Source != "" {
		args = append(args, q.Source)
		clauses = append(clauses, fmt.Sprintf("e.source_name = $%d", len(args)))
	}
	if q.Type != "" {
		args = append(args, q.Type)
		clauses = append(clauses, fmt.Sprintf("e.event_type = $%d", len(args)))
	}
	if !q.Before.IsZero() {
		args = append(args, q.Before)
		clauses = append(clauses, fmt.Sprintf("e.occurred_at < $%d", len(args)))
	}
	if q.WorkloadID != "" {
		args = append(args, q.WorkloadID)
		clauses = append(clauses, fmt.Sprintf("c.workload_id = $%d", len(args)))
	}
	sql := `SELECT ` + eventCols + `, COALESCE(c.name, ''), COALESCE(c.runtime, '')
FROM lifecycle_events e LEFT JOIN containers c ON c.id = e.container_id`
	if len(clauses) > 0 {
		sql += " WHERE " + strings.Join(clauses, " AND ")
	}
	args = append(args, q.Limit)
	sql += fmt.Sprintf(" ORDER BY e.occurred_at DESC LIMIT $%d", len(args))

	rows, err := r.db.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("timescale: event find recent: %w", err)
	}
	defer rows.Close()
	return scanEvents(rows, true)
}

// scanEvents iterates rows and decodes each LifecycleEvent, including JSONB metadata.
// When withContainer is true the rows carry two extra trailing columns:
// container name and runtime.
func scanEvents(rows pgx.Rows, withContainer bool) ([]*model.LifecycleEvent, error) {
	var events []*model.LifecycleEvent
	for rows.Next() {
		var (
			e         model.LifecycleEvent
			metaBytes []byte
		)
		dest := []any{
			&e.ID, &e.ContainerID, &e.EventType, &e.ExitCode,
			&e.NodeName, &metaBytes, &e.OccurredAt, &e.SourceName, &e.HasLogs,
		}
		if withContainer {
			dest = append(dest, &e.ContainerName, &e.Runtime)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		if len(metaBytes) > 0 {
			if err := json.Unmarshal(metaBytes, &e.Metadata); err != nil {
				return nil, fmt.Errorf("unmarshal event metadata: %w", err)
			}
		}
		events = append(events, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("event rows: %w", err)
	}
	return events, nil
}

// ---------------------------------------------------------------------------
// ResourceRepository implementation
// ---------------------------------------------------------------------------

type resourceRepo struct{ db *DB }

const saveResourceSQL = `
INSERT INTO resource_samples
  (container_id, source_name, cpu_percent, mem_bytes, mem_percent,
   net_rx_bytes, net_tx_bytes, block_read_bytes, block_write_bytes, sampled_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`

// Save persists a ResourceSample and populates its assigned ID.
func (r *resourceRepo) Save(ctx context.Context, s *model.ResourceSample) (*model.ResourceSample, error) {
	err := r.db.pool.QueryRow(ctx, saveResourceSQL,
		s.ContainerID, s.SourceName, s.CPUPercent, s.MemBytes, s.MemPercent,
		s.NetRxBytes, s.NetTxBytes, s.BlockReadBytes, s.BlockWriteBytes, s.SampledAt,
	).Scan(&s.ID)
	if err != nil {
		return nil, fmt.Errorf("timescale: resource save: %w", err)
	}
	return s, nil
}

const findResourcesByContainerSQL = `
SELECT id, container_id, cpu_percent, mem_bytes, mem_percent,
  net_rx_bytes, net_tx_bytes, block_read_bytes, block_write_bytes, sampled_at, source_name
FROM resource_samples
WHERE container_id = $1 AND sampled_at BETWEEN $2 AND $3
ORDER BY sampled_at ASC`

// findResourcesBucketedSQL downsamples samples into fixed-width buckets. Gauges
// are averaged; cumulative counters take the bucket maximum so rate
// calculations on the client remain meaningful.
const findResourcesBucketedSQL = `
SELECT 0::bigint, container_id,
  AVG(cpu_percent), AVG(mem_bytes)::bigint, AVG(mem_percent),
  MAX(net_rx_bytes), MAX(net_tx_bytes), MAX(block_read_bytes), MAX(block_write_bytes),
  time_bucket($4::interval, sampled_at) AS bucket, MAX(source_name)
FROM resource_samples
WHERE container_id = $1 AND sampled_at BETWEEN $2 AND $3
GROUP BY container_id, bucket
ORDER BY bucket ASC`

// FindByContainerID returns samples in [from, to] ordered by SampledAt ascending,
// downsampled into buckets of the given width when bucket > 0.
func (r *resourceRepo) FindByContainerID(ctx context.Context, containerID string, from, to time.Time, bucket time.Duration) ([]*model.ResourceSample, error) {
	var (
		rows pgx.Rows
		err  error
	)
	if bucket > 0 {
		rows, err = r.db.pool.Query(ctx, findResourcesBucketedSQL, containerID, from, to, bucket)
	} else {
		rows, err = r.db.pool.Query(ctx, findResourcesByContainerSQL, containerID, from, to)
	}
	if err != nil {
		return nil, fmt.Errorf("timescale: resource find by container: %w", err)
	}
	defer rows.Close()

	var samples []*model.ResourceSample
	for rows.Next() {
		var s model.ResourceSample
		if err := rows.Scan(
			&s.ID, &s.ContainerID, &s.CPUPercent, &s.MemBytes, &s.MemPercent,
			&s.NetRxBytes, &s.NetTxBytes, &s.BlockReadBytes, &s.BlockWriteBytes, &s.SampledAt,
			&s.SourceName,
		); err != nil {
			return nil, fmt.Errorf("timescale: resource find by container scan: %w", err)
		}
		samples = append(samples, &s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("timescale: resource find by container rows: %w", err)
	}
	return samples, nil
}

const getAggregatedSQL = `
SELECT
  AVG(cpu_percent)      AS avg_cpu,
  MAX(cpu_percent)      AS max_cpu,
  AVG(mem_bytes)        AS avg_mem,
  MAX(mem_bytes)        AS max_mem,
  SUM(net_rx_bytes)     AS total_rx,
  SUM(net_tx_bytes)     AS total_tx
FROM resource_samples
WHERE container_id = $1 AND sampled_at BETWEEN $2 AND $3`

// GetAggregated returns aggregate metrics for a container over [from, to].
// When no samples exist in the window all numeric fields are zero.
func (r *resourceRepo) GetAggregated(ctx context.Context, containerID string, from, to time.Time) (*repository.AggregatedResource, error) {
	// Use pointer targets so pgx maps SQL NULL → nil without panicking.
	var (
		avgCPU  *float64
		maxCPU  *float64
		avgMem  *float64
		maxMem  *float64
		totalRx *float64
		totalTx *float64
	)
	err := r.db.pool.QueryRow(ctx, getAggregatedSQL, containerID, from, to).Scan(
		&avgCPU, &maxCPU, &avgMem, &maxMem, &totalRx, &totalTx,
	)
	if err != nil {
		return nil, fmt.Errorf("timescale: resource get aggregated: %w", err)
	}

	deref := func(p *float64) float64 {
		if p == nil {
			return 0
		}
		return *p
	}

	return &repository.AggregatedResource{
		ContainerID:     containerID,
		WindowStart:     from,
		WindowEnd:       to,
		AvgCPUPercent:   deref(avgCPU),
		MaxCPUPercent:   deref(maxCPU),
		AvgMemBytes:     int64(deref(avgMem)),
		MaxMemBytes:     int64(deref(maxMem)),
		TotalNetRxBytes: int64(deref(totalRx)),
		TotalNetTxBytes: int64(deref(totalTx)),
	}, nil
}

// ---------------------------------------------------------------------------
// ScoreRepository implementation
// ---------------------------------------------------------------------------

type scoreRepo struct{ db *DB }

const saveScoreSQL = `
INSERT INTO stability_scores
  (container_id, source_name, score, restart_penalty, crash_penalty,
   oom_penalty, volatility_penalty, availability_penalty, calculated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`

// Save persists a StabilityScore and populates its assigned ID.
func (r *scoreRepo) Save(ctx context.Context, s *model.StabilityScore) (*model.StabilityScore, error) {
	err := r.db.pool.QueryRow(ctx, saveScoreSQL,
		s.ContainerID, s.SourceName, s.Score, s.RestartPenalty, s.CrashPenalty,
		s.OOMPenalty, s.VolatilityPenalty, s.AvailabilityPenalty, s.CalculatedAt,
	).Scan(&s.ID)
	if err != nil {
		return nil, fmt.Errorf("timescale: score save: %w", err)
	}
	return s, nil
}

const findLatestScoreSQL = `
SELECT id, container_id, score, restart_penalty, crash_penalty,
  oom_penalty, volatility_penalty, availability_penalty, calculated_at, source_name
FROM stability_scores
WHERE container_id = $1
ORDER BY calculated_at DESC LIMIT 1`

// FindLatest returns the most recent StabilityScore for the container.
// Returns nil, nil when no score has been computed yet.
func (r *scoreRepo) FindLatest(ctx context.Context, containerID string) (*model.StabilityScore, error) {
	s, err := scanScore(r.db.pool.QueryRow(ctx, findLatestScoreSQL, containerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("timescale: score find latest: %w", err)
	}
	return s, nil
}

const findScoreHistorySQL = `
SELECT id, container_id, score, restart_penalty, crash_penalty,
  oom_penalty, volatility_penalty, availability_penalty, calculated_at, source_name
FROM stability_scores
WHERE container_id = $1
ORDER BY calculated_at DESC LIMIT $2`

// FindHistory returns the n most recent scores for the container in descending time order.
func (r *scoreRepo) FindHistory(ctx context.Context, containerID string, n int) ([]*model.StabilityScore, error) {
	rows, err := r.db.pool.Query(ctx, findScoreHistorySQL, containerID, n)
	if err != nil {
		return nil, fmt.Errorf("timescale: score find history: %w", err)
	}
	defer rows.Close()

	var scores []*model.StabilityScore
	for rows.Next() {
		s, err := scanScore(rows)
		if err != nil {
			return nil, fmt.Errorf("timescale: score find history scan: %w", err)
		}
		scores = append(scores, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("timescale: score find history rows: %w", err)
	}
	return scores, nil
}

// scanScore reads a single StabilityScore from any pgx row-like value.
func scanScore(row interface {
	Scan(dest ...any) error
}) (*model.StabilityScore, error) {
	var s model.StabilityScore
	return &s, row.Scan(
		&s.ID, &s.ContainerID, &s.Score, &s.RestartPenalty, &s.CrashPenalty,
		&s.OOMPenalty, &s.VolatilityPenalty, &s.AvailabilityPenalty, &s.CalculatedAt,
		&s.SourceName,
	)
}
