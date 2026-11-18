package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/emirhanusta/kymo/internal/config"
	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

// SessionCookie is the name of the session cookie.
const SessionCookie = "kymo_session"

// touchEvery throttles last_seen writes.
const touchEvery = time.Minute

// Via values of an Identity.
const (
	ViaSession  = "session"
	ViaToken    = "token"
	ViaDisabled = "disabled"
)

// ErrUnauthenticated means credentials were presented but are not valid.
var ErrUnauthenticated = errors.New("unauthenticated")

// Options configures a Service.
type Options struct {
	Enabled        bool
	PublicURL      string
	AllowedOrigins []string
	IdleTimeout    time.Duration
	MaxAge         time.Duration
	OIDC           config.OIDCConfig
}

// OptionsFromConfig derives Options from the loaded configuration.
func OptionsFromConfig(c *config.Config) Options {
	return Options{
		Enabled: c.Auth.Enabled, PublicURL: c.Server.PublicURL, AllowedOrigins: c.Server.AllowedOrigins,
		IdleTimeout: c.Auth.SessionIdleTimeout, MaxAge: c.Auth.SessionMaxAge, OIDC: c.Auth.OIDC,
	}
}

// Identity is the authenticated caller of a request.
type Identity struct {
	User *model.User
	// Role is the effective role (a token's role is capped by its owner's).
	Role        model.Role
	Via         string
	Token       *model.APIToken
	SessionHash string
}

// Label is the actor shown in the audit log.
func (i *Identity) Label() string {
	switch {
	case i == nil:
		return "system"
	case i.Via == ViaToken && i.Token != nil:
		return "token:" + i.Token.Name
	case i.User != nil && i.User.Username != "":
		return i.User.Username
	case i.User != nil:
		return i.User.Name
	}
	return "system"
}

// UserID returns the acting user id, or 0 (no user: auth disabled / system).
func (i *Identity) UserID() int64 {
	if i == nil || i.User == nil {
		return 0
	}
	return i.User.ID
}

// Service holds the shared auth state.
type Service struct {
	Repo    repository.AuthRepository
	Opts    Options
	Log     *zap.Logger
	Limiter *Limiter
	OIDC    *OIDC // nil unless SSO is enabled
	Now     func() time.Time

	secure bool
}

// New builds a Service. With Opts.Enabled=false it never touches repo for
// authentication. The OIDC provider is contacted lazily on first use.
func New(repo repository.AuthRepository, opts Options, log *zap.Logger) (*Service, error) {
	if log == nil {
		log = zap.NewNop()
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = config.DefaultSessionIdleTimeout
	}
	if opts.MaxAge < opts.IdleTimeout {
		opts.MaxAge = max(opts.IdleTimeout, config.DefaultSessionMaxAge)
	}
	s := &Service{
		Repo: repo, Opts: opts, Log: log, Now: time.Now,
		Limiter: NewLimiter(LoginMaxAttempts, LoginWindow),
		secure:  strings.HasPrefix(strings.ToLower(opts.PublicURL), "https://"),
	}
	if opts.Enabled && opts.OIDC.Enabled {
		o, err := NewOIDC(repo, opts.OIDC, opts.PublicURL, s.secure, log)
		if err != nil {
			return nil, err
		}
		s.OIDC = o
	}
	return s, nil
}

// Enabled reports whether authentication is enforced.
func (s *Service) Enabled() bool { return s != nil && s.Opts.Enabled }

// PasswordLogin reports whether local password login is allowed.
func (s *Service) PasswordLogin() bool { return !(s.OIDC != nil && s.Opts.OIDC.DisablePasswordLogin) }

// LocalIdentity is the synthetic owner used when auth is disabled.
func LocalIdentity() *Identity {
	return &Identity{
		User: &model.User{ID: 0, Username: "local", Name: "local", Role: model.RoleOwner, Status: model.UserActive, CreatedAt: time.Now().UTC()},
		Role: model.RoleOwner, Via: ViaDisabled,
	}
}

// SecureCookies reports whether cookies carry the Secure flag.
func (s *Service) SecureCookies() bool { return s.secure }

// ---------------------------------------------------------------------------
// Resolving the caller
// ---------------------------------------------------------------------------

// Resolve identifies the caller of r. It returns (nil, nil) when no
// credentials were sent and ErrUnauthenticated when they are invalid.
func (s *Service) Resolve(r *http.Request) (*Identity, error) {
	if !s.Enabled() {
		return LocalIdentity(), nil
	}
	ctx := r.Context()
	if h := r.Header.Get("Authorization"); h != "" {
		if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
			return s.resolveToken(ctx, strings.TrimSpace(h[7:]))
		}
		return nil, ErrUnauthenticated
	}
	if ck, err := r.Cookie(SessionCookie); err == nil && ck.Value != "" {
		return s.resolveSession(ctx, ck.Value)
	}
	return nil, nil
}

func (s *Service) resolveToken(ctx context.Context, tok string) (*Identity, error) {
	if !LooksLikeAPIToken(tok) {
		return nil, ErrUnauthenticated
	}
	t, u, err := s.Repo.GetTokenByHash(ctx, HashToken(tok))
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrUnauthenticated
		}
		return nil, err
	}
	now := s.Now()
	if t.RevokedAt != nil || (t.ExpiresAt != nil && !now.Before(*t.ExpiresAt)) || !u.Active() {
		return nil, ErrUnauthenticated
	}
	if t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) > touchEvery {
		if err := s.Repo.TouchToken(ctx, t.ID, now); err != nil {
			s.Log.Warn("token touch failed", zap.Error(err))
		}
	}
	return &Identity{User: u, Role: model.MinRole(t.Role, u.Role), Via: ViaToken, Token: t}, nil
}

func (s *Service) resolveSession(ctx context.Context, cookie string) (*Identity, error) {
	hash := HashToken(cookie)
	sess, u, err := s.Repo.GetSession(ctx, hash)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrUnauthenticated
		}
		return nil, err
	}
	now := s.Now()
	if !now.Before(sess.ExpiresAt) || !u.Active() {
		_ = s.Repo.DeleteSession(ctx, hash)
		return nil, ErrUnauthenticated
	}
	if now.Sub(sess.LastSeenAt) > touchEvery {
		exp := now.Add(s.Opts.IdleTimeout)
		if abs := sess.CreatedAt.Add(s.Opts.MaxAge); exp.After(abs) {
			exp = abs
		}
		if err := s.Repo.TouchSession(ctx, hash, now, exp); err != nil {
			s.Log.Warn("session touch failed", zap.Error(err))
		}
	}
	return &Identity{User: u, Role: u.Role, Via: ViaSession, SessionHash: hash}, nil
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

// StartSession creates a session for u and sets the cookie. Any session
// presented by the request is revoked first (id rotation at login).
func (s *Service) StartSession(w http.ResponseWriter, r *http.Request, u *model.User, method, ip string) error {
	ctx := r.Context()
	if ck, err := r.Cookie(SessionCookie); err == nil && ck.Value != "" {
		_ = s.Repo.DeleteSession(ctx, HashToken(ck.Value))
	}
	raw, err := RandomToken(32)
	if err != nil {
		return err
	}
	now := s.Now()
	ua := r.UserAgent()
	if len(ua) > 300 {
		ua = ua[:300]
	}
	exp := now.Add(s.Opts.IdleTimeout)
	if abs := now.Add(s.Opts.MaxAge); exp.After(abs) {
		exp = abs
	}
	if err := s.Repo.CreateSession(ctx, &model.Session{
		IDHash: HashToken(raw), UserID: u.ID, Method: method, CreatedAt: now, LastSeenAt: now, ExpiresAt: exp, IP: ip, UserAgent: ua,
	}); err != nil {
		return err
	}
	if err := s.Repo.SetLastLogin(ctx, u.ID, now); err != nil {
		s.Log.Warn("last login update failed", zap.Error(err))
	}
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: raw, Path: "/", HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode,
		Expires: now.Add(s.Opts.MaxAge), MaxAge: int(s.Opts.MaxAge.Seconds()),
	})
	return nil
}

// EndSession revokes the request's session and clears the cookie.
func (s *Service) EndSession(w http.ResponseWriter, r *http.Request) {
	if ck, err := r.Cookie(SessionCookie); err == nil && ck.Value != "" {
		_ = s.Repo.DeleteSession(r.Context(), HashToken(ck.Value))
	}
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode,
		MaxAge: -1, Expires: time.Unix(0, 0),
	})
}

// RevokeSessions deletes every session of userID except keepHash.
func (s *Service) RevokeSessions(ctx context.Context, userID int64, keepHash string) {
	if _, err := s.Repo.DeleteUserSessions(ctx, userID, keepHash); err != nil {
		s.Log.Error("revoking sessions failed", zap.Int64("user_id", userID), zap.Error(err))
	}
}

// ---------------------------------------------------------------------------
// Audit
// ---------------------------------------------------------------------------

// Actor describes who performed an audited action.
type Actor struct {
	UserID int64 // 0 = none
	Label  string
	IP     string
}

// SystemActor is used for config-driven changes.
var SystemActor = Actor{Label: "system"}

// ActorOf builds an Actor from an identity.
func ActorOf(id *Identity, ip string) Actor {
	if id == nil {
		return Actor{Label: "system", IP: ip}
	}
	return Actor{UserID: id.UserID(), Label: id.Label(), IP: ip}
}

// Record writes an audit event; failures are logged, never returned (an audit
// hiccup must not undo the action that already happened).
func (s *Service) Record(ctx context.Context, a Actor, action, targetType, targetID string, details map[string]any) {
	if s == nil || s.Repo == nil {
		return
	}
	var raw json.RawMessage = json.RawMessage(`{}`)
	if len(details) > 0 {
		if b, err := json.Marshal(details); err == nil {
			raw = b
		}
	}
	e := &model.AuditEvent{
		Time: s.Now().UTC(), ActorLabel: a.Label, Action: action, TargetType: targetType, TargetID: targetID, Details: raw, IP: a.IP,
	}
	if a.UserID != 0 {
		id := a.UserID
		e.ActorUserID = &id
	}
	// Detach from request cancellation: a client hanging up must not lose the entry.
	if err := s.Repo.InsertAudit(context.WithoutCancel(ctx), e); err != nil {
		s.Log.Error("audit insert failed", zap.String("action", action), zap.Error(err))
	}
}

// Cleanup removes expired sessions and old links once, then daily until ctx ends.
func (s *Service) Cleanup(ctx context.Context) {
	run := func() {
		now := s.Now()
		if _, err := s.Repo.DeleteExpiredSessions(ctx, now); err != nil {
			s.Log.Warn("expired session cleanup failed", zap.Error(err))
		}
		if _, err := s.Repo.DeleteExpiredLinks(ctx, now); err != nil {
			s.Log.Warn("expired link cleanup failed", zap.Error(err))
		}
	}
	run()
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}
