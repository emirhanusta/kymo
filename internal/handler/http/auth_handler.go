package httphandler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/auth"
	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

// Lifetimes of one-time links.
const (
	inviteTTL = 7 * 24 * time.Hour
	resetTTL  = time.Hour
)

// WithAuth enables accounts and access control. Without it every protected
// route answers 503 (fail closed).
func WithAuth(a *auth.Service) Option { return func(h *Handler) { h.auth = a } }

// require guards a route with a minimum role.
func (h *Handler) require(min model.Role) gin.HandlerFunc {
	if h.auth == nil {
		return func(c *gin.Context) {
			auth.Abort(c, http.StatusServiceUnavailable, "auth_unavailable", "authentication is not configured")
		}
	}
	return auth.Require(min)
}

// requireAllowPending guards a route that stays open to a session whose
// password must still be changed.
func (h *Handler) requireAllowPending(min model.Role) gin.HandlerFunc {
	if h.auth == nil {
		return h.require(min)
	}
	return auth.RequireAllowPending(min)
}

// audit records an event attributed to the caller of c.
func (h *Handler) audit(c *gin.Context, action, targetType, targetID string, details map[string]any) {
	if h.auth == nil {
		return
	}
	h.auth.Record(c.Request.Context(), auth.ActorOf(auth.IdentityOf(c), c.ClientIP()), action, targetType, targetID, details)
}

func apiErr(c *gin.Context, status int, code, msg string) {
	c.JSON(status, gin.H{"error": msg, "code": code})
}

func (h *Handler) internalErr(c *gin.Context, op string, err error) {
	h.log.Error(op+" failed", zap.Error(err))
	apiErr(c, http.StatusInternalServerError, "internal", "internal error")
}

func bindBody(c *gin.Context, v any) bool {
	if err := c.ShouldBindJSON(v); err != nil {
		apiErr(c, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return false
	}
	return true
}

// authReady rejects calls when auth is not configured or disabled.
func (h *Handler) authReady(c *gin.Context) bool {
	if h.auth == nil {
		apiErr(c, http.StatusServiceUnavailable, "auth_unavailable", "authentication is not configured")
		return false
	}
	if !h.auth.Enabled() {
		apiErr(c, http.StatusConflict, "auth_disabled", "authentication is disabled on this server")
		return false
	}
	return true
}

// baseURL is the externally visible origin used for invite/reset links.
func (h *Handler) baseURL(c *gin.Context) string {
	if h.auth != nil && h.auth.Opts.PublicURL != "" {
		return h.auth.Opts.PublicURL
	}
	scheme := "http"
	if c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	host := c.Request.Host
	if fh := c.GetHeader("X-Forwarded-Host"); fh != "" {
		host = strings.TrimSpace(strings.Split(fh, ",")[0])
	}
	return scheme + "://" + host
}

func (h *Handler) registerAuthRoutes(v1 *gin.RouterGroup) {
	// Public.
	a := v1.Group("/auth")
	a.GET("/config", h.AuthConfig)
	a.POST("/login", h.AuthLogin)
	a.POST("/logout", h.AuthLogout)
	a.GET("/oidc/login", h.OIDCLogin)
	a.GET("/oidc/callback", h.OIDCCallback)
	a.GET("/links/:token", h.GetLink)
	a.POST("/links/:token", h.AcceptLink)

	// Any authenticated caller.
	a.GET("/me", h.requireAllowPending(model.RoleViewer), h.Me)
	v1.PATCH("/me", h.requireAllowPending(model.RoleViewer), h.UpdateMe)
	v1.POST("/me/password", h.requireAllowPending(model.RoleViewer), h.ChangePassword)
	v1.GET("/users", h.require(model.RoleViewer), h.ListUsers)
	v1.GET("/tokens", h.require(model.RoleViewer), h.ListTokens)
	v1.POST("/tokens", h.require(model.RoleViewer), h.CreateToken)
	v1.DELETE("/tokens/:id", h.require(model.RoleViewer), h.RevokeToken)

	// Admin and owner.
	v1.GET("/users/invites", h.require(model.RoleAdmin), h.ListInvites)
	v1.POST("/users/invites", h.require(model.RoleAdmin), h.CreateInvite)
	v1.DELETE("/users/invites/:id", h.require(model.RoleAdmin), h.DeleteInvite)
	v1.PATCH("/users/:id", h.require(model.RoleAdmin), h.UpdateUser)
	v1.POST("/users/:id/reset-link", h.require(model.RoleAdmin), h.CreateResetLink)
	v1.POST("/users/:id/transfer-ownership", h.require(model.RoleOwner), h.TransferOwnership)
	v1.GET("/audit", h.require(model.RoleAdmin), h.ListAudit)
}

// ---------------------------------------------------------------------------
// Public endpoints
// ---------------------------------------------------------------------------

// AuthConfig tells the login page what to show.
//
//	GET /api/v1/auth/config
func (h *Handler) AuthConfig(c *gin.Context) {
	if h.auth == nil {
		apiErr(c, http.StatusServiceUnavailable, "auth_unavailable", "authentication is not configured")
		return
	}
	oidc := gin.H{"enabled": false, "name": ""}
	if h.auth.OIDC != nil {
		oidc = gin.H{"enabled": true, "name": h.auth.OIDC.Name()}
	}
	c.JSON(http.StatusOK, gin.H{
		"auth_enabled":   h.auth.Enabled(),
		"password_login": h.auth.PasswordLogin(),
		"oidc":           oidc,
	})
}

func (h *Handler) rateKeys(c *gin.Context, login string) (ip, lk string) {
	return "ip:" + c.ClientIP(), "login:" + login
}

// limited writes a 429 when any key has used its failure budget.
func (h *Handler) limited(c *gin.Context, keys ...string) bool {
	for _, k := range keys {
		if blocked, wait := h.auth.Limiter.Blocked(k); blocked {
			c.Header("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
			apiErr(c, http.StatusTooManyRequests, "rate_limited", "too many attempts; try again later")
			return true
		}
	}
	return false
}

// AuthLogin signs in with a username or email and a password.
//
//	POST /api/v1/auth/login {login, password}
func (h *Handler) AuthLogin(c *gin.Context) {
	if !h.authReady(c) {
		return
	}
	var in struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if !bindBody(c, &in) {
		return
	}
	if !h.auth.PasswordLogin() {
		apiErr(c, http.StatusForbidden, "password_login_disabled", "password login is disabled; use single sign-on")
		return
	}
	login := strings.ToLower(strings.TrimSpace(in.Login))
	ipKey, loginKey := h.rateKeys(c, login)
	if h.limited(c, ipKey, loginKey) {
		return
	}
	ctx := c.Request.Context()
	ip := c.ClientIP()
	fail := func(reason string) {
		h.auth.Limiter.Fail(ipKey)
		h.auth.Limiter.Fail(loginKey)
		h.auth.Record(ctx, auth.Actor{Label: login, IP: ip}, "login.failure", "user", "", map[string]any{"reason": reason, "method": "password"})
		apiErr(c, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
	}
	// Usernames cannot contain '@', so the two namespaces never collide.
	var (
		u   *model.User
		err error
	)
	if strings.Contains(login, "@") {
		u, err = h.auth.Repo.GetUserByEmail(ctx, login)
	} else {
		u, err = h.auth.Repo.GetUserByUsername(ctx, login)
	}
	switch {
	case errors.Is(err, repository.ErrNotFound):
		auth.BurnPasswordCheck(in.Password)
		fail("unknown_user")
		return
	case err != nil:
		h.internalErr(c, "login lookup", err)
		return
	}
	if u.PasswordHash == "" {
		auth.BurnPasswordCheck(in.Password)
		fail("no_password")
		return
	}
	if !auth.VerifyPassword(in.Password, u.PasswordHash) {
		fail("bad_password")
		return
	}
	if !u.Active() {
		fail("disabled")
		return
	}
	h.auth.Limiter.Reset(loginKey)
	if err := h.auth.StartSession(c.Writer, c.Request, u, model.SessionPassword, ip); err != nil {
		h.internalErr(c, "start session", err)
		return
	}
	h.auth.Record(ctx, auth.Actor{UserID: u.ID, Label: u.Username, IP: ip}, "login.success", "user", fmt.Sprint(u.ID), map[string]any{"method": "password"})
	c.JSON(http.StatusOK, gin.H{"user": u})
}

// AuthLogout ends the current session (idempotent, always 204).
//
//	POST /api/v1/auth/logout
func (h *Handler) AuthLogout(c *gin.Context) {
	if h.auth == nil {
		c.Status(http.StatusNoContent)
		return
	}
	if id := auth.IdentityOf(c); id != nil && id.Via == auth.ViaSession {
		h.audit(c, "logout", "user", fmt.Sprint(id.UserID()), nil)
	}
	if h.auth.Enabled() {
		h.auth.EndSession(c.Writer, c.Request)
	}
	c.Status(http.StatusNoContent)
}

// OIDCLogin redirects to the identity provider.
//
//	GET /api/v1/auth/oidc/login?return_to=/path
func (h *Handler) OIDCLogin(c *gin.Context) {
	if !h.authReady(c) {
		return
	}
	if h.auth.OIDC == nil {
		apiErr(c, http.StatusNotFound, "oidc_disabled", "single sign-on is not enabled")
		return
	}
	authURL, cookie, err := h.auth.OIDC.Begin(c.Request.Context(), c.Query("return_to"))
	if err != nil {
		h.log.Error("oidc login start failed", zap.Error(err))
		c.Redirect(http.StatusFound, "/login?error="+auth.ErrCodeFailed)
		return
	}
	h.auth.OIDC.SetPendingCookie(c.Writer, cookie)
	c.Header("Cache-Control", "no-store")
	c.Redirect(http.StatusFound, authURL)
}

// OIDCCallback completes the SSO login and redirects into the UI.
//
//	GET /api/v1/auth/oidc/callback
func (h *Handler) OIDCCallback(c *gin.Context) {
	if !h.authReady(c) {
		return
	}
	o := h.auth.OIDC
	if o == nil {
		apiErr(c, http.StatusNotFound, "oidc_disabled", "single sign-on is not enabled")
		return
	}
	ctx := c.Request.Context()
	ip := c.ClientIP()
	o.ClearPendingCookie(c.Writer)
	c.Header("Cache-Control", "no-store")
	refuse := func(code, reason string, err error) {
		if err != nil {
			h.log.Warn("sso login failed", zap.String("reason", reason), zap.Error(err))
		}
		h.auth.Record(ctx, auth.Actor{Label: "sso", IP: ip}, "login.failure", "user", "", map[string]any{"reason": reason, "method": "oidc"})
		c.Redirect(http.StatusFound, "/login?error="+url.QueryEscape(code))
	}
	if c.Query("error") != "" {
		refuse(auth.ErrCodeFailed, "provider_error:"+c.Query("error"), nil)
		return
	}
	claims, returnTo, err := o.Complete(ctx, o.PendingCookie(c.Request), c.Query("state"), c.Query("code"))
	if err != nil {
		refuse(auth.ErrCodeFailed, "verification", err)
		return
	}
	res, err := o.ResolveUser(ctx, claims)
	if err != nil {
		var le *auth.LoginError
		if errors.As(err, &le) {
			refuse(le.Code, le.Code, nil)
			return
		}
		refuse(auth.ErrCodeFailed, "resolve", err)
		return
	}
	u := res.User
	sid := fmt.Sprint(u.ID)
	if res.Created {
		h.auth.Record(ctx, auth.Actor{UserID: u.ID, Label: u.Username, IP: ip}, "user.create", "user", sid, map[string]any{"via": "oidc", "role": u.Role})
	}
	if res.RoleChanged {
		h.auth.RevokeSessions(ctx, u.ID, "")
		h.auth.Record(ctx, auth.Actor{Label: "sso", IP: ip}, "user.role_change", "user", sid,
			map[string]any{"from": res.OldRole, "to": u.Role, "via": "oidc"})
	}
	if err := h.auth.StartSession(c.Writer, c.Request, u, model.SessionOIDC, ip); err != nil {
		h.log.Error("start session failed", zap.Error(err))
		c.Redirect(http.StatusFound, "/login?error="+auth.ErrCodeFailed)
		return
	}
	h.auth.Record(ctx, auth.Actor{UserID: u.ID, Label: u.Username, IP: ip}, "login.success", "user", sid, map[string]any{"method": "oidc"})
	c.Redirect(http.StatusFound, auth.SafeReturnTo(returnTo))
}

// lookupLink loads a usable link or writes 404/410.
func (h *Handler) lookupLink(c *gin.Context) *model.UserLink {
	l, err := h.auth.Repo.GetLinkByHash(c.Request.Context(), auth.HashToken(c.Param("token")))
	if errors.Is(err, repository.ErrNotFound) {
		apiErr(c, http.StatusNotFound, "link_not_found", "this link is not valid")
		return nil
	}
	if err != nil {
		h.internalErr(c, "link lookup", err)
		return nil
	}
	if l.UsedAt != nil {
		apiErr(c, http.StatusGone, "link_used", "this link was already used")
		return nil
	}
	if !l.ExpiresAt.After(h.auth.Now()) {
		apiErr(c, http.StatusGone, "link_expired", "this link has expired")
		return nil
	}
	return l
}

// GetLink describes an invite / reset link.
//
//	GET /api/v1/auth/links/:token
func (h *Handler) GetLink(c *gin.Context) {
	if !h.authReady(c) {
		return
	}
	if h.limited(c, "link:"+c.ClientIP()) {
		return
	}
	l := h.lookupLink(c)
	if l == nil {
		h.auth.Limiter.Fail("link:" + c.ClientIP())
		return
	}
	username, email, role := l.Username, l.Email, string(l.Role)
	if l.Kind == model.LinkPasswordReset && l.UserID != nil {
		if u, err := h.auth.Repo.GetUser(c.Request.Context(), *l.UserID); err == nil {
			username, email, role = u.Username, u.Email, string(u.Role)
		}
	}
	c.JSON(http.StatusOK, gin.H{"kind": l.Kind, "username": username, "email": email, "role": role, "expires_at": l.ExpiresAt})
}

// AcceptLink redeems an invite (creates the account) or a reset link (sets the
// password) and signs the user in.
//
//	POST /api/v1/auth/links/:token {name?, password}
func (h *Handler) AcceptLink(c *gin.Context) {
	if !h.authReady(c) {
		return
	}
	var in struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if !bindBody(c, &in) {
		return
	}
	if h.limited(c, "link:"+c.ClientIP()) {
		return
	}
	l := h.lookupLink(c)
	if l == nil {
		h.auth.Limiter.Fail("link:" + c.ClientIP())
		return
	}
	if err := auth.ValidatePassword(in.Password); err != nil {
		apiErr(c, http.StatusBadRequest, "weak_password", err.Error())
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		h.internalErr(c, "hash password", err)
		return
	}
	ctx := c.Request.Context()
	ip := c.ClientIP()
	var u *model.User
	switch l.Kind {
	case model.LinkInvite:
		name := strings.TrimSpace(in.Name)
		if name == "" {
			name = l.Username
		}
		u = &model.User{Username: l.Username, Email: l.Email, Name: name, Role: l.Role, PasswordHash: hash, Status: model.UserActive}
		if err := h.auth.Repo.CreateUser(ctx, u); err != nil {
			if errors.Is(err, repository.ErrConflict) {
				apiErr(c, http.StatusConflict, "user_exists", "an account with this username or email already exists")
				return
			}
			h.internalErr(c, "create invited user", err)
			return
		}
		h.auth.Record(ctx, auth.Actor{UserID: u.ID, Label: u.Username, IP: ip}, "invite.accept", "user", fmt.Sprint(u.ID), map[string]any{"role": u.Role})
	case model.LinkPasswordReset:
		if l.UserID == nil {
			apiErr(c, http.StatusNotFound, "link_not_found", "this link is not valid")
			return
		}
		u, err = h.auth.Repo.GetUser(ctx, *l.UserID)
		if err != nil {
			h.internalErr(c, "reset lookup", err)
			return
		}
		if !u.Active() {
			apiErr(c, http.StatusForbidden, "disabled", "this account is disabled")
			return
		}
		no := false
		if err := h.auth.Repo.UpdateUser(ctx, u.ID, model.UserUpdate{PasswordHash: &hash, MustChangePassword: &no}); err != nil {
			h.internalErr(c, "reset password", err)
			return
		}
		h.auth.RevokeSessions(ctx, u.ID, "")
		u.HasPassword = true
		h.auth.Record(ctx, auth.Actor{UserID: u.ID, Label: u.Username, IP: ip}, "user.password_reset", "user", fmt.Sprint(u.ID), map[string]any{"via": "link"})
	default:
		apiErr(c, http.StatusNotFound, "link_not_found", "this link is not valid")
		return
	}
	if err := h.auth.Repo.ConsumeLink(ctx, l.ID, h.auth.Now()); err != nil && !errors.Is(err, repository.ErrNotFound) {
		h.log.Error("consume link failed", zap.Error(err))
	}
	if err := h.auth.StartSession(c.Writer, c.Request, u, model.SessionPassword, ip); err != nil {
		h.internalErr(c, "start session", err)
		return
	}
	h.auth.Record(ctx, auth.Actor{UserID: u.ID, Label: u.Username, IP: ip}, "login.success", "user", fmt.Sprint(u.ID), map[string]any{"method": "link"})
	c.JSON(http.StatusOK, gin.H{"user": u})
}

// ---------------------------------------------------------------------------
// Authenticated endpoints
// ---------------------------------------------------------------------------

// Me returns the caller and permissions.
//
//	GET /api/v1/auth/me
func (h *Handler) Me(c *gin.Context) {
	id := auth.IdentityOf(c)
	user := *id.User
	user.Role = id.Role
	c.JSON(http.StatusOK, gin.H{
		"auth_enabled": h.auth.Enabled(),
		"user":         &user,
		"permissions":  auth.Permissions(id.Role),
	})
}

// sessionIdentity requires a cookie session (not a token, not auth-disabled).
func (h *Handler) sessionIdentity(c *gin.Context) *auth.Identity {
	id := auth.IdentityOf(c)
	if id.Via == auth.ViaDisabled {
		apiErr(c, http.StatusConflict, "auth_disabled", "authentication is disabled on this server")
		return nil
	}
	if id.Via != auth.ViaSession {
		apiErr(c, http.StatusForbidden, "session_required", "sign in with a browser session to do this")
		return nil
	}
	return id
}

// ChangePassword sets a new password for the caller; other sessions end.
//
//	POST /api/v1/me/password {current_password, new_password}
func (h *Handler) ChangePassword(c *gin.Context) {
	id := h.sessionIdentity(c)
	if id == nil {
		return
	}
	var in struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if !bindBody(c, &in) {
		return
	}
	key := "pw:" + fmt.Sprint(id.UserID())
	if h.limited(c, key) {
		return
	}
	u := id.User
	if u.PasswordHash != "" && !auth.VerifyPassword(in.Current, u.PasswordHash) {
		h.auth.Limiter.Fail(key)
		apiErr(c, http.StatusBadRequest, "invalid_credentials", "the current password is wrong")
		return
	}
	if err := auth.ValidatePassword(in.New); err != nil {
		apiErr(c, http.StatusBadRequest, "weak_password", err.Error())
		return
	}
	if in.New == in.Current {
		apiErr(c, http.StatusBadRequest, "same_password", "the new password must differ from the current one")
		return
	}
	hash, err := auth.HashPassword(in.New)
	if err != nil {
		h.internalErr(c, "hash password", err)
		return
	}
	ctx := c.Request.Context()
	no := false
	if err := h.auth.Repo.UpdateUser(ctx, u.ID, model.UserUpdate{PasswordHash: &hash, MustChangePassword: &no}); err != nil {
		h.internalErr(c, "change password", err)
		return
	}
	h.auth.Limiter.Reset(key)
	h.auth.RevokeSessions(ctx, u.ID, id.SessionHash)
	h.audit(c, "user.password_change", "user", fmt.Sprint(u.ID), nil)
	c.Status(http.StatusNoContent)
}

// checkEmail validates an optional email for user selfID. It returns the
// normalised address ("" = clear) or writes 400 / 409 and reports false.
func (h *Handler) checkEmail(c *gin.Context, raw string, selfID int64) (string, bool) {
	if strings.TrimSpace(raw) == "" {
		return "", true
	}
	email, err := auth.NormalizeEmail(raw)
	if err != nil {
		apiErr(c, http.StatusBadRequest, "invalid_email", "enter a valid email address")
		return "", false
	}
	other, err := h.auth.Repo.GetUserByEmail(c.Request.Context(), email)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		return email, true
	case err != nil:
		h.internalErr(c, "email lookup", err)
		return "", false
	case other.ID == selfID:
		return email, true
	}
	apiErr(c, http.StatusConflict, "email_taken", "this email is already used by another account")
	return "", false
}

// UpdateMe edits the caller's own name and email.
//
//	PATCH /api/v1/me {name?, email?}   (email "" clears it)
func (h *Handler) UpdateMe(c *gin.Context) {
	id := h.sessionIdentity(c)
	if id == nil {
		return
	}
	var in struct {
		Name  *string `json:"name"`
		Email *string `json:"email"`
	}
	if !bindBody(c, &in) {
		return
	}
	upd := model.UserUpdate{}
	changes := map[string]any{}
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || len(n) > 100 {
			apiErr(c, http.StatusBadRequest, "invalid_request", "name must be 1 to 100 characters")
			return
		}
		upd.Name = &n
		changes["name"] = n
	}
	if in.Email != nil {
		em, ok := h.checkEmail(c, *in.Email, id.UserID())
		if !ok {
			return
		}
		upd.Email = &em
		changes["email"] = em
	}
	if len(changes) == 0 {
		apiErr(c, http.StatusBadRequest, "invalid_request", "nothing to change")
		return
	}
	ctx := c.Request.Context()
	if err := h.auth.Repo.UpdateUser(ctx, id.UserID(), upd); err != nil {
		if errors.Is(err, repository.ErrConflict) {
			apiErr(c, http.StatusConflict, "email_taken", "this email is already used by another account")
			return
		}
		h.internalErr(c, "update profile", err)
		return
	}
	h.audit(c, "user.profile_update", "user", fmt.Sprint(id.UserID()), map[string]any{"changes": changes})
	fresh, err := h.auth.Repo.GetUser(ctx, id.UserID())
	if err != nil {
		h.internalErr(c, "reload user", err)
		return
	}
	fresh.Role = id.Role
	c.JSON(http.StatusOK, gin.H{"user": fresh})
}

// ListUsers returns every user.
//
//	GET /api/v1/users
func (h *Handler) ListUsers(c *gin.Context) {
	users, err := h.auth.Repo.ListUsers(c.Request.Context())
	if err != nil {
		h.internalErr(c, "list users", err)
		return
	}
	c.JSON(http.StatusOK, users)
}

func forbidden(c *gin.Context, required model.Role, msg string) {
	c.JSON(http.StatusForbidden, gin.H{"error": msg, "code": "forbidden", "required_role": string(required)})
}

func userID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		apiErr(c, http.StatusBadRequest, "invalid_request", "id must be a number")
		return 0, false
	}
	return id, true
}

// target loads the user named by :id, writing 404 when missing.
func (h *Handler) target(c *gin.Context) (*model.User, bool) {
	id, ok := userID(c)
	if !ok {
		return nil, false
	}
	u, err := h.auth.Repo.GetUser(c.Request.Context(), id)
	if errors.Is(err, repository.ErrNotFound) {
		apiErr(c, http.StatusNotFound, "not_found", "user not found")
		return nil, false
	}
	if err != nil {
		h.internalErr(c, "get user", err)
		return nil, false
	}
	return u, true
}

// UpdateUser changes a user's name, role or status.
//
//	PATCH /api/v1/users/:id {name?, role?, status?}
func (h *Handler) UpdateUser(c *gin.Context) {
	var in struct {
		Name   *string `json:"name"`
		Email  *string `json:"email"`
		Role   *string `json:"role"`
		Status *string `json:"status"`
	}
	if !bindBody(c, &in) {
		return
	}
	actor := auth.IdentityOf(c)
	u, ok := h.target(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	upd := model.UserUpdate{}
	changes := map[string]any{}
	touchesAccess := in.Role != nil || in.Status != nil

	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || len(n) > 100 {
			apiErr(c, http.StatusBadRequest, "invalid_request", "name must be 1 to 100 characters")
			return
		}
		upd.Name = &n
		changes["name"] = n
	}
	if in.Email != nil {
		em, ok := h.checkEmail(c, *in.Email, u.ID)
		if !ok {
			return
		}
		upd.Email = &em
		changes["email"] = map[string]any{"from": u.Email, "to": em}
	}
	if in.Role != nil {
		r, ok := model.ParseRole(*in.Role)
		if !ok {
			apiErr(c, http.StatusBadRequest, "invalid_request", "role must be viewer, operator, admin or owner")
			return
		}
		if r == model.RoleOwner {
			apiErr(c, http.StatusBadRequest, "invalid_request", "use transfer-ownership to change the owner")
			return
		}
		if r == model.RoleAdmin && actor.Role != model.RoleOwner {
			forbidden(c, model.RoleOwner, "only the owner can grant the admin role")
			return
		}
		upd.Role = &r
		changes["role"] = map[string]any{"from": u.Role, "to": r}
	}
	if in.Status != nil {
		if *in.Status != model.UserActive && *in.Status != model.UserDisabled {
			apiErr(c, http.StatusBadRequest, "invalid_request", "status must be active or disabled")
			return
		}
		upd.Status = in.Status
		changes["status"] = map[string]any{"from": u.Status, "to": *in.Status}
	}
	if len(changes) == 0 {
		apiErr(c, http.StatusBadRequest, "invalid_request", "nothing to change")
		return
	}
	if touchesAccess && u.ID == actor.UserID() {
		forbidden(c, model.RoleOwner, "you cannot change your own role or status")
		return
	}
	if u.Role == model.RoleOwner && touchesAccess {
		forbidden(c, model.RoleOwner, "the owner cannot be demoted or disabled; transfer ownership first")
		return
	}
	if (u.Role == model.RoleAdmin || u.Role == model.RoleOwner) && actor.Role != model.RoleOwner && !(u.ID == actor.UserID() && !touchesAccess) {
		forbidden(c, model.RoleOwner, "only the owner can change an admin")
		return
	}
	if err := h.auth.Repo.UpdateUser(ctx, u.ID, upd); err != nil {
		if errors.Is(err, repository.ErrConflict) {
			apiErr(c, http.StatusConflict, "email_taken", "this email is already used by another account")
			return
		}
		h.internalErr(c, "update user", err)
		return
	}
	if touchesAccess {
		h.auth.RevokeSessions(ctx, u.ID, "")
	}
	h.audit(c, "user.update", "user", fmt.Sprint(u.ID), map[string]any{"username": u.Username, "changes": changes})
	fresh, err := h.auth.Repo.GetUser(ctx, u.ID)
	if err != nil {
		h.internalErr(c, "reload user", err)
		return
	}
	c.JSON(http.StatusOK, fresh)
}

// newLink stores a one-time link and returns its token.
func (h *Handler) newLink(ctx context.Context, l *model.UserLink) (string, error) {
	tok, err := auth.RandomToken(32)
	if err != nil {
		return "", err
	}
	l.TokenHash = auth.HashToken(tok)
	if err := h.auth.Repo.CreateLink(ctx, l); err != nil {
		return "", err
	}
	return tok, nil
}

func actorID(id *auth.Identity) *int64 {
	if id == nil || id.UserID() == 0 {
		return nil
	}
	v := id.UserID()
	return &v
}

// CreateResetLink creates a one-hour password reset link for a local user.
//
//	POST /api/v1/users/:id/reset-link
func (h *Handler) CreateResetLink(c *gin.Context) {
	actor := auth.IdentityOf(c)
	u, ok := h.target(c)
	if !ok {
		return
	}
	if (u.Role == model.RoleAdmin || u.Role == model.RoleOwner) && actor.Role != model.RoleOwner && u.ID != actor.UserID() {
		forbidden(c, model.RoleOwner, "only the owner can reset an admin's password")
		return
	}
	if !u.HasPassword {
		apiErr(c, http.StatusConflict, "no_local_password", "this user signs in with single sign-on and has no local password")
		return
	}
	uid := u.ID
	exp := h.auth.Now().Add(resetTTL)
	tok, err := h.newLink(c.Request.Context(), &model.UserLink{Kind: model.LinkPasswordReset, Username: u.Username, Email: u.Email, UserID: &uid, CreatedBy: actorID(actor), ExpiresAt: exp})
	if err != nil {
		h.internalErr(c, "create reset link", err)
		return
	}
	h.audit(c, "user.reset_link", "user", fmt.Sprint(u.ID), map[string]any{"username": u.Username})
	c.JSON(http.StatusOK, gin.H{"url": h.baseURL(c) + "/reset/" + tok, "expires_at": exp})
}

// TransferOwnership hands the owner role to an active admin.
//
//	POST /api/v1/users/:id/transfer-ownership
func (h *Handler) TransferOwnership(c *gin.Context) {
	actor := auth.IdentityOf(c)
	u, ok := h.target(c)
	if !ok {
		return
	}
	if actor.UserID() == 0 || actor.Via == auth.ViaToken {
		apiErr(c, http.StatusForbidden, "session_required", "sign in with a browser session to transfer ownership")
		return
	}
	if u.ID == actor.UserID() || u.Role != model.RoleAdmin || !u.Active() {
		apiErr(c, http.StatusBadRequest, "invalid_target", "the new owner must be another active admin")
		return
	}
	ctx := c.Request.Context()
	if err := h.auth.Repo.TransferOwnership(ctx, actor.UserID(), u.ID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			apiErr(c, http.StatusConflict, "ownership_changed", "ownership changed meanwhile; reload and retry")
			return
		}
		h.internalErr(c, "transfer ownership", err)
		return
	}
	h.auth.RevokeSessions(ctx, u.ID, "")
	h.auth.RevokeSessions(ctx, actor.UserID(), actor.SessionHash)
	h.audit(c, "user.ownership_transfer", "user", fmt.Sprint(u.ID), map[string]any{"from": actor.User.Username, "to": u.Username})
	c.Status(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Invites
// ---------------------------------------------------------------------------

// ListInvites returns pending invites.
//
//	GET /api/v1/users/invites
func (h *Handler) ListInvites(c *gin.Context) {
	ctx := c.Request.Context()
	links, err := h.auth.Repo.ListInvites(ctx, h.auth.Now())
	if err != nil {
		h.internalErr(c, "list invites", err)
		return
	}
	names := map[int64]string{}
	if users, err := h.auth.Repo.ListUsers(ctx); err == nil {
		for _, u := range users {
			names[u.ID] = u.Username
		}
	}
	out := make([]gin.H, 0, len(links))
	for _, l := range links {
		by, byID := "", any(nil)
		if l.CreatedBy != nil {
			by, byID = names[*l.CreatedBy], *l.CreatedBy
		}
		out = append(out, gin.H{"id": l.ID, "username": l.Username, "email": l.Email, "role": l.Role, "created_by": by, "created_by_id": byID,
			"created_at": l.CreatedAt, "expires_at": l.ExpiresAt})
	}
	c.JSON(http.StatusOK, out)
}

// CreateInvite creates a 7-day invite link.
//
//	POST /api/v1/users/invites {username, role, email?}
func (h *Handler) CreateInvite(c *gin.Context) {
	var in struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Role     string `json:"role"`
	}
	if !bindBody(c, &in) {
		return
	}
	actor := auth.IdentityOf(c)
	username, err := auth.NormalizeUsername(in.Username)
	if err != nil {
		apiErr(c, http.StatusBadRequest, "invalid_username", err.Error())
		return
	}
	role, ok := model.ParseRole(in.Role)
	if !ok || role == model.RoleOwner {
		apiErr(c, http.StatusBadRequest, "invalid_request", "role must be viewer, operator or admin")
		return
	}
	if role == model.RoleAdmin && actor.Role != model.RoleOwner {
		forbidden(c, model.RoleOwner, "only the owner can invite admins")
		return
	}
	ctx := c.Request.Context()
	email := ""
	if strings.TrimSpace(in.Email) != "" {
		if email, err = auth.NormalizeEmail(in.Email); err != nil {
			apiErr(c, http.StatusBadRequest, "invalid_email", "enter a valid email address")
			return
		}
	}
	taken := func(code, msg string, err error) bool {
		if err == nil {
			apiErr(c, http.StatusConflict, code, msg)
			return true
		}
		if !errors.Is(err, repository.ErrNotFound) {
			h.internalErr(c, "invite lookup", err)
			return true
		}
		return false
	}
	if _, err := h.auth.Repo.GetUserByUsername(ctx, username); taken("username_taken", "a user with this username already exists", err) {
		return
	}
	if _, err := h.auth.Repo.GetInviteByUsername(ctx, username, h.auth.Now()); taken("username_taken", "a pending invite for this username already exists", err) {
		return
	}
	if email != "" {
		if _, err := h.auth.Repo.GetUserByEmail(ctx, email); taken("email_taken", "a user with this email already exists", err) {
			return
		}
	}
	exp := h.auth.Now().Add(inviteTTL)
	l := &model.UserLink{Kind: model.LinkInvite, Username: username, Email: email, Role: role, CreatedBy: actorID(actor), ExpiresAt: exp}
	tok, err := h.newLink(ctx, l)
	if err != nil {
		h.internalErr(c, "create invite", err)
		return
	}
	h.audit(c, "invite.create", "invite", fmt.Sprint(l.ID), map[string]any{"username": username, "email": email, "role": role})
	c.JSON(http.StatusCreated, gin.H{"id": l.ID, "username": username, "email": email, "role": role, "url": h.baseURL(c) + "/invite/" + tok, "expires_at": exp})
}

// DeleteInvite revokes an invite.
//
//	DELETE /api/v1/users/invites/:id
func (h *Handler) DeleteInvite(c *gin.Context) {
	id, ok := userID(c)
	if !ok {
		return
	}
	if err := h.auth.Repo.DeleteInvite(c.Request.Context(), id); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			apiErr(c, http.StatusNotFound, "not_found", "invite not found")
			return
		}
		h.internalErr(c, "delete invite", err)
		return
	}
	h.audit(c, "invite.delete", "invite", fmt.Sprint(id), nil)
	c.Status(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// API tokens
// ---------------------------------------------------------------------------

// ListTokens returns the caller's tokens; ?all=1 (admin) returns everyone's.
//
//	GET /api/v1/tokens[?all=1]
func (h *Handler) ListTokens(c *gin.Context) {
	id := auth.IdentityOf(c)
	uid := id.UserID()
	if all := c.Query("all"); all == "1" || all == "true" {
		if !auth.Can(id.Role, auth.PermTokensManageAll) {
			forbidden(c, model.RoleAdmin, "only admins can list every token")
			return
		}
		uid = 0
	} else if uid == 0 {
		c.JSON(http.StatusOK, []*model.APIToken{})
		return
	}
	list, err := h.auth.Repo.ListTokens(c.Request.Context(), uid)
	if err != nil {
		h.internalErr(c, "list tokens", err)
		return
	}
	c.JSON(http.StatusOK, list)
}

// CreateToken mints an API token (the secret is returned once).
//
//	POST /api/v1/tokens {name, role, expires_in_days}
func (h *Handler) CreateToken(c *gin.Context) {
	id := h.sessionIdentity(c)
	if id == nil {
		return
	}
	var in struct {
		Name          string `json:"name"`
		Role          string `json:"role"`
		ExpiresInDays *int   `json:"expires_in_days"`
	}
	if !bindBody(c, &in) {
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 100 {
		apiErr(c, http.StatusBadRequest, "invalid_request", "name must be 1 to 100 characters")
		return
	}
	role, ok := model.ParseRole(in.Role)
	if !ok || role == model.RoleOwner {
		apiErr(c, http.StatusBadRequest, "invalid_request", "role must be viewer, operator or admin")
		return
	}
	if role.Rank() > id.Role.Rank() {
		forbidden(c, role, "a token cannot have a higher role than you")
		return
	}
	var expires *time.Time
	if in.ExpiresInDays != nil {
		if *in.ExpiresInDays < 1 || *in.ExpiresInDays > 3650 {
			apiErr(c, http.StatusBadRequest, "invalid_request", "expires_in_days must be between 1 and 3650, or null")
			return
		}
		t := h.auth.Now().Add(time.Duration(*in.ExpiresInDays) * 24 * time.Hour)
		expires = &t
	}
	secretTok, prefix, err := auth.NewAPIToken()
	if err != nil {
		h.internalErr(c, "generate token", err)
		return
	}
	t := &model.APIToken{UserID: id.UserID(), UserEmail: id.User.Email, UserUsername: id.User.Username, Name: name, Prefix: prefix, Role: role,
		TokenHash: auth.HashToken(secretTok), ExpiresAt: expires}
	if err := h.auth.Repo.CreateToken(c.Request.Context(), t); err != nil {
		h.internalErr(c, "create token", err)
		return
	}
	h.audit(c, "token.create", "token", fmt.Sprint(t.ID), map[string]any{"name": name, "role": role, "prefix": prefix})
	c.JSON(http.StatusCreated, gin.H{"token": t, "secret": secretTok})
}

// RevokeToken revokes a token (own tokens, or any as admin).
//
//	DELETE /api/v1/tokens/:id
func (h *Handler) RevokeToken(c *gin.Context) {
	id := auth.IdentityOf(c)
	tid, ok := userID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	t, err := h.auth.Repo.GetToken(ctx, tid)
	if errors.Is(err, repository.ErrNotFound) {
		apiErr(c, http.StatusNotFound, "not_found", "token not found")
		return
	}
	if err != nil {
		h.internalErr(c, "get token", err)
		return
	}
	if t.UserID != id.UserID() && !auth.Can(id.Role, auth.PermTokensManageAll) {
		forbidden(c, model.RoleAdmin, "you can only revoke your own tokens")
		return
	}
	if err := h.auth.Repo.RevokeToken(ctx, tid, h.auth.Now()); err != nil {
		h.internalErr(c, "revoke token", err)
		return
	}
	h.audit(c, "token.revoke", "token", fmt.Sprint(tid), map[string]any{"name": t.Name, "owner": t.UserUsername})
	c.Status(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Audit
// ---------------------------------------------------------------------------

// ListAudit returns audit events, newest first.
//
//	GET /api/v1/audit?user_id&action&before&limit
func (h *Handler) ListAudit(c *gin.Context) {
	q := repository.AuditQuery{Action: strings.TrimSpace(c.Query("action")), Limit: 100}
	if v := c.Query("user_id"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			apiErr(c, http.StatusBadRequest, "invalid_request", "user_id must be a number")
			return
		}
		q.UserID = n
	}
	if v := c.Query("before"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			apiErr(c, http.StatusBadRequest, "invalid_request", "before must be an RFC3339 timestamp")
			return
		}
		q.Before = t
	}
	if v, err := strconv.Atoi(c.Query("limit")); err == nil && v > 0 {
		q.Limit = min(v, 500)
	}
	list, err := h.auth.Repo.ListAudit(c.Request.Context(), q)
	if err != nil {
		h.internalErr(c, "list audit", err)
		return
	}
	c.JSON(http.StatusOK, list)
}
