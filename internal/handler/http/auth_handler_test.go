package httphandler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/auth"
	"github.com/emirhanusta/kymo/internal/auth/authtest"
	"github.com/emirhanusta/kymo/internal/config"
	"github.com/emirhanusta/kymo/internal/model"
)

const pw = "a-very-long-password"

type env struct {
	t    *testing.T
	r    *gin.Engine
	repo *authtest.Mem
	svc  *auth.Service
}

func newEnv(t *testing.T, mut func(*auth.Options)) *env {
	t.Helper()
	gin.SetMode(gin.TestMode)
	repo := authtest.New()
	opts := auth.Options{Enabled: true, PublicURL: "https://kymo.example.com"}
	if mut != nil {
		mut(&opts)
	}
	svc, err := auth.New(repo, opts, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.UseRawPath, r.UnescapePathValues = true, true
	r.Use(auth.CORS(opts.AllowedOrigins), auth.CSRF(opts.PublicURL, opts.AllowedOrigins))
	New(nil, nil, nil, nil, zap.NewNop(), r, WithAuth(svc))
	return &env{t: t, r: r, repo: repo, svc: svc}
}

type reqOpt func(*http.Request)

func withCookie(c *http.Cookie) reqOpt { return func(r *http.Request) { r.AddCookie(c) } }
func withBearer(tok string) reqOpt {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
}
func withHeader(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }
func noCSRF() reqOpt                { return func(r *http.Request) { r.Header.Del(auth.CSRFHeader) } }

func (e *env) do(method, path string, body any, opts ...reqOpt) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(auth.CSRFHeader, "1")
	req.RemoteAddr = "10.0.0.1:1234"
	for _, o := range opts {
		o(req)
	}
	w := httptest.NewRecorder()
	e.r.ServeHTTP(w, req)
	return w
}

func sessionCookie(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.SessionCookie && c.MaxAge >= 0 {
			return c
		}
	}
	return nil
}

func errCode(w *httptest.ResponseRecorder) string {
	var b struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &b)
	return b.Code
}

func (e *env) want(w *httptest.ResponseRecorder, status int) {
	e.t.Helper()
	if w.Code != status {
		e.t.Fatalf("status %d, want %d: %s", w.Code, status, w.Body.String())
	}
}

// addUser inserts a user with a password and returns a logged-in cookie.
// A name containing "@" is an email whose local part becomes the username;
// otherwise it is the username and the user has no email.
func (e *env) addUser(who string, role model.Role) (*model.User, *http.Cookie) {
	e.t.Helper()
	username, email := who, ""
	if strings.Contains(who, "@") {
		username, email = auth.SanitizeUsername(strings.SplitN(who, "@", 2)[0]), who
	}
	u := &model.User{Username: username, Email: email, Name: who, Role: role, PasswordHash: testHash()}
	if role == model.RoleOwner {
		if err := e.repo.CreateFirstOwner(context.Background(), u); err != nil {
			e.t.Fatal(err)
		}
	} else if err := e.repo.CreateUser(context.Background(), u); err != nil {
		e.t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	if err := e.svc.StartSession(rec, httptest.NewRequest("POST", "/", nil), u, model.SessionPassword, "10.0.0.1"); err != nil {
		e.t.Fatal(err)
	}
	return u, sessionCookie(rec)
}

var (
	hashOnce sync.Once
	hashVal  string
)

// testHash hashes the shared test password once (argon2id is deliberately slow).
func testHash() string {
	hashOnce.Do(func() { hashVal, _ = auth.HashPassword(pw) })
	return hashVal
}

func TestAuthConfigAndSessionBasics(t *testing.T) {
	e := newEnv(t, nil)
	w := e.do("GET", "/api/v1/auth/config", nil)
	e.want(w, 200)
	var raw map[string]any
	decode(t, w, &raw)
	if _, has := raw["needs_setup"]; has {
		t.Error("needs_setup must be gone from /auth/config")
	}
	if raw["auth_enabled"] != true || raw["password_login"] != true {
		t.Errorf("config: %v", raw)
	}
	// The setup endpoint no longer exists.
	if w := e.do("POST", "/api/v1/auth/setup", map[string]string{"code": "x"}); w.Code != 404 {
		t.Errorf("setup endpoint: %d", w.Code)
	}

	_, ck := e.addUser("boss@example.com", model.RoleOwner)
	w = e.do("GET", "/api/v1/auth/me", nil, withCookie(ck))
	e.want(w, 200)
	var me struct {
		AuthEnabled bool       `json:"auth_enabled"`
		User        model.User `json:"user"`
		Permissions []string   `json:"permissions"`
	}
	decode(t, w, &me)
	if !me.AuthEnabled || me.User.Username != "boss" || me.User.Email != "boss@example.com" || me.User.Role != model.RoleOwner || len(me.Permissions) != 8 ||
		strings.Contains(w.Body.String(), "argon2") || !strings.Contains(w.Body.String(), `"must_change_password":false`) {
		t.Errorf("me: %+v %s", me, w.Body)
	}
	if w := e.do("GET", "/api/v1/auth/me", nil); w.Code != 401 || errCode(w) != "unauthenticated" {
		t.Errorf("anonymous me: %d %s", w.Code, w.Body)
	}
	w = e.do("POST", "/api/v1/auth/logout", nil, withCookie(ck))
	e.want(w, 204)
	if w := e.do("GET", "/api/v1/auth/me", nil, withCookie(ck)); w.Code != 401 {
		t.Errorf("session must be dead after logout: %d", w.Code)
	}
	if w := e.do("POST", "/api/v1/auth/logout", nil); w.Code != 204 {
		t.Errorf("anonymous logout is idempotent: %d", w.Code)
	}
}

func TestLoginLogoutAndRateLimit(t *testing.T) {
	e := newEnv(t, nil)
	u, _ := e.addUser("a@example.com", model.RoleViewer)

	w := e.do("POST", "/api/v1/auth/login", map[string]string{"login": "A@Example.com", "password": pw})
	e.want(w, 200)
	if ck := sessionCookie(w); ck == nil {
		t.Fatal("no cookie")
	}
	// Generic failure: same status/code/message for unknown user and wrong password.
	w1 := e.do("POST", "/api/v1/auth/login", map[string]string{"login": "a@example.com", "password": "wrong-wrong-wrong"})
	w2 := e.do("POST", "/api/v1/auth/login", map[string]string{"login": "ghost@example.com", "password": "wrong-wrong-wrong"})
	if w1.Code != 401 || w2.Code != 401 || w1.Body.String() != w2.Body.String() || errCode(w1) != "invalid_credentials" {
		t.Errorf("failures must be indistinguishable: %s | %s", w1.Body, w2.Body)
	}
	// SSO-only / disabled accounts fail the same way.
	st := model.UserDisabled
	_ = e.repo.UpdateUser(context.Background(), u.ID, model.UserUpdate{Status: &st})
	if w := e.do("POST", "/api/v1/auth/login", map[string]string{"login": "a@example.com", "password": pw}); w.Code != 401 {
		t.Errorf("disabled login: %d", w.Code)
	}
	actions := map[string]int{}
	for _, ev := range e.repo.Audit() {
		actions[ev.Action]++
	}
	if actions["login.success"] != 1 || actions["login.failure"] != 3 {
		t.Errorf("audit: %v", actions)
	}

	// 10 failures per email -> 429 even for the right password.
	e2 := newEnv(t, nil)
	e2.addUser("v@example.com", model.RoleViewer)
	for i := 0; i < 10; i++ {
		e2.do("POST", "/api/v1/auth/login", map[string]string{"login": "v@example.com", "password": "nope-nope-nope-nope"})
	}
	w = e2.do("POST", "/api/v1/auth/login", map[string]string{"login": "v@example.com", "password": pw})
	if w.Code != 429 || errCode(w) != "rate_limited" || w.Header().Get("Retry-After") == "" {
		t.Errorf("rate limit: %d %s", w.Code, w.Body)
	}
	// One account's failures must not lock out other users behind the same IP
	// (office NAT, proxy): the per-IP budget is larger.
	if w := e2.do("POST", "/api/v1/auth/login", map[string]string{"login": "other@example.com", "password": pw}); w.Code == 429 {
		t.Errorf("same IP, other email must not be blocked yet: %d", w.Code)
	}
	for i := 0; i < auth.LoginMaxAttemptsPerIP; i++ {
		e2.do("POST", "/api/v1/auth/login", map[string]string{"login": fmt.Sprintf("x%d@example.com", i), "password": "nope-nope-nope-nope"})
	}
	if w := e2.do("POST", "/api/v1/auth/login", map[string]string{"login": "other@example.com", "password": pw}); w.Code != 429 {
		t.Errorf("per-ip limit: %d", w.Code)
	}
	if w := e2.do("POST", "/api/v1/auth/login", map[string]string{"login": "other@example.com", "password": pw}, func(r *http.Request) { r.RemoteAddr = "10.9.9.9:1" }); w.Code == 429 {
		t.Error("a different IP must not be blocked")
	}
}

func TestLoginRotatesSession(t *testing.T) {
	e := newEnv(t, nil)
	_, old := e.addUser("a@example.com", model.RoleViewer)
	w := e.do("POST", "/api/v1/auth/login", map[string]string{"login": "a@example.com", "password": pw}, withCookie(old))
	e.want(w, 200)
	if w := e.do("GET", "/api/v1/auth/me", nil, withCookie(old)); w.Code != 401 {
		t.Errorf("old session id must die on login: %d", w.Code)
	}
}

func TestPasswordLoginDisabledWithOIDC(t *testing.T) {
	idp := authtest.NewFakeIdP(t)
	e := newEnv(t, func(o *auth.Options) {
		o.OIDC = config.OIDCConfig{Enabled: true, Name: "Fake", Issuer: idp.Issuer(), ClientID: idp.ClientID, ClientSecret: idp.ClientSecret,
			Scopes: []string{"openid", "email"}, DefaultRole: "viewer", AutoCreate: true, DisablePasswordLogin: true}
	})
	if w := e.do("POST", "/api/v1/auth/login", map[string]string{"login": "a@example.com", "password": pw}); w.Code != 403 || errCode(w) != "password_login_disabled" {
		t.Errorf("%d %s", w.Code, w.Body)
	}
	var cfg struct {
		PasswordLogin bool `json:"password_login"`
		OIDC          struct {
			Enabled bool   `json:"enabled"`
			Name    string `json:"name"`
		} `json:"oidc"`
	}
	decode(t, e.do("GET", "/api/v1/auth/config", nil), &cfg)
	if cfg.PasswordLogin || !cfg.OIDC.Enabled || cfg.OIDC.Name != "Fake" {
		t.Errorf("%+v", cfg)
	}
}

func TestAuthDisabledMode(t *testing.T) {
	e := newEnv(t, func(o *auth.Options) { o.Enabled = false })
	w := e.do("GET", "/api/v1/auth/me", nil)
	e.want(w, 200)
	var me struct {
		AuthEnabled bool       `json:"auth_enabled"`
		User        model.User `json:"user"`
		Permissions []string   `json:"permissions"`
	}
	decode(t, w, &me)
	if me.AuthEnabled || me.User.ID != 0 || me.User.Name != "local" || me.User.Role != model.RoleOwner || len(me.Permissions) != 8 {
		t.Errorf("%+v", me)
	}
	var cfg struct {
		AuthEnabled bool `json:"auth_enabled"`
	}
	decode(t, e.do("GET", "/api/v1/auth/config", nil), &cfg)
	if cfg.AuthEnabled {
		t.Errorf("%+v", cfg)
	}
	if w := e.do("POST", "/api/v1/auth/login", map[string]string{"login": "a@b.co", "password": pw}); w.Code != 409 {
		t.Errorf("login when disabled: %d", w.Code)
	}
	if w := e.do("GET", "/api/v1/users", nil); w.Code != 200 {
		t.Errorf("users: %d", w.Code)
	}
}

func TestUserManagementRules(t *testing.T) {
	e := newEnv(t, nil)
	owner, ownerCk := e.addUser("owner@example.com", model.RoleOwner)
	admin, adminCk := e.addUser("admin@example.com", model.RoleAdmin)
	admin2, _ := e.addUser("admin2@example.com", model.RoleAdmin)
	op, opCk := e.addUser("op@example.com", model.RoleOperator)
	viewer, viewerCk := e.addUser("viewer@example.com", model.RoleViewer)
	admin3, _ := e.addUser("admin3@example.com", model.RoleAdmin)
	path := func(u *model.User) string { return "/api/v1/users/" + id64(u.ID) }
	patch := func(u *model.User, ck *http.Cookie, body map[string]any) *httptest.ResponseRecorder {
		return e.do("PATCH", path(u), body, withCookie(ck))
	}

	// List is open to every role; password hashes never leak.
	w := e.do("GET", "/api/v1/users", nil, withCookie(viewerCk))
	e.want(w, 200)
	if strings.Contains(w.Body.String(), "argon2") || strings.Contains(w.Body.String(), "password_hash") {
		t.Error("hash leaked")
	}
	if w := patch(viewer, opCk, map[string]any{"name": "x"}); w.Code != 403 {
		t.Errorf("operator patch: %d", w.Code)
	}

	// Admin edits lower roles; sessions of the target die on role change.
	if w := patch(viewer, adminCk, map[string]any{"role": "operator"}); w.Code != 200 {
		t.Fatalf("promote: %d %s", w.Code, w.Body)
	}
	if e.repo.SessionCount(viewer.ID) != 0 {
		t.Error("role change must revoke the user's sessions")
	}
	if w := e.do("GET", "/api/v1/auth/me", nil, withCookie(viewerCk)); w.Code != 401 {
		t.Errorf("old session: %d", w.Code)
	}
	if w := patch(op, adminCk, map[string]any{"status": "disabled"}); w.Code != 200 {
		t.Errorf("disable: %d", w.Code)
	}
	if w := e.do("GET", "/api/v1/auth/me", nil, withCookie(opCk)); w.Code != 401 {
		t.Errorf("disabled user's session: %d", w.Code)
	}
	if w := patch(op, adminCk, map[string]any{"status": "active", "name": "Op"}); w.Code != 200 {
		t.Errorf("enable: %d", w.Code)
	}
	// Name-only change keeps sessions.
	_, vck := e.addUser("v2@example.com", model.RoleViewer)
	v2, _ := e.repo.GetUserByEmail(context.Background(), "v2@example.com")
	patch(v2, adminCk, map[string]any{"name": "New Name"})
	if w := e.do("GET", "/api/v1/auth/me", nil, withCookie(vck)); w.Code != 200 {
		t.Errorf("rename must not revoke sessions: %d", w.Code)
	}

	for name, c := range map[string]struct {
		target *model.User
		ck     *http.Cookie
		body   map[string]any
		want   int
	}{
		"admin cannot grant admin":           {op, adminCk, map[string]any{"role": "admin"}, 403},
		"admin cannot change another admin":  {admin2, adminCk, map[string]any{"role": "viewer"}, 403},
		"admin cannot disable another admin": {admin2, adminCk, map[string]any{"status": "disabled"}, 403},
		"admin cannot touch the owner":       {owner, adminCk, map[string]any{"status": "disabled"}, 403},
		"admin cannot demote themselves":     {admin, adminCk, map[string]any{"role": "viewer"}, 403},
		"admin cannot disable themselves":    {admin, adminCk, map[string]any{"status": "disabled"}, 403},
		"owner cannot demote themselves":     {owner, ownerCk, map[string]any{"role": "admin"}, 403},
		"owner cannot disable themselves":    {owner, ownerCk, map[string]any{"status": "disabled"}, 403},
		"owner role via patch is refused":    {op, ownerCk, map[string]any{"role": "owner"}, 400},
		"unknown role":                       {op, ownerCk, map[string]any{"role": "god"}, 400},
		"unknown status":                     {op, ownerCk, map[string]any{"status": "banned"}, 400},
		"empty body":                         {op, ownerCk, map[string]any{}, 400},
		"owner promotes to admin":            {op, ownerCk, map[string]any{"role": "admin"}, 200},
		"owner demotes admin":                {admin3, ownerCk, map[string]any{"role": "operator"}, 200},
		"admin may rename themselves":        {admin, adminCk, map[string]any{"name": "Me"}, 200},
	} {
		if w := patch(c.target, c.ck, c.body); w.Code != c.want {
			t.Errorf("%s: %d want %d (%s)", name, w.Code, c.want, w.Body)
		}
	}
	if w := e.do("PATCH", "/api/v1/users/99999", map[string]any{"name": "x"}, withCookie(adminCk)); w.Code != 404 {
		t.Errorf("missing user: %d", w.Code)
	}
	if w := e.do("PATCH", "/api/v1/users/abc", map[string]any{"name": "x"}, withCookie(adminCk)); w.Code != 400 {
		t.Errorf("bad id: %d", w.Code)
	}
	// Audit trail has the changes.
	found := false
	for _, ev := range e.repo.Audit() {
		found = found || (ev.Action == "user.update" && ev.ActorLabel == "admin")
	}
	if !found {
		t.Error("user.update not audited")
	}
}

func id64(n int64) string { return itoa(int(n)) }

func TestTransferOwnership(t *testing.T) {
	e := newEnv(t, nil)
	owner, ownerCk := e.addUser("owner@example.com", model.RoleOwner)
	admin, adminCk := e.addUser("admin@example.com", model.RoleAdmin)
	op, _ := e.addUser("op@example.com", model.RoleOperator)
	tp := func(u *model.User, ck *http.Cookie) *httptest.ResponseRecorder {
		return e.do("POST", "/api/v1/users/"+id64(u.ID)+"/transfer-ownership", nil, withCookie(ck))
	}
	if w := tp(owner, adminCk); w.Code != 403 {
		t.Errorf("admin cannot transfer: %d", w.Code)
	}
	if w := tp(op, ownerCk); w.Code != 400 {
		t.Errorf("target must be admin: %d", w.Code)
	}
	if w := tp(owner, ownerCk); w.Code != 400 {
		t.Errorf("self transfer: %d", w.Code)
	}
	e.want(tp(admin, ownerCk), 204)
	if w := e.do("GET", "/api/v1/auth/me", nil, withCookie(ownerCk)); w.Code != 200 {
		t.Errorf("the previous owner keeps their current session: %d", w.Code)
	}
	var me struct{ User model.User }
	decode(t, e.do("GET", "/api/v1/auth/me", nil, withCookie(ownerCk)), &me)
	if me.User.Role != model.RoleAdmin {
		t.Errorf("old owner is now %s", me.User.Role)
	}
	if w := e.do("GET", "/api/v1/auth/me", nil, withCookie(adminCk)); w.Code != 401 {
		t.Errorf("new owner's old sessions are revoked: %d", w.Code)
	}
}

func TestInviteAndResetLinks(t *testing.T) {
	e := newEnv(t, nil)
	_, ownerCk := e.addUser("owner@example.com", model.RoleOwner)
	_, adminCk := e.addUser("admin@example.com", model.RoleAdmin)
	_, opCk := e.addUser("op@example.com", model.RoleOperator)

	if w := e.do("POST", "/api/v1/users/invites", map[string]string{"username": "newbie", "role": "viewer"}, withCookie(opCk)); w.Code != 403 {
		t.Errorf("operator invite: %d", w.Code)
	}
	for name, body := range map[string]map[string]string{
		"owner role":     {"username": "newbie", "role": "owner"},
		"bad role":       {"username": "newbie", "role": "x"},
		"bad username":   {"username": "n b", "role": "viewer"},
		"short username": {"username": "n", "role": "viewer"},
		"no username":    {"email": "n@example.com", "role": "viewer"},
		"bad email":      {"username": "newbie", "email": "n", "role": "viewer"},
		"existing user":  {"username": "OP", "role": "viewer"},
		"existing email": {"username": "fresh", "email": "OP@example.com", "role": "viewer"},
	} {
		if w := e.do("POST", "/api/v1/users/invites", body, withCookie(adminCk)); w.Code < 400 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	if w := e.do("POST", "/api/v1/users/invites", map[string]string{"username": "newbie", "role": "admin"}, withCookie(adminCk)); w.Code != 403 {
		t.Errorf("admin inviting admin: %d", w.Code)
	}

	w := e.do("POST", "/api/v1/users/invites", map[string]string{"username": "NewUser", "email": "New@Example.com", "role": "operator"}, withCookie(adminCk))
	e.want(w, 201)
	var inv struct {
		ID        int64     `json:"id"`
		URL       string    `json:"url"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	decode(t, w, &inv)
	if !strings.HasPrefix(inv.URL, "https://kymo.example.com/invite/") || time.Until(inv.ExpiresAt) < 6*24*time.Hour {
		t.Fatalf("invite: %+v", inv)
	}
	token := inv.URL[strings.LastIndex(inv.URL, "/")+1:]

	var list []struct {
		ID        int64  `json:"id"`
		Username  string `json:"username"`
		Email     string `json:"email"`
		Role      string `json:"role"`
		CreatedBy string `json:"created_by"`
	}
	decode(t, e.do("GET", "/api/v1/users/invites", nil, withCookie(adminCk)), &list)
	if len(list) != 1 || list[0].Username != "newuser" || list[0].Email != "new@example.com" || list[0].CreatedBy != "admin" || list[0].ID != inv.ID {
		t.Errorf("list: %+v", list)
	}
	if w := e.do("GET", "/api/v1/users/invites", nil, withCookie(opCk)); w.Code != 403 {
		t.Errorf("operator list invites: %d", w.Code)
	}

	// Public lookup (no session).
	w = e.do("GET", "/api/v1/auth/links/"+token, nil)
	e.want(w, 200)
	var info map[string]any
	decode(t, w, &info)
	if info["kind"] != "invite" || info["username"] != "newuser" || info["email"] != "new@example.com" || info["role"] != "operator" {
		t.Errorf("link info: %v", info)
	}
	if w := e.do("GET", "/api/v1/auth/links/not-a-token", nil); w.Code != 404 {
		t.Errorf("unknown link: %d", w.Code)
	}
	if w := e.do("POST", "/api/v1/auth/links/"+token, map[string]string{"password": "short"}); w.Code != 400 {
		t.Errorf("weak password: %d", w.Code)
	}
	w = e.do("POST", "/api/v1/auth/links/"+token, map[string]string{"name": "Newbie", "password": pw})
	e.want(w, 200)
	ck := sessionCookie(w)
	if ck == nil {
		t.Fatal("accepting an invite signs in")
	}
	var me struct{ User model.User }
	decode(t, e.do("GET", "/api/v1/auth/me", nil, withCookie(ck)), &me)
	if me.User.Username != "newuser" || me.User.Email != "new@example.com" || me.User.Role != model.RoleOperator || me.User.Name != "Newbie" {
		t.Errorf("new user: %+v", me.User)
	}
	// Single use.
	if w := e.do("GET", "/api/v1/auth/links/"+token, nil); w.Code != 410 {
		t.Errorf("used link: %d", w.Code)
	}
	if w := e.do("POST", "/api/v1/auth/links/"+token, map[string]string{"password": pw}); w.Code != 410 {
		t.Errorf("reuse: %d", w.Code)
	}

	// Revoke an invite.
	w = e.do("POST", "/api/v1/users/invites", map[string]string{"username": "gone", "role": "viewer"}, withCookie(adminCk))
	decode(t, w, &inv)
	e.want(e.do("DELETE", "/api/v1/users/invites/"+id64(inv.ID), nil, withCookie(adminCk)), 204)
	if w := e.do("DELETE", "/api/v1/users/invites/"+id64(inv.ID), nil, withCookie(adminCk)); w.Code != 404 {
		t.Errorf("delete twice: %d", w.Code)
	}

	// Expired invite -> 410.
	w = e.do("POST", "/api/v1/users/invites", map[string]string{"username": "late", "role": "viewer"}, withCookie(adminCk))
	decode(t, w, &inv)
	tok2 := inv.URL[strings.LastIndex(inv.URL, "/")+1:]
	e.svc.Now = func() time.Time { return time.Now().Add(8 * 24 * time.Hour) }
	if w := e.do("GET", "/api/v1/auth/links/"+tok2, nil); w.Code != 410 {
		t.Errorf("expired invite: %d", w.Code)
	}
	e.svc.Now = time.Now

	// Password reset link: admin for a lower role; only owner for admins.
	newUser, _ := e.repo.GetUserByEmail(context.Background(), "new@example.com")
	w = e.do("POST", "/api/v1/users/"+id64(newUser.ID)+"/reset-link", nil, withCookie(adminCk))
	e.want(w, 200)
	var rl struct {
		URL       string    `json:"url"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	decode(t, w, &rl)
	if !strings.HasPrefix(rl.URL, "https://kymo.example.com/reset/") || time.Until(rl.ExpiresAt) > time.Hour+time.Minute {
		t.Errorf("reset link: %+v", rl)
	}
	rtok := rl.URL[strings.LastIndex(rl.URL, "/")+1:]
	decode(t, e.do("GET", "/api/v1/auth/links/"+rtok, nil), &info)
	if info["kind"] != "password_reset" || info["username"] != "newuser" || info["email"] != "new@example.com" {
		t.Errorf("reset info: %v", info)
	}
	newPw := "brand-new-password-1"
	w = e.do("POST", "/api/v1/auth/links/"+rtok, map[string]string{"password": newPw})
	e.want(w, 200)
	if w := e.do("GET", "/api/v1/auth/me", nil, withCookie(ck)); w.Code != 401 {
		t.Errorf("reset must revoke earlier sessions: %d", w.Code)
	}
	e.want(e.do("POST", "/api/v1/auth/login", map[string]string{"login": "new@example.com", "password": newPw}), 200)
	if w := e.do("POST", "/api/v1/auth/links/"+rtok, map[string]string{"password": newPw}); w.Code != 410 {
		t.Errorf("reset link reuse: %d", w.Code)
	}
	adminUser, _ := e.repo.GetUserByEmail(context.Background(), "admin@example.com")
	if w := e.do("POST", "/api/v1/users/"+id64(adminUser.ID)+"/reset-link", nil, withCookie(opCk)); w.Code != 403 {
		t.Errorf("operator: %d", w.Code)
	}
	ownerUser, _ := e.repo.GetUserByEmail(context.Background(), "owner@example.com")
	if w := e.do("POST", "/api/v1/users/"+id64(ownerUser.ID)+"/reset-link", nil, withCookie(adminCk)); w.Code != 403 {
		t.Errorf("admin resetting the owner must be refused (account takeover): %d", w.Code)
	}
	if w := e.do("POST", "/api/v1/users/"+id64(adminUser.ID)+"/reset-link", nil, withCookie(ownerCk)); w.Code != 200 {
		t.Errorf("owner resetting an admin: %d", w.Code)
	}
	// SSO-only users have no local password.
	sso := &model.User{Username: "sso", Email: "sso@example.com", Role: model.RoleViewer}
	_ = e.repo.CreateUser(context.Background(), sso)
	if w := e.do("POST", "/api/v1/users/"+id64(sso.ID)+"/reset-link", nil, withCookie(adminCk)); w.Code != 409 {
		t.Errorf("sso user reset: %d", w.Code)
	}
}

func TestChangePassword(t *testing.T) {
	e := newEnv(t, nil)
	u, ck1 := e.addUser("a@example.com", model.RoleViewer)
	w := e.do("POST", "/api/v1/auth/login", map[string]string{"login": "a@example.com", "password": pw})
	ck2 := sessionCookie(w)
	cp := func(cur, nw string, ck *http.Cookie) *httptest.ResponseRecorder {
		return e.do("POST", "/api/v1/me/password", map[string]string{"current_password": cur, "new_password": nw}, withCookie(ck))
	}
	if w := cp("wrong-wrong-wrong", "another-long-password", ck1); w.Code != 400 {
		t.Errorf("wrong current: %d", w.Code)
	}
	if w := cp(pw, "short", ck1); w.Code != 400 {
		t.Errorf("weak: %d", w.Code)
	}
	e.want(cp(pw, "another-long-password", ck1), 204)
	if w := e.do("GET", "/api/v1/auth/me", nil, withCookie(ck1)); w.Code != 200 {
		t.Errorf("current session survives: %d", w.Code)
	}
	if w := e.do("GET", "/api/v1/auth/me", nil, withCookie(ck2)); w.Code != 401 {
		t.Errorf("other sessions are revoked: %d", w.Code)
	}
	e.want(e.do("POST", "/api/v1/auth/login", map[string]string{"login": "a@example.com", "password": "another-long-password"}), 200)
	if w := e.do("POST", "/api/v1/auth/login", map[string]string{"login": "a@example.com", "password": pw}); w.Code != 401 {
		t.Errorf("old password still works: %d", w.Code)
	}
	_ = u
}

func TestAPITokens(t *testing.T) {
	e := newEnv(t, nil)
	_, adminCk := e.addUser("admin@example.com", model.RoleAdmin)
	_, opCk := e.addUser("op@example.com", model.RoleOperator)
	_, viewerCk := e.addUser("viewer@example.com", model.RoleViewer)

	create := func(ck *http.Cookie, body map[string]any) *httptest.ResponseRecorder {
		return e.do("POST", "/api/v1/tokens", body, withCookie(ck))
	}
	if w := create(opCk, map[string]any{"name": "x", "role": "admin", "expires_in_days": nil}); w.Code != 403 {
		t.Errorf("role above own: %d", w.Code)
	}
	for name, body := range map[string]map[string]any{
		"no name":   {"name": " ", "role": "viewer"},
		"owner":     {"name": "x", "role": "owner"},
		"bad days":  {"name": "x", "role": "viewer", "expires_in_days": 0},
		"huge days": {"name": "x", "role": "viewer", "expires_in_days": 99999},
	} {
		if w := create(adminCk, body); w.Code != 400 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	w := create(opCk, map[string]any{"name": "ci", "role": "operator", "expires_in_days": 30})
	e.want(w, 201)
	var out struct {
		Token  model.APIToken `json:"token"`
		Secret string         `json:"secret"`
	}
	decode(t, w, &out)
	if !strings.HasPrefix(out.Secret, "kymo_"+out.Token.Prefix+"_") || out.Token.ExpiresAt == nil || out.Token.Role != model.RoleOperator || out.Token.UserUsername != "op" {
		t.Fatalf("token: %+v %q", out.Token, out.Secret)
	}
	if strings.Contains(w.Body.String(), "token_hash") {
		t.Error("hash leaked")
	}

	// Bearer works without the CSRF header, and respects the role.
	if w := e.do("GET", "/api/v1/auth/me", nil, withBearer(out.Secret)); w.Code != 200 {
		t.Errorf("bearer me: %d", w.Code)
	}
	if w := e.do("GET", "/api/v1/users", nil, withBearer(out.Secret), noCSRF()); w.Code != 200 {
		t.Errorf("bearer list: %d", w.Code)
	}
	if w := e.do("GET", "/api/v1/audit", nil, withBearer(out.Secret)); w.Code != 403 {
		t.Errorf("operator token on admin route: %d", w.Code)
	}
	if w := e.do("POST", "/api/v1/users/invites", map[string]string{"username": "zed", "role": "viewer"}, withBearer(out.Secret), noCSRF()); w.Code != 403 {
		t.Errorf("operator token inviting: %d", w.Code)
	}
	// ... but a token cannot mint tokens or change passwords.
	if w := e.do("POST", "/api/v1/tokens", map[string]any{"name": "x", "role": "viewer"}, withBearer(out.Secret), noCSRF()); w.Code != 403 || errCode(w) != "session_required" {
		t.Errorf("token minting tokens: %d %s", w.Code, w.Body)
	}
	if w := e.do("GET", "/api/v1/users", nil, withBearer("kymo_deadbeef_"+strings.Repeat("a", 43))); w.Code != 401 {
		t.Errorf("bad bearer: %d", w.Code)
	}

	// Listing: own tokens; ?all needs admin.
	var list []model.APIToken
	decode(t, e.do("GET", "/api/v1/tokens", nil, withCookie(opCk)), &list)
	if len(list) != 1 || list[0].LastUsedAt == nil {
		t.Errorf("own list: %+v", list)
	}
	decode(t, e.do("GET", "/api/v1/tokens", nil, withCookie(viewerCk)), &list)
	if len(list) != 0 {
		t.Errorf("viewer sees %d tokens", len(list))
	}
	if w := e.do("GET", "/api/v1/tokens?all=1", nil, withCookie(opCk)); w.Code != 403 {
		t.Errorf("all as operator: %d", w.Code)
	}
	decode(t, e.do("GET", "/api/v1/tokens?all=1", nil, withCookie(adminCk)), &list)
	if len(list) != 1 {
		t.Errorf("all as admin: %d", len(list))
	}

	// Revocation: not by other non-admins; by owner of the token; by admin.
	id := id64(out.Token.ID)
	if w := e.do("DELETE", "/api/v1/tokens/"+id, nil, withCookie(viewerCk)); w.Code != 403 {
		t.Errorf("foreign revoke: %d", w.Code)
	}
	if w := e.do("DELETE", "/api/v1/tokens/999", nil, withCookie(adminCk)); w.Code != 404 {
		t.Errorf("missing: %d", w.Code)
	}
	e.want(e.do("DELETE", "/api/v1/tokens/"+id, nil, withCookie(adminCk)), 204)
	if w := e.do("GET", "/api/v1/auth/me", nil, withBearer(out.Secret)); w.Code != 401 {
		t.Errorf("revoked token: %d", w.Code)
	}
	decode(t, e.do("GET", "/api/v1/tokens", nil, withCookie(opCk)), &list)
	if len(list) != 1 || list[0].RevokedAt == nil {
		t.Errorf("revoked token stays listed with revoked_at: %+v", list)
	}
	// Never-expiring token.
	w = create(opCk, map[string]any{"name": "forever", "role": "viewer", "expires_in_days": nil})
	e.want(w, 201)
	decode(t, w, &out)
	if out.Token.ExpiresAt != nil {
		t.Error("expires_in_days:null must not expire")
	}
	actions := map[string]bool{}
	for _, ev := range e.repo.Audit() {
		actions[ev.Action] = true
	}
	if !actions["token.create"] || !actions["token.revoke"] {
		t.Errorf("audit: %v", actions)
	}
}

func TestAuditEndpoint(t *testing.T) {
	e := newEnv(t, nil)
	_, adminCk := e.addUser("admin@example.com", model.RoleAdmin)
	op, opCk := e.addUser("op@example.com", model.RoleOperator)
	if w := e.do("GET", "/api/v1/audit", nil, withCookie(opCk)); w.Code != 403 {
		t.Errorf("operator: %d", w.Code)
	}
	e.svc.Record(context.Background(), auth.Actor{UserID: op.ID, Label: op.Username, IP: "1.1.1.1"}, "pod.restart", "pod", "ns/p", map[string]any{"source": "s"})
	var list []model.AuditEvent
	w := e.do("GET", "/api/v1/audit?action=pod&user_id="+id64(op.ID)+"&limit=5", nil, withCookie(adminCk))
	e.want(w, 200)
	decode(t, w, &list)
	if len(list) != 1 || list[0].Action != "pod.restart" || list[0].IP != "1.1.1.1" || list[0].ActorLabel != "op" {
		t.Errorf("filtered: %+v", list)
	}
	decode(t, e.do("GET", "/api/v1/audit", nil, withCookie(adminCk)), &list)
	if len(list) < 1 {
		t.Errorf("all: %d", len(list))
	}
	for _, q := range []string{"user_id=x", "before=yesterday"} {
		if w := e.do("GET", "/api/v1/audit?"+q, nil, withCookie(adminCk)); w.Code != 400 {
			t.Errorf("%s: %d", q, w.Code)
		}
	}
	var empty []model.AuditEvent
	decode(t, e.do("GET", "/api/v1/audit?before=2000-01-01T00:00:00Z", nil, withCookie(adminCk)), &empty)
	if empty == nil || len(empty) != 0 {
		t.Errorf("before filter: %v", empty)
	}
}

func TestCSRFAppliesToAPIAndCORSIsNotWildcard(t *testing.T) {
	e := newEnv(t, func(o *auth.Options) { o.AllowedOrigins = []string{"https://dash.example.com"} })
	_, ck := e.addUser("a@example.com", model.RoleAdmin)
	if w := e.do("POST", "/api/v1/users/invites", map[string]string{"username": "xavier", "role": "viewer"}, withCookie(ck), noCSRF()); w.Code != 403 || errCode(w) != "csrf" {
		t.Errorf("missing header: %d", w.Code)
	}
	if w := e.do("POST", "/api/v1/users/invites", map[string]string{"username": "xavier", "role": "viewer"}, withCookie(ck), withHeader("Origin", "https://evil.com")); w.Code != 403 {
		t.Errorf("cross-origin: %d", w.Code)
	}
	w := e.do("GET", "/api/v1/auth/config", nil, withHeader("Origin", "https://dash.example.com"))
	if w.Header().Get("Access-Control-Allow-Origin") != "https://dash.example.com" || w.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Errorf("allowed origin: %v", w.Header())
	}
	w = e.do("GET", "/api/v1/auth/config", nil, withHeader("Origin", "https://evil.com"))
	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("foreign origin: %v", w.Header())
	}
}

func TestOIDCEndpoints(t *testing.T) {
	idp := authtest.NewFakeIdP(t)
	e := newEnv(t, func(o *auth.Options) {
		o.OIDC = config.OIDCConfig{Enabled: true, Name: "Fake", Issuer: idp.Issuer(), ClientID: idp.ClientID, ClientSecret: idp.ClientSecret,
			Scopes: []string{"openid", "email", "profile"}, DefaultRole: "viewer", AutoCreate: true, AllowedDomains: []string{"example.com"}}
	})
	cookieOf := func(w *httptest.ResponseRecorder, name string) *http.Cookie {
		for _, c := range w.Result().Cookies() {
			if c.Name == name && c.MaxAge >= 0 {
				return c
			}
		}
		return nil
	}
	start := func(returnTo string) (*httptest.ResponseRecorder, string) {
		w := e.do("GET", "/api/v1/auth/oidc/login?return_to="+url.QueryEscape(returnTo), nil)
		return w, w.Header().Get("Location")
	}
	callback := func(pending *http.Cookie, state, code string) *httptest.ResponseRecorder {
		opts := []reqOpt{}
		if pending != nil {
			opts = append(opts, withCookie(pending))
		}
		return e.do("GET", "/api/v1/auth/oidc/callback?state="+url.QueryEscape(state)+"&code="+url.QueryEscape(code), nil, opts...)
	}

	// Success.
	w, loc := start("/issues?status=open")
	if w.Code != 302 || !strings.HasPrefix(loc, idp.Issuer()+"/authorize") {
		t.Fatalf("login redirect: %d %s", w.Code, loc)
	}
	pending := cookieOf(w, "kymo_oidc")
	if pending == nil || !pending.HttpOnly || !pending.Secure {
		t.Fatalf("pending cookie: %+v", pending)
	}
	state, code := idp.Login(t, loc, map[string]any{"sub": "s1", "email": "Sam@Example.com", "email_verified": true, "name": "Sam"})
	w = callback(pending, state, code)
	if w.Code != 302 || w.Header().Get("Location") != "/issues?status=open" {
		t.Fatalf("callback: %d %s", w.Code, w.Header().Get("Location"))
	}
	ck := sessionCookie(w)
	if ck == nil {
		t.Fatal("no session cookie")
	}
	var me struct{ User model.User }
	decode(t, e.do("GET", "/api/v1/auth/me", nil, withCookie(ck)), &me)
	if me.User.Username != "sam" || me.User.Email != "sam@example.com" || me.User.Role != model.RoleViewer || me.User.HasPassword || !me.User.SSO {
		t.Errorf("sso user: %+v", me.User)
	}
	actions := map[string]bool{}
	for _, ev := range e.repo.Audit() {
		actions[ev.Action] = true
	}
	if !actions["user.create"] || !actions["login.success"] {
		t.Errorf("audit: %v", actions)
	}

	// Open redirect attempt is neutralised.
	w, loc = start("//evil.com/x")
	pending = cookieOf(w, "kymo_oidc")
	state, code = idp.Login(t, loc, map[string]any{"sub": "s1", "email": "sam@example.com", "email_verified": true})
	if w = callback(pending, state, code); w.Header().Get("Location") != "/" {
		t.Errorf("open redirect: %s", w.Header().Get("Location"))
	}

	// Failures redirect to /login?error=...
	check := func(name, wantLoc string, w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code != 302 || w.Header().Get("Location") != wantLoc || sessionCookie(w) != nil {
			t.Errorf("%s: %d %q cookie=%v", name, w.Code, w.Header().Get("Location"), sessionCookie(w))
		}
	}
	w, loc = start("/")
	pending = cookieOf(w, "kymo_oidc")
	state, code = idp.Login(t, loc, map[string]any{"sub": "s2", "email": "x@evil.org", "email_verified": true})
	check("domain", "/login?error=domain_not_allowed", callback(pending, state, code))

	w, loc = start("/")
	pending = cookieOf(w, "kymo_oidc")
	state, code = idp.Login(t, loc, map[string]any{"sub": "s3", "email": "y@example.com", "email_verified": false})
	check("unverified", "/login?error=oidc_failed", callback(pending, state, code))

	w, loc = start("/")
	pending = cookieOf(w, "kymo_oidc")
	state, code = idp.Login(t, loc, map[string]any{"sub": "s4", "username": "zed", "email_verified": true})
	check("state mismatch", "/login?error=oidc_failed", callback(pending, "forged", code))
	check("no cookie", "/login?error=oidc_failed", callback(nil, state, code))
	check("provider error", "/login?error=oidc_failed", e.do("GET", "/api/v1/auth/oidc/callback?error=access_denied", nil))

	// Disabled users.
	u, _ := e.repo.GetUserByEmail(context.Background(), "sam@example.com")
	st := model.UserDisabled
	_ = e.repo.UpdateUser(context.Background(), u.ID, model.UserUpdate{Status: &st})
	w, loc = start("/")
	pending = cookieOf(w, "kymo_oidc")
	state, code = idp.Login(t, loc, map[string]any{"sub": "s1", "email": "sam@example.com", "email_verified": true})
	check("disabled", "/login?error=disabled", callback(pending, state, code))

	// OIDC not enabled -> 404.
	plain := newEnv(t, nil)
	if w := plain.do("GET", "/api/v1/auth/oidc/login", nil); w.Code != 404 {
		t.Errorf("oidc disabled: %d", w.Code)
	}
}
