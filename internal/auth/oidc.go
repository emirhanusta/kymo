package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"go.uber.org/zap"
	"golang.org/x/oauth2"

	"github.com/emirhanusta/kymo/internal/config"
	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
	"github.com/emirhanusta/kymo/internal/secret"
)

// OIDC callback path (relative to server.public_url).
const OIDCCallbackPath = "/api/v1/auth/oidc/callback"

// Error codes the callback redirects with (/login?error=<code>).
const (
	ErrCodeDomain     = "domain_not_allowed"
	ErrCodeNotInvited = "not_invited"
	ErrCodeDisabled   = "disabled"
	ErrCodeFailed     = "oidc_failed"
)

const (
	oidcCookie  = "kymo_oidc"
	oidcPending = 10 * time.Minute
)

// LoginError is a sign-in refusal with a machine code for the login page.
type LoginError struct{ Code string }

func (e *LoginError) Error() string { return "sso login refused: " + e.Code }

func loginErr(code string) error { return &LoginError{Code: code} }

// Claims are the identity attributes taken from a verified ID token.
type Claims struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	// PreferredUsername is the preferred_username claim (may be empty).
	PreferredUsername string
	Groups            []string
}

// OIDC implements the authorization-code flow with PKCE, state and nonce.
type OIDC struct {
	repo        repository.AuthRepository
	cfg         config.OIDCConfig
	redirectURL string
	secure      bool
	log         *zap.Logger
	box         *secret.Box // seals the pending-login cookie; key lives in memory only

	HTTPClient *http.Client
	Now        func() time.Time

	mu       sync.Mutex
	provider *oidc.Provider
}

// NewOIDC validates the configuration. The provider (discovery document) is
// fetched lazily so a temporarily unreachable IdP does not stop KYMO.
func NewOIDC(repo repository.AuthRepository, cfg config.OIDCConfig, publicURL string, secure bool, log *zap.Logger) (*OIDC, error) {
	if publicURL == "" {
		return nil, errors.New("auth.oidc.enabled requires server.public_url")
	}
	key, err := RandomToken(32)
	if err != nil {
		return nil, err
	}
	box, err := secret.New(key)
	if err != nil {
		return nil, err
	}
	return &OIDC{
		repo: repo, cfg: cfg, redirectURL: strings.TrimRight(publicURL, "/") + OIDCCallbackPath, secure: secure, log: log, box: box,
		HTTPClient: &http.Client{Timeout: 15 * time.Second}, Now: time.Now,
	}, nil
}

// Name is the sign-in button label.
func (o *OIDC) Name() string { return o.cfg.Name }

// RedirectURL is the callback URL to register at the provider.
func (o *OIDC) RedirectURL() string { return o.redirectURL }

// Provider is the identifier stored in user_identities.provider.
func (o *OIDC) Provider() string { return "oidc:" + o.cfg.Issuer }

func (o *OIDC) discover(ctx context.Context) (*oidc.Provider, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.provider != nil {
		return o.provider, nil
	}
	p, err := oidc.NewProvider(oidc.ClientContext(ctx, o.HTTPClient), o.cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery for %s: %w", o.cfg.Issuer, err)
	}
	o.provider = p
	return p, nil
}

func (o *OIDC) oauth2(p *oidc.Provider) *oauth2.Config {
	return &oauth2.Config{
		ClientID: o.cfg.ClientID, ClientSecret: o.cfg.ClientSecret, Endpoint: p.Endpoint(),
		RedirectURL: o.redirectURL, Scopes: o.cfg.Scopes,
	}
}

type pendingLogin struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	ReturnTo string `json:"r"`
	Exp      int64  `json:"e"`
}

// SafeReturnTo returns s when it is a same-site relative path and "/"
// otherwise, which prevents open redirects.
func SafeReturnTo(s string) string {
	if s == "" || len(s) > 2048 || s[0] != '/' {
		return "/"
	}
	if strings.HasPrefix(s, "//") || strings.ContainsAny(s, "\\\r\n\t") {
		return "/"
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "/"
		}
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || !strings.HasPrefix(u.Path, "/") {
		return "/"
	}
	return s
}

// Begin starts a login: it returns the provider URL to redirect to and the
// sealed cookie value that carries state, nonce and the PKCE verifier.
func (o *OIDC) Begin(ctx context.Context, returnTo string) (authURL, cookieValue string, err error) {
	p, err := o.discover(ctx)
	if err != nil {
		return "", "", err
	}
	state, err := RandomToken(24)
	if err != nil {
		return "", "", err
	}
	nonce, err := RandomToken(24)
	if err != nil {
		return "", "", err
	}
	verifier := oauth2.GenerateVerifier()
	pl := pendingLogin{State: state, Nonce: nonce, Verifier: verifier, ReturnTo: SafeReturnTo(returnTo), Exp: o.Now().Add(oidcPending).Unix()}
	raw, _ := json.Marshal(pl)
	sealed, err := o.box.Seal(raw)
	if err != nil {
		return "", "", err
	}
	u := o.oauth2(p).AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
	return u, base64.RawURLEncoding.EncodeToString(sealed), nil
}

// SetPendingCookie stores the sealed login state.
func (o *OIDC) SetPendingCookie(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{
		Name: oidcCookie, Value: value, Path: "/api/v1/auth/oidc", HttpOnly: true, Secure: o.secure,
		SameSite: http.SameSiteLaxMode, MaxAge: int(oidcPending.Seconds()),
	})
}

// ClearPendingCookie removes the pending-login cookie.
func (o *OIDC) ClearPendingCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: "", Path: "/api/v1/auth/oidc", HttpOnly: true, Secure: o.secure,
		SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

// PendingCookie reads the pending-login cookie of r.
func (o *OIDC) PendingCookie(r *http.Request) string {
	if ck, err := r.Cookie(oidcCookie); err == nil {
		return ck.Value
	}
	return ""
}

func (o *OIDC) open(cookieValue string) (*pendingLogin, error) {
	sealed, err := base64.RawURLEncoding.DecodeString(cookieValue)
	if err != nil {
		return nil, errors.New("bad login cookie")
	}
	raw, err := o.box.Open(sealed)
	if err != nil {
		return nil, errors.New("bad login cookie")
	}
	var pl pendingLogin
	if err := json.Unmarshal(raw, &pl); err != nil {
		return nil, errors.New("bad login cookie")
	}
	if o.Now().Unix() > pl.Exp {
		return nil, errors.New("login expired")
	}
	return &pl, nil
}

// Complete finishes the flow: it checks state, exchanges the code (with the
// PKCE verifier), verifies the ID token signature, audience, expiry and nonce
// and returns the claims plus the safe return path.
func (o *OIDC) Complete(ctx context.Context, cookieValue, state, code string) (*Claims, string, error) {
	pl, err := o.open(cookieValue)
	if err != nil {
		return nil, "", err
	}
	if state == "" || code == "" || !constEq(state, pl.State) {
		return nil, "", errors.New("state mismatch")
	}
	p, err := o.discover(ctx)
	if err != nil {
		return nil, "", err
	}
	ctx = oidc.ClientContext(ctx, o.HTTPClient)
	tok, err := o.oauth2(p).Exchange(ctx, code, oauth2.VerifierOption(pl.Verifier))
	if err != nil {
		return nil, "", fmt.Errorf("code exchange: %w", err)
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		return nil, "", errors.New("provider returned no id_token")
	}
	idt, err := p.Verifier(&oidc.Config{ClientID: o.cfg.ClientID, Now: o.Now}).Verify(ctx, rawID)
	if err != nil {
		return nil, "", fmt.Errorf("id token: %w", err)
	}
	if !constEq(idt.Nonce, pl.Nonce) {
		return nil, "", errors.New("nonce mismatch")
	}
	var all map[string]any
	if err := idt.Claims(&all); err != nil {
		return nil, "", fmt.Errorf("claims: %w", err)
	}
	c := &Claims{Subject: idt.Subject}
	c.Email, _ = all["email"].(string)
	c.EmailVerified = truthy(all["email_verified"])
	c.Name, _ = all["name"].(string)
	c.PreferredUsername, _ = all["preferred_username"].(string)
	if c.Name == "" {
		c.Name = c.PreferredUsername
	}
	c.Groups = claimStrings(all, o.cfg.GroupsClaim)
	return c, pl.ReturnTo, nil
}

func constEq(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(x, "true")
	}
	return false
}

// claimStrings extracts a string list from claims by key; dotted paths such as
// "realm_access.roles" are followed when the literal key is absent.
func claimStrings(all map[string]any, key string) []string {
	if key == "" {
		return nil
	}
	v, ok := all[key]
	if !ok && strings.Contains(key, ".") {
		var cur any = all
		for _, part := range strings.Split(key, ".") {
			m, isMap := cur.(map[string]any)
			if !isMap {
				cur = nil
				break
			}
			cur = m[part]
		}
		v = cur
	}
	switch x := v.(type) {
	case string:
		if x == "" {
			return nil
		}
		return []string{x}
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// MapRole returns the highest role whose groups intersect the user's groups.
func MapRole(groups []string, mapping map[string][]string) (model.Role, bool) {
	var best model.Role
	for roleName, want := range mapping {
		r, ok := model.ParseRole(roleName)
		if !ok || r == model.RoleOwner || r.Rank() <= best.Rank() {
			continue
		}
		for _, w := range want {
			for _, g := range groups {
				if w == g {
					best = r
				}
			}
		}
	}
	return best, best != ""
}

// LoginResult is the outcome of resolving SSO claims to a user.
type LoginResult struct {
	User        *model.User
	Created     bool
	Linked      bool
	RoleChanged bool
	OldRole     model.Role
}

// ResolveUser maps verified claims to a KYMO user: it enforces verified email
// and allowed domains, matches by (issuer-qualified provider, subject) and then
// by verified email, creates users (auto_create or a pending invite) and
// re-syncs the role from role_mapping (never touching the owner).
func (o *OIDC) ResolveUser(ctx context.Context, c *Claims) (*LoginResult, error) {
	email, err := NormalizeEmail(c.Email)
	if err != nil || !c.EmailVerified || c.Subject == "" {
		return nil, loginErr(ErrCodeFailed)
	}
	if len(o.cfg.AllowedDomains) > 0 {
		domain := email[strings.LastIndex(email, "@")+1:]
		ok := false
		for _, d := range o.cfg.AllowedDomains {
			ok = ok || strings.EqualFold(d, domain)
		}
		if !ok {
			return nil, loginErr(ErrCodeDomain)
		}
	}
	provider := o.Provider()
	res := &LoginResult{}

	u, err := o.repo.GetUserByIdentity(ctx, provider, c.Subject)
	switch {
	case err == nil:
		res.User = u
	case errors.Is(err, repository.ErrNotFound):
		u, err = o.repo.GetUserByEmail(ctx, email)
		switch {
		case err == nil:
			if !u.Active() {
				return nil, loginErr(ErrCodeDisabled)
			}
			if err := o.repo.AddIdentity(ctx, u.ID, provider, c.Subject, email); err != nil {
				return nil, err
			}
			res.User, res.Linked = u, true
		case errors.Is(err, repository.ErrNotFound):
			nu, err := o.createUser(ctx, c, email)
			if err != nil {
				return nil, err
			}
			res.User, res.Created = nu, true
		default:
			return nil, err
		}
	default:
		return nil, err
	}
	if !res.User.Active() {
		return nil, loginErr(ErrCodeDisabled)
	}

	// Re-sync the role from group mapping (new users already got it).
	if len(o.cfg.RoleMapping) > 0 && !res.Created && res.User.Role != model.RoleOwner {
		want, ok := MapRole(c.Groups, o.cfg.RoleMapping)
		if !ok {
			want, _ = model.ParseRole(o.cfg.DefaultRole)
		}
		if want.Valid() && want != res.User.Role {
			res.OldRole = res.User.Role
			if err := o.repo.UpdateUser(ctx, res.User.ID, model.UserUpdate{Role: &want}); err != nil {
				return nil, err
			}
			res.User.Role, res.RoleChanged = want, true
		}
	}
	if name := strings.TrimSpace(c.Name); name != "" && res.User.Name == "" {
		if err := o.repo.UpdateUser(ctx, res.User.ID, model.UserUpdate{Name: &name}); err == nil {
			res.User.Name = name
		}
	}
	return res, nil
}

func (o *OIDC) createUser(ctx context.Context, c *Claims, email string) (*model.User, error) {
	now := o.Now()
	role, _ := model.ParseRole(o.cfg.DefaultRole)
	if !role.Valid() {
		role = model.RoleViewer
	}
	invite, ierr := o.repo.GetInviteByEmail(ctx, email, now)
	if ierr != nil && !errors.Is(ierr, repository.ErrNotFound) {
		return nil, ierr
	}
	if ierr == nil && invite.Role.Valid() && invite.Role != model.RoleOwner {
		role = invite.Role
	}
	if len(o.cfg.RoleMapping) > 0 {
		if r, ok := MapRole(c.Groups, o.cfg.RoleMapping); ok && (ierr != nil || r.Rank() > role.Rank()) {
			role = r
		}
	}
	if !o.cfg.AutoCreate && ierr != nil {
		return nil, loginErr(ErrCodeNotInvited)
	}
	u := &model.User{Email: email, Name: strings.TrimSpace(c.Name), Role: role, Status: model.UserActive}
	if err := o.createWithUsername(ctx, u, DeriveUsername(c, email)); err != nil {
		return nil, err
	}
	if err := o.repo.AddIdentity(ctx, u.ID, o.Provider(), c.Subject, email); err != nil {
		return nil, err
	}
	u.SSO = true
	if ierr == nil {
		if err := o.repo.ConsumeLink(ctx, invite.ID, now); err != nil && !errors.Is(err, repository.ErrNotFound) {
			o.log.Warn("consuming invite failed", zap.Error(err))
		}
	}
	return u, nil
}

// DeriveUsername picks the username base for a new SSO user: preferred_username,
// else the email local part, else "user" (sanitized; not yet de-duplicated).
func DeriveUsername(c *Claims, email string) string {
	switch {
	case strings.TrimSpace(c.PreferredUsername) != "":
		return SanitizeUsername(c.PreferredUsername)
	case email != "":
		return SanitizeUsername(email[:strings.LastIndex(email, "@")])
	}
	return "user"
}

// createWithUsername inserts u with the first free username derived from base,
// retrying when a concurrent sign-in takes the candidate.
func (o *OIDC) createWithUsername(ctx context.Context, u *model.User, base string) error {
	for attempt := 0; attempt < 5; attempt++ {
		name, err := UniqueUsername(ctx, o.repo, base)
		if err != nil {
			return err
		}
		u.Username = name
		err = o.repo.CreateUser(ctx, u)
		if !errors.Is(err, repository.ErrConflict) {
			return err
		}
		if _, gerr := o.repo.GetUserByEmail(ctx, u.Email); gerr == nil {
			return err // the email, not the username, collided
		}
	}
	return repository.ErrConflict
}
