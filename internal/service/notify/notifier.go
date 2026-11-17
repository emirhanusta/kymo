// Package notify delivers issue notifications to Slack, Discord and generic
// webhook channels and manages those channels (stored encrypted at rest).
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
	"github.com/emirhanusta/kymo/internal/secret"
)

// IssueMarker records that an issue was notified (the issue repository).
type IssueMarker interface {
	MarkNotified(ctx context.Context, id int64, at time.Time) error
}

// Options tunes the service; zero values take the defaults.
type Options struct {
	// Enabled switches sending on. Channel management works either way.
	Enabled bool
	// PublicURL is the base URL of the web UI used for "Open in KYMO" links.
	PublicURL string
	// Workers is the size of the delivery pool (default 4).
	Workers int
	// QueueSize bounds pending deliveries (default 512); beyond it they are dropped.
	QueueSize int
	// Timeout is the per-request timeout (default 10s).
	Timeout time.Duration
	// MaxAttempts is the number of tries per delivery (default 3).
	MaxAttempts int
	// Backoff are the waits before the 2nd, 3rd, … attempt (default 1s, 5s, 15s).
	Backoff []time.Duration
	// Retention of the delivery log (default 30 days).
	Retention time.Duration
	// Client overrides the HTTP client (tests).
	Client *http.Client
	// Now overrides the clock (tests).
	Now func() time.Time
}

// Service sends notifications and manages channels.
type Service struct {
	repo      repository.NotificationRepository
	crashLogs repository.CrashLogRepository
	box       *secret.Box
	marker    IssueMarker
	opt       Options
	log       *zap.Logger
	client    *http.Client

	queue chan job
	wg    sync.WaitGroup // pending jobs

	mu       sync.Mutex
	ctx      context.Context
	inflight map[string]bool
}

type job struct {
	ch    *model.NotificationChannel
	ev    model.IssueEvent
	token string // inflight key ("" = none)
}

// New builds a Service. marker and crashLogs may be nil.
func New(repo repository.NotificationRepository, crashLogs repository.CrashLogRepository, box *secret.Box,
	marker IssueMarker, opt Options, log *zap.Logger) *Service {
	if opt.Workers <= 0 {
		opt.Workers = 4
	}
	if opt.QueueSize <= 0 {
		opt.QueueSize = 512
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 10 * time.Second
	}
	if opt.MaxAttempts <= 0 {
		opt.MaxAttempts = 3
	}
	if opt.Backoff == nil {
		opt.Backoff = []time.Duration{time.Second, 5 * time.Second, 15 * time.Second}
	}
	if opt.Retention <= 0 {
		opt.Retention = 30 * 24 * time.Hour
	}
	if opt.Now == nil {
		opt.Now = func() time.Time { return time.Now().UTC() }
	}
	opt.PublicURL = strings.TrimRight(strings.TrimSpace(opt.PublicURL), "/")
	if log == nil {
		log = zap.NewNop()
	}
	client := opt.Client
	if client == nil {
		client = &http.Client{
			Timeout: opt.Timeout,
			// A webhook that redirects is misconfigured; never follow it elsewhere.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return &Service{
		repo: repo, crashLogs: crashLogs, box: box, marker: marker, opt: opt, log: log, client: client,
		queue: make(chan job, opt.QueueSize), inflight: map[string]bool{}, ctx: context.Background(),
	}
}

// Enabled reports whether notifications are being sent.
func (s *Service) Enabled() bool { return s.opt.Enabled }

// PublicURL is the configured UI base URL ("" when unset).
func (s *Service) PublicURL() string { return s.opt.PublicURL }

// Start launches the delivery workers and the hourly log cleanup; it returns
// immediately and stops when ctx is cancelled.
func (s *Service) Start(ctx context.Context) {
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()
	for i := 0; i < s.opt.Workers; i++ {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case j := <-s.queue:
					s.run(j)
				}
			}
		}()
	}
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if _, err := s.repo.DeleteDeliveriesBefore(ctx, s.opt.Now().Add(-s.opt.Retention)); err != nil {
					s.log.Warn("delivery log cleanup failed", zap.Error(err))
				}
			}
		}
	}()
}

// Wait blocks until every queued delivery has finished (tests).
func (s *Service) Wait() { s.wg.Wait() }

func (s *Service) runCtx() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ctx
}

func contains(list []string, v string) bool { return len(list) == 0 || slices.Contains(list, v) }

// matches reports whether the channel wants the event.
func matches(ch *model.NotificationChannel, ev model.IssueEvent) bool {
	i := &ev.Issue
	if !ch.Enabled || !model.SeverityAtLeast(i.Severity, ch.MinSeverity) {
		return false
	}
	if ev.Kind == model.EventResolved && !ch.NotifyResolved {
		return false
	}
	return contains(ch.Sources, i.SourceName) && contains(ch.Namespaces, i.Namespace) && contains(ch.Types, string(i.Type))
}

// enqueue schedules a delivery without blocking; a full queue drops it.
func (s *Service) enqueue(j job) bool {
	s.wg.Add(1)
	select {
	case s.queue <- j:
		return true
	default:
		s.wg.Done()
		s.log.Warn("notification queue full; dropping", zap.String("channel", j.ch.Name), zap.String("event", string(j.ev.Kind)))
		return false
	}
}

// Emit queues an opened / resolved / reminder event for every matching channel.
func (s *Service) Emit(ctx context.Context, ev model.IssueEvent) {
	if !s.opt.Enabled {
		return
	}
	chans, err := s.repo.ListChannels(ctx)
	if err != nil {
		s.log.Warn("notifications: list channels failed", zap.Error(err))
		return
	}
	sent := false
	for _, ch := range chans {
		if matches(ch, ev) && s.enqueue(job{ch: ch, ev: ev}) {
			sent = true
		}
	}
	if sent && s.marker != nil && ev.Issue.ID != 0 {
		if err := s.marker.MarkNotified(ctx, ev.Issue.ID, s.opt.Now()); err != nil {
			s.log.Warn("notifications: mark notified failed", zap.Error(err))
		}
	}
}

// Remind re-sends open issues on channels with a repeat interval once
// repeat_minutes have passed since the channel last heard about the issue.
func (s *Service) Remind(ctx context.Context, open []model.Issue) {
	if !s.opt.Enabled || len(open) == 0 {
		return
	}
	chans, err := s.repo.ListChannels(ctx)
	if err != nil {
		s.log.Warn("notifications: list channels failed", zap.Error(err))
		return
	}
	var repeating []*model.NotificationChannel
	for _, ch := range chans {
		if ch.Enabled && ch.RepeatMinutes > 0 {
			repeating = append(repeating, ch)
		}
	}
	if len(repeating) == 0 {
		return
	}
	marks, err := s.repo.LastIssueDeliveries(ctx)
	if err != nil {
		s.log.Warn("notifications: last deliveries failed", zap.Error(err))
		return
	}
	type pair struct{ ch, issue int64 }
	last := make(map[pair]time.Time, len(marks))
	for _, m := range marks {
		last[pair{m.ChannelID, m.IssueID}] = m.At
	}
	now := s.opt.Now()
	notified := map[int64]bool{}
	for _, ch := range repeating {
		every := time.Duration(ch.RepeatMinutes) * time.Minute
		for _, iss := range open {
			ev := model.IssueEvent{Kind: model.EventReminder, Issue: iss}
			if !matches(ch, ev) {
				continue
			}
			at, ok := last[pair{ch.ID, iss.ID}]
			if !ok {
				at = iss.FirstSeen
			}
			if now.Sub(at) < every {
				continue
			}
			token := fmt.Sprintf("%d/%d", ch.ID, iss.ID)
			s.mu.Lock()
			busy := s.inflight[token]
			if !busy {
				s.inflight[token] = true
			}
			s.mu.Unlock()
			if busy {
				continue
			}
			if s.enqueue(job{ch: ch, ev: ev, token: token}) {
				notified[iss.ID] = true
			} else {
				s.mu.Lock()
				delete(s.inflight, token)
				s.mu.Unlock()
			}
		}
	}
	if s.marker != nil {
		for id := range notified {
			if err := s.marker.MarkNotified(ctx, id, now); err != nil {
				s.log.Warn("notifications: mark notified failed", zap.Error(err))
			}
		}
	}
}

func (s *Service) run(j job) {
	defer s.wg.Done()
	if j.token != "" {
		defer func() {
			s.mu.Lock()
			delete(s.inflight, j.token)
			s.mu.Unlock()
		}()
	}
	ctx := s.runCtx()
	res := s.deliver(ctx, j.ch, j.ev, s.opt.MaxAttempts)
	s.record(ctx, j.ch, j.ev, res)
}

// Result is the outcome of one delivery.
type Result struct {
	OK         bool   `json:"ok"`
	StatusCode int    `json:"status_code"`
	Error      string `json:"error"`
	Attempts   int    `json:"-"`
}

func (s *Service) record(ctx context.Context, ch *model.NotificationChannel, ev model.IssueEvent, res Result) {
	d := &model.ChannelDelivery{
		ChannelID: ch.ID, Event: ev.Kind, OK: res.OK, StatusCode: res.StatusCode,
		Error: res.Error, Attempts: res.Attempts, SentAt: s.opt.Now(),
	}
	if ev.Issue.ID != 0 {
		id := ev.Issue.ID
		d.IssueID = &id
	}
	// The delivery log must survive a shutdown that cancelled ctx.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.repo.RecordDelivery(rctx, d); err != nil {
		s.log.Warn("notifications: record delivery failed", zap.Int64("channel", ch.ID), zap.Error(err))
	}
	if !res.OK {
		s.log.Warn("notification delivery failed", zap.String("channel", ch.Name), zap.String("event", string(ev.Kind)),
			zap.Int("status", res.StatusCode), zap.String("error", res.Error))
	}
}

// Test sends a sample notification to a channel once (no retries), records it
// and returns the outcome.
func (s *Service) Test(ctx context.Context, id int64) (*Result, error) {
	ch, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	now := s.opt.Now()
	sample := model.IssueEvent{Kind: model.EventTest, Issue: model.Issue{
		SourceName: "demo-cluster", WorkloadID: "demo-cluster:default:Deployment:example", Namespace: "default",
		Kind: model.WorkloadDeployment, Name: "example", Type: model.IssueCrashLoop, Severity: model.SeverityWarning,
		Title: "KYMO test notification", Detail: "This is a test message. Nothing is wrong.",
		Status: model.IssueOpen, FirstSeen: now, LastSeen: now,
	}}
	res := s.deliver(ctx, ch, sample, 1)
	s.record(ctx, ch, sample, res)
	return &res, nil
}

// deliver builds the request once and tries up to maxAttempts times.
func (s *Service) deliver(ctx context.Context, ch *model.NotificationChannel, ev model.IssueEvent, maxAttempts int) Result {
	rawURL, sec, err := s.plain(ch)
	if err != nil {
		return Result{Error: err.Error(), Attempts: 1}
	}
	body, headers, err := s.build(ctx, ch, ev, sec)
	if err != nil {
		return Result{Error: "build payload: " + err.Error(), Attempts: 1}
	}
	var res Result
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			wait := s.backoff(attempt - 2)
			select {
			case <-ctx.Done():
				res.Error = "canceled: " + ctx.Err().Error()
				return res
			case <-time.After(wait):
			}
		}
		res = s.post(ctx, rawURL, body, headers)
		res.Attempts = attempt
		if res.OK || !retryable(res) {
			return res
		}
	}
	return res
}

func (s *Service) backoff(i int) time.Duration {
	if len(s.opt.Backoff) == 0 {
		return 0
	}
	return s.opt.Backoff[min(i, len(s.opt.Backoff)-1)]
}

// retryable: network errors, 429 and 5xx are worth retrying; other 4xx are not.
func retryable(r Result) bool {
	return r.StatusCode == 0 || r.StatusCode == http.StatusTooManyRequests || r.StatusCode >= 500
}

func (s *Service) post(ctx context.Context, rawURL string, body []byte, headers map[string]string) Result {
	rctx, cancel := context.WithTimeout(ctx, s.opt.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return Result{Error: "invalid request: " + sanitizeErr(err, rawURL)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "KYMO-Notifier")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return Result{Error: sanitizeErr(err, rawURL)}
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return Result{OK: true, StatusCode: resp.StatusCode}
	}
	msg := fmt.Sprintf("HTTP %d", resp.StatusCode)
	if t := strings.TrimSpace(string(snippet)); t != "" {
		msg += ": " + t
	}
	return Result{StatusCode: resp.StatusCode, Error: msg}
}

// sanitizeErr removes the (secret) webhook URL from a transport error.
func sanitizeErr(err error, rawURL string) string {
	var ue interface{ Unwrap() error }
	msg := err.Error()
	if errors.As(err, &ue) {
		if inner := ue.Unwrap(); inner != nil {
			msg = inner.Error()
		}
	}
	return strings.ReplaceAll(msg, rawURL, "<url>")
}

// Sign returns the "sha256=<hex>" HMAC signature of body (X-KYMO-Signature).
func Sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}
