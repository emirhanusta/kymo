package httphandler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emirhanusta/kymo/internal/auth"
	"github.com/emirhanusta/kymo/internal/model"
)

func TestLoginByUsernameAndEmail(t *testing.T) {
	e := newEnv(t, nil)
	e.addUser("alice@example.com", model.RoleViewer) // username "alice"
	e.addUser("bob", model.RoleViewer)               // no email

	for _, login := range []string{"alice", "ALICE", " Alice ", "alice@example.com", "Alice@Example.COM", "bob", "Bob"} {
		w := e.do("POST", "/api/v1/auth/login", map[string]string{"login": login, "password": pw})
		if w.Code != 200 || sessionCookie(w) == nil {
			t.Errorf("login %q: %d %s", login, w.Code, w.Body)
		}
	}
	// Old-style body is rejected generically; unknown / wrong are indistinguishable.
	w1 := e.do("POST", "/api/v1/auth/login", map[string]string{"login": "alice", "password": "wrong-wrong-wrong"})
	w2 := e.do("POST", "/api/v1/auth/login", map[string]string{"login": "ghost", "password": "wrong-wrong-wrong"})
	w3 := e.do("POST", "/api/v1/auth/login", map[string]string{"email": "alice@example.com", "password": pw})
	if w1.Code != 401 || w1.Body.String() != w2.Body.String() || w3.Code != 401 {
		t.Errorf("failures: %s | %s | %d", w1.Body, w2.Body, w3.Code)
	}
	// "bob" has no email, so an email never matches him.
	if w := e.do("POST", "/api/v1/auth/login", map[string]string{"login": "bob@example.com", "password": pw}); w.Code != 401 {
		t.Errorf("email login for user without email: %d", w.Code)
	}
}

func TestLoginRateLimitUsesNormalizedLogin(t *testing.T) {
	e := newEnv(t, nil)
	e.addUser("carol", model.RoleViewer)
	variants := []string{"carol", "CAROL", " Carol "}
	for i := 0; i < auth.LoginMaxAttempts; i++ {
		e.do("POST", "/api/v1/auth/login", map[string]string{"login": variants[i%3], "password": "nope-nope-nope-nope"})
	}
	if w := e.do("POST", "/api/v1/auth/login", map[string]string{"login": "cArOl", "password": pw}); w.Code != 429 {
		t.Errorf("case variants must share one budget: %d", w.Code)
	}
	if w := e.do("POST", "/api/v1/auth/login", map[string]string{"login": "someone-else", "password": pw}); w.Code == 429 {
		t.Errorf("other logins stay usable: %d", w.Code)
	}
}

// addFlagged creates an owner whose password must be changed, with a session.
func (e *env) addFlagged(username string) (*model.User, *http.Cookie) {
	e.t.Helper()
	u, ck := e.addUser(username, model.RoleOwner)
	yes := true
	if err := e.repo.UpdateUser(context.Background(), u.ID, model.UserUpdate{MustChangePassword: &yes}); err != nil {
		e.t.Fatal(err)
	}
	return u, ck
}

// mustChangeAllowed are the only non-public routes open while the password
// must still be changed.
var mustChangeAllowed = map[string]bool{
	"GET /api/v1/auth/me":      true,
	"POST /api/v1/me/password": true,
	"PATCH /api/v1/me":         true,
}

func TestMustChangePasswordGate(t *testing.T) {
	e := newEnv(t, nil)
	e.r = rebuildWithRecovery(t, e)
	_, ck := e.addFlagged("admin")

	// Allowed endpoints work (public ones too: config, logout).
	e.want(e.do("GET", "/api/v1/auth/me", nil, withCookie(ck)), 200)
	e.want(e.do("GET", "/api/v1/auth/config", nil, withCookie(ck)), 200)
	var me struct{ User model.User }
	decode(t, e.do("GET", "/api/v1/auth/me", nil, withCookie(ck)), &me)
	if !me.User.MustChangePassword {
		t.Error("me must report must_change_password")
	}

	// Every other route is blocked with 403 password_change_required.
	checked := 0
	for k, req := range routeRoles {
		if req == "public" || mustChangeAllowed[k] {
			continue
		}
		method, path, _ := strings.Cut(k, " ")
		url := paramRe.ReplaceAllString(path, "99999")
		var body any
		if method != "GET" && method != "DELETE" {
			body = map[string]any{}
		}
		w := e.do(method, url, body, withCookie(ck))
		if w.Code != 403 || errCode(w) != "password_change_required" {
			t.Errorf("%s while must_change: %d %s", k, w.Code, w.Body)
		}
		checked++
	}
	if checked < 50 {
		t.Errorf("only %d routes checked", checked)
	}
	// API tokens cannot be created meanwhile.
	if w := e.do("POST", "/api/v1/tokens", map[string]any{"name": "x", "role": "viewer"}, withCookie(ck)); errCode(w) != "password_change_required" {
		t.Errorf("token create: %d %s", w.Code, w.Body)
	}

	// Profile edits are allowed.
	e.want(e.do("PATCH", "/api/v1/me", map[string]string{"email": "root@example.com"}, withCookie(ck)), 200)

	// Changing the password clears the flag and unlocks the session.
	if w := e.do("POST", "/api/v1/me/password", map[string]string{"current_password": pw, "new_password": pw}, withCookie(ck)); w.Code != 400 || errCode(w) != "same_password" {
		t.Errorf("same password: %d %s", w.Code, w.Body)
	}
	e.want(e.do("POST", "/api/v1/me/password", map[string]string{"current_password": pw, "new_password": "a-brand-new-password"}, withCookie(ck)), 204)
	decode(t, e.do("GET", "/api/v1/auth/me", nil, withCookie(ck)), &me)
	if me.User.MustChangePassword {
		t.Error("flag must be cleared")
	}
	e.want(e.do("GET", "/api/v1/users", nil, withCookie(ck)), 200)
	e.want(e.do("POST", "/api/v1/tokens", map[string]any{"name": "x", "role": "viewer"}, withCookie(ck)), 201)
}

func TestMustChangeGateAppliesToLoginSessions(t *testing.T) {
	e := newEnv(t, nil)
	e.addFlagged("admin")
	w := e.do("POST", "/api/v1/auth/login", map[string]string{"login": "admin", "password": pw})
	e.want(w, 200)
	if !strings.Contains(w.Body.String(), `"must_change_password":true`) {
		t.Errorf("login body: %s", w.Body)
	}
	ck := sessionCookie(w)
	if w := e.do("GET", "/api/v1/users", nil, withCookie(ck)); errCode(w) != "password_change_required" {
		t.Errorf("%d %s", w.Code, w.Body)
	}
	// The gate does not stop a flagged session from signing out or in again.
	e.want(e.do("POST", "/api/v1/auth/logout", nil, withCookie(ck)), 204)
}

func TestResetLinkDoesNotSetFlagAndClearsIt(t *testing.T) {
	e := newEnv(t, nil)
	_, ownerCk := e.addUser("owner", model.RoleOwner)
	u, _ := e.addUser("worker", model.RoleViewer)
	yes := true
	_ = e.repo.UpdateUser(context.Background(), u.ID, model.UserUpdate{MustChangePassword: &yes})
	w := e.do("POST", "/api/v1/users/"+id64(u.ID)+"/reset-link", nil, withCookie(ownerCk))
	e.want(w, 200)
	var rl struct{ URL string }
	decode(t, w, &rl)
	tok := rl.URL[strings.LastIndex(rl.URL, "/")+1:]
	e.want(e.do("POST", "/api/v1/auth/links/"+tok, map[string]string{"password": "a-new-long-password"}), 200)
	got, _ := e.repo.GetUser(context.Background(), u.ID)
	if got.MustChangePassword {
		t.Error("reset via link must not leave the flag set")
	}
	// A fresh invite never sets it either.
	w = e.do("POST", "/api/v1/users/invites", map[string]string{"username": "fresh", "role": "viewer"}, withCookie(ownerCk))
	e.want(w, 201)
	decode(t, w, &rl)
	tok = rl.URL[strings.LastIndex(rl.URL, "/")+1:]
	e.want(e.do("POST", "/api/v1/auth/links/"+tok, map[string]string{"password": pw}), 200)
	f, _ := e.repo.GetUserByUsername(context.Background(), "fresh")
	if f.MustChangePassword || f.Email != "" || f.Name != "fresh" {
		t.Errorf("invited user: %+v", f)
	}
}

func TestInviteConflicts(t *testing.T) {
	e := newEnv(t, nil)
	_, adminCk := e.addUser("admin@example.com", model.RoleOwner)
	e.addUser("taken@example.com", model.RoleViewer)
	inv := func(body map[string]string) *httptest.ResponseRecorder {
		return e.do("POST", "/api/v1/users/invites", body, withCookie(adminCk))
	}
	if w := inv(map[string]string{"username": "taken", "role": "viewer"}); w.Code != 409 || errCode(w) != "username_taken" {
		t.Errorf("username taken: %d %s", w.Code, w.Body)
	}
	if w := inv(map[string]string{"username": "TAKEN", "role": "viewer"}); w.Code != 409 {
		t.Errorf("username taken (case): %d", w.Code)
	}
	if w := inv(map[string]string{"username": "other", "email": "Taken@Example.com", "role": "viewer"}); w.Code != 409 || errCode(w) != "email_taken" {
		t.Errorf("email taken: %d %s", w.Code, w.Body)
	}
	e.want(inv(map[string]string{"username": "pending", "role": "viewer"}), 201)
	if w := inv(map[string]string{"username": "pending", "role": "viewer"}); w.Code != 409 || errCode(w) != "username_taken" {
		t.Errorf("pending invite duplicate: %d %s", w.Code, w.Body)
	}
	if w := inv(map[string]string{"username": "bad name", "role": "viewer"}); w.Code != 400 || errCode(w) != "invalid_username" {
		t.Errorf("invalid username: %d %s", w.Code, w.Body)
	}
	if w := inv(map[string]string{"username": "okname", "email": "nope", "role": "viewer"}); w.Code != 400 || errCode(w) != "invalid_email" {
		t.Errorf("invalid email: %d %s", w.Code, w.Body)
	}
}

func TestUpdateMeProfile(t *testing.T) {
	e := newEnv(t, nil)
	u, ck := e.addUser("dana", model.RoleViewer)
	e.addUser("erin@example.com", model.RoleViewer)
	patch := func(body map[string]any, opts ...reqOpt) *httptest.ResponseRecorder {
		return e.do("PATCH", "/api/v1/me", body, append([]reqOpt{withCookie(ck)}, opts...)...)
	}
	if w := e.do("PATCH", "/api/v1/me", map[string]any{"name": "x"}); w.Code != 401 {
		t.Errorf("anonymous: %d", w.Code)
	}
	for name, c := range map[string]struct {
		body map[string]any
		code int
		err  string
	}{
		"empty":         {map[string]any{}, 400, "invalid_request"},
		"blank name":    {map[string]any{"name": "  "}, 400, "invalid_request"},
		"invalid email": {map[string]any{"email": "nope"}, 400, "invalid_email"},
		"taken email":   {map[string]any{"email": "ERIN@example.com"}, 409, "email_taken"},
	} {
		if w := patch(c.body); w.Code != c.code || errCode(w) != c.err {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	w := patch(map[string]any{"name": "Dana D", "email": " Dana@Example.com "})
	e.want(w, 200)
	var out struct{ User model.User }
	decode(t, w, &out)
	if out.User.Name != "Dana D" || out.User.Email != "dana@example.com" || out.User.Username != "dana" || out.User.ID != u.ID {
		t.Errorf("patched: %+v", out.User)
	}
	// Setting the same email again is fine; "" clears it.
	e.want(patch(map[string]any{"email": "dana@example.com"}), 200)
	w = patch(map[string]any{"email": ""})
	e.want(w, 200)
	decode(t, w, &out)
	if out.User.Email != "" {
		t.Errorf("cleared: %+v", out.User)
	}
	// The freed email can be used by someone else.
	_, ck2 := e.addUser("fay", model.RoleViewer)
	e.want(e.do("PATCH", "/api/v1/me", map[string]any{"email": "dana@example.com"}, withCookie(ck2)), 200)
	// Tokens cannot edit the profile; no CSRF header is refused.
	if w := patch(map[string]any{"name": "x"}, noCSRF()); w.Code != 403 || errCode(w) != "csrf" {
		t.Errorf("csrf: %d %s", w.Code, w.Body)
	}
}

func TestAdminSetsUserEmail(t *testing.T) {
	e := newEnv(t, nil)
	_, adminCk := e.addUser("admin", model.RoleAdmin)
	target, _ := e.addUser("gus", model.RoleViewer)
	other, _ := e.addUser("hal@example.com", model.RoleViewer)
	path := "/api/v1/users/" + id64(target.ID)
	e.want(e.do("PATCH", path, map[string]any{"email": "Gus@Example.com"}, withCookie(adminCk)), 200)
	got, _ := e.repo.GetUser(context.Background(), target.ID)
	if got.Email != "gus@example.com" {
		t.Errorf("email: %q", got.Email)
	}
	if w := e.do("PATCH", path, map[string]any{"email": other.Email}, withCookie(adminCk)); w.Code != 409 || errCode(w) != "email_taken" {
		t.Errorf("conflict: %d %s", w.Code, w.Body)
	}
	if w := e.do("PATCH", path, map[string]any{"email": "bad"}, withCookie(adminCk)); w.Code != 400 {
		t.Errorf("invalid: %d", w.Code)
	}
	// Email edits keep the user's sessions.
	_, vck := e.addUser("ivy", model.RoleViewer)
	iv, _ := e.repo.GetUserByUsername(context.Background(), "ivy")
	e.want(e.do("PATCH", "/api/v1/users/"+id64(iv.ID), map[string]any{"email": "ivy@example.com"}, withCookie(adminCk)), 200)
	e.want(e.do("GET", "/api/v1/auth/me", nil, withCookie(vck)), 200)
}
