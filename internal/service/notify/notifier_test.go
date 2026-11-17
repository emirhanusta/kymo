package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
	"github.com/emirhanusta/kymo/internal/secret"
	"github.com/emirhanusta/kymo/internal/service/notify/notifytest"
)

// ---- helpers ---------------------------------------------------------------

type capture struct {
	mu      sync.Mutex
	bodies  [][]byte
	headers []http.Header
	codes   []int // status per request, last one repeats
}

func (c *capture) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		n := len(c.bodies)
		c.bodies = append(c.bodies, b)
		c.headers = append(c.headers, r.Header.Clone())
		code := http.StatusOK
		if len(c.codes) > 0 {
			code = c.codes[min(n, len(c.codes)-1)]
		}
		c.mu.Unlock()
		w.WriteHeader(code)
		_, _ = w.Write([]byte("body-" + http.StatusText(code)))
	}
}

func (c *capture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.bodies)
}

func (c *capture) json(t *testing.T, i int) map[string]any {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	var m map[string]any
	if err := json.Unmarshal(c.bodies[i], &m); err != nil {
		t.Fatalf("invalid JSON %q: %v", c.bodies[i], err)
	}
	return m
}

type fakeCrashLogs struct {
	repository.CrashLogRepository
	logs []*model.CrashLog
}

func (f *fakeCrashLogs) FindByWorkload(context.Context, string, int) ([]*model.CrashLog, error) {
	return f.logs, nil
}

type marker struct {
	mu  sync.Mutex
	ids []int64
}

func (m *marker) MarkNotified(_ context.Context, id int64, _ time.Time) error {
	m.mu.Lock()
	m.ids = append(m.ids, id)
	m.mu.Unlock()
	return nil
}

type rig struct {
	t     *testing.T
	svc   *Service
	repo  *notifytest.MemRepo
	srv   *httptest.Server
	cap   *capture
	logs  *fakeCrashLogs
	mark  *marker
	now   time.Time
	nowMu sync.Mutex
}

func newRig(t *testing.T, opt Options) *rig {
	t.Helper()
	r := &rig{t: t, repo: notifytest.NewMemRepo(), cap: &capture{}, logs: &fakeCrashLogs{}, mark: &marker{},
		now: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)}
	r.srv = httptest.NewServer(r.cap.handler())
	t.Cleanup(r.srv.Close)
	opt.Enabled = true
	if opt.Backoff == nil {
		opt.Backoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	}
	opt.Now = func() time.Time { r.nowMu.Lock(); defer r.nowMu.Unlock(); return r.now }
	box, _ := secret.New("test-key")
	r.svc = New(r.repo, r.logs, box, r.mark, opt, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r.svc.Start(ctx)
	return r
}

func (r *rig) advance(d time.Duration) { r.nowMu.Lock(); r.now = r.now.Add(d); r.nowMu.Unlock() }

// channel stores a config-origin channel (plain URL) pointing at the test server.
func (r *rig) channel(typ string, mod func(*model.NotificationChannel)) *model.NotificationChannel {
	r.t.Helper()
	c := &model.NotificationChannel{
		Name: typ, Type: typ, Origin: model.ChannelOriginConfig, URLEnc: []byte(r.srv.URL + "/hook/abcd1234"),
		Enabled: true, MinSeverity: model.SeverityWarning, NotifyResolved: true, IncludeLogs: true,
	}
	if mod != nil {
		mod(c)
	}
	if err := r.repo.CreateChannel(context.Background(), c); err != nil {
		r.t.Fatal(err)
	}
	return c
}

func issue(mod func(*model.Issue)) model.Issue {
	i := model.Issue{
		ID: 7, SourceName: "prod", WorkloadID: "prod:shop:Deployment:api", Namespace: "shop", Kind: "Deployment", Name: "api",
		Type: model.IssueCrashLoop, Severity: model.SeverityCritical, Title: "Crash loop · exit 1", Detail: "1/3 pods ready",
		Status: model.IssueOpen, FirstSeen: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC), LastSeen: time.Date(2026, 3, 1, 9, 59, 0, 0, time.UTC),
	}
	if mod != nil {
		mod(&i)
	}
	return i
}

func (r *rig) emit(kind model.IssueEventKind, i model.Issue) {
	r.t.Helper()
	r.svc.Emit(context.Background(), model.IssueEvent{Kind: kind, Issue: i})
	r.svc.Wait()
}

func (r *rig) crashLog(lines int, at time.Time) {
	var sb strings.Builder
	for i := 1; i <= lines; i++ {
		fmt.Fprintf(&sb, "line %02d\n", i)
	}
	r.logs.logs = append(r.logs.logs, &model.CrashLog{OccurredAt: at, Lines: sb.String()})
}

// ---- payloads --------------------------------------------------------------

func TestSlackPayload(t *testing.T) {
	r := newRig(t, Options{PublicURL: "https://kymo.example.com/"})
	r.channel(model.ChannelSlack, nil)
	r.crashLog(30, r.now.Add(-time.Minute))
	r.emit(model.EventOpened, issue(nil))

	if r.cap.count() != 1 {
		t.Fatalf("requests: %d", r.cap.count())
	}
	m := r.cap.json(t, 0)
	text, _ := m["text"].(string)
	if !strings.Contains(text, "🔴 Crash loop · exit 1") || !strings.Contains(text, "Deployment/api") {
		t.Errorf("fallback text %q", text)
	}
	blocks := m["blocks"].([]any)
	header := blocks[0].(map[string]any)
	if header["type"] != "header" || !strings.HasPrefix(header["text"].(map[string]any)["text"].(string), "🔴 ") {
		t.Errorf("header: %v", header)
	}
	all, _ := json.Marshal(blocks)
	s := string(all)
	for _, want := range []string{"Deployment/api", "shop", "prod", "1/3 pods ready", "line 30", "line 11"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in %s", want, s)
		}
	}
	if strings.Contains(s, "line 10") {
		t.Error("only the last 20 log lines belong in the message")
	}
	last := blocks[len(blocks)-1].(map[string]any)
	btn := last["elements"].([]any)[0].(map[string]any)
	if last["type"] != "actions" || btn["url"] != "https://kymo.example.com/workloads/prod%3Ashop%3ADeployment%3Aapi" ||
		btn["text"].(map[string]any)["text"] != "Open in KYMO" {
		t.Errorf("link button: %v", last)
	}
	if len(r.mark.ids) != 1 || r.mark.ids[0] != 7 {
		t.Errorf("issue not marked notified: %v", r.mark.ids)
	}
}

func TestSlackWithoutPublicURLOrLogs(t *testing.T) {
	r := newRig(t, Options{})
	r.channel(model.ChannelSlack, func(c *model.NotificationChannel) { c.IncludeLogs = false })
	r.crashLog(5, r.now)
	r.emit(model.EventOpened, issue(nil))
	body := string(r.cap.bodies[0])
	if strings.Contains(body, "Open in KYMO") || strings.Contains(body, "line 05") || strings.Contains(body, "```") {
		t.Errorf("unexpected link/logs: %s", body)
	}

	// Resolved: green check, no logs.
	r2 := newRig(t, Options{})
	r2.channel(model.ChannelSlack, nil)
	r2.crashLog(5, r2.now)
	resolved := r2.now
	r2.emit(model.EventResolved, issue(func(i *model.Issue) { i.Status, i.ResolvedAt = model.IssueResolved, &resolved }))
	m := r2.cap.json(t, 0)
	if !strings.HasPrefix(m["text"].(string), "✅ Resolved: Crash loop") || strings.Contains(string(r2.cap.bodies[0]), "line 05") {
		t.Errorf("resolved message: %s", r2.cap.bodies[0])
	}
}

func TestSlackTruncationAndEscaping(t *testing.T) {
	r := newRig(t, Options{})
	r.channel(model.ChannelSlack, nil)
	long := strings.Repeat("x", 500)
	var sb strings.Builder
	for i := 0; i < 20; i++ {
		sb.WriteString(long + " <b> ``` &\n")
	}
	r.logs.logs = []*model.CrashLog{{OccurredAt: r.now, Lines: sb.String()}}
	r.emit(model.EventOpened, issue(func(i *model.Issue) { i.Title = strings.Repeat("T", 400); i.Detail = "<script>&" }))
	m := r.cap.json(t, 0)
	for _, b := range m["blocks"].([]any) {
		blk := b.(map[string]any)
		if txt, ok := blk["text"].(map[string]any); ok {
			n := len([]rune(txt["text"].(string)))
			limit := 3000
			if blk["type"] == "header" {
				limit = 150
			}
			if n > limit {
				t.Errorf("%s text has %d runes (limit %d)", blk["type"], n, limit)
			}
		}
	}
	body := string(r.cap.bodies[0])
	if strings.Contains(body, "<script>") || strings.Contains(body, "```\\n```") {
		t.Errorf("unescaped content: %s", body)
	}
	if !strings.Contains(body, "…") {
		t.Error("truncation marker expected")
	}
}

func TestDiscordPayload(t *testing.T) {
	r := newRig(t, Options{PublicURL: "https://kymo.example.com"})
	r.channel(model.ChannelDiscord, nil)
	var sb strings.Builder
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&sb, "a fairly long log line number %02d with some padding to blow the limit\n", i)
	}
	r.logs.logs = []*model.CrashLog{{OccurredAt: r.now, Lines: sb.String()}}
	r.emit(model.EventOpened, issue(nil))

	m := r.cap.json(t, 0)
	emb := m["embeds"].([]any)[0].(map[string]any)
	if int(emb["color"].(float64)) != 0xE5484D || !strings.HasPrefix(emb["title"].(string), "🔴 ") ||
		emb["url"] != "https://kymo.example.com/workloads/prod%3Ashop%3ADeployment%3Aapi" {
		t.Errorf("embed: %v", emb)
	}
	got := map[string]string{}
	for _, f := range emb["fields"].([]any) {
		fm := f.(map[string]any)
		got[fm["name"].(string)] = fm["value"].(string)
	}
	if got["Workload"] != "Deployment/api" || got["Namespace"] != "shop" || got["Cluster"] != "prod" || got["Severity"] != "critical" {
		t.Errorf("fields: %v", got)
	}
	logs := got["Last crash log"]
	if !strings.HasPrefix(logs, "```\n") || !strings.HasSuffix(logs, "\n```") || len([]rune(logs)) > 1000 || !strings.Contains(logs, "number 40") {
		t.Errorf("logs field (%d runes): %q", len([]rune(logs)), logs)
	}

	r.emit(model.EventOpened, issue(func(i *model.Issue) { i.Severity = model.SeverityWarning }))
	if c := int(r.cap.json(t, 1)["embeds"].([]any)[0].(map[string]any)["color"].(float64)); c != 0xF5A524 {
		t.Errorf("warning color %x", c)
	}
	resolvedAt := r.now
	r.emit(model.EventResolved, issue(func(i *model.Issue) { i.Status, i.ResolvedAt = model.IssueResolved, &resolvedAt }))
	if c := int(r.cap.json(t, 2)["embeds"].([]any)[0].(map[string]any)["color"].(float64)); c != 0x30A46C {
		t.Errorf("resolved color %x", c)
	}
}

func TestWebhookPayloadAndSignature(t *testing.T) {
	r := newRig(t, Options{PublicURL: "https://kymo.example.com"})
	r.channel(model.ChannelWebhook, func(c *model.NotificationChannel) { c.SecretEnc = []byte("s3cret") })
	r.crashLog(30, r.now)
	r.emit(model.EventOpened, issue(nil))

	m := r.cap.json(t, 0)
	if m["event"] != "opened" || m["workload_url"] != "https://kymo.example.com/workloads/prod%3Ashop%3ADeployment%3Aapi" {
		t.Errorf("payload: %v", m)
	}
	iss := m["issue"].(map[string]any)
	if iss["id"] != float64(7) || iss["type"] != "crash_loop" || iss["workload_id"] != "prod:shop:Deployment:api" ||
		iss["resolved_at"] != nil || iss["status"] != "open" {
		t.Errorf("issue: %v", iss)
	}
	tail := m["crash_log_tail"].(string)
	if !strings.HasPrefix(tail, "line 11") || !strings.HasSuffix(tail, "line 30") {
		t.Errorf("tail %q", tail)
	}
	if _, err := time.Parse(time.RFC3339Nano, m["sent_at"].(string)); err != nil {
		t.Errorf("sent_at: %v", err)
	}

	h := r.cap.headers[0]
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(r.cap.bodies[0])
	if want := "sha256=" + hex.EncodeToString(mac.Sum(nil)); h.Get("X-KYMO-Signature") != want {
		t.Errorf("signature %q, want %q", h.Get("X-KYMO-Signature"), want)
	}
	if h.Get("X-KYMO-Event") != "opened" || h.Get("Content-Type") != "application/json" {
		t.Errorf("headers: %v", h)
	}

	// Without a secret there is no signature header.
	r2 := newRig(t, Options{})
	r2.channel(model.ChannelWebhook, nil)
	r2.emit(model.EventOpened, issue(nil))
	if r2.cap.headers[0].Get("X-KYMO-Signature") != "" || r2.cap.json(t, 0)["workload_url"] != "" {
		t.Errorf("unsigned webhook: %v %s", r2.cap.headers[0], r2.cap.bodies[0])
	}
}

// ---- routing ---------------------------------------------------------------

func TestFilters(t *testing.T) {
	r := newRig(t, Options{})
	critOnly := r.channel(model.ChannelWebhook, func(c *model.NotificationChannel) { c.Name = "crit"; c.MinSeverity = model.SeverityCritical })
	r.channel(model.ChannelWebhook, func(c *model.NotificationChannel) { c.Name = "all-warn" })
	r.channel(model.ChannelWebhook, func(c *model.NotificationChannel) { c.Name = "staging"; c.Sources = []string{"staging"} })
	r.channel(model.ChannelWebhook, func(c *model.NotificationChannel) { c.Name = "oomonly"; c.Types = []string{"oom"} })
	r.channel(model.ChannelWebhook, func(c *model.NotificationChannel) { c.Name = "ns"; c.Namespaces = []string{"other"} })
	r.channel(model.ChannelWebhook, func(c *model.NotificationChannel) { c.Name = "off"; c.Enabled = false })
	r.channel(model.ChannelWebhook, func(c *model.NotificationChannel) { c.Name = "noresolved"; c.NotifyResolved = false })

	delivered := func() map[string]bool {
		out := map[string]bool{}
		for _, d := range r.repo.All() {
			out[r.repo.Channels[d.ChannelID].Name] = true
		}
		r.repo.Deliveries = nil
		return out
	}
	expect := func(label string, got map[string]bool, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Errorf("%s: delivered to %v, want %v", label, got, want)
			return
		}
		for _, w := range want {
			if !got[w] {
				t.Errorf("%s: delivered to %v, want %v", label, got, want)
			}
		}
	}

	r.emit(model.EventOpened, issue(nil))
	expect("critical crash_loop in prod/shop", delivered(), "crit", "all-warn", "noresolved")

	r.emit(model.EventOpened, issue(func(i *model.Issue) { i.Severity = model.SeverityWarning; i.Type = model.IssueProbeFailing }))
	expect("warning probe", delivered(), "all-warn", "noresolved")

	r.emit(model.EventOpened, issue(func(i *model.Issue) { i.Type = model.IssueOOM; i.SourceName = "staging"; i.Namespace = "other" }))
	expect("oom staging/other", delivered(), "crit", "all-warn", "staging", "oomonly", "ns", "noresolved")

	now := r.now
	r.emit(model.EventResolved, issue(func(i *model.Issue) { i.Status, i.ResolvedAt = model.IssueResolved, &now }))
	expect("resolved", delivered(), "crit", "all-warn")
	_ = critOnly
}

func TestDisabledNotificationsSendNothing(t *testing.T) {
	r := newRig(t, Options{})
	r.svc.opt.Enabled = false
	r.channel(model.ChannelWebhook, nil)
	r.emit(model.EventOpened, issue(nil))
	if r.cap.count() != 0 || len(r.repo.All()) != 0 {
		t.Fatal("notifications.enabled=false must not send")
	}
}

// ---- delivery --------------------------------------------------------------

func TestRetriesOn500ThenSucceeds(t *testing.T) {
	r := newRig(t, Options{})
	r.cap.codes = []int{500, 502, 200}
	ch := r.channel(model.ChannelWebhook, nil)
	r.emit(model.EventOpened, issue(nil))

	if r.cap.count() != 3 {
		t.Fatalf("requests: %d", r.cap.count())
	}
	log := r.repo.All()
	if len(log) != 1 {
		t.Fatalf("deliveries: %+v", log)
	}
	d := log[0]
	if !d.OK || d.Attempts != 3 || d.StatusCode != 200 || d.ChannelID != ch.ID || d.Event != model.EventOpened ||
		d.IssueID == nil || *d.IssueID != 7 || d.Error != "" || d.SentAt.IsZero() {
		t.Errorf("delivery: %+v", d)
	}
}

func TestGivesUpAfterThreeAttemptsAndRecordsError(t *testing.T) {
	r := newRig(t, Options{})
	r.cap.codes = []int{500}
	r.channel(model.ChannelWebhook, nil)
	r.emit(model.EventOpened, issue(nil))
	if r.cap.count() != 3 {
		t.Fatalf("requests: %d", r.cap.count())
	}
	d := r.repo.All()[0]
	if d.OK || d.Attempts != 3 || d.StatusCode != 500 || !strings.Contains(d.Error, "HTTP 500") || !strings.Contains(d.Error, "body-Internal Server Error") {
		t.Errorf("delivery: %+v", d)
	}
}

func TestClientErrorsAreNotRetried(t *testing.T) {
	r := newRig(t, Options{})
	r.cap.codes = []int{404}
	r.channel(model.ChannelWebhook, nil)
	r.emit(model.EventOpened, issue(nil))
	if r.cap.count() != 1 {
		t.Fatalf("requests: %d", r.cap.count())
	}
	if d := r.repo.All()[0]; d.OK || d.Attempts != 1 || d.StatusCode != 404 {
		t.Errorf("delivery: %+v", d)
	}
}

func TestNetworkErrorDoesNotLeakURL(t *testing.T) {
	r := newRig(t, Options{})
	url := r.srv.URL + "/hook/abcd1234"
	r.srv.Close()
	r.channel(model.ChannelWebhook, nil)
	r.emit(model.EventOpened, issue(nil))
	d := r.repo.All()[0]
	if d.OK || d.Attempts != 3 || d.StatusCode != 0 || d.Error == "" || strings.Contains(d.Error, "abcd1234") || strings.Contains(d.Error, url) {
		t.Errorf("delivery: %+v", d)
	}
}

func TestRequestTimeout(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond) }))
	defer slow.Close()
	r := newRig(t, Options{Timeout: 50 * time.Millisecond, MaxAttempts: 1})
	r.channel(model.ChannelWebhook, func(c *model.NotificationChannel) { c.URLEnc = []byte(slow.URL) })
	r.emit(model.EventOpened, issue(nil))
	if d := r.repo.All()[0]; d.OK || d.Attempts != 1 || d.Error == "" {
		t.Errorf("delivery: %+v", d)
	}
}

func TestTestNotification(t *testing.T) {
	r := newRig(t, Options{PublicURL: "https://kymo.example.com"})
	r.cap.codes = []int{500, 200}
	ch := r.channel(model.ChannelSlack, nil)

	res, err := r.svc.Test(context.Background(), ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || res.StatusCode != 500 || !strings.Contains(res.Error, "HTTP 500") || r.cap.count() != 1 {
		t.Fatalf("test result %+v after %d requests (no retries expected)", res, r.cap.count())
	}
	res, _ = r.svc.Test(context.Background(), ch.ID)
	if !res.OK || res.StatusCode != 200 {
		t.Fatalf("second test: %+v", res)
	}
	if !strings.Contains(string(r.cap.bodies[1]), "KYMO test notification") {
		t.Errorf("body: %s", r.cap.bodies[1])
	}
	log := r.repo.All()
	if len(log) != 2 || log[0].Event != model.EventTest || log[0].IssueID != nil || log[1].OK != true {
		t.Errorf("log: %+v", log)
	}
	if _, err := r.svc.Test(context.Background(), 999); err != ErrChannelNotFound {
		t.Errorf("unknown channel: %v", err)
	}
}

// ---- reminders -------------------------------------------------------------

func TestReminders(t *testing.T) {
	r := newRig(t, Options{})
	r.channel(model.ChannelWebhook, func(c *model.NotificationChannel) { c.Name = "pings"; c.RepeatMinutes = 10 })
	r.channel(model.ChannelWebhook, func(c *model.NotificationChannel) { c.Name = "once" })
	iss := issue(func(i *model.Issue) { i.FirstSeen = r.now })
	ctx := context.Background()

	// Opened goes to both.
	r.emit(model.EventOpened, iss)
	if r.cap.count() != 2 {
		t.Fatalf("opened deliveries: %d", r.cap.count())
	}

	remind := func() {
		r.svc.Remind(ctx, []model.Issue{iss})
		r.svc.Wait()
	}
	r.advance(9 * time.Minute)
	remind()
	if r.cap.count() != 2 {
		t.Fatalf("too early: %d", r.cap.count())
	}
	r.advance(2 * time.Minute) // 11 minutes since the opened notification
	remind()
	if r.cap.count() != 3 {
		t.Fatalf("expected one reminder (only the repeating channel), got %d total", r.cap.count())
	}
	if m := r.cap.json(t, 2); m["event"] != "reminder" {
		t.Errorf("event: %v", m["event"])
	}
	r.advance(5 * time.Minute)
	remind()
	if r.cap.count() != 3 {
		t.Fatalf("reminder repeated too soon: %d", r.cap.count())
	}
	r.advance(6 * time.Minute)
	remind()
	if r.cap.count() != 4 {
		t.Fatalf("second reminder missing: %d", r.cap.count())
	}
	last := r.repo.All()
	if d := last[len(last)-1]; d.Event != model.EventReminder || !d.OK {
		t.Errorf("last delivery: %+v", d)
	}

	// Slack reminders are labelled.
	r2 := newRig(t, Options{})
	r2.channel(model.ChannelSlack, func(c *model.NotificationChannel) { c.RepeatMinutes = 1 })
	old := issue(func(i *model.Issue) { i.FirstSeen = r2.now.Add(-time.Hour) })
	r2.svc.Remind(ctx, []model.Issue{old})
	r2.svc.Wait()
	if r2.cap.count() != 1 || !strings.Contains(string(r2.cap.bodies[0]), "Still open: Crash loop") {
		t.Fatalf("slack reminder: %d %s", r2.cap.count(), r2.cap.bodies)
	}
}

func TestOldCrashLogIsNotAttached(t *testing.T) {
	r := newRig(t, Options{})
	r.channel(model.ChannelWebhook, nil)
	r.crashLog(5, r.now.Add(-48*time.Hour))
	r.emit(model.EventOpened, issue(nil))
	if r.cap.json(t, 0)["crash_log_tail"] != "" {
		t.Error("a two-day-old crash log must not be attached")
	}
	// k8s-event issues never carry crash logs.
	r.crashLog(5, r.now)
	r.emit(model.EventOpened, issue(func(i *model.Issue) { i.Type = model.IssueImagePull }))
	if r.cap.json(t, 1)["crash_log_tail"] != "" {
		t.Error("image_pull issue must not carry a crash log")
	}
}
