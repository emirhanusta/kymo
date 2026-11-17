package timescale

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

type sourceRepo struct{ db *DB }

// Sources returns the SourceRepository backed by this DB.
func (db *DB) Sources() repository.SourceRepository { return &sourceRepo{db} }

const sourceCols = `name, type, enabled, read_only, kubeconfig_path, kube_context,
  kubeconfig_enc, poll_interval_seconds, created_at, updated_at`

func scanSource(row interface{ Scan(...any) error }) (*model.SourceSpec, error) {
	var (
		s    model.SourceSpec
		poll int
	)
	if err := row.Scan(&s.Name, &s.Type, &s.Enabled, &s.ReadOnly, &s.KubeconfigPath,
		&s.KubeContext, &s.KubeconfigEnc, &poll, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	s.Origin = model.SourceOriginUser
	s.PollInterval = time.Duration(poll) * time.Second
	return &s, nil
}

func (r *sourceRepo) List(ctx context.Context) ([]*model.SourceSpec, error) {
	rows, err := r.db.pool.Query(ctx, `SELECT `+sourceCols+` FROM sources ORDER BY created_at, name`)
	if err != nil {
		return nil, fmt.Errorf("timescale: source list: %w", err)
	}
	defer rows.Close()
	var out []*model.SourceSpec
	for rows.Next() {
		s, err := scanSource(rows)
		if err != nil {
			return nil, fmt.Errorf("timescale: source scan: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *sourceRepo) Get(ctx context.Context, name string) (*model.SourceSpec, error) {
	s, err := scanSource(r.db.pool.QueryRow(ctx, `SELECT `+sourceCols+` FROM sources WHERE name = $1`, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, repository.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("timescale: source get: %w", err)
	}
	return s, nil
}

func (r *sourceRepo) Create(ctx context.Context, s *model.SourceSpec) error {
	_, err := r.db.pool.Exec(ctx, `INSERT INTO sources
  (name, type, enabled, read_only, kubeconfig_path, kube_context, kubeconfig_enc, poll_interval_seconds)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		s.Name, s.Type, s.Enabled, s.ReadOnly, s.KubeconfigPath, s.KubeContext,
		s.KubeconfigEnc, int(s.PollInterval/time.Second))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return repository.ErrConflict
	}
	if err != nil {
		return fmt.Errorf("timescale: source create: %w", err)
	}
	return nil
}

func (r *sourceRepo) Update(ctx context.Context, s *model.SourceSpec) error {
	tag, err := r.db.pool.Exec(ctx, `UPDATE sources SET
  enabled = $2, read_only = $3, kubeconfig_path = $4, kube_context = $5,
  kubeconfig_enc = $6, poll_interval_seconds = $7, updated_at = NOW()
WHERE name = $1`,
		s.Name, s.Enabled, s.ReadOnly, s.KubeconfigPath, s.KubeContext,
		s.KubeconfigEnc, int(s.PollInterval/time.Second))
	if err != nil {
		return fmt.Errorf("timescale: source update: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (r *sourceRepo) Delete(ctx context.Context, name string) error {
	tag, err := r.db.pool.Exec(ctx, `DELETE FROM sources WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("timescale: source delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return repository.ErrNotFound
	}
	return nil
}
