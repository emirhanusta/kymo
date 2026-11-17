// Package notifytest provides in-memory fakes for notification tests.
package notifytest

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

// MemRepo is an in-memory repository.NotificationRepository.
type MemRepo struct {
	mu         sync.Mutex
	next       int64
	nextDel    int64
	Channels   map[int64]*model.NotificationChannel
	Deliveries []*model.ChannelDelivery
}

// NewMemRepo returns an empty repository.
func NewMemRepo() *MemRepo { return &MemRepo{Channels: map[int64]*model.NotificationChannel{}} }

var _ repository.NotificationRepository = (*MemRepo)(nil)

func clone(c *model.NotificationChannel) *model.NotificationChannel {
	d := *c
	d.URLEnc = append([]byte(nil), c.URLEnc...)
	d.SecretEnc = append([]byte(nil), c.SecretEnc...)
	if len(c.SecretEnc) == 0 {
		d.SecretEnc = nil
	}
	return &d
}

func (m *MemRepo) ListChannels(context.Context) ([]*model.NotificationChannel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*model.NotificationChannel{}
	for _, c := range m.Channels {
		out = append(out, clone(c))
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out, nil
}

func (m *MemRepo) GetChannel(_ context.Context, id int64) (*model.NotificationChannel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c := m.Channels[id]; c != nil {
		return clone(c), nil
	}
	return nil, repository.ErrNotFound
}

func (m *MemRepo) CreateChannel(_ context.Context, c *model.NotificationChannel) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	c.ID = m.next
	if c.Origin == "" {
		c.Origin = model.ChannelOriginUser
	}
	c.CreatedAt, c.UpdatedAt = time.Now().UTC(), time.Now().UTC()
	m.Channels[c.ID] = clone(c)
	return nil
}

func (m *MemRepo) UpdateChannel(_ context.Context, c *model.NotificationChannel) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.Channels[c.ID]
	if cur == nil {
		return repository.ErrNotFound
	}
	c.Origin, c.CreatedAt, c.UpdatedAt = cur.Origin, cur.CreatedAt, time.Now().UTC()
	m.Channels[c.ID] = clone(c)
	return nil
}

func (m *MemRepo) DeleteChannel(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Channels[id] == nil {
		return repository.ErrNotFound
	}
	delete(m.Channels, id)
	kept := m.Deliveries[:0]
	for _, d := range m.Deliveries {
		if d.ChannelID != id {
			kept = append(kept, d)
		}
	}
	m.Deliveries = kept
	return nil
}

func (m *MemRepo) RecordDelivery(_ context.Context, d *model.ChannelDelivery) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextDel++
	d.ID = m.nextDel
	c := *d
	m.Deliveries = append(m.Deliveries, &c)
	return nil
}

// All returns a copy of the delivery log in insertion order.
func (m *MemRepo) All() []*model.ChannelDelivery {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*model.ChannelDelivery(nil), m.Deliveries...)
}

func (m *MemRepo) ListDeliveries(_ context.Context, channelID int64, limit int) ([]*model.ChannelDelivery, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*model.ChannelDelivery{}
	for i := len(m.Deliveries) - 1; i >= 0 && len(out) < limit; i-- {
		if d := m.Deliveries[i]; channelID == 0 || d.ChannelID == channelID {
			out = append(out, d)
		}
	}
	return out, nil
}

func (m *MemRepo) LastDeliveries(context.Context) (map[int64]*model.ChannelDelivery, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[int64]*model.ChannelDelivery{}
	for _, d := range m.Deliveries {
		out[d.ChannelID] = d
	}
	return out, nil
}

func (m *MemRepo) LastIssueDeliveries(context.Context) ([]model.DeliveryMark, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	type pair struct{ c, i int64 }
	best := map[pair]time.Time{}
	for _, d := range m.Deliveries {
		if d.IssueID == nil || (d.Event != model.EventOpened && d.Event != model.EventReminder) {
			continue
		}
		p := pair{d.ChannelID, *d.IssueID}
		if d.SentAt.After(best[p]) {
			best[p] = d.SentAt
		}
	}
	var out []model.DeliveryMark
	for p, at := range best {
		out = append(out, model.DeliveryMark{ChannelID: p.c, IssueID: p.i, At: at})
	}
	return out, nil
}

func (m *MemRepo) DeleteDeliveriesBefore(_ context.Context, t time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	kept := m.Deliveries[:0]
	for _, d := range m.Deliveries {
		if d.SentAt.Before(t) {
			n++
		} else {
			kept = append(kept, d)
		}
	}
	m.Deliveries = kept
	return n, nil
}
