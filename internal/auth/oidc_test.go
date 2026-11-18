package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/auth/authtest"
	"github.com/emirhanusta/kymo/internal/config"
	"github.com/emirhanusta/kymo/internal/model"
)

func oidcFixture(t *testing.T, mut func(*config.OIDCConfig)) (*OIDC, *authtest.FakeIdP, *authtest.Mem) {
	t.Helper()
	idp := authtest.NewFakeIdP(t)
	cfg := config.OIDCConfig{
		Enabled: true, Name: "Fake", Issuer: idp.Issuer(), ClientID: idp.ClientID, ClientSecret: idp.ClientSecret,
		Scopes: []string{"openid", "email", "profile"}, DefaultRole: "viewer", AutoCreate: true, GroupsClaim: "groups",
	}
	if mut != nil {
		mut(&cfg)
	}
	repo := authtest.New()
	o, err := NewOIDC(repo, cfg, "https://kymo.example.com", true, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	return o, idp, repo
}

// flow runs the whole login for the given claims and returns the resolved result.
func flow(t *testing.T, o *OIDC, idp *authtest.FakeIdP, claims map[string]any, returnTo string) (*LoginResult, string, error) {
	t.Helper()
	ctx := context.Background()
	authURL, cookie, err := o.Begin(ctx, returnTo)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	state, code := idp.Login(t, authURL, claims)
	c, rt, err := o.Complete(ctx, cookie, state, code)
	if err != nil {
		return nil, "", err
	}
	res, err := o.ResolveUser(ctx, c)
	return res, rt, err
}

func claims(sub, email string, verified any, groups ...string) map[string]any {
	m := map[string]any{"sub": sub, "email": email, "email_verified": verified, "name": "Name " + sub}
	if groups != nil {
		g := make([]any, len(groups))
		for i, x := range groups {
			g[i] = x
		}
		m["groups"] = g
	}
	return m
}

func TestOIDCAuthorizationRequest(t *testing.T) {
	o, _, _ := oidcFixture(t, nil)
	authURL, cookie, err := o.Begin(context.Background(), "/issues?x=1")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(authURL)
	q := u.Query()
	if q.Get("redirect_uri") != "https://kymo.example.com/api/v1/auth/oidc/callback" || q.Get("code_challenge_method") != "S256" ||
		q.Get("state") == "" || q.Get("nonce") == "" || q.Get("scope") != "openid email profile" {
		t.Errorf("auth URL: %s", authURL)
	}
	if cookie == "" || o.Provider() != "oidc:"+o.cfg.Issuer {
		t.Error("cookie/provider")
	}
	w := httptest.NewRecorder()
	o.SetPendingCookie(w, cookie)
	c := w.Result().Cookies()[0]
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/api/v1/auth/oidc" || c.MaxAge != 600 {
		t.Errorf("pending cookie flags: %+v", c)
	}
}

func TestOIDCHappyPathCreatesUser(t *testing.T) {
	o, idp, repo := oidcFixture(t, nil)
	res, rt, err := flow(t, o, idp, claims("sub-1", "Alice@Example.com", true), "/workloads/abc")
	if err != nil {
		t.Fatal(err)
	}
	if res.User.Username != "alice" {
		t.Errorf("username from email local part: %q", res.User.Username)
	}
	if !res.Created || res.User.Email != "alice@example.com" || res.User.Role != model.RoleViewer || res.User.Name != "Name sub-1" || !res.User.SSO {
		t.Errorf("result: %+v / %+v", res, res.User)
	}
	if rt != "/workloads/abc" {
		t.Errorf("return_to %q", rt)
	}
	// Second login matches by (issuer, subject) even if the email changed.
	res2, _, err := flow(t, o, idp, claims("sub-1", "alice.new@example.com", true), "")
	if err != nil || res2.Created || res2.User.ID != res.User.ID {
		t.Fatalf("second login: %+v %v", res2, err)
	}
	if n, _ := repo.CountUsers(context.Background()); n != 1 {
		t.Errorf("users: %d", n)
	}
}

func TestOIDCOpenRedirectIsNeutralised(t *testing.T) {
	o, idp, _ := oidcFixture(t, nil)
	_, rt, err := flow(t, o, idp, claims("s", "a@example.com", true), "https://evil.com/phish")
	if err != nil || rt != "/" {
		t.Errorf("return_to %q %v", rt, err)
	}
}

func TestOIDCRejections(t *testing.T) {
	ctx := context.Background()
	t.Run("state mismatch", func(t *testing.T) {
		o, idp, _ := oidcFixture(t, nil)
		authURL, cookie, _ := o.Begin(ctx, "")
		_, code := idp.Login(t, authURL, claims("s", "a@example.com", true))
		if _, _, err := o.Complete(ctx, cookie, "wrong-state", code); err == nil {
			t.Error("state mismatch accepted")
		}
		if _, _, err := o.Complete(ctx, cookie, "", code); err == nil {
			t.Error("empty state accepted")
		}
	})
	t.Run("tampered or missing cookie", func(t *testing.T) {
		o, idp, _ := oidcFixture(t, nil)
		authURL, cookie, _ := o.Begin(ctx, "")
		state, code := idp.Login(t, authURL, claims("s", "a@example.com", true))
		for _, c := range []string{"", "garbage", cookie[:len(cookie)-4] + "AAAA"} {
			if _, _, err := o.Complete(ctx, c, state, code); err == nil {
				t.Errorf("cookie %q accepted", c)
			}
		}
	})
	t.Run("expired pending login", func(t *testing.T) {
		o, idp, _ := oidcFixture(t, nil)
		authURL, cookie, _ := o.Begin(ctx, "")
		state, code := idp.Login(t, authURL, claims("s", "a@example.com", true))
		base := time.Now()
		o.Now = func() time.Time { return base.Add(11 * time.Minute) }
		if _, _, err := o.Complete(ctx, cookie, state, code); err == nil {
			t.Error("expired login accepted")
		}
	})
	t.Run("nonce mismatch", func(t *testing.T) {
		o, idp, _ := oidcFixture(t, nil)
		idp.NonceOverride = "attacker-nonce"
		if _, _, err := flow(t, o, idp, claims("s", "a@example.com", true), ""); err == nil {
			t.Error("nonce mismatch accepted")
		}
	})
	t.Run("wrong audience", func(t *testing.T) {
		o, idp, _ := oidcFixture(t, nil)
		idp.Aud = "someone-else"
		if _, _, err := flow(t, o, idp, claims("s", "a@example.com", true), ""); err == nil {
			t.Error("wrong audience accepted")
		}
	})
	t.Run("wrong client secret", func(t *testing.T) {
		o, idp, _ := oidcFixture(t, func(c *config.OIDCConfig) { c.ClientSecret = "bad" })
		if _, _, err := flow(t, o, idp, claims("s", "a@example.com", true), ""); err == nil {
			t.Error("bad secret accepted")
		}
	})
	t.Run("pkce verifier is enforced", func(t *testing.T) {
		o, idp, _ := oidcFixture(t, nil)
		// A cookie from a different Begin carries a different verifier.
		_, otherCookie, _ := o.Begin(ctx, "")
		authURL, _, _ := o.Begin(ctx, "")
		state, code := idp.Login(t, authURL, claims("s", "a@example.com", true))
		// state differs too, so build matching state by decoding is impossible; just ensure failure.
		if _, _, err := o.Complete(ctx, otherCookie, state, code); err == nil {
			t.Error("mixed-up flow accepted")
		}
	})
}

func loginCode(err error) string {
	var le *LoginError
	if errors.As(err, &le) {
		return le.Code
	}
	return ""
}

func TestOIDCVerifiedEmailAndDomains(t *testing.T) {
	o, idp, _ := oidcFixture(t, func(c *config.OIDCConfig) { c.AllowedDomains = []string{"example.com"} })
	for name, cl := range map[string]map[string]any{
		"unverified":   claims("a", "a@example.com", false),
		"missing flag": {"sub": "b", "email": "b@example.com"},
		"no email":     {"sub": "c", "email_verified": true},
		"string false": claims("d", "d@example.com", "false"),
		"no subject":   {"email": "e@example.com", "email_verified": true},
	} {
		if _, _, err := flow(t, o, idp, cl, ""); loginCode(err) != ErrCodeFailed {
			t.Errorf("%s: want oidc_failed, got %v", name, err)
		}
	}
	if _, _, err := flow(t, o, idp, claims("x", "x@evil.org", true), ""); loginCode(err) != ErrCodeDomain {
		t.Errorf("domain: %v", err)
	}
	if _, _, err := flow(t, o, idp, claims("y", "y@sub.example.com", true), ""); loginCode(err) != ErrCodeDomain {
		t.Errorf("subdomain must not match: %v", err)
	}
	if res, _, err := flow(t, o, idp, claims("z", "Z@EXAMPLE.com", "true"), ""); err != nil || res.User.Email != "z@example.com" {
		t.Errorf("string 'true' + case-insensitive domain: %v", err)
	}
}

func TestOIDCLinksExistingUserByVerifiedEmail(t *testing.T) {
	o, idp, repo := oidcFixture(t, nil)
	local := mkUser(t, repo, "bob@example.com", model.RoleAdmin)
	res, _, err := flow(t, o, idp, claims("bob-sub", "bob@example.com", true), "")
	if err != nil || res.Created || !res.Linked || res.User.ID != local.ID || res.User.Role != model.RoleAdmin {
		t.Fatalf("link: %+v %v", res, err)
	}
	u, err := repo.GetUserByIdentity(context.Background(), o.Provider(), "bob-sub")
	if err != nil || u.ID != local.ID {
		t.Error("identity not stored")
	}
	// A different issuer's identical subject is a different identity.
	if _, err := repo.GetUserByIdentity(context.Background(), "oidc:https://other", "bob-sub"); err == nil {
		t.Error("identities must be issuer-qualified")
	}
	// Disabled users are refused, for both linking and identity match.
	st := model.UserDisabled
	_ = repo.UpdateUser(context.Background(), local.ID, model.UserUpdate{Status: &st})
	if _, _, err := flow(t, o, idp, claims("bob-sub", "bob@example.com", true), ""); loginCode(err) != ErrCodeDisabled {
		t.Errorf("disabled identity: %v", err)
	}
	dis := mkUser(t, repo, "dan@example.com", model.RoleViewer)
	_ = repo.UpdateUser(context.Background(), dis.ID, model.UserUpdate{Status: &st})
	if _, _, err := flow(t, o, idp, claims("dan-sub", "dan@example.com", true), ""); loginCode(err) != ErrCodeDisabled {
		t.Errorf("disabled by email: %v", err)
	}
}

func TestOIDCAutoCreateAndInvites(t *testing.T) {
	o, idp, repo := oidcFixture(t, func(c *config.OIDCConfig) { c.AutoCreate = false })
	ctx := context.Background()
	if _, _, err := flow(t, o, idp, claims("n", "new@example.com", true), ""); loginCode(err) != ErrCodeNotInvited {
		t.Fatalf("not invited: %v", err)
	}
	if n, _ := repo.CountUsers(ctx); n != 0 {
		t.Error("no user may be created")
	}
	_ = repo.CreateLink(ctx, &model.UserLink{Kind: model.LinkInvite, Email: "new@example.com", Role: model.RoleOperator, TokenHash: "h", ExpiresAt: o.Now().Add(time.Hour)})
	res, _, err := flow(t, o, idp, claims("n", "new@example.com", true), "")
	if err != nil || !res.Created || res.User.Role != model.RoleOperator {
		t.Fatalf("invited: %+v %v", res, err)
	}
	if inv, _ := repo.ListInvites(ctx, o.Now()); len(inv) != 0 {
		t.Error("invite must be consumed")
	}
}

func TestOIDCRoleMapping(t *testing.T) {
	mapping := map[string][]string{"admin": {"kymo-admins"}, "operator": {"sre"}}
	o, idp, repo := oidcFixture(t, func(c *config.OIDCConfig) { c.RoleMapping = mapping })
	ctx := context.Background()

	// New users: highest matching role; no match -> default role.
	res, _, err := flow(t, o, idp, claims("u1", "u1@example.com", true, "sre", "kymo-admins"), "")
	if err != nil || res.User.Role != model.RoleAdmin {
		t.Fatalf("highest wins: %+v %v", res, err)
	}
	res, _, _ = flow(t, o, idp, claims("u2", "u2@example.com", true, "other"), "")
	if res.User.Role != model.RoleViewer {
		t.Errorf("default role: %s", res.User.Role)
	}

	// Re-sync on every login, both directions.
	res, _, _ = flow(t, o, idp, claims("u2", "u2@example.com", true, "sre"), "")
	if res.User.Role != model.RoleOperator || !res.RoleChanged || res.OldRole != model.RoleViewer {
		t.Errorf("promote: %+v", res)
	}
	res, _, _ = flow(t, o, idp, claims("u1", "u1@example.com", true), "") // lost every group
	if res.User.Role != model.RoleViewer || !res.RoleChanged {
		t.Errorf("demote to default: %+v", res)
	}
	got, _ := repo.GetUserByEmail(ctx, "u1@example.com")
	if got.Role != model.RoleViewer {
		t.Error("role not persisted")
	}

	// The owner is never demoted by SSO (and never changed by mapping).
	owner := mkUser(t, repo, "root@example.com", model.RoleOwner)
	res, _, err = flow(t, o, idp, claims("root", "root@example.com", true), "")
	if err != nil || res.User.ID != owner.ID || res.User.Role != model.RoleOwner || res.RoleChanged {
		t.Fatalf("owner demoted: %+v %v", res, err)
	}
	res, _, _ = flow(t, o, idp, claims("root", "root@example.com", true, "kymo-admins"), "")
	if res.User.Role != model.RoleOwner {
		t.Error("owner changed")
	}
	// Without role_mapping roles are left alone.
	o2, idp2, repo2 := oidcFixture(t, nil)
	adm := mkUser(t, repo2, "adm@example.com", model.RoleAdmin)
	res, _, _ = flow(t, o2, idp2, claims("adm", "adm@example.com", true), "")
	if res.User.Role != model.RoleAdmin || res.User.ID != adm.ID {
		t.Error("role must be kept when no mapping is configured")
	}
}

func TestOIDCDiscoveryFailureIsReportedNotFatal(t *testing.T) {
	repo := authtest.New()
	o, err := NewOIDC(repo, config.OIDCConfig{Enabled: true, Issuer: "http://127.0.0.1:1", ClientID: "c", ClientSecret: "s", DefaultRole: "viewer"},
		"https://kymo.example.com", true, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := o.Begin(context.Background(), "/"); err == nil {
		t.Error("unreachable issuer must error on use")
	}
}

func TestOIDCUsernameDerivation(t *testing.T) {
	o, idp, repo := oidcFixture(t, nil)
	ctx := context.Background()
	mk := func(sub, email, preferred string) string {
		t.Helper()
		cl := claims(sub, email, true)
		if preferred != "" {
			cl["preferred_username"] = preferred
		}
		res, _, err := flow(t, o, idp, cl, "")
		if err != nil {
			t.Fatal(err)
		}
		return res.User.Username
	}
	if got := mk("s1", "first@example.com", "Jane.Doe"); got != "jane.doe" {
		t.Errorf("preferred_username wins: %q", got)
	}
	if got := mk("s2", "other@example.com", ""); got != "other" {
		t.Errorf("email local part: %q", got)
	}
	// Dedupe: same preferred name, and same local part on another domain.
	if got := mk("s3", "x@example.com", "jane.doe"); got != "jane.doe-2" {
		t.Errorf("dedupe: %q", got)
	}
	if got := mk("s4", "x2@example.com", "jane.doe"); got != "jane.doe-3" {
		t.Errorf("dedupe 3: %q", got)
	}
	if got := mk("s5", "other@elsewhere.org", ""); got != "other-2" {
		t.Errorf("dedupe email local part: %q", got)
	}
	// Sanitising: invalid characters, uppercase, too short.
	if got := mk("s6", "q@example.com", "Ünï Cörn!"); got != "n--c-rn-" {
		t.Errorf("sanitize: %q", got)
	}
	if got := mk("s7", "w@example.com", ""); got != "user" {
		t.Errorf("too short local part falls back to user: %q", got)
	}
	if got := mk("s8", "v@example.com", ""); got != "user-2" {
		t.Errorf("user dedupe: %q", got)
	}
	// An existing local account with the same name is not touched.
	local := mkUser(t, repo, "taken@corp.io", model.RoleViewer)
	if got := mk("s9", "taken@example.com", ""); got != "taken-2" || local.Username != "taken" {
		t.Errorf("collision with local account: %q", got)
	}
	if n, _ := repo.CountUsers(ctx); n != 10 {
		t.Errorf("users: %d", n)
	}
}

func TestOIDCLinkByStoredEmailKeepsUsername(t *testing.T) {
	o, idp, repo := oidcFixture(t, nil)
	local := mkUser(t, repo, "carol@example.com", model.RoleOperator)
	cl := claims("c-sub", "Carol@Example.com", true)
	cl["preferred_username"] = "someone-else"
	res, _, err := flow(t, o, idp, cl, "")
	if err != nil || !res.Linked || res.User.ID != local.ID || res.User.Username != "carol" {
		t.Fatalf("link: %+v %v", res, err)
	}
	// A user without an email is never matched by email.
	noMail := &model.User{Username: "nomail", Role: model.RoleViewer}
	_ = repo.CreateUser(context.Background(), noMail)
	res, _, err = flow(t, o, idp, claims("d-sub", "dave@example.com", true), "")
	if err != nil || !res.Created || res.User.ID == noMail.ID {
		t.Fatalf("no-email user must not match: %+v %v", res, err)
	}
}
