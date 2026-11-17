package timescale

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

// Notifications returns the NotificationRepository backed by this DB.
func (db *DB) Notifications() repository.NotificationRepository { return &notificationRepo{db} }

type notificationRepo struct{ db *DB }

const channelCols = `id, name, type, origin, url_enc, secret_enc, enabled, min_severity, sources, namespaces, types,
  notify_resolved, include_logs, repeat_minutes, created_at, updated_at`

func scanChannel(row interface{ Scan(...any) error }) (*model.NotificationChannel, error) {
	var c model.NotificationChannel
	if err := row.Scan(&c.ID, &c.Name, &c.Type, &c.Origin, &c.URLEnc, &c.SecretEnc, &c.Enabled, &c.MinSeverity,
		&c.Sources, &c.Namespaces, &c.Types, &c.NotifyResolved, &c.IncludeLogs, &c.RepeatMinutes,
		&c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	if c.Sources == nil {
		c.Sources = []string{}
	}
	if c.Namespaces == nil {
		c.Namespaces = []string{}
	}
	if c.Types == nil {
		c.Types = []string{}
	}
	return &c, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (r *notificationRepo) ListChannels(ctx context.Context) ([]*model.NotificationChannel, error) {
	rows, err := r.db.pool.Query(ctx, `SELECT `+channelCols+` FROM notification_channels ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("timescale: channel list: %w", err)
	}
	defer rows.Close()
	out := []*model.NotificationChannel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, fmt.Errorf("timescale: channel scan: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *notificationRepo) GetChannel(ctx context.Context, id int64) (*model.NotificationChannel, error) {
	c, err := scanChannel(r.db.pool.QueryRow(ctx, `SELECT `+channelCols+` FROM notification_channels WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, repository.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("timescale: channel get: %w", err)
	}
	return c, nil
}

func (r *notificationRepo) CreateChannel(ctx context.Context, c *model.NotificationChannel) error {
	origin := c.Origin
	if origin == "" {
		origin = model.ChannelOriginUser
	}
	err := r.db.pool.QueryRow(ctx, `INSERT INTO notification_channels
  (name, type, origin, url_enc, secret_enc, enabled, min_severity, sources, namespaces, types,
   notify_resolved, include_logs, repeat_minutes)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING id, created_at, updated_at`,
		c.Name, c.Type, origin, c.URLEnc, c.SecretEnc, c.Enabled, string(c.MinSeverity),
		nonNil(c.Sources), nonNil(c.Namespaces), nonNil(c.Types), c.NotifyResolved, c.IncludeLogs, c.RepeatMinutes,
	).Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return fmt.Errorf("timescale: channel create: %w", err)
	}
	c.Origin = origin
	return nil
}

func (r *notificationRepo) UpdateChannel(ctx context.Context, c *model.NotificationChannel) error {
	err := r.db.pool.QueryRow(ctx, `UPDATE notification_channels SET
  name = $2, type = $3, url_enc = $4, secret_enc = $5, enabled = $6, min_severity = $7,
  sources = $8, namespaces = $9, types = $10, notify_resolved = $11, include_logs = $12,
  repeat_minutes = $13, updated_at = NOW()
WHERE id = $1 RETURNING updated_at`,
		c.ID, c.Name, c.Type, c.URLEnc, c.SecretEnc, c.Enabled, string(c.MinSeverity),
		nonNil(c.Sources), nonNil(c.Namespaces), nonNil(c.Types), c.NotifyResolved, c.IncludeLogs, c.RepeatMinutes,
	).Scan(&c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return repository.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("timescale: channel update: %w", err)
	}
	return nil
}

func (r *notificationRepo) DeleteChannel(ctx context.Context, id int64) error {
	tag, err := r.db.pool.Exec(ctx, `DELETE FROM notification_channels WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("timescale: channel delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return repository.ErrNotFound
	}
	return nil
}

const deliveryCols = `id, channel_id, issue_id, event, ok, status_code, error, attempts, sent_at`

func scanDelivery(row interface{ Scan(...any) error }) (*model.ChannelDelivery, error) {
	var d model.ChannelDelivery
	if err := row.Scan(&d.ID, &d.ChannelID, &d.IssueID, &d.Event, &d.OK, &d.StatusCode, &d.Error, &d.Attempts, &d.SentAt); err != nil {
		return nil, err
	}
	return &d, nil
}

func (r *notificationRepo) RecordDelivery(ctx context.Context, d *model.ChannelDelivery) error {
	sent := d.SentAt
	if sent.IsZero() {
		sent = time.Now().UTC()
	}
	attempts := d.Attempts
	if attempts < 1 {
		attempts = 1
	}
	err := r.db.pool.QueryRow(ctx, `INSERT INTO notification_deliveries
  (channel_id, issue_id, event, ok, status_code, error, attempts, sent_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id, sent_at`,
		d.ChannelID, d.IssueID, string(d.Event), d.OK, d.StatusCode, stripNUL(d.Error), attempts, sent,
	).Scan(&d.ID, &d.SentAt)
	if err != nil {
		return fmt.Errorf("timescale: delivery record: %w", err)
	}
	return nil
}

func (r *notificationRepo) ListDeliveries(ctx context.Context, channelID int64, limit int) ([]*model.ChannelDelivery, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.pool.Query(ctx, `SELECT `+deliveryCols+` FROM notification_deliveries
WHERE ($1::bigint = 0 OR channel_id = $1) ORDER BY sent_at DESC, id DESC LIMIT $2`, channelID, limit)
	if err != nil {
		return nil, fmt.Errorf("timescale: delivery list: %w", err)
	}
	defer rows.Close()
	out := []*model.ChannelDelivery{}
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, fmt.Errorf("timescale: delivery scan: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *notificationRepo) LastDeliveries(ctx context.Context) (map[int64]*model.ChannelDelivery, error) {
	rows, err := r.db.pool.Query(ctx, `SELECT DISTINCT ON (channel_id) `+deliveryCols+`
FROM notification_deliveries ORDER BY channel_id, sent_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("timescale: last deliveries: %w", err)
	}
	defer rows.Close()
	out := map[int64]*model.ChannelDelivery{}
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, fmt.Errorf("timescale: last deliveries scan: %w", err)
		}
		out[d.ChannelID] = d
	}
	return out, rows.Err()
}

func (r *notificationRepo) LastIssueDeliveries(ctx context.Context) ([]model.DeliveryMark, error) {
	rows, err := r.db.pool.Query(ctx, `SELECT channel_id, issue_id, MAX(sent_at) FROM notification_deliveries
WHERE issue_id IS NOT NULL AND event IN ('opened','reminder') GROUP BY channel_id, issue_id`)
	if err != nil {
		return nil, fmt.Errorf("timescale: issue deliveries: %w", err)
	}
	defer rows.Close()
	var out []model.DeliveryMark
	for rows.Next() {
		var m model.DeliveryMark
		if err := rows.Scan(&m.ChannelID, &m.IssueID, &m.At); err != nil {
			return nil, fmt.Errorf("timescale: issue deliveries scan: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *notificationRepo) DeleteDeliveriesBefore(ctx context.Context, t time.Time) (int64, error) {
	tag, err := r.db.pool.Exec(ctx, `DELETE FROM notification_deliveries WHERE sent_at < $1`, t)
	if err != nil {
		return 0, fmt.Errorf("timescale: delivery cleanup: %w", err)
	}
	return tag.RowsAffected(), nil
}
