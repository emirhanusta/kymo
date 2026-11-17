package notify

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/emirhanusta/kymo/internal/config"
	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
	"github.com/emirhanusta/kymo/internal/secret"
)

// Errors returned by the channel service. Handlers map them to HTTP statuses.
var (
	// ErrChannelNotFound is returned for unknown channel ids.
	ErrChannelNotFound = errors.New("notification channel not found")
	// ErrChannelLocked is returned when changing a config.yaml channel.
	ErrChannelLocked = errors.New("channel is defined in config.yaml and cannot be changed from the API")
)

// ValidationError describes invalid channel input (HTTP 400).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(format string, args ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

// ChannelInput is the create / update payload. On update, omitted (nil) fields
// keep their current value; on create they take the defaults (enabled, min
// severity critical, resolved notices and logs on, no reminders).
type ChannelInput struct {
	Name           *string   `json:"name"`
	Type           *string   `json:"type"`
	URL            *string   `json:"url"`
	Secret         *string   `json:"secret"`
	Enabled        *bool     `json:"enabled"`
	MinSeverity    *string   `json:"min_severity"`
	Sources        *[]string `json:"sources"`
	Namespaces     *[]string `json:"namespaces"`
	Types          *[]string `json:"types"`
	NotifyResolved *bool     `json:"notify_resolved"`
	IncludeLogs    *bool     `json:"include_logs"`
	RepeatMinutes  *int      `json:"repeat_minutes"`
}

// LastDelivery summarises a channel's newest delivery.
type LastDelivery struct {
	SentAt time.Time            `json:"sent_at"`
	OK     bool                 `json:"ok"`
	Error  string               `json:"error"`
	Event  model.IssueEventKind `json:"event"`
}

// ChannelView is the API representation of a channel. The URL and secret are
// never returned; URLHint shows just enough to recognise the destination.
type ChannelView struct {
	ID             int64         `json:"id"`
	Name           string        `json:"name"`
	Type           string        `json:"type"`
	Origin         string        `json:"origin"`
	Enabled        bool          `json:"enabled"`
	MinSeverity    string        `json:"min_severity"`
	Sources        []string      `json:"sources"`
	Namespaces     []string      `json:"namespaces"`
	Types          []string      `json:"types"`
	NotifyResolved bool          `json:"notify_resolved"`
	IncludeLogs    bool          `json:"include_logs"`
	RepeatMinutes  int           `json:"repeat_minutes"`
	URLHint        string        `json:"url_hint"`
	HasSecret      bool          `json:"has_secret"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
	LastDelivery   *LastDelivery `json:"last_delivery"`
}

const (
	maxRepeatMinutes = 7 * 24 * 60
	maxListEntries   = 100
)

// plain returns the channel's destination URL and secret in clear text.
func (s *Service) plain(c *model.NotificationChannel) (url, sec string, err error) {
	if c.Origin == model.ChannelOriginConfig {
		return string(c.URLEnc), string(c.SecretEnc), nil
	}
	if !s.box.Configured() {
		return "", "", secret.ErrNoKey
	}
	u, err := s.box.Open(c.URLEnc)
	if err != nil {
		return "", "", err
	}
	if len(c.SecretEnc) > 0 {
		b, err := s.box.Open(c.SecretEnc)
		if err != nil {
			return "", "", err
		}
		sec = string(b)
	}
	return string(u), sec, nil
}

// URLHint renders "host/…/last4" for a destination URL.
func URLHint(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	p := strings.Trim(u.Path, "/")
	if len(p) <= 8 {
		return u.Host + "/…"
	}
	return u.Host + "/…/" + p[len(p)-4:]
}

func (s *Service) view(c *model.NotificationChannel, last map[int64]*model.ChannelDelivery) ChannelView {
	v := ChannelView{
		ID: c.ID, Name: c.Name, Type: c.Type, Origin: c.Origin, Enabled: c.Enabled,
		MinSeverity: string(c.MinSeverity), Sources: nonNil(c.Sources), Namespaces: nonNil(c.Namespaces),
		Types: nonNil(c.Types), NotifyResolved: c.NotifyResolved, IncludeLogs: c.IncludeLogs,
		RepeatMinutes: c.RepeatMinutes, HasSecret: len(c.SecretEnc) > 0,
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
	if u, _, err := s.plain(c); err == nil {
		v.URLHint = URLHint(u)
	}
	if d := last[c.ID]; d != nil {
		v.LastDelivery = &LastDelivery{SentAt: d.SentAt, OK: d.OK, Error: d.Error, Event: d.Event}
	}
	return v
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ListChannels returns every channel with its newest delivery.
func (s *Service) ListChannels(ctx context.Context) ([]ChannelView, error) {
	list, err := s.repo.ListChannels(ctx)
	if err != nil {
		return nil, err
	}
	last, err := s.repo.LastDeliveries(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ChannelView, 0, len(list))
	for _, c := range list {
		out = append(out, s.view(c, last))
	}
	return out, nil
}

func cleanList(name string, in []string, check func(string) error) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || slices.Contains(out, v) {
			continue
		}
		if check != nil {
			if err := check(v); err != nil {
				return nil, invalid("%s: %v", name, err)
			}
		}
		out = append(out, v)
	}
	if len(out) > maxListEntries {
		return nil, invalid("%s: at most %d entries", name, maxListEntries)
	}
	return out, nil
}

// apply merges in over c (defaults for a new channel are set by the caller) and
// validates the result. newURL / newSecret report whether they were supplied.
func (s *Service) apply(c *model.NotificationChannel, in ChannelInput, create bool) (newURL, newSecret *string, err error) {
	if in.Name != nil {
		c.Name = strings.TrimSpace(*in.Name)
	}
	if c.Name == "" {
		return nil, nil, invalid("name is required")
	}
	if len(c.Name) > 100 {
		return nil, nil, invalid("name must be at most 100 characters")
	}
	if in.Type != nil {
		c.Type = *in.Type
	}
	if err := model.ValidateChannelType(c.Type); err != nil {
		return nil, nil, invalid("%v", err)
	}
	if in.Enabled != nil {
		c.Enabled = *in.Enabled
	}
	if in.MinSeverity != nil {
		switch sev := model.IssueSeverity(*in.MinSeverity); sev {
		case model.SeverityCritical, model.SeverityWarning:
			c.MinSeverity = sev
		default:
			return nil, nil, invalid("min_severity must be critical or warning")
		}
	}
	if in.Sources != nil {
		if c.Sources, err = cleanList("sources", *in.Sources, nil); err != nil {
			return nil, nil, err
		}
	}
	if in.Namespaces != nil {
		if c.Namespaces, err = cleanList("namespaces", *in.Namespaces, nil); err != nil {
			return nil, nil, err
		}
	}
	if in.Types != nil {
		check := func(t string) error {
			if !model.ValidIssueType(t) {
				return fmt.Errorf("unknown issue type %q", t)
			}
			return nil
		}
		if c.Types, err = cleanList("types", *in.Types, check); err != nil {
			return nil, nil, err
		}
	}
	if in.NotifyResolved != nil {
		c.NotifyResolved = *in.NotifyResolved
	}
	if in.IncludeLogs != nil {
		c.IncludeLogs = *in.IncludeLogs
	}
	if in.RepeatMinutes != nil {
		if *in.RepeatMinutes < 0 || *in.RepeatMinutes > maxRepeatMinutes {
			return nil, nil, invalid("repeat_minutes must be between 0 and %d", maxRepeatMinutes)
		}
		c.RepeatMinutes = *in.RepeatMinutes
	}
	if in.URL != nil && strings.TrimSpace(*in.URL) != "" {
		u := strings.TrimSpace(*in.URL)
		newURL = &u
	}
	if create && newURL == nil {
		return nil, nil, invalid("url is required")
	}
	in2 := in.Secret
	if in2 != nil {
		sec := strings.TrimSpace(*in2)
		newSecret = &sec
	}
	return newURL, newSecret, nil
}

// Create validates, seals and stores a new user channel.
func (s *Service) Create(ctx context.Context, in ChannelInput) (*ChannelView, error) {
	c := &model.NotificationChannel{
		Origin: model.ChannelOriginUser, Enabled: true, MinSeverity: model.SeverityCritical,
		NotifyResolved: true, IncludeLogs: true,
	}
	newURL, newSecret, err := s.apply(c, in, true)
	if err != nil {
		return nil, err
	}
	if err := model.ValidateChannelURL(c.Type, *newURL); err != nil {
		return nil, invalid("%v", err)
	}
	if !s.box.Configured() {
		return nil, invalid("KYMO_SECRET_KEY is not set: notification URLs are stored encrypted, so the server needs a secret key (set the KYMO_SECRET_KEY environment variable and restart), or declare the channel in config.yaml")
	}
	if c.URLEnc, err = s.box.Seal([]byte(*newURL)); err != nil {
		return nil, err
	}
	if newSecret != nil && *newSecret != "" {
		if c.SecretEnc, err = s.box.Seal([]byte(*newSecret)); err != nil {
			return nil, err
		}
	}
	if err := s.repo.CreateChannel(ctx, c); err != nil {
		return nil, err
	}
	v := s.view(c, nil)
	return &v, nil
}

func (s *Service) get(ctx context.Context, id int64) (*model.NotificationChannel, error) {
	c, err := s.repo.GetChannel(ctx, id)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, ErrChannelNotFound
	}
	return c, err
}

// Update changes a user channel; omitted fields (including url and secret) are kept.
func (s *Service) Update(ctx context.Context, id int64, in ChannelInput) (*ChannelView, error) {
	c, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.Origin == model.ChannelOriginConfig {
		return nil, ErrChannelLocked
	}
	newURL, newSecret, err := s.apply(c, in, false)
	if err != nil {
		return nil, err
	}
	curURL := ""
	if newURL == nil {
		// Validate the stored URL against a possibly changed type.
		if curURL, _, err = s.plain(c); err != nil {
			return nil, invalid("the stored URL cannot be read (%v): enter the URL again", err)
		}
	} else {
		curURL = *newURL
	}
	if err := model.ValidateChannelURL(c.Type, curURL); err != nil {
		return nil, invalid("%v", err)
	}
	if !s.box.Configured() {
		return nil, invalid("KYMO_SECRET_KEY is not set; notification channels cannot be modified")
	}
	if newURL != nil {
		if c.URLEnc, err = s.box.Seal([]byte(*newURL)); err != nil {
			return nil, err
		}
	}
	if newSecret != nil {
		c.SecretEnc = nil
		if *newSecret != "" {
			if c.SecretEnc, err = s.box.Seal([]byte(*newSecret)); err != nil {
				return nil, err
			}
		}
	}
	if err := s.repo.UpdateChannel(ctx, c); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrChannelNotFound
		}
		return nil, err
	}
	last, _ := s.repo.LastDeliveries(ctx)
	v := s.view(c, last)
	return &v, nil
}

// Delete removes a user channel and its delivery log.
func (s *Service) Delete(ctx context.Context, id int64) error {
	c, err := s.get(ctx, id)
	if err != nil {
		return err
	}
	if c.Origin == model.ChannelOriginConfig {
		return ErrChannelLocked
	}
	if err := s.repo.DeleteChannel(ctx, id); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ErrChannelNotFound
		}
		return err
	}
	return nil
}

// ListDeliveries returns the delivery log, newest first (channelID 0 = all).
func (s *Service) ListDeliveries(ctx context.Context, channelID int64, limit int) ([]*model.ChannelDelivery, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.repo.ListDeliveries(ctx, channelID, min(limit, 500))
}

// SyncConfig makes the config-origin channels in the database mirror the ones
// declared in config.yaml: new ones are added, changed ones updated, vanished
// ones removed (with their deliveries). User channels are never touched.
func (s *Service) SyncConfig(ctx context.Context, declared []config.NotificationChannel) error {
	existing, err := s.repo.ListChannels(ctx)
	if err != nil {
		return err
	}
	byName := map[string]*model.NotificationChannel{}
	for _, c := range existing {
		if c.Origin == model.ChannelOriginConfig {
			byName[c.Name] = c
		}
	}
	for _, d := range declared {
		want := configChannel(d)
		cur := byName[d.Name]
		delete(byName, d.Name)
		if cur == nil {
			if err := s.repo.CreateChannel(ctx, want); err != nil {
				return err
			}
			continue
		}
		want.ID = cur.ID
		if sameChannel(cur, want) {
			continue
		}
		if err := s.repo.UpdateChannel(ctx, want); err != nil {
			return err
		}
	}
	for _, gone := range byName {
		if err := s.repo.DeleteChannel(ctx, gone.ID); err != nil && !errors.Is(err, repository.ErrNotFound) {
			return err
		}
	}
	return nil
}

func configChannel(d config.NotificationChannel) *model.NotificationChannel {
	on := func(p *bool) bool { return p == nil || *p }
	c := &model.NotificationChannel{
		Name: d.Name, Type: d.Type, Origin: model.ChannelOriginConfig,
		URLEnc: []byte(d.URL), Enabled: on(d.Enabled), MinSeverity: model.SeverityCritical,
		Sources: nonNil(d.Sources), Namespaces: nonNil(d.Namespaces), Types: nonNil(d.Types),
		NotifyResolved: on(d.NotifyResolved), IncludeLogs: on(d.IncludeLogs), RepeatMinutes: d.RepeatMinutes,
	}
	if d.MinSeverity == string(model.SeverityWarning) {
		c.MinSeverity = model.SeverityWarning
	}
	if d.Secret != "" {
		c.SecretEnc = []byte(d.Secret)
	}
	return c
}

func sameChannel(a, b *model.NotificationChannel) bool {
	return a.Type == b.Type && string(a.URLEnc) == string(b.URLEnc) && string(a.SecretEnc) == string(b.SecretEnc) &&
		a.Enabled == b.Enabled && a.MinSeverity == b.MinSeverity && slices.Equal(a.Sources, b.Sources) &&
		slices.Equal(a.Namespaces, b.Namespaces) && slices.Equal(a.Types, b.Types) &&
		a.NotifyResolved == b.NotifyResolved && a.IncludeLogs == b.IncludeLogs && a.RepeatMinutes == b.RepeatMinutes
}
