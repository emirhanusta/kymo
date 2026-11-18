package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/emirhanusta/kymo/internal/auth/authtest"
	"github.com/emirhanusta/kymo/internal/config"
	"github.com/emirhanusta/kymo/internal/model"
)

func newSvc(t *testing.T, opts Options) (*Service, *authtest.Mem) {
	t.Helper()
	repo := authtest.New()
	opts.Enabled = true
	s, err := New(repo, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s, repo
}

func mkUser(t *testing.T, repo *authtest.Mem, email string, role model.Role) *model.User {
	t.Helper()
	u := &model.User{Username: SanitizeUsername(strings.SplitN(email, "@", 2)[0]), Email: email, Name: email, Role: role}
	if err := repo.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return u
}

func login(t *testing.T, s *Service, u *model.User) *http.Cookie {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/", nil)
	if err := s.StartSession(w, r, u, model.SessionPassword, "1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == SessionCookie {
			return c
		}
	}
	t.Fatal("no session cookie")
	return nil
}

func reqWith(c *http.Cookie, bearer string) *http.Request {
	r := httptest.NewRequest("GET", "/api/v1/x", nil)
	if c != nil {
		r.AddCookie(c)
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	return r
}

func TestSessionCookieFlags(t *testing.T) {
	for _, tc := range []struct {
		url    string
		secure bool
	}{{"https://kymo.example.com", true}, {"http://localhost:8080", false}, {"", false}} {
		s, repo := newSvc(t, Options{PublicURL: tc.url})
		c := login(t, s, mkUser(t, repo, "a@x.io", model.RoleViewer))
		if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Secure != tc.secure {
			t.Errorf("%q: cookie flags %+v", tc.url, c)
		}
		if len(c.Value) < 40 {
			t.Errorf("cookie value too short: %q", c.Value)
		}
		if c.MaxAge != int((30 * 24 * time.Hour).Seconds()) {
			t.Errorf("max age %d", c.MaxAge)
		}
	}
}

func TestSessionLifecycle(t *testing.T) {
	s, repo := newSvc(t, Options{IdleTimeout: 7 * 24 * time.Hour, MaxAge: 30 * 24 * time.Hour})
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	u := mkUser(t, repo, "a@x.io", model.RoleOperator)
	c := login(t, s, u)

	id, err := s.Resolve(reqWith(c, ""))
	if err != nil || id == nil || id.User.ID != u.ID || id.Role != model.RoleOperator || id.Via != ViaSession {
		t.Fatalf("resolve: %+v %v", id, err)
	}
	// Anonymous.
	if id, err := s.Resolve(reqWith(nil, "")); id != nil || err != nil {
		t.Errorf("anonymous: %v %v", id, err)
	}
	// Unknown cookie.
	if _, err := s.Resolve(reqWith(&http.Cookie{Name: SessionCookie, Value: "nope"}, "")); err != ErrUnauthenticated {
		t.Errorf("unknown cookie: %v", err)
	}
	// Sliding: activity 6 days later extends beyond day 7.
	now = now.Add(6 * 24 * time.Hour)
	if _, err := s.Resolve(reqWith(c, "")); err != nil {
		t.Fatalf("day 6: %v", err)
	}
	now = now.Add(6 * 24 * time.Hour) // day 12 > initial 7d, but extended
	if _, err := s.Resolve(reqWith(c, "")); err != nil {
		t.Fatalf("sliding failed on day 12: %v", err)
	}
	// Idle for more than 7 days expires.
	now = now.Add(8 * 24 * time.Hour)
	if _, err := s.Resolve(reqWith(c, "")); err != ErrUnauthenticated {
		t.Errorf("idle session must expire: %v", err)
	}
	// Absolute 30 days: keep it active daily; it still ends at day 30.
	now = time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	c2 := login(t, s, u)
	start := now
	for d := 1; d <= 29; d++ {
		now = start.Add(time.Duration(d) * 24 * time.Hour)
		if _, err := s.Resolve(reqWith(c2, "")); err != nil {
			t.Fatalf("day %d: %v", d, err)
		}
	}
	now = start.Add(30*24*time.Hour + time.Minute)
	if _, err := s.Resolve(reqWith(c2, "")); err != ErrUnauthenticated {
		t.Errorf("absolute lifetime must end the session: %v", err)
	}
}

func TestSessionRotationAndRevocation(t *testing.T) {
	s, repo := newSvc(t, Options{})
	u := mkUser(t, repo, "a@x.io", model.RoleAdmin)
	old := login(t, s, u)
	// Logging in again with the old cookie rotates: the old id dies.
	w := httptest.NewRecorder()
	r := reqWith(old, "")
	if err := s.StartSession(w, r, u, model.SessionPassword, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve(reqWith(old, "")); err != ErrUnauthenticated {
		t.Errorf("old session id must be revoked on login: %v", err)
	}
	var fresh *http.Cookie
	for _, c := range w.Result().Cookies() {
		fresh = c
	}
	if fresh == nil || fresh.Value == old.Value {
		t.Fatal("session id must change")
	}
	if _, err := s.Resolve(reqWith(fresh, "")); err != nil {
		t.Errorf("new session: %v", err)
	}
	// Revoke-all keeps only the excepted one.
	other := login(t, s, u)
	s.RevokeSessions(context.Background(), u.ID, HashToken(fresh.Value))
	if _, err := s.Resolve(reqWith(other, "")); err != ErrUnauthenticated {
		t.Error("other session must be revoked")
	}
	if _, err := s.Resolve(reqWith(fresh, "")); err != nil {
		t.Error("kept session must survive")
	}
	// Disabled user's session stops working immediately.
	st := model.UserDisabled
	_ = repo.UpdateUser(context.Background(), u.ID, model.UserUpdate{Status: &st})
	if _, err := s.Resolve(reqWith(fresh, "")); err != ErrUnauthenticated {
		t.Error("disabled user must be rejected")
	}
	// Logout clears the cookie.
	w = httptest.NewRecorder()
	s.EndSession(w, reqWith(fresh, ""))
	if c := w.Result().Cookies(); len(c) != 1 || c[0].MaxAge >= 0 {
		t.Errorf("logout cookie: %+v", c)
	}
}

func TestAPITokenAuth(t *testing.T) {
	s, repo := newSvc(t, Options{})
	ctx := context.Background()
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	u := mkUser(t, repo, "a@x.io", model.RoleOperator)
	mk := func(role model.Role, exp *time.Time) (string, *model.APIToken) {
		tok, prefix, _ := NewAPIToken()
		tk := &model.APIToken{UserID: u.ID, Name: "ci", Prefix: prefix, Role: role, TokenHash: HashToken(tok), ExpiresAt: exp}
		if err := repo.CreateToken(ctx, tk); err != nil {
			t.Fatal(err)
		}
		return tok, tk
	}
	tok, tk := mk(model.RoleOperator, nil)
	id, err := s.Resolve(reqWith(nil, tok))
	if err != nil || id.Via != ViaToken || id.Role != model.RoleOperator || id.Label() != "token:ci" || id.UserID() != u.ID {
		t.Fatalf("token auth: %+v %v", id, err)
	}
	// Role is capped by the owner's current role.
	admin, _ := mk(model.RoleAdmin, nil)
	if id, _ := s.Resolve(reqWith(nil, admin)); id.Role != model.RoleOperator {
		t.Errorf("token role must be capped by user role, got %s", id.Role)
	}
	// Bad / revoked / expired / disabled owner.
	if _, err := s.Resolve(reqWith(nil, "kymo_deadbeef_"+"x1234567890123456789012345")); err != ErrUnauthenticated {
		t.Errorf("unknown token: %v", err)
	}
	if _, err := s.Resolve(reqWith(nil, "garbage")); err != ErrUnauthenticated {
		t.Errorf("garbage token: %v", err)
	}
	past := now.Add(-time.Hour)
	expired, _ := mk(model.RoleViewer, &past)
	if _, err := s.Resolve(reqWith(nil, expired)); err != ErrUnauthenticated {
		t.Errorf("expired token: %v", err)
	}
	_ = repo.RevokeToken(ctx, tk.ID, now)
	if _, err := s.Resolve(reqWith(nil, tok)); err != ErrUnauthenticated {
		t.Errorf("revoked token: %v", err)
	}
	live, _ := mk(model.RoleViewer, nil)
	st := model.UserDisabled
	_ = repo.UpdateUser(ctx, u.ID, model.UserUpdate{Status: &st})
	if _, err := s.Resolve(reqWith(nil, live)); err != ErrUnauthenticated {
		t.Errorf("disabled owner: %v", err)
	}
	// Non-bearer Authorization is rejected, not ignored.
	r := reqWith(nil, "")
	r.Header.Set("Authorization", "Basic abc")
	if _, err := s.Resolve(r); err != ErrUnauthenticated {
		t.Errorf("basic auth: %v", err)
	}
}

func TestAuthDisabledResolvesLocalOwner(t *testing.T) {
	s, err := New(authtest.New(), Options{Enabled: false}, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Resolve(reqWith(nil, ""))
	if err != nil || id.Role != model.RoleOwner || id.User.ID != 0 || id.User.Name != "local" || id.Via != ViaDisabled {
		t.Fatalf("%+v %v", id, err)
	}
}

// logCapture returns a service whose log lines are collected.
func logCapture(t *testing.T) (*Service, *authtest.Mem, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zapcore.InfoLevel)
	repo := authtest.New()
	s, err := New(repo, Options{Enabled: true}, zap.New(core))
	if err != nil {
		t.Fatal(err)
	}
	return s, repo, logs
}

func TestBootstrapRandomAdmin(t *testing.T) {
	s, repo, logs := logCapture(t)
	ctx := context.Background()
	ok, err := s.BootstrapAdmin(ctx, BootstrapOptions{})
	if err != nil || !ok {
		t.Fatalf("bootstrap: %v %v", ok, err)
	}
	u, err := repo.GetUserByUsername(ctx, "admin")
	if err != nil || u.Role != model.RoleOwner || !u.MustChangePassword || u.Email != "" {
		t.Fatalf("owner: %+v %v", u, err)
	}
	entries := logs.FilterMessageSnippet("initial admin").All()
	if len(entries) != 1 {
		t.Fatalf("want exactly one 'initial admin' log line, got %d", len(entries))
	}
	msg := entries[0].Message
	m := regexp.MustCompile(`^initial admin account created: username "admin", password "([A-Za-z0-9]{24})" — sign in and change it$`).FindStringSubmatch(msg)
	if m == nil {
		t.Fatalf("log line: %q", msg)
	}
	if !VerifyPassword(m[1], u.PasswordHash) {
		t.Error("logged password does not match")
	}
	// Second start: users exist -> nothing, no second log line.
	if ok, _ := s.BootstrapAdmin(ctx, BootstrapOptions{}); ok || len(logs.FilterMessageSnippet("initial admin").All()) != 1 {
		t.Error("bootstrap must not run twice")
	}
}

func TestBootstrapFromEnv(t *testing.T) {
	s, repo, logs := logCapture(t)
	ctx := context.Background()
	if _, err := s.BootstrapAdmin(ctx, BootstrapOptions{Username: "bad name", Password: "longenoughpassword"}); err == nil {
		t.Error("bad username")
	}
	if _, err := s.BootstrapAdmin(ctx, BootstrapOptions{Email: "nope", Password: "longenoughpassword"}); err == nil {
		t.Error("bad email")
	}
	if _, err := s.BootstrapAdmin(ctx, BootstrapOptions{Password: "short"}); err == nil {
		t.Error("short password")
	}
	ok, err := s.BootstrapAdmin(ctx, BootstrapOptions{Username: "Boss", Email: "Boss@X.io", Name: "The Boss", Password: "longenoughpassword"})
	if err != nil || !ok {
		t.Fatalf("bootstrap: %v %v", ok, err)
	}
	u, _ := repo.GetUserByUsername(ctx, "boss")
	if u.Role != model.RoleOwner || u.Email != "boss@x.io" || u.Name != "The Boss" || u.MustChangePassword || !VerifyPassword("longenoughpassword", u.PasswordHash) {
		t.Errorf("owner: %+v", u)
	}
	for _, e := range logs.All() {
		if strings.Contains(e.Message, "longenoughpassword") {
			t.Error("the configured password must never be logged")
		}
	}
	// Env is ignored once users exist.
	if ok, _ := s.BootstrapAdmin(ctx, BootstrapOptions{Username: "other", Password: "longenoughpassword"}); ok {
		t.Error("bootstrap must not run when users exist")
	}
	if n, _ := repo.CountUsers(ctx); n != 1 {
		t.Errorf("users: %d", n)
	}

	// MustChange honoured with an explicit password; default username is admin.
	s2, repo2, _ := logCapture(t)
	if ok, err := s2.BootstrapAdmin(ctx, BootstrapOptions{Password: "longenoughpassword", MustChange: true}); err != nil || !ok {
		t.Fatal(err)
	}
	a, _ := repo2.GetUserByUsername(ctx, "admin")
	if a == nil || !a.MustChangePassword {
		t.Errorf("admin: %+v", a)
	}
}

func TestBootstrapDisabledAuth(t *testing.T) {
	s, err := New(authtest.New(), Options{Enabled: false}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.BootstrapAdmin(context.Background(), BootstrapOptions{Password: "longenoughpassword"}); ok || err != nil {
		t.Errorf("auth disabled: %v %v", ok, err)
	}
}

func TestResetPassword(t *testing.T) {
	s, repo := newSvc(t, Options{})
	ctx := context.Background()
	u := mkUser(t, repo, "a@x.io", model.RoleViewer)
	login(t, s, u)
	if err := ResetPassword(ctx, repo, "", "A@x.io", "short", s); err == nil {
		t.Error("weak password")
	}
	if err := ResetPassword(ctx, repo, "", "nobody@x.io", "a-long-enough-password", s); err == nil {
		t.Error("unknown user")
	}
	mc := true
	_ = repo.UpdateUser(ctx, u.ID, model.UserUpdate{MustChangePassword: &mc})
	if err := ResetPassword(ctx, repo, "nobody", "", "a-long-enough-password", s); err == nil {
		t.Error("unknown username")
	}
	if err := ResetPassword(ctx, repo, "User", "", "a-long-enough-password", s); err != nil {
		t.Fatalf("by username: %v", err)
	}
	if err := ResetPassword(ctx, repo, "", "A@x.io", "a-long-enough-password", s); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.GetUser(ctx, u.ID)
	if got.MustChangePassword {
		t.Error("a reset clears (never sets) must_change_password")
	}
	if !VerifyPassword("a-long-enough-password", got.PasswordHash) || repo.SessionCount(u.ID) != 0 {
		t.Error("password not set or sessions not revoked")
	}
	if ev := repo.Audit(); len(ev) < 1 || ev[0].Action != "user.password_reset" || ev[0].ActorLabel != "cli" {
		t.Errorf("audit: %+v", ev)
	}
}

func TestAuditRecord(t *testing.T) {
	s, repo := newSvc(t, Options{})
	ctx := context.Background()
	s.Record(ctx, SystemActor, "source.config_reload", "config", "x", map[string]any{"added": []string{"a"}})
	s.Record(ctx, Actor{UserID: 4, Label: "a@x.io", IP: "9.9.9.9"}, "pod.restart", "pod", "ns/p", nil)
	ev := repo.Audit()
	if len(ev) != 2 || ev[0].ActorUserID != nil || ev[0].ActorLabel != "system" || ev[1].ActorUserID == nil || *ev[1].ActorUserID != 4 || ev[1].IP != "9.9.9.9" ||
		string(ev[1].Details) != "{}" {
		t.Errorf("events: %+v", ev)
	}
}

func TestNewRejectsOIDCWithoutPublicURL(t *testing.T) {
	_, err := New(authtest.New(), Options{Enabled: true, OIDC: config.OIDCConfig{Enabled: true, Issuer: "https://i", ClientID: "c", ClientSecret: "s"}}, nil)
	if err == nil {
		t.Fatal("OIDC without public_url must fail")
	}
}
