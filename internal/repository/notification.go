package repository

import (
	"context"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
)

// NotificationRepository persists notification channels and their delivery log.
// URLs and secrets are stored exactly as given (sealing happens in the service).
type NotificationRepository interface {
	ListChannels(ctx context.Context) ([]*model.NotificationChannel, error)
	// GetChannel returns the channel or ErrNotFound.
	GetChannel(ctx context.Context, id int64) (*model.NotificationChannel, error)
	// CreateChannel inserts the channel and sets ID, CreatedAt and UpdatedAt.
	CreateChannel(ctx context.Context, c *model.NotificationChannel) error
	// UpdateChannel replaces every field except id, origin and created_at.
	// ErrNotFound when the channel does not exist.
	UpdateChannel(ctx context.Context, c *model.NotificationChannel) error
	// DeleteChannel removes the channel and (cascading) its deliveries.
	DeleteChannel(ctx context.Context, id int64) error

	RecordDelivery(ctx context.Context, d *model.ChannelDelivery) error
	// ListDeliveries returns deliveries newest first; channelID 0 means all channels.
	ListDeliveries(ctx context.Context, channelID int64, limit int) ([]*model.ChannelDelivery, error)
	// LastDeliveries returns the newest delivery of every channel that has one.
	LastDeliveries(ctx context.Context) (map[int64]*model.ChannelDelivery, error)
	// LastIssueDeliveries returns, per (channel, issue), the newest opened or
	// reminder delivery attempt (used to pace reminders).
	LastIssueDeliveries(ctx context.Context) ([]model.DeliveryMark, error)
	// DeleteDeliveriesBefore removes deliveries older than t.
	DeleteDeliveriesBefore(ctx context.Context, t time.Time) (int64, error)
}
