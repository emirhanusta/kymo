package repository

import (
	"context"
	"errors"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
)

// ErrUsersExist is returned by CreateFirstOwner when the installation already
// has users.
var ErrUsersExist = errors.New("users already exist")

// AuditQuery filters ListAudit.
type AuditQuery struct {
	UserID int64 // 0 = any actor
	// Action matches exactly or as a dotted prefix ("user" matches "user.update").
	Action string
	Before time.Time // zero = now
	Limit  int
}

// AuthRepository persists users, sessions, tokens, links and the audit log.
// Usernames and emails are expected lower-cased by the caller; an empty email
// means "none".
type AuthRepository interface {
	CountUsers(ctx context.Context) (int, error)
	// CreateUser inserts the user and its '*' role binding (u.Role) and sets ID
	// and timestamps. ErrConflict when the username or email is taken.
	CreateUser(ctx context.Context, u *model.User) error
	// CreateFirstOwner is CreateUser with Role owner, atomically refused with
	// ErrUsersExist when any user exists.
	CreateFirstOwner(ctx context.Context, u *model.User) error
	// GetUser / GetUserByEmail / GetUserByUsername return ErrNotFound when missing.
	GetUser(ctx context.Context, id int64) (*model.User, error)
	GetUserByUsername(ctx context.Context, username string) (*model.User, error)
	GetUserByEmail(ctx context.Context, email string) (*model.User, error)
	ListUsers(ctx context.Context) ([]*model.User, error)
	// UpdateUser applies the non-nil fields. ErrNotFound when the user is missing,
	// ErrConflict when the new email is taken.
	UpdateUser(ctx context.Context, id int64, upd model.UserUpdate) error
	SetLastLogin(ctx context.Context, id int64, t time.Time) error
	// TransferOwnership makes toID the owner and fromID an admin atomically.
	TransferOwnership(ctx context.Context, fromID, toID int64) error

	// GetUserByIdentity returns ErrNotFound when the identity is unknown.
	GetUserByIdentity(ctx context.Context, provider, subject string) (*model.User, error)
	AddIdentity(ctx context.Context, userID int64, provider, subject, email string) error

	CreateSession(ctx context.Context, s *model.Session) error
	// GetSession returns the session and its user, or ErrNotFound.
	GetSession(ctx context.Context, idHash string) (*model.Session, *model.User, error)
	TouchSession(ctx context.Context, idHash string, lastSeen, expires time.Time) error
	DeleteSession(ctx context.Context, idHash string) error
	// DeleteUserSessions removes every session of the user except keepHash ("" = none).
	DeleteUserSessions(ctx context.Context, userID int64, keepHash string) (int64, error)
	DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error)

	CreateToken(ctx context.Context, t *model.APIToken) error
	// GetTokenByHash returns the token and its user, or ErrNotFound.
	GetTokenByHash(ctx context.Context, hash string) (*model.APIToken, *model.User, error)
	GetToken(ctx context.Context, id int64) (*model.APIToken, error)
	// ListTokens returns the user's tokens (userID 0 = every token), newest first.
	ListTokens(ctx context.Context, userID int64) ([]*model.APIToken, error)
	// RevokeToken sets revoked_at (idempotent). ErrNotFound when missing.
	RevokeToken(ctx context.Context, id int64, now time.Time) error
	TouchToken(ctx context.Context, id int64, t time.Time) error

	CreateLink(ctx context.Context, l *model.UserLink) error
	GetLinkByHash(ctx context.Context, hash string) (*model.UserLink, error)
	// ListInvites returns unused, unexpired invites, newest first.
	ListInvites(ctx context.Context, now time.Time) ([]*model.UserLink, error)
	// GetInviteByEmail returns the newest unused, unexpired invite or ErrNotFound.
	GetInviteByEmail(ctx context.Context, email string, now time.Time) (*model.UserLink, error)
	// GetInviteByUsername returns the newest unused, unexpired invite or ErrNotFound.
	GetInviteByUsername(ctx context.Context, username string, now time.Time) (*model.UserLink, error)
	// ConsumeLink marks the link used; ErrNotFound when it is already used or expired.
	ConsumeLink(ctx context.Context, id int64, now time.Time) error
	// DeleteInvite removes an invite; ErrNotFound when it does not exist.
	DeleteInvite(ctx context.Context, id int64) error
	DeleteExpiredLinks(ctx context.Context, now time.Time) (int64, error)

	InsertAudit(ctx context.Context, e *model.AuditEvent) error
	ListAudit(ctx context.Context, q AuditQuery) ([]*model.AuditEvent, error)
	DeleteAuditBefore(ctx context.Context, t time.Time) (int64, error)
}
