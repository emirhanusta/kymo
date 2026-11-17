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

// Issues returns the IssueRepository backed by this DB.
func (db *DB) Issues() repository.IssueRepository { return &issueRepo{db} }

type issueRepo struct{ db *DB }

const issueCols = `id, source_name, workload_id, namespace, kind, name, type, severity, title, detail, status,
  first_seen, last_seen, resolved_at, last_notified_at, notify_count, flaps`

func scanIssue(row interface{ Scan(...any) error }) (*model.Issue, error) {
	var i model.Issue
	if err := row.Scan(&i.ID, &i.SourceName, &i.WorkloadID, &i.Namespace, &i.Kind, &i.Name, &i.Type, &i.Severity,
		&i.Title, &i.Detail, &i.Status, &i.FirstSeen, &i.LastSeen, &i.ResolvedAt, &i.LastNotifiedAt,
		&i.NotifyCount, &i.Flaps); err != nil {
		return nil, err
	}
	return &i, nil
}

func scanIssues(rows pgx.Rows) ([]*model.Issue, error) {
	defer rows.Close()
	out := []*model.Issue{}
	for rows.Next() {
		i, err := scanIssue(rows)
		if err != nil {
			return nil, fmt.Errorf("timescale: issue scan: %w", err)
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func (r *issueRepo) Create(ctx context.Context, i *model.Issue) error {
	err := r.db.pool.QueryRow(ctx, `INSERT INTO issues
  (source_name, workload_id, namespace, kind, name, type, severity, title, detail, status, first_seen, last_seen)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'open',$10,$11) RETURNING id`,
		i.SourceName, i.WorkloadID, i.Namespace, i.Kind, i.Name, string(i.Type), string(i.Severity),
		stripNUL(i.Title), stripNUL(i.Detail), i.FirstSeen, i.LastSeen).Scan(&i.ID)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return repository.ErrConflict
	}
	if err != nil {
		return fmt.Errorf("timescale: issue create: %w", err)
	}
	i.Status = model.IssueOpen
	i.ResolvedAt = nil
	return nil
}

func (r *issueRepo) UpdateOpen(ctx context.Context, i *model.Issue) error {
	tag, err := r.db.pool.Exec(ctx, `UPDATE issues SET severity = $2, title = $3, detail = $4, last_seen = GREATEST(last_seen, $5)
WHERE id = $1 AND status = 'open'`, i.ID, string(i.Severity), stripNUL(i.Title), stripNUL(i.Detail), i.LastSeen)
	if err != nil {
		return fmt.Errorf("timescale: issue update: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (r *issueRepo) Resolve(ctx context.Context, id int64, at time.Time) error {
	tag, err := r.db.pool.Exec(ctx, `UPDATE issues SET status = 'resolved', resolved_at = $2
WHERE id = $1 AND status = 'open'`, id, at)
	if err != nil {
		return fmt.Errorf("timescale: issue resolve: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (r *issueRepo) Reopen(ctx context.Context, i *model.Issue) error {
	tag, err := r.db.pool.Exec(ctx, `UPDATE issues SET status = 'open', resolved_at = NULL, flaps = flaps + 1,
  severity = $2, title = $3, detail = $4, last_seen = $5
WHERE id = $1 AND status = 'resolved'`, i.ID, string(i.Severity), stripNUL(i.Title), stripNUL(i.Detail), i.LastSeen)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return repository.ErrConflict
	}
	if err != nil {
		return fmt.Errorf("timescale: issue reopen: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return repository.ErrNotFound
	}
	i.Status = model.IssueOpen
	i.ResolvedAt = nil
	i.Flaps++
	return nil
}

func (r *issueRepo) ListActive(ctx context.Context, resolvedSince time.Time) ([]*model.Issue, error) {
	rows, err := r.db.pool.Query(ctx, `SELECT `+issueCols+` FROM issues
WHERE status = 'open' OR (status = 'resolved' AND resolved_at >= $1)
ORDER BY status DESC, resolved_at DESC NULLS LAST, id DESC`, resolvedSince)
	if err != nil {
		return nil, fmt.Errorf("timescale: issue list active: %w", err)
	}
	return scanIssues(rows)
}

func (r *issueRepo) List(ctx context.Context, q repository.IssueQuery) ([]*model.Issue, error) {
	var (
		where []string
		args  []any
	)
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	status := q.Status
	if status == "" {
		status = "open"
	}
	if status != "all" {
		add("status = $%d", status)
	}
	if q.Source != "" {
		add("source_name = $%d", q.Source)
	}
	if q.Severity != "" {
		add("severity = $%d", q.Severity)
	}
	if q.Type != "" {
		add("type = $%d", q.Type)
	}
	if q.WorkloadID != "" {
		add("workload_id = $%d", q.WorkloadID)
	}
	var order string
	switch status {
	case "resolved":
		order = "resolved_at DESC, id DESC"
		if !q.Before.IsZero() {
			add("resolved_at < $%d", q.Before)
		}
	case "all":
		order = "first_seen DESC, id DESC"
		if !q.Before.IsZero() {
			add("first_seen < $%d", q.Before)
		}
	default:
		order = "CASE severity WHEN 'critical' THEN 0 ELSE 1 END, first_seen DESC, id DESC"
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 200
	}
	args = append(args, limit)
	sql := `SELECT ` + issueCols + ` FROM issues`
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	sql += fmt.Sprintf(" ORDER BY %s LIMIT $%d", order, len(args))
	rows, err := r.db.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("timescale: issue list: %w", err)
	}
	return scanIssues(rows)
}

func (r *issueRepo) Get(ctx context.Context, id int64) (*model.Issue, error) {
	i, err := scanIssue(r.db.pool.QueryRow(ctx, `SELECT `+issueCols+` FROM issues WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, repository.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("timescale: issue get: %w", err)
	}
	return i, nil
}

func (r *issueRepo) Summary(ctx context.Context) (*model.IssueSummary, error) {
	rows, err := r.db.pool.Query(ctx, `SELECT source_name, severity, COUNT(*) FROM issues
WHERE status = 'open' GROUP BY source_name, severity`)
	if err != nil {
		return nil, fmt.Errorf("timescale: issue summary: %w", err)
	}
	defer rows.Close()
	s := &model.IssueSummary{BySource: map[string]int{}}
	for rows.Next() {
		var (
			src, sev string
			n        int
		)
		if err := rows.Scan(&src, &sev, &n); err != nil {
			return nil, fmt.Errorf("timescale: issue summary scan: %w", err)
		}
		s.Open += n
		s.BySource[src] += n
		if sev == string(model.SeverityCritical) {
			s.Critical += n
		} else {
			s.Warning += n
		}
	}
	return s, rows.Err()
}

func (r *issueRepo) MarkNotified(ctx context.Context, id int64, at time.Time) error {
	if _, err := r.db.pool.Exec(ctx, `UPDATE issues SET last_notified_at = $2, notify_count = notify_count + 1
WHERE id = $1`, id, at); err != nil {
		return fmt.Errorf("timescale: issue mark notified: %w", err)
	}
	return nil
}

func (r *issueRepo) DeleteResolvedBefore(ctx context.Context, t time.Time) (int64, error) {
	tag, err := r.db.pool.Exec(ctx, `DELETE FROM issues WHERE status = 'resolved' AND resolved_at < $1`, t)
	if err != nil {
		return 0, fmt.Errorf("timescale: issue cleanup: %w", err)
	}
	return tag.RowsAffected(), nil
}
