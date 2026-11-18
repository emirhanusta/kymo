package model

import (
	"encoding/json"
	"strings"
	"time"
)

// Role is a global access level. Higher ranks include every lower rank.
type Role string

// Roles, lowest to highest.
const (
	RoleViewer   Role = "viewer"
	RoleOperator Role = "operator"
	RoleAdmin    Role = "admin"
	RoleOwner    Role = "owner"
)

// Rank orders roles (0 = unknown role, which grants nothing).
func (r Role) Rank() int {
	switch r {
	case RoleViewer:
		return 1
	case RoleOperator:
		return 2
	case RoleAdmin:
		return 3
	case RoleOwner:
		return 4
	}
	return 0
}

// Valid reports whether r is one of the four roles.
func (r Role) Valid() bool { return r.Rank() > 0 }

// AtLeast reports whether r grants everything min grants.
func (r Role) AtLeast(min Role) bool { return r.Rank() >= min.Rank() && r.Rank() > 0 }

// ParseRole normalises s into a Role (ok=false when unknown).
func ParseRole(s string) (Role, bool) {
	r := Role(strings.ToLower(strings.TrimSpace(s)))
	return r, r.Valid()
}

// MinRole returns the lower of a and b.
func MinRole(a, b Role) Role {
	if a.Rank() <= b.Rank() {
		return a
	}
	return b
}

// User statuses.
const (
	UserActive   = "active"
	UserDisabled = "disabled"
)

// Session methods.
const (
	SessionPassword = "password"
	SessionOIDC     = "oidc"
)

// Link kinds.
const (
	LinkInvite        = "invite"
	LinkPasswordReset = "password_reset"
)

// User is an account. It serialises as the API's User shape.
type User struct {
	ID int64 `json:"id"`
	// Username is the login identity (lower-case, unique). Email is optional ("" = none).
	Username    string `json:"username"`
	Email       string `json:"email"`
	Name        string `json:"name"`
	Role        Role   `json:"role"`
	Status      string `json:"status"`
	HasPassword bool   `json:"has_password"`
	SSO         bool   `json:"sso"`
	// MustChangePassword gates the session until the password is changed.
	MustChangePassword bool       `json:"must_change_password"`
	CreatedAt          time.Time  `json:"created_at"`
	LastLoginAt        *time.Time `json:"last_login_at"`

	// PasswordHash is the encoded argon2id hash ("" = no local password).
	PasswordHash string    `json:"-"`
	UpdatedAt    time.Time `json:"-"`
}

// Active reports whether the user may sign in.
func (u *User) Active() bool { return u != nil && u.Status == UserActive }

// UserUpdate lists the fields UpdateUser changes (nil = keep).
type UserUpdate struct {
	Name *string
	// Email sets the email; a pointer to "" clears it.
	Email              *string
	MustChangePassword *bool
	Role               *Role
	Status             *string
	PasswordHash       *string
}

// Session is a server-side login session.
type Session struct {
	IDHash     string
	UserID     int64
	Method     string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	IP         string
	UserAgent  string
}

// APIToken is a long-lived credential for scripts. TokenHash is never serialised.
type APIToken struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	Prefix       string     `json:"prefix"`
	Role         Role       `json:"role"`
	UserID       int64      `json:"user_id"`
	UserEmail    string     `json:"user_email"`
	UserUsername string     `json:"user_username"`
	CreatedAt    time.Time  `json:"created_at"`
	ExpiresAt    *time.Time `json:"expires_at"`
	LastUsedAt   *time.Time `json:"last_used_at"`
	RevokedAt    *time.Time `json:"revoked_at"`

	TokenHash string `json:"-"`
}

// UserLink is a single-use invite or password-reset link.
type UserLink struct {
	ID        int64
	TokenHash string
	Kind      string
	Username  string // invites: username of the account to create
	Email     string // invites: optional; reset links: the user's email
	UserID    *int64
	Role      Role
	CreatedBy *int64
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    *time.Time
}

// AuditEvent is one audit-log entry.
type AuditEvent struct {
	ID          int64           `json:"id"`
	Time        time.Time       `json:"time"`
	ActorUserID *int64          `json:"actor_user_id"`
	ActorLabel  string          `json:"actor_label"`
	Action      string          `json:"action"`
	TargetType  string          `json:"target_type"`
	TargetID    string          `json:"target_id"`
	Details     json.RawMessage `json:"details"`
	IP          string          `json:"ip"`
}
