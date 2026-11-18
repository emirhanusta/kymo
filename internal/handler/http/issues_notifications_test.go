package httphandler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/config"
	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
	"github.com/emirhanusta/kymo/internal/secret"
	"github.com/emirhanusta/kymo/internal/service/notify"
	"github.com/emirhanusta/kymo/internal/service/notify/notifytest"
)

type fakeIssueRepo struct {
	repository.IssueRepository
	q       repository.IssueQuery
	list    []*model.Issue
	summary *model.IssueSummary
}

func (f *fakeIssueRepo) List(_ context.Context, q repository.IssueQuery) ([]*model.Issue, error) {
	f.q = q
	return f.list, nil
}

func (f *fakeIssueRepo) Get(_ context.Context, id int64) (*model.Issue, error) {
	for _, i := range f.list {
		if i.ID == id {
			return i, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (f *fakeIssueRepo) Summary(context.Context) (*model.IssueSummary, error) { return f.summary, nil }

func request(r *gin.Engine, method, url string, body any) *httptest.ResponseRecorder {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, url, rd)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func apiRouter(h *Handler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.UseRawPath, r.UnescapePathValues = true, true
	if h.auth == nil {
		h.auth = disabledAuth()
	}
	h.registerRoutes(r)
	return r
}

func TestIssueEndpoints(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	f := &fakeIssueRepo{
		list: []*model.Issue{{ID: 3, SourceName: "prod", WorkloadID: "prod:ns:Deployment:api", Namespace: "ns", Kind: "Deployment",
			Name: "api", Type: model.IssueOOM, Severity: model.SeverityCritical, Title: "OOMKilled ×2 in 24h", Status: model.IssueOpen,
			FirstSeen: now, LastSeen: now, Flaps: 1}},
		summary: &model.IssueSummary{Open: 3, Critical: 2, Warning: 1, BySource: map[string]int{"prod": 3}},
	}
	r := apiRouter(&Handler{log: zap.NewNop(), issues: f})

	w := request(r, "GET", "/api/v1/issues", nil)
	var got []map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || len(got) != 1 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	for _, k := range []string{"id", "source_name", "workload_id", "namespace", "kind", "name", "type", "severity", "title", "detail", "status", "first_seen", "last_seen", "resolved_at", "flaps"} {
		if _, ok := got[0][k]; !ok {
			t.Errorf("issue JSON lacks %q: %v", k, got[0])
		}
	}
	if _, leaked := got[0]["notify_count"]; leaked || got[0]["resolved_at"] != nil {
		t.Errorf("issue JSON: %v", got[0])
	}
	if f.q.Status != "open" || f.q.Limit != 200 {
		t.Errorf("defaults: %+v", f.q)
	}

	do := func(u string) *httptest.ResponseRecorder { return request(r, "GET", u, nil) }
	if w := do("/api/v1/issues?status=all&source=prod&severity=warning&type=oom&limit=9999&before=2026-01-01T00:00:00Z"); w.Code != 200 ||
		f.q.Status != "all" || f.q.Source != "prod" || f.q.Severity != "warning" || f.q.Type != "oom" || f.q.Limit != 500 || f.q.Before.Year() != 2026 {
		t.Errorf("params: %d %+v", w.Code, f.q)
	}
	for _, u := range []string{"?status=bogus", "?severity=info", "?type=nope", "?before=yesterday"} {
		if w := do("/api/v1/issues" + u); w.Code != 400 {
			t.Errorf("%s: %d", u, w.Code)
		}
	}

	if w := do("/api/v1/workloads/prod%3Ans%3ADeployment%3Aapi/issues?status=resolved"); w.Code != 200 ||
		f.q.WorkloadID != "prod:ns:Deployment:api" || f.q.Status != "resolved" {
		t.Errorf("workload issues: %d %+v", w.Code, f.q)
	}

	w = do("/api/v1/issues/summary")
	var sum map[string]any
	json.Unmarshal(w.Body.Bytes(), &sum)
	if w.Code != 200 || sum["open"] != float64(3) || sum["critical"] != float64(2) || sum["warning"] != float64(1) ||
		sum["by_source"].(map[string]any)["prod"] != float64(3) {
		t.Errorf("summary: %d %s", w.Code, w.Body)
	}

	if w := do("/api/v1/issues/3"); w.Code != 200 || !strings.Contains(w.Body.String(), `"OOMKilled ×2 in 24h"`) {
		t.Errorf("get: %d %s", w.Code, w.Body)
	}
	if w := do("/api/v1/issues/99"); w.Code != 404 {
		t.Errorf("missing: %d", w.Code)
	}
	if w := do("/api/v1/issues/abc"); w.Code != 400 {
		t.Errorf("bad id: %d", w.Code)
	}

	// Without an issue store everything is empty rather than failing.
	r2 := apiRouter(&Handler{log: zap.NewNop()})
	if w := request(r2, "GET", "/api/v1/issues", nil); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("no store: %d %s", w.Code, w.Body)
	}
	if w := request(r2, "GET", "/api/v1/issues/summary", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"by_source":{}`) {
		t.Errorf("no store summary: %d %s", w.Code, w.Body)
	}
}

func TestMetaReportsPublicURLAndNotifications(t *testing.T) {
	r := apiRouter(&Handler{log: zap.NewNop(), publicURL: "https://kymo.example.com", notifEnabled: true})
	var m map[string]any
	w := request(r, "GET", "/api/v1/meta", nil)
	json.Unmarshal(w.Body.Bytes(), &m)
	if m["public_url"] != "https://kymo.example.com" || m["notifications_enabled"] != true {
		t.Errorf("meta: %v", m)
	}
}

type notifRig struct {
	r    *gin.Engine
	repo *notifytest.MemRepo
	svc  *notify.Service
	hook *httptest.Server
	hits int
}

func newNotifRig(t *testing.T, key string) *notifRig {
	t.Helper()
	n := &notifRig{repo: notifytest.NewMemRepo()}
	n.hook = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.hits++
		w.WriteHeader(204)
	}))
	t.Cleanup(n.hook.Close)
	box, _ := secret.New(key)
	n.svc = notify.New(n.repo, nil, box, nil, notify.Options{Enabled: true}, zap.NewNop())
	n.r = apiRouter(&Handler{log: zap.NewNop(), notif: n.svc})
	return n
}

func decode(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %q: %v", w.Body, err)
	}
}

func TestChannelCRUDAndMasking(t *testing.T) {
	n := newNotifRig(t, "key")
	const slack = "https://hooks.slack.com/services/T00000000/B00000000/TOPSECRETTOKEN9876"

	// Create.
	w := request(n.r, "POST", "/api/v1/notifications/channels", map[string]any{
		"name": "ops", "type": "slack", "url": slack, "secret": "hmac", "min_severity": "warning", "repeat_minutes": 30,
		"sources": []string{"prod"},
	})
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "TOPSECRETTOKEN") || strings.Contains(w.Body.String(), "hmac") ||
		strings.Contains(w.Body.String(), `"url"`) || strings.Contains(w.Body.String(), `"secret"`) {
		t.Fatalf("create response leaks the URL/secret: %s", w.Body)
	}
	var ch map[string]any
	decode(t, w, &ch)
	if ch["url_hint"] != "hooks.slack.com/…/9876" || ch["has_secret"] != true || ch["origin"] != "user" || ch["enabled"] != true ||
		ch["min_severity"] != "warning" || ch["repeat_minutes"] != float64(30) || ch["notify_resolved"] != true ||
		ch["include_logs"] != true || ch["last_delivery"] != nil {
		t.Errorf("channel: %v", ch)
	}
	for _, k := range []string{"id", "name", "type", "origin", "enabled", "min_severity", "sources", "namespaces", "types", "notify_resolved",
		"include_logs", "repeat_minutes", "url_hint", "has_secret", "created_at", "updated_at", "last_delivery"} {
		if _, ok := ch[k]; !ok {
			t.Errorf("channel JSON lacks %q", k)
		}
	}
	id := int(ch["id"].(float64))
	path := "/api/v1/notifications/channels/" + itoa(id)

	// List is masked too.
	w = request(n.r, "GET", "/api/v1/notifications/channels", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "TOPSECRET") || !strings.Contains(w.Body.String(), "hooks.slack.com/…/9876") {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}

	// Update keeps URL / secret when omitted.
	w = request(n.r, "PUT", path, map[string]any{"name": "ops-2", "enabled": false})
	var up map[string]any
	decode(t, w, &up)
	if w.Code != 200 || up["name"] != "ops-2" || up["enabled"] != false || up["has_secret"] != true || up["url_hint"] != "hooks.slack.com/…/9876" {
		t.Fatalf("update: %d %s", w.Code, w.Body)
	}
	if w := request(n.r, "PUT", path, map[string]any{"type": "carrier-pigeon"}); w.Code != 400 {
		t.Errorf("bad type: %d", w.Code)
	}
	if w := request(n.r, "PUT", "/api/v1/notifications/channels/999", map[string]any{"name": "x"}); w.Code != 404 {
		t.Errorf("unknown: %d", w.Code)
	}
	if w := request(n.r, "PUT", "/api/v1/notifications/channels/abc", map[string]any{}); w.Code != 400 {
		t.Errorf("bad id: %d", w.Code)
	}

	// Delete.
	if w := request(n.r, "DELETE", path, nil); w.Code != 200 {
		t.Errorf("delete: %d %s", w.Code, w.Body)
	}
	if w := request(n.r, "DELETE", path, nil); w.Code != 404 {
		t.Errorf("delete again: %d", w.Code)
	}
	w = request(n.r, "GET", "/api/v1/notifications/channels", nil)
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("list after delete: %s", w.Body)
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func TestChannelValidationErrors(t *testing.T) {
	n := newNotifRig(t, "key")
	post := func(body map[string]any) *httptest.ResponseRecorder {
		return request(n.r, "POST", "/api/v1/notifications/channels", body)
	}
	for name, body := range map[string]map[string]any{
		"bad type":     {"name": "a", "type": "email", "url": "https://example.com/x"},
		"no url":       {"name": "a", "type": "webhook"},
		"http remote":  {"name": "a", "type": "webhook", "url": "http://example.com/x"},
		"slack host":   {"name": "a", "type": "slack", "url": "https://example.com/services/x"},
		"discord path": {"name": "a", "type": "discord", "url": "https://discord.com/foo"},
		"no name":      {"type": "webhook", "url": "https://example.com/x"},
		"severity":     {"name": "a", "type": "webhook", "url": "https://example.com/x", "min_severity": "info"},
	} {
		w := post(body)
		var e map[string]any
		decode(t, w, &e)
		if w.Code != 400 || e["error"] == "" {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	// Malformed JSON.
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/notifications/channels", strings.NewReader("{"))
	n.r.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("malformed: %d", w.Code)
	}
	if w := post(map[string]any{"name": "ok", "type": "webhook", "url": "http://127.0.0.1:9/x"}); w.Code != 201 {
		t.Errorf("localhost http: %d %s", w.Code, w.Body)
	}
}

func TestCreateWithoutSecretKeyIs400(t *testing.T) {
	n := newNotifRig(t, "")
	w := request(n.r, "POST", "/api/v1/notifications/channels", map[string]any{"name": "a", "type": "webhook", "url": "https://example.com/x"})
	var e map[string]string
	decode(t, w, &e)
	if w.Code != 400 || !strings.Contains(e["error"], "KYMO_SECRET_KEY") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestConfigChannelsAreLocked(t *testing.T) {
	n := newNotifRig(t, "key")
	if err := n.svc.SyncConfig(context.Background(), []config.NotificationChannel{
		{Name: "from-file", Type: "webhook", URL: n.hook.URL + "/plain-token-in-file"},
	}); err != nil {
		t.Fatal(err)
	}
	w := request(n.r, "GET", "/api/v1/notifications/channels", nil)
	var list []map[string]any
	decode(t, w, &list)
	if len(list) != 1 || list[0]["origin"] != "config" || strings.Contains(w.Body.String(), "plain-token-in-file") {
		t.Fatalf("list: %s", w.Body)
	}
	path := "/api/v1/notifications/channels/" + itoa(int(list[0]["id"].(float64)))
	if w := request(n.r, "PUT", path, map[string]any{"name": "x"}); w.Code != 403 {
		t.Errorf("PUT: %d", w.Code)
	}
	if w := request(n.r, "DELETE", path, nil); w.Code != 403 {
		t.Errorf("DELETE: %d", w.Code)
	}
	// Testing a config channel is allowed.
	if w := request(n.r, "POST", path+"/test", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Errorf("test: %d %s", w.Code, w.Body)
	}
}

func TestChannelTestAndDeliveries(t *testing.T) {
	n := newNotifRig(t, "key")
	w := request(n.r, "POST", "/api/v1/notifications/channels", map[string]any{"name": "hook", "type": "webhook", "url": n.hook.URL + "/h"})
	var ch map[string]any
	decode(t, w, &ch)
	id := itoa(int(ch["id"].(float64)))

	w = request(n.r, "POST", "/api/v1/notifications/channels/"+id+"/test", nil)
	var res map[string]any
	decode(t, w, &res)
	if w.Code != 200 || res["ok"] != true || res["status_code"] != float64(204) || res["error"] != "" || n.hits != 1 {
		t.Fatalf("test: %d %s (hits %d)", w.Code, w.Body, n.hits)
	}

	// A failing endpoint is reported in the body, not as an HTTP error.
	n.hook.Close()
	w = request(n.r, "POST", "/api/v1/notifications/channels/"+id+"/test", nil)
	decode(t, w, &res)
	if w.Code != 200 || res["ok"] != false || res["error"] == "" || strings.Contains(w.Body.String(), "/h\"") {
		t.Fatalf("failing test: %d %s", w.Code, w.Body)
	}
	if w := request(n.r, "POST", "/api/v1/notifications/channels/77/test", nil); w.Code != 404 {
		t.Errorf("unknown channel: %d", w.Code)
	}

	w = request(n.r, "GET", "/api/v1/notifications/deliveries?channel_id="+id+"&limit=1", nil)
	var ds []map[string]any
	decode(t, w, &ds)
	if w.Code != 200 || len(ds) != 1 || ds[0]["ok"] != false || ds[0]["event"] != "test" {
		t.Fatalf("deliveries newest first: %s", w.Body)
	}
	w = request(n.r, "GET", "/api/v1/notifications/deliveries", nil)
	decode(t, w, &ds)
	if len(ds) != 2 || ds[1]["ok"] != true || ds[1]["issue_id"] != nil {
		t.Fatalf("all deliveries: %s", w.Body)
	}
	for _, k := range []string{"id", "channel_id", "issue_id", "event", "ok", "status_code", "error", "attempts", "sent_at"} {
		if _, ok := ds[0][k]; !ok {
			t.Errorf("delivery lacks %q", k)
		}
	}
	if w := request(n.r, "GET", "/api/v1/notifications/deliveries?channel_id=x", nil); w.Code != 400 {
		t.Errorf("bad channel_id: %d", w.Code)
	}

	// last_delivery appears in the channel list.
	w = request(n.r, "GET", "/api/v1/notifications/channels", nil)
	var list []map[string]any
	decode(t, w, &list)
	ld, _ := list[0]["last_delivery"].(map[string]any)
	if ld == nil || ld["ok"] != false || ld["event"] != "test" || ld["sent_at"] == nil {
		t.Errorf("last_delivery: %v", list[0]["last_delivery"])
	}
}

func TestNotificationsUnavailable(t *testing.T) {
	r := apiRouter(&Handler{log: zap.NewNop()})
	if w := request(r, "GET", "/api/v1/notifications/channels", nil); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("list: %d %s", w.Code, w.Body)
	}
	if w := request(r, "POST", "/api/v1/notifications/channels", map[string]any{}); w.Code != 503 {
		t.Errorf("create: %d", w.Code)
	}
}
