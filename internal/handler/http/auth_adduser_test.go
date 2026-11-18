package httphandler

import (
	"context"
	"strings"
	"testing"

	"github.com/emirhanusta/kymo/internal/model"
)

func TestAddUserMethodLink(t *testing.T) {
	e := newEnv(t, nil)
	_, adminCk := e.addUser("admin@example.com", model.RoleAdmin)
	_, opCk := e.addUser("op@example.com", model.RoleOperator)

	if w := e.do("POST", "/api/v1/users", map[string]string{"username": "x1", "role": "viewer"}, withCookie(opCk)); w.Code != 403 {
		t.Errorf("operator: %d", w.Code)
	}
	// method defaults to link.
	w := e.do("POST", "/api/v1/users", map[string]string{"username": "Linky", "role": "viewer"}, withCookie(adminCk))
	e.want(w, 201)
	var out struct {
		Method   string `json:"method"`
		ID       int64  `json:"id"`
		Username string `json:"username"`
		URL      string `json:"url"`
	}
	decode(t, w, &out)
	if out.Method != "link" || out.Username != "linky" || !strings.HasPrefix(out.URL, "https://kymo.example.com/invite/") || out.ID == 0 {
		t.Fatalf("link: %+v", out)
	}
	var list []struct{ Username string }
	decode(t, e.do("GET", "/api/v1/users/invites", nil, withCookie(adminCk)), &list)
	if len(list) != 1 || list[0].Username != "linky" {
		t.Errorf("pending links: %+v", list)
	}
	e.want(e.do("POST", "/api/v1/users", map[string]string{"username": "l2", "role": "viewer", "method": "link"}, withCookie(adminCk)), 201)
	if w := e.do("POST", "/api/v1/users", map[string]string{"username": "l3", "role": "viewer", "method": "sms"}, withCookie(adminCk)); w.Code != 400 {
		t.Errorf("bad method: %d", w.Code)
	}
}

func TestAddUserMethodPassword(t *testing.T) {
	e := newEnv(t, nil)
	_, ownerCk := e.addUser("owner", model.RoleOwner)

	// Generated password: returned once, user must change it.
	w := e.do("POST", "/api/v1/users", map[string]string{"username": "gen", "role": "operator", "method": "password", "name": "Gen Erated", "email": "Gen@Example.com"}, withCookie(ownerCk))
	e.want(w, 201)
	var out struct {
		Method   string      `json:"method"`
		Password string      `json:"password"`
		User     *model.User `json:"user"`
	}
	decode(t, w, &out)
	if out.Method != "password" || len(out.Password) != 20 || out.User == nil || out.User.Username != "gen" ||
		out.User.Role != model.RoleOperator || out.User.Name != "Gen Erated" || out.User.Email != "gen@example.com" ||
		!out.User.MustChangePassword || !out.User.HasPassword {
		t.Fatalf("generated: %+v", out)
	}
	if strings.Contains(w.Body.String(), "argon2") {
		t.Error("hash leaked")
	}
	lw := e.do("POST", "/api/v1/auth/login", map[string]string{"login": "gen", "password": out.Password})
	e.want(lw, 200)
	if !strings.Contains(lw.Body.String(), `"must_change_password":true`) {
		t.Errorf("login body: %s", lw.Body)
	}
	ck := sessionCookie(lw)
	if w := e.do("GET", "/api/v1/users", nil, withCookie(ck)); errCode(w) != "password_change_required" {
		t.Errorf("gate: %d %s", w.Code, w.Body)
	}
	e.want(e.do("POST", "/api/v1/me/password", map[string]string{"current_password": out.Password, "new_password": "my-brand-new-password"}, withCookie(ck)), 204)
	got, _ := e.repo.GetUserByUsername(context.Background(), "gen")
	if got.MustChangePassword {
		t.Error("flag must clear after the change")
	}

	// Given password: not echoed back.
	w = e.do("POST", "/api/v1/users", map[string]string{"username": "given", "role": "viewer", "method": "password", "password": "chosen-by-admin-1"}, withCookie(ownerCk))
	e.want(w, 201)
	var out2 map[string]any
	decode(t, w, &out2)
	if _, has := out2["password"]; has || strings.Contains(w.Body.String(), "chosen-by-admin-1") {
		t.Errorf("given password echoed: %s", w.Body)
	}
	u, _ := out2["user"].(map[string]any)
	if u == nil || u["must_change_password"] != true || u["name"] != "given" {
		t.Errorf("given user: %v", out2)
	}
	e.want(e.do("POST", "/api/v1/auth/login", map[string]string{"login": "given", "password": "chosen-by-admin-1"}), 200)

	// Weak password.
	if w := e.do("POST", "/api/v1/users", map[string]string{"username": "weak", "role": "viewer", "method": "password", "password": "short"}, withCookie(ownerCk)); w.Code != 400 || errCode(w) != "weak_password" {
		t.Errorf("weak: %d %s", w.Code, w.Body)
	}
	if _, err := e.repo.GetUserByUsername(context.Background(), "weak"); err == nil {
		t.Error("weak password must not create the user")
	}

	// Audit.
	found := false
	for _, ev := range e.repo.Audit() {
		if ev.Action == "user.create" && strings.Contains(string(ev.Details), `"method":"password"`) {
			found = true
		}
	}
	if !found {
		t.Error("user.create not audited with method")
	}
}

func TestAddUserOwnerOnlyAdminAndConflicts(t *testing.T) {
	e := newEnv(t, nil)
	_, ownerCk := e.addUser("owner", model.RoleOwner)
	_, adminCk := e.addUser("boss@example.com", model.RoleAdmin)
	e.addUser("taken@example.com", model.RoleViewer)

	for _, m := range []string{"link", "password"} {
		if w := e.do("POST", "/api/v1/users", map[string]string{"username": "adm" + m, "role": "admin", "method": m}, withCookie(adminCk)); w.Code != 403 {
			t.Errorf("%s: admin creating admin: %d", m, w.Code)
		}
		if w := e.do("POST", "/api/v1/users", map[string]string{"username": "own" + m, "role": "owner", "method": m}, withCookie(ownerCk)); w.Code != 400 {
			t.Errorf("%s: owner role: %d", m, w.Code)
		}
		e.want(e.do("POST", "/api/v1/users", map[string]string{"username": "adm" + m, "role": "admin", "method": m}, withCookie(ownerCk)), 201)

		add := func(body map[string]string) (int, string) {
			body["method"], body["role"] = m, "viewer"
			w := e.do("POST", "/api/v1/users", body, withCookie(ownerCk))
			return w.Code, errCode(w)
		}
		if c, k := add(map[string]string{"username": "taken"}); c != 409 || k != "username_taken" {
			t.Errorf("%s username: %d %s", m, c, k)
		}
		if c, k := add(map[string]string{"username": "fresh" + m, "email": "TAKEN@example.com"}); c != 409 || k != "email_taken" {
			t.Errorf("%s email: %d %s", m, c, k)
		}
		if c, k := add(map[string]string{"username": "bad name"}); c != 400 || k != "invalid_username" {
			t.Errorf("%s invalid username: %d %s", m, c, k)
		}
		if c, k := add(map[string]string{"username": "okname", "email": "nope"}); c != 400 || k != "invalid_email" {
			t.Errorf("%s invalid email: %d %s", m, c, k)
		}
	}
	// Pending links block both username and email, for either method.
	e.want(e.do("POST", "/api/v1/users", map[string]string{"username": "pend", "email": "pend@example.com", "role": "viewer"}, withCookie(ownerCk)), 201)
	if c := e.do("POST", "/api/v1/users", map[string]string{"username": "pend", "role": "viewer", "method": "password"}, withCookie(ownerCk)); c.Code != 409 || errCode(c) != "username_taken" {
		t.Errorf("pending username: %d %s", c.Code, c.Body)
	}
	if c := e.do("POST", "/api/v1/users", map[string]string{"username": "pend2", "email": "pend@example.com", "role": "viewer", "method": "password"}, withCookie(ownerCk)); c.Code != 409 || errCode(c) != "email_taken" {
		t.Errorf("pending email: %d %s", c.Code, c.Body)
	}
}
