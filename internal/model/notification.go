package model

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Notification channel types.
const (
	ChannelSlack   = "slack"
	ChannelDiscord = "discord"
	ChannelWebhook = "webhook"
)

// Channel origins.
const (
	ChannelOriginUser   = "user"
	ChannelOriginConfig = "config"
)

// NotificationChannel is a destination for issue notifications. URLEnc and
// SecretEnc are AES-GCM sealed for user channels and plain bytes for config
// channels (see migration 012); they are never serialised.
type NotificationChannel struct {
	ID             int64
	Name           string
	Type           string
	Origin         string
	URLEnc         []byte
	SecretEnc      []byte
	Enabled        bool
	MinSeverity    IssueSeverity
	Sources        []string
	Namespaces     []string
	Types          []string
	NotifyResolved bool
	IncludeLogs    bool
	RepeatMinutes  int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ChannelDelivery is one logged notification attempt series.
type ChannelDelivery struct {
	ID         int64          `json:"id"`
	ChannelID  int64          `json:"channel_id"`
	IssueID    *int64         `json:"issue_id"`
	Event      IssueEventKind `json:"event"`
	OK         bool           `json:"ok"`
	StatusCode int            `json:"status_code"`
	Error      string         `json:"error"`
	Attempts   int            `json:"attempts"`
	SentAt     time.Time      `json:"sent_at"`
}

// DeliveryMark is the newest delivery time for a (channel, issue) pair.
type DeliveryMark struct {
	ChannelID int64
	IssueID   int64
	At        time.Time
}

// ValidateChannelType reports whether t is a known channel type.
func ValidateChannelType(t string) error {
	switch t {
	case ChannelSlack, ChannelDiscord, ChannelWebhook:
		return nil
	}
	return fmt.Errorf("type must be one of slack, discord, webhook")
}

// ValidateChannelURL checks a channel URL: https (plain http only for
// localhost / 127.0.0.1 / ::1, for testing), Slack URLs on hooks.slack.com and
// Discord URLs on discord.com/api/webhooks.
func ValidateChannelURL(typ, raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return fmt.Errorf("url is not a valid URL")
	}
	host := strings.ToLower(u.Hostname())
	switch u.Scheme {
	case "https":
	case "http":
		if host != "localhost" && host != "127.0.0.1" && host != "::1" {
			return fmt.Errorf("url must use https (http is only allowed for localhost)")
		}
	default:
		return fmt.Errorf("url must use https")
	}
	if u.User != nil {
		return fmt.Errorf("url must not contain credentials")
	}
	switch typ {
	case ChannelSlack:
		if u.Scheme != "https" || host != "hooks.slack.com" {
			return fmt.Errorf("a Slack webhook URL must be https://hooks.slack.com/services/…")
		}
	case ChannelDiscord:
		okHost := host == "discord.com" || host == "discordapp.com" ||
			host == "ptb.discord.com" || host == "canary.discord.com"
		if u.Scheme != "https" || !okHost || !strings.HasPrefix(u.Path, "/api/webhooks/") {
			return fmt.Errorf("a Discord webhook URL must be https://discord.com/api/webhooks/…")
		}
	}
	return nil
}
