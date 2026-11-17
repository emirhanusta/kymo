package timescale

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

// K8sEvents returns the K8sEventRepository backed by this DB.
func (db *DB) K8sEvents() repository.K8sEventRepository { return &k8sEventRepo{db} }

type k8sEventRepo struct{ db *DB }

const upsertK8sEventSQL = `
INSERT INTO k8s_events
  (source_name, uid, namespace, kind, name, workload_id, type, reason, message, count, first_seen, last_seen, component)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
ON CONFLICT (source_name, uid, first_seen) DO UPDATE SET
  count       = GREATEST(k8s_events.count, EXCLUDED.count),
  last_seen   = GREATEST(k8s_events.last_seen, EXCLUDED.last_seen),
  message     = EXCLUDED.message,
  workload_id = CASE WHEN EXCLUDED.workload_id <> '' THEN EXCLUDED.workload_id ELSE k8s_events.workload_id END`

func (r *k8sEventRepo) Upsert(ctx context.Context, e *model.K8sEvent) error {
	count := e.Count
	if count < 1 {
		count = 1
	}
	if _, err := r.db.pool.Exec(ctx, upsertK8sEventSQL,
		e.SourceName, e.UID, e.Namespace, e.Kind, e.Name, e.WorkloadID, e.Type,
		stripNUL(e.Reason), stripNUL(e.Message), count, e.FirstSeen, e.LastSeen, e.Component,
	); err != nil {
		return fmt.Errorf("timescale: k8s event upsert: %w", err)
	}
	return nil
}

// likeEscaper escapes LIKE wildcards so user input is matched literally.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func (r *k8sEventRepo) List(ctx context.Context, q repository.K8sEventQuery) ([]*model.K8sEvent, error) {
	var (
		where []string
		args  []any
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
	if q.Type != "" {
		add("type = $%d", q.Type)
	}
	if q.Reason != "" {
		add("reason = $%d", q.Reason)
	}
	if q.WorkloadID != "" {
		add("workload_id = $%d", q.WorkloadID)
	}
	if q.Query != "" {
		args = append(args, "%"+likeEscaper.Replace(q.Query)+"%")
		n := len(args)
		where = append(where, fmt.Sprintf("(reason ILIKE $%[1]d OR message ILIKE $%[1]d OR name ILIKE $%[1]d)", n))
	}
	if !q.Since.IsZero() {
		add("last_seen >= $%d", q.Since)
	}
	if !q.Before.IsZero() {
		add("last_seen < $%d", q.Before)
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	args = append(args, limit)
	sql := `SELECT id, source_name, uid, namespace, kind, name, workload_id, type, reason, message,
  count, first_seen, last_seen, component FROM k8s_events`
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	sql += fmt.Sprintf(" ORDER BY last_seen DESC, id DESC LIMIT $%d", len(args))

	rows, err := r.db.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("timescale: k8s event list: %w", err)
	}
	defer rows.Close()
	out := []*model.K8sEvent{}
	for rows.Next() {
		var e model.K8sEvent
		if err := rows.Scan(&e.ID, &e.SourceName, &e.UID, &e.Namespace, &e.Kind, &e.Name, &e.WorkloadID,
			&e.Type, &e.Reason, &e.Message, &e.Count, &e.FirstSeen, &e.LastSeen, &e.Component); err != nil {
			return nil, fmt.Errorf("timescale: k8s event scan: %w", err)
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

func (r *k8sEventRepo) Reasons(ctx context.Context, source string, since time.Time) ([]model.K8sEventReason, error) {
	rows, err := r.db.pool.Query(ctx, `
SELECT reason, type, COUNT(*) AS n FROM k8s_events
WHERE ($1 = '' OR source_name = $1) AND last_seen >= $2
GROUP BY reason, type ORDER BY n DESC, reason, type`, source, since)
	if err != nil {
		return nil, fmt.Errorf("timescale: k8s event reasons: %w", err)
	}
	defer rows.Close()
	out := []model.K8sEventReason{}
	for rows.Next() {
		var x model.K8sEventReason
		if err := rows.Scan(&x.Reason, &x.Type, &x.Count); err != nil {
			return nil, fmt.Errorf("timescale: k8s event reasons scan: %w", err)
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (r *k8sEventRepo) DeleteBySource(ctx context.Context, sourceName string) (int64, error) {
	tag, err := r.db.pool.Exec(ctx, `DELETE FROM k8s_events WHERE source_name = $1`, sourceName)
	if err != nil {
		return 0, fmt.Errorf("timescale: k8s event delete by source: %w", err)
	}
	return tag.RowsAffected(), nil
}
