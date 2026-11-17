package timescale

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

// errorBuckets is the number of buckets returned per signature.
const errorBuckets = 24

// ErrorLogs returns the ErrorLogRepository backed by this DB.
func (db *DB) ErrorLogs() repository.ErrorLogRepository { return &errorLogRepo{db} }

type errorLogRepo struct{ db *DB }

// ---------------------------------------------------------------------------
// Targets
// ---------------------------------------------------------------------------

const targetCols = `id, source_name, namespace, workload_id, origin, created_at`

func scanTarget(row interface{ Scan(...any) error }) (*model.LogCaptureTarget, error) {
	var t model.LogCaptureTarget
	if err := row.Scan(&t.ID, &t.SourceName, &t.Namespace, &t.WorkloadID, &t.Origin, &t.CreatedAt); err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *errorLogRepo) ListTargets(ctx context.Context) ([]*model.LogCaptureTarget, error) {
	rows, err := r.db.pool.Query(ctx, `SELECT `+targetCols+` FROM log_capture_targets ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("timescale: log capture targets list: %w", err)
	}
	defer rows.Close()
	out := []*model.LogCaptureTarget{}
	for rows.Next() {
		t, err := scanTarget(rows)
		if err != nil {
			return nil, fmt.Errorf("timescale: log capture target scan: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *errorLogRepo) CreateTarget(ctx context.Context, t *model.LogCaptureTarget) error {
	origin := t.Origin
	if origin == "" {
		origin = model.TargetOriginUI
	}
	err := r.db.pool.QueryRow(ctx, `INSERT INTO log_capture_targets (source_name, namespace, workload_id, origin)
VALUES ($1,$2,$3,$4) RETURNING id, origin, created_at`,
		t.SourceName, t.Namespace, t.WorkloadID, origin).Scan(&t.ID, &t.Origin, &t.CreatedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return repository.ErrConflict
	}
	if err != nil {
		return fmt.Errorf("timescale: log capture target create: %w", err)
	}
	return nil
}

func (r *errorLogRepo) GetTarget(ctx context.Context, id int64) (*model.LogCaptureTarget, error) {
	t, err := scanTarget(r.db.pool.QueryRow(ctx, `SELECT `+targetCols+` FROM log_capture_targets WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, repository.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("timescale: log capture target get: %w", err)
	}
	return t, nil
}

func (r *errorLogRepo) DeleteTarget(ctx context.Context, id int64) error {
	tag, err := r.db.pool.Exec(ctx, `DELETE FROM log_capture_targets WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("timescale: log capture target delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (r *errorLogRepo) SyncConfigTargets(ctx context.Context, declared []*model.LogCaptureTarget) error {
	tx, err := r.db.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("timescale: log capture sync begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	type key struct{ src, ns, wl string }
	want := map[key]bool{}
	for _, d := range declared {
		k := key{d.SourceName, d.Namespace, d.WorkloadID}
		want[k] = true
		// A UI target of the same scope is promoted to the (locked) config one.
		if _, err := tx.Exec(ctx, `INSERT INTO log_capture_targets (source_name, namespace, workload_id, origin)
VALUES ($1,$2,$3,'config')
ON CONFLICT (source_name, namespace, workload_id) DO UPDATE SET origin = 'config'`,
			d.SourceName, d.Namespace, d.WorkloadID); err != nil {
			return fmt.Errorf("timescale: log capture sync upsert: %w", err)
		}
	}
	rows, err := tx.Query(ctx, `SELECT id, source_name, namespace, workload_id FROM log_capture_targets WHERE origin = 'config'`)
	if err != nil {
		return fmt.Errorf("timescale: log capture sync list: %w", err)
	}
	var stale []int64
	for rows.Next() {
		var (
			id int64
			k  key
		)
		if err := rows.Scan(&id, &k.src, &k.ns, &k.wl); err != nil {
			rows.Close()
			return fmt.Errorf("timescale: log capture sync scan: %w", err)
		}
		if !want[k] {
			stale = append(stale, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("timescale: log capture sync rows: %w", err)
	}
	if len(stale) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM log_capture_targets WHERE id = ANY($1)`, stale); err != nil {
			return fmt.Errorf("timescale: log capture sync delete: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// ---------------------------------------------------------------------------
// Ingest
// ---------------------------------------------------------------------------

const upsertSignatureSQL = `
INSERT INTO error_signatures
  (id, source_name, workload_id, namespace, kind, name, level, template, sample, total_count, first_seen, last_seen)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
ON CONFLICT (id) DO UPDATE SET
  total_count = error_signatures.total_count + EXCLUDED.total_count,
  first_seen  = LEAST(error_signatures.first_seen, EXCLUDED.first_seen),
  sample      = CASE WHEN EXCLUDED.last_seen >= error_signatures.last_seen THEN EXCLUDED.sample ELSE error_signatures.sample END,
  last_seen   = GREATEST(error_signatures.last_seen, EXCLUDED.last_seen),
  level       = CASE WHEN EXCLUDED.level = 'fatal' OR error_signatures.level = 'fatal' THEN 'fatal' ELSE 'error' END`

const upsertCountSQL = `
INSERT INTO error_counts (signature_id, bucket, count, pods) VALUES ($1,$2,$3,$4)
ON CONFLICT (signature_id, bucket) DO UPDATE SET
  count = error_counts.count + EXCLUDED.count,
  pods  = ARRAY(SELECT DISTINCT p FROM unnest(error_counts.pods || EXCLUDED.pods) AS p)`

const insertOccurrenceSQL = `
INSERT INTO error_occurrences (signature_id, time, container_id, pod, container, level, message)
VALUES ($1,$2,$3,$4,$5,$6,$7)`

// trimOccurrencesSQL keeps the newest $2 occurrences of a signature.
const trimOccurrencesSQL = `
DELETE FROM error_occurrences
WHERE signature_id = $1
  AND time < (SELECT time FROM error_occurrences WHERE signature_id = $1
              ORDER BY time DESC OFFSET $2 - 1 LIMIT 1)`

func (r *errorLogRepo) SaveBatch(ctx context.Context, b *model.ErrorBatch, keep int) error {
	if b == nil || len(b.Signatures) == 0 {
		return nil
	}
	if keep < 1 {
		keep = 50
	}
	batch := &pgx.Batch{}
	for _, s := range b.Signatures {
		first, last := s.FirstSeen, s.LastSeen
		if first.IsZero() {
			first = last
		}
		if last.IsZero() {
			last = first
		}
		level := s.Level
		if level != model.ErrorLevelFatal {
			level = model.ErrorLevelError
		}
		batch.Queue(upsertSignatureSQL, s.ID, s.SourceName, s.WorkloadID, s.Namespace, s.Kind, s.Name, level,
			stripNUL(s.Template), stripNUL(s.Sample), int64(s.Total), first, last)
		for _, bk := range s.Buckets {
			pods := bk.Pods
			if pods == nil {
				pods = []string{}
			}
			batch.Queue(upsertCountSQL, s.ID, bk.Bucket.Truncate(time.Minute), bk.Count, pods)
		}
		for _, o := range s.Occurrences {
			lv := o.Level
			if lv != model.ErrorLevelFatal {
				lv = model.ErrorLevelError
			}
			batch.Queue(insertOccurrenceSQL, s.ID, o.Time, o.ContainerID, o.Pod, o.Container, lv, stripNUL(o.Message))
		}
		if len(s.Occurrences) > 0 {
			batch.Queue(trimOccurrencesSQL, s.ID, keep)
		}
	}

	tx, err := r.db.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("timescale: error batch begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	br := tx.SendBatch(ctx, batch)
	for i := 0; i < batch.Len(); i++ {
		if _, err := br.Exec(); err != nil {
			_ = br.Close()
			return fmt.Errorf("timescale: error batch statement %d: %w", i+1, err)
		}
	}
	if err := br.Close(); err != nil {
		return fmt.Errorf("timescale: error batch close: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("timescale: error batch commit: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Queries
// ---------------------------------------------------------------------------

const signatureCols = `id, source_name, workload_id, namespace, kind, name, level, template, sample, total_count, first_seen, last_seen`

func scanSignature(row interface{ Scan(...any) error }) (*model.ErrorSignature, error) {
	var s model.ErrorSignature
	if err := row.Scan(&s.ID, &s.SourceName, &s.WorkloadID, &s.Namespace, &s.Kind, &s.Name, &s.Level,
		&s.Template, &s.Sample, &s.TotalCount, &s.FirstSeen, &s.LastSeen); err != nil {
		return nil, err
	}
	s.Buckets = make([]int64, errorBuckets)
	return &s, nil
}

func (r *errorLogRepo) ListSignatures(ctx context.Context, q repository.ErrorQuery) ([]*model.ErrorSignature, error) {
	until := q.Until
	if until.IsZero() {
		until = time.Now().UTC()
	}
	since := q.Since
	if since.IsZero() {
		since = until.Add(-24 * time.Hour)
	}
	var (
		where = []string{"last_seen >= $1"}
		args  = []any{since}
	)
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if q.Source != "" {
		add("source_name = $%d", q.Source)
	}
	if q.Namespace != "" {
		add("namespace = $%d", q.Namespace)
	}
	if q.WorkloadID != "" {
		add("workload_id = $%d", q.WorkloadID)
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	args = append(args, limit)
	rows, err := r.db.pool.Query(ctx, `SELECT `+signatureCols+` FROM error_signatures WHERE `+
		strings.Join(where, " AND ")+fmt.Sprintf(" ORDER BY last_seen DESC, id LIMIT $%d", len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("timescale: error signatures list: %w", err)
	}
	defer rows.Close()
	out := []*model.ErrorSignature{}
	for rows.Next() {
		s, err := scanSignature(rows)
		if err != nil {
			return nil, fmt.Errorf("timescale: error signature scan: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if err := r.enrich(ctx, out, since, until); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *errorLogRepo) GetSignature(ctx context.Context, id string, since, until time.Time) (*model.ErrorSignature, error) {
	if until.IsZero() {
		until = time.Now().UTC()
	}
	if since.IsZero() {
		since = until.Add(-24 * time.Hour)
	}
	s, err := scanSignature(r.db.pool.QueryRow(ctx, `SELECT `+signatureCols+` FROM error_signatures WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, repository.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("timescale: error signature get: %w", err)
	}
	if err := r.enrich(ctx, []*model.ErrorSignature{s}, since, until); err != nil {
		return nil, err
	}
	return s, nil
}

// enrich fills count, pods, is_new and the 24 buckets of the window.
func (r *errorLogRepo) enrich(ctx context.Context, sigs []*model.ErrorSignature, since, until time.Time) error {
	if len(sigs) == 0 {
		return nil
	}
	ids := make([]string, len(sigs))
	byID := make(map[string]*model.ErrorSignature, len(sigs))
	for i, s := range sigs {
		ids[i] = s.ID
		byID[s.ID] = s
		s.IsNew = !s.FirstSeen.Before(since)
	}
	width := until.Sub(since).Seconds() / errorBuckets
	if width <= 0 {
		width = 1
	}
	from := since.Truncate(time.Minute)

	rows, err := r.db.pool.Query(ctx, `
SELECT signature_id,
       LEAST($4::int - 1, GREATEST(0, FLOOR(EXTRACT(EPOCH FROM (bucket - $2::timestamptz)) / $3::float8)))::int AS idx,
       SUM(count)::bigint
FROM error_counts
WHERE signature_id = ANY($1) AND bucket >= $5 AND bucket <= $6
GROUP BY 1, 2`, ids, since, width, errorBuckets, from, until)
	if err != nil {
		return fmt.Errorf("timescale: error buckets: %w", err)
	}
	for rows.Next() {
		var (
			id  string
			idx int
			n   int64
		)
		if err := rows.Scan(&id, &idx, &n); err != nil {
			rows.Close()
			return fmt.Errorf("timescale: error buckets scan: %w", err)
		}
		if s := byID[id]; s != nil && idx >= 0 && idx < errorBuckets {
			s.Buckets[idx] += n
			s.Count += n
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	prow, err := r.db.pool.Query(ctx, `
SELECT signature_id, COUNT(DISTINCT p)::int
FROM error_counts, unnest(pods) AS p
WHERE signature_id = ANY($1) AND bucket >= $2 AND bucket <= $3
GROUP BY 1`, ids, from, until)
	if err != nil {
		return fmt.Errorf("timescale: error pods: %w", err)
	}
	defer prow.Close()
	for prow.Next() {
		var (
			id string
			n  int
		)
		if err := prow.Scan(&id, &n); err != nil {
			return fmt.Errorf("timescale: error pods scan: %w", err)
		}
		if s := byID[id]; s != nil {
			s.Pods = n
		}
	}
	return prow.Err()
}

func (r *errorLogRepo) ListOccurrences(ctx context.Context, signatureID string, before time.Time, limit int) ([]*model.ErrorOccurrence, error) {
	if limit <= 0 {
		limit = 50
	}
	args := []any{signatureID}
	cond := ""
	if !before.IsZero() {
		args = append(args, before)
		cond = " AND time < $2"
	}
	args = append(args, limit)
	rows, err := r.db.pool.Query(ctx, `SELECT time, container_id, pod, container, level, message
FROM error_occurrences WHERE signature_id = $1`+cond+
		fmt.Sprintf(" ORDER BY time DESC, id DESC LIMIT $%d", len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("timescale: error occurrences: %w", err)
	}
	defer rows.Close()
	out := []*model.ErrorOccurrence{}
	for rows.Next() {
		var o model.ErrorOccurrence
		if err := rows.Scan(&o.Time, &o.ContainerID, &o.Pod, &o.Container, &o.Level, &o.Message); err != nil {
			return nil, fmt.Errorf("timescale: error occurrence scan: %w", err)
		}
		out = append(out, &o)
	}
	return out, rows.Err()
}

func (r *errorLogRepo) WorkloadErrorRates(ctx context.Context, since time.Time) ([]model.WorkloadErrorRate, error) {
	rows, err := r.db.pool.Query(ctx, `
SELECT s.workload_id, s.source_name, s.namespace, s.kind, s.name, s.template, SUM(c.count)::bigint
FROM error_counts c JOIN error_signatures s ON s.id = c.signature_id
WHERE c.bucket >= $1
GROUP BY s.workload_id, s.source_name, s.namespace, s.kind, s.name, s.id, s.template`, since)
	if err != nil {
		return nil, fmt.Errorf("timescale: workload error rates: %w", err)
	}
	defer rows.Close()
	byWL := map[string]*model.WorkloadErrorRate{}
	var order []string
	for rows.Next() {
		var (
			x    model.WorkloadErrorRate
			tmpl string
			n    int64
		)
		if err := rows.Scan(&x.WorkloadID, &x.SourceName, &x.Namespace, &x.Kind, &x.Name, &tmpl, &n); err != nil {
			return nil, fmt.Errorf("timescale: workload error rates scan: %w", err)
		}
		cur := byWL[x.WorkloadID]
		if cur == nil {
			cur = &x
			byWL[x.WorkloadID] = cur
			order = append(order, x.WorkloadID)
		}
		cur.Count += n
		if n > cur.TopCount {
			cur.TopCount, cur.TopTemplate = n, tmpl
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]model.WorkloadErrorRate, 0, len(order))
	for _, id := range order {
		out = append(out, *byWL[id])
	}
	return out, nil
}

func (r *errorLogRepo) DeleteStale(ctx context.Context, before time.Time) (int64, error) {
	tag, err := r.db.pool.Exec(ctx, `DELETE FROM error_signatures WHERE last_seen < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("timescale: error signatures delete stale: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (r *errorLogRepo) SetRetention(ctx context.Context, d time.Duration) error {
	if d < time.Hour {
		return fmt.Errorf("timescale: retention %s is shorter than 1h", d)
	}
	interval := fmt.Sprintf("%d seconds", int64(d.Seconds()))
	for _, table := range []string{"error_counts", "error_occurrences"} {
		if _, err := r.db.pool.Exec(ctx, `SELECT remove_retention_policy($1::regclass, if_exists => true)`, table); err != nil {
			return fmt.Errorf("timescale: remove retention %s: %w", table, err)
		}
		if _, err := r.db.pool.Exec(ctx, `SELECT add_retention_policy($1::regclass, $2::interval, if_not_exists => true)`, table, interval); err != nil {
			return fmt.Errorf("timescale: add retention %s: %w", table, err)
		}
	}
	return nil
}
