package notify

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emirhanusta/kymo/internal/config"
	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/secret"
	"github.com/emirhanusta/kymo/internal/service/notify/notifytest"
)

func ptr[T any](v T) *T { return &v }

func newSvc(t *testing.T, key string) (*Service, *notifytest.MemRepo) {
	t.Helper()
	repo := notifytest.NewMemRepo()
	box, err := secret.New(key)
	if err != nil {
		t.Fatal(err)
	}
	return New(repo, nil, box, nil, Options{Enabled: true, Backoff: []time.Duration{0}}, nil), repo
}

func wantInvalid(t *testing.T, err error, contains string) {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(ve.Msg, contains) {
		t.Fatalf("want validation error containing %q, got %v", contains, err)
	}
}

func TestCreateRequiresSecretKey(t *testing.T) {
	s, repo := newSvc(t, "")
	_, err := s.Create(context.Background(), ChannelInput{Name: ptr("ops"), Type: ptr("webhook"), URL: ptr("https://example.com/hook")})
	wantInvalid(t, err, "KYMO_SECRET_KEY")
	if len(repo.Channels) != 0 {
		t.Fatal("nothing must be stored")
	}
}

func TestCreateValidation(t *testing.T) {
	s, _ := newSvc(t, "k")
	ctx := context.Background()
	base := func() ChannelInput {
		return ChannelInput{Name: ptr("ops"), Type: ptr("webhook"), URL: ptr("https://example.com/hook")}
	}
	cases := []struct {
		name string
		mod  func(*ChannelInput)
		msg  string
	}{
		{"no name", func(i *ChannelInput) { i.Name = ptr(" ") }, "name"},
		{"bad type", func(i *ChannelInput) { i.Type = ptr("email") }, "type"},
		{"missing type", func(i *ChannelInput) { i.Type = nil }, "type"},
		{"no url", func(i *ChannelInput) { i.URL = nil }, "url is required"},
		{"blank url", func(i *ChannelInput) { i.URL = ptr("  ") }, "url is required"},
		{"http remote", func(i *ChannelInput) { i.URL = ptr("http://example.com/hook") }, "https"},
		{"ftp", func(i *ChannelInput) { i.URL = ptr("ftp://example.com/hook") }, "https"},
		{"garbage", func(i *ChannelInput) { i.URL = ptr("not a url") }, "URL"},
		{"credentials", func(i *ChannelInput) { i.URL = ptr("https://u:p@example.com/hook") }, "credentials"},
		{"slack host", func(i *ChannelInput) { i.Type = ptr("slack"); i.URL = ptr("https://example.com/services/T/B/X") }, "hooks.slack.com"},
		{"slack http", func(i *ChannelInput) { i.Type = ptr("slack"); i.URL = ptr("http://localhost/services/T") }, "hooks.slack.com"},
		{"discord host", func(i *ChannelInput) { i.Type = ptr("discord"); i.URL = ptr("https://example.com/api/webhooks/1/x") }, "discord.com/api/webhooks"},
		{"discord path", func(i *ChannelInput) { i.Type = ptr("discord"); i.URL = ptr("https://discord.com/channels/1") }, "discord.com/api/webhooks"},
		{"severity", func(i *ChannelInput) { i.MinSeverity = ptr("info") }, "min_severity"},
		{"repeat", func(i *ChannelInput) { i.RepeatMinutes = ptr(-1) }, "repeat_minutes"},
		{"repeat max", func(i *ChannelInput) { i.RepeatMinutes = ptr(10_000_000) }, "repeat_minutes"},
		{"issue type", func(i *ChannelInput) { i.Types = &[]string{"nope"} }, "unknown issue type"},
	}
	for _, c := range cases {
		in := base()
		c.mod(&in)
		_, err := s.Create(ctx, in)
		var ve *ValidationError
		if !errors.As(err, &ve) || !strings.Contains(ve.Msg, c.msg) {
			t.Errorf("%s: got %v, want validation error containing %q", c.name, err, c.msg)
		}
	}

	for _, ok := range []ChannelInput{
		{Name: ptr("a"), Type: ptr("slack"), URL: ptr("https://hooks.slack.com/services/T000/B000/XXXXXXXX")},
		{Name: ptr("b"), Type: ptr("discord"), URL: ptr("https://discord.com/api/webhooks/123/token")},
		{Name: ptr("c"), Type: ptr("webhook"), URL: ptr("http://localhost:9000/hook")},
		{Name: ptr("d"), Type: ptr("webhook"), URL: ptr("http://127.0.0.1:9000/hook")},
		{Name: ptr("e"), Type: ptr("webhook"), URL: ptr("http://[::1]:9000/hook")},
	} {
		if _, err := s.Create(ctx, ok); err != nil {
			t.Errorf("%s: %v", *ok.Name, err)
		}
	}
}

func TestCreateDefaultsEncryptionAndMasking(t *testing.T) {
	s, repo := newSvc(t, "k")
	ctx := context.Background()
	const raw = "https://hooks.slack.com/services/T000/B000/SECRETSECRET1234"
	v, err := s.Create(ctx, ChannelInput{Name: ptr("ops"), Type: ptr("slack"), URL: ptr(raw), Secret: ptr("hmac-secret")})
	if err != nil {
		t.Fatal(err)
	}
	if v.ID == 0 || v.Origin != "user" || !v.Enabled || v.MinSeverity != "critical" || !v.NotifyResolved || !v.IncludeLogs ||
		v.RepeatMinutes != 0 || v.Sources == nil || v.Namespaces == nil || v.Types == nil || !v.HasSecret {
		t.Errorf("view: %+v", v)
	}
	if v.URLHint != "hooks.slack.com/…/1234" || strings.Contains(v.URLHint, "SECRETSECRET") {
		t.Errorf("hint %q", v.URLHint)
	}
	stored := repo.Channels[v.ID]
	if bytes.Contains(stored.URLEnc, []byte("hooks.slack.com")) || bytes.Contains(stored.SecretEnc, []byte("hmac-secret")) {
		t.Fatal("URL and secret must be encrypted at rest")
	}
	u, sec, err := s.plain(stored)
	if err != nil || u != raw || sec != "hmac-secret" {
		t.Fatalf("round trip: %q %q %v", u, sec, err)
	}

	// A different key cannot read it (and the view degrades gracefully).
	other, _ := secret.New("other-key")
	s2 := New(repo, nil, other, nil, Options{}, nil)
	if _, _, err := s2.plain(stored); err == nil {
		t.Error("wrong key must fail")
	}
	list, err := s2.ListChannels(ctx)
	if err != nil || len(list) != 1 || list[0].URLHint != "" {
		t.Errorf("list with wrong key: %+v %v", list, err)
	}
}

func TestUpdateKeepsOmittedFields(t *testing.T) {
	s, repo := newSvc(t, "k")
	ctx := context.Background()
	v, _ := s.Create(ctx, ChannelInput{
		Name: ptr("ops"), Type: ptr("webhook"), URL: ptr("https://example.com/hook/abcdef"), Secret: ptr("s"),
		MinSeverity: ptr("warning"), Sources: &[]string{"prod"}, RepeatMinutes: ptr(30), NotifyResolved: ptr(false),
	})
	oldURL := append([]byte(nil), repo.Channels[v.ID].URLEnc...)

	up, err := s.Update(ctx, v.ID, ChannelInput{Name: ptr("renamed"), Enabled: ptr(false)})
	if err != nil {
		t.Fatal(err)
	}
	if up.Name != "renamed" || up.Enabled || up.MinSeverity != "warning" || up.RepeatMinutes != 30 || up.NotifyResolved ||
		len(up.Sources) != 1 || !up.HasSecret || up.URLHint != "example.com/…/cdef" {
		t.Errorf("update: %+v", up)
	}
	if !bytes.Equal(repo.Channels[v.ID].URLEnc, oldURL) {
		t.Error("url must be kept when omitted")
	}

	// Explicit empty secret clears it; empty url keeps the url; lists can be emptied.
	up, err = s.Update(ctx, v.ID, ChannelInput{Secret: ptr(""), URL: ptr(""), Sources: &[]string{}})
	if err != nil || up.HasSecret || len(up.Sources) != 0 || up.URLHint != "example.com/…/cdef" {
		t.Errorf("clear: %+v %v", up, err)
	}

	// Changing the type validates the stored URL against it.
	_, err = s.Update(ctx, v.ID, ChannelInput{Type: ptr("slack")})
	wantInvalid(t, err, "hooks.slack.com")
	// A new URL replaces the old one.
	up, err = s.Update(ctx, v.ID, ChannelInput{URL: ptr("https://other.example.com/zzzzzzzz1")})
	if err != nil || up.URLHint != "other.example.com/…/zzz1" {
		t.Errorf("new url: %+v %v", up, err)
	}
	if _, err := s.Update(ctx, 404, ChannelInput{}); !errors.Is(err, ErrChannelNotFound) {
		t.Errorf("unknown: %v", err)
	}
}

func TestDeleteAndLastDelivery(t *testing.T) {
	s, repo := newSvc(t, "k")
	ctx := context.Background()
	v, _ := s.Create(ctx, ChannelInput{Name: ptr("ops"), Type: ptr("webhook"), URL: ptr("http://127.0.0.1:1/hook")})
	res, err := s.Test(ctx, v.ID)
	if err != nil || res.OK || res.Error == "" {
		t.Fatalf("test against a closed port: %+v %v", res, err)
	}
	list, _ := s.ListChannels(ctx)
	ld := list[0].LastDelivery
	if ld == nil || ld.OK || ld.Event != model.EventTest || ld.Error == "" || ld.SentAt.IsZero() {
		t.Errorf("last_delivery: %+v", ld)
	}
	if err := s.Delete(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if len(repo.Channels) != 0 || len(repo.All()) != 0 {
		t.Error("channel and its deliveries should be gone")
	}
	if err := s.Delete(ctx, v.ID); !errors.Is(err, ErrChannelNotFound) {
		t.Errorf("second delete: %v", err)
	}
}

func TestSyncConfigChannels(t *testing.T) {
	s, repo := newSvc(t, "k")
	ctx := context.Background()
	user, _ := s.Create(ctx, ChannelInput{Name: ptr("mine"), Type: ptr("webhook"), URL: ptr("https://example.com/h")})

	declared := []config.NotificationChannel{
		{Name: "slack-ops", Type: "slack", URL: "https://hooks.slack.com/services/T/B/ABCDEFGH", MinSeverity: "warning", RepeatMinutes: 15, Types: []string{"oom"}},
		{Name: "hook", Type: "webhook", URL: "https://example.com/in", Secret: "sec", Enabled: ptr(false), NotifyResolved: ptr(false), IncludeLogs: ptr(false)},
	}
	if err := s.SyncConfig(ctx, declared); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListChannels(ctx)
	if len(list) != 3 {
		t.Fatalf("channels: %+v", list)
	}
	byName := map[string]ChannelView{}
	for _, c := range list {
		byName[c.Name] = c
	}
	so, h := byName["slack-ops"], byName["hook"]
	if so.Origin != "config" || so.MinSeverity != "warning" || so.RepeatMinutes != 15 || !so.Enabled || !so.NotifyResolved ||
		!so.IncludeLogs || so.Types[0] != "oom" || so.URLHint != "hooks.slack.com/…/EFGH" || so.HasSecret {
		t.Errorf("slack-ops: %+v", so)
	}
	if h.Enabled || h.NotifyResolved || h.IncludeLogs || !h.HasSecret || h.MinSeverity != "critical" {
		t.Errorf("hook: %+v", h)
	}
	// Locked in the API.
	if _, err := s.Update(ctx, so.ID, ChannelInput{Name: ptr("x")}); !errors.Is(err, ErrChannelLocked) {
		t.Errorf("update config channel: %v", err)
	}
	if err := s.Delete(ctx, so.ID); !errors.Is(err, ErrChannelLocked) {
		t.Errorf("delete config channel: %v", err)
	}

	// Re-sync: unchanged keeps ids and updated_at; changed updates in place; removed disappears; user channel untouched.
	declared[0].MinSeverity = "critical"
	declared = declared[:1]
	updatedBefore := repo.Channels[so.ID].UpdatedAt
	if err := s.SyncConfig(ctx, declared); err != nil {
		t.Fatal(err)
	}
	list, _ = s.ListChannels(ctx)
	if len(list) != 2 {
		t.Fatalf("after removal: %+v", list)
	}
	if got := repo.Channels[so.ID]; got == nil || got.MinSeverity != model.SeverityCritical || !got.UpdatedAt.After(updatedBefore) && !got.UpdatedAt.Equal(updatedBefore) {
		t.Errorf("slack-ops after change: %+v", got)
	}
	if repo.Channels[user.ID] == nil {
		t.Error("user channel must survive a config sync")
	}
	stamp := repo.Channels[so.ID].UpdatedAt
	if err := s.SyncConfig(ctx, declared); err != nil || !repo.Channels[so.ID].UpdatedAt.Equal(stamp) {
		t.Error("identical sync must not rewrite rows")
	}
}

func TestURLHint(t *testing.T) {
	for in, want := range map[string]string{
		"https://hooks.slack.com/services/T0/B0/abcdWXYZ": "hooks.slack.com/…/WXYZ",
		"https://example.com/":                            "example.com/…",
		"http://localhost:8080/hook":                      "localhost:8080/…",
		"bad":                                             "",
	} {
		if got := URLHint(in); got != want {
			t.Errorf("%s: %q want %q", in, got, want)
		}
	}
}
