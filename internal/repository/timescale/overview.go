package timescale

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

// Overview returns the OverviewRepository backed by this DB.
func (db *DB) Overview() repository.OverviewRepository { return &overviewRepo{db} }

type overviewRepo struct{ db *DB }

// scopeClause appends the cluster restriction on column col to clauses/args.
func scopeClause(s repository.OverviewScope, col string, clauses []string, args []any) ([]string, []any) {
	switch {
	case s.Source != "":
		args = append(args, s.Source)
		clauses = append(clauses, fmt.Sprintf("%s = $%d", col, len(args)))
	case s.Sources != nil:
		args = append(args, s.Sources)
		clauses = append(clauses, fmt.Sprintf("%s = ANY($%d)", col, len(args)))
	}
	return clauses, args
}

func (r *overviewRepo) Activity(ctx context.Context, q repository.ActivityQuery) (*model.Activity, error) {
	hours := q.Hours
	if hours < 1 {
		hours = 24
	}
	now := q.Now
	if now.IsZero() {
		now = time.Now()
	}
	end := now.UTC().Truncate(time.Hour).Add(time.Hour)
	start := end.Add(-time.Duration(hours) * time.Hour)
	prev := start.Add(-time.Duration(hours) * time.Hour)

	args := []any{prev, end, start}
	clauses := []string{
		"e.occurred_at >= $1", "e.occurred_at < $2",
		"e.event_type IN ('start','crash','oom')",
		"c.ignored = false", "c.is_system = false", "c.workload_kind NOT IN ('Job','CronJob')",
	}
	clauses, args = scopeClause(q.OverviewScope, "e.source_name", clauses, args)

	// idx < 0 is the previous window; 0..hours-1 index the buckets.
	sql := `SELECT floor(extract(epoch FROM (e.occurred_at - $3::timestamptz)) / 3600)::int AS idx,
       e.event_type, COUNT(*)::int
FROM lifecycle_events e JOIN containers c ON c.id = e.container_id
WHERE ` + strings.Join(clauses, " AND ") + `
GROUP BY idx, e.event_type`
	rows, err := r.db.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("timescale: overview activity: %w", err)
	}
	defer rows.Close()

	out := &model.Activity{Buckets: make([]model.ActivityBucket, hours)}
	for i := range out.Buckets {
		out.Buckets[i].Start = start.Add(time.Duration(i) * time.Hour)
	}
	for rows.Next() {
		var (
			idx int
			typ string
			n   int
		)
		if err := rows.Scan(&idx, &typ, &n); err != nil {
			return nil, fmt.Errorf("timescale: overview activity scan: %w", err)
		}
		if idx < 0 {
			if typ == string(model.EventTypeCrash) || typ == string(model.EventTypeOOM) {
				out.PreviousCrashes += n
			}
			continue
		}
		if idx >= hours {
			continue
		}
		switch model.EventType(typ) {
		case model.EventTypeStart:
			out.Buckets[idx].Starts += n
		case model.EventTypeCrash:
			out.Buckets[idx].Crashes += n
		case model.EventTypeOOM:
			out.Buckets[idx].OOMs += n
		}
	}
	return out, rows.Err()
}

func (r *overviewRepo) Changes(ctx context.Context, q repository.ChangesQuery) ([]*model.Change, error) {
	limit := q.Limit
	if limit < 1 {
		limit = 6
	}
	clauses, args := scopeClause(q.OverviewScope, "c.source_name", nil, nil)
	extra := ""
	if len(clauses) > 0 {
		extra = " AND " + strings.Join(clauses, " AND ")
	}
	args = append(args, limit)

	// One row per (workload, revision) with the time the revision first appeared; LAG gives the revision it
	// replaced. Only workloads that still have active instances are listed (so every row links to a live page).
	sql := fmt.Sprintf(`
WITH revs AS (
  SELECT c.workload_id,
         MAX(c.source_name)   AS source_name,
         MAX(c.namespace)     AS namespace,
         MAX(c.workload_kind) AS kind,
         MAX(c.workload_name) AS name,
         c.revision,
         MIN(c.first_seen_at) AS at,
         (array_agg(c.image ORDER BY c.first_seen_at, c.name))[1] AS image
  FROM containers c
  WHERE c.workload_id <> '' AND c.revision <> '' AND c.ignored = false AND c.is_system = false
    AND c.workload_kind NOT IN ('Job','CronJob')%s
    AND EXISTS (SELECT 1 FROM containers a WHERE a.workload_id = c.workload_id AND a.deleted_at IS NULL AND a.ignored = false)
  GROUP BY c.workload_id, c.revision
), seq AS (
  SELECT revs.*, LAG(revision) OVER w AS prev_revision, LAG(image) OVER w AS prev_image
  FROM revs WINDOW w AS (PARTITION BY workload_id ORDER BY at, revision)
)
SELECT workload_id, kind, name, namespace, source_name, revision, prev_revision, image, COALESCE(prev_image, ''), at
FROM seq WHERE prev_revision IS NOT NULL
ORDER BY at DESC, workload_id LIMIT $%d`, extra, len(args))

	rows, err := r.db.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("timescale: overview changes: %w", err)
	}
	defer rows.Close()
	out := []*model.Change{}
	for rows.Next() {
		var c model.Change
		if err := rows.Scan(&c.WorkloadID, &c.Kind, &c.Name, &c.Namespace, &c.SourceName,
			&c.Revision, &c.PreviousRevision, &c.Image, &c.PreviousImage, &c.At); err != nil {
			return nil, fmt.Errorf("timescale: overview changes scan: %w", err)
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}
