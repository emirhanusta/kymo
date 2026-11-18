// Package authtest provides an in-memory repository.AuthRepository for tests.
package authtest

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

// Mem is an in-memory AuthRepository mirroring the SQL semantics.
type Mem struct {
	mu         sync.Mutex
	next       int64
	users      map[int64]*model.User
	identities map[string]int64 // provider\x00subject -> user
	sessions   map[string]*model.Session
	tokens     map[int64]*model.APIToken
	links      map[int64]*model.UserLink
	audit      []*model.AuditEvent
}

// New returns an empty repository.
func New() *Mem {
	return &Mem{users: map[int64]*model.User{}, identities: map[string]int64{}, sessions: map[string]*model.Session{},
		tokens: map[int64]*model.APIToken{}, links: map[int64]*model.UserLink{}}
}

var _ repository.AuthRepository = (*Mem)(nil)

func (m *Mem) id() int64 { m.next++; return m.next }

func (m *Mem) view(u *model.User) *model.User {
	c := *u
	c.HasPassword = c.PasswordHash != ""
	c.SSO = false
	for _, uid := range m.identities {
		if uid == u.ID {
			c.SSO = true
		}
	}
	return &c
}

func (m *Mem) CountUsers(context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.users), nil
}

func (m *Mem) insert(u *model.User) error {
	for _, o := range m.users {
		if o.Username == u.Username || (u.Email != "" && o.Email == u.Email) {
			return repository.ErrConflict
		}
		if u.Role == model.RoleOwner && o.Role == model.RoleOwner {
			return repository.ErrConflict
		}
	}
	u.ID = m.id()
	if u.Status == "" {
		u.Status = model.UserActive
	}
	u.CreatedAt = time.Now().UTC()
	u.UpdatedAt = u.CreatedAt
	c := *u
	m.users[u.ID] = &c
	u.HasPassword = u.PasswordHash != ""
	return nil
}

func (m *Mem) CreateUser(_ context.Context, u *model.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.insert(u)
}

func (m *Mem) CreateFirstOwner(_ context.Context, u *model.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.users) > 0 {
		return repository.ErrUsersExist
	}
	u.Role = model.RoleOwner
	return m.insert(u)
}

func (m *Mem) GetUser(_ context.Context, id int64) (*model.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return m.view(u), nil
}

func (m *Mem) GetUserByEmail(_ context.Context, email string) (*model.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.users {
		if email != "" && u.Email == strings.ToLower(email) {
			return m.view(u), nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *Mem) GetUserByUsername(_ context.Context, username string) (*model.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.users {
		if u.Username == strings.ToLower(username) {
			return m.view(u), nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *Mem) ListUsers(context.Context) ([]*model.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*model.User{}
	for _, u := range m.users {
		out = append(out, m.view(u))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *Mem) UpdateUser(_ context.Context, id int64, upd model.UserUpdate) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return repository.ErrNotFound
	}
	if upd.Email != nil && *upd.Email != "" {
		for _, o := range m.users {
			if o.ID != id && o.Email == strings.ToLower(*upd.Email) {
				return repository.ErrConflict
			}
		}
	}
	if upd.Name != nil {
		u.Name = *upd.Name
	}
	if upd.Email != nil {
		u.Email = strings.ToLower(*upd.Email)
	}
	if upd.MustChangePassword != nil {
		u.MustChangePassword = *upd.MustChangePassword
	}
	if upd.Status != nil {
		u.Status = *upd.Status
	}
	if upd.PasswordHash != nil {
		u.PasswordHash = *upd.PasswordHash
	}
	if upd.Role != nil {
		if *upd.Role == model.RoleOwner {
			for _, o := range m.users {
				if o.ID != id && o.Role == model.RoleOwner {
					return repository.ErrConflict
				}
			}
		}
		u.Role = *upd.Role
	}
	return nil
}

func (m *Mem) SetLastLogin(_ context.Context, id int64, t time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if u, ok := m.users[id]; ok {
		u.LastLoginAt = &t
	}
	return nil
}

func (m *Mem) TransferOwnership(_ context.Context, fromID, toID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, t := m.users[fromID], m.users[toID]
	if f == nil || t == nil || f.Role != model.RoleOwner || t.Role != model.RoleAdmin {
		return repository.ErrNotFound
	}
	f.Role, t.Role = model.RoleAdmin, model.RoleOwner
	return nil
}

func (m *Mem) GetUserByIdentity(_ context.Context, provider, subject string) (*model.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	uid, ok := m.identities[provider+"\x00"+subject]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return m.view(m.users[uid]), nil
}

func (m *Mem) AddIdentity(_ context.Context, userID int64, provider, subject, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.identities[provider+"\x00"+subject] = userID
	return nil
}

func (m *Mem) CreateSession(_ context.Context, s *model.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := *s
	m.sessions[s.IDHash] = &c
	return nil
}

func (m *Mem) GetSession(_ context.Context, h string) (*model.Session, *model.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[h]
	if !ok {
		return nil, nil, repository.ErrNotFound
	}
	c := *s
	return &c, m.view(m.users[s.UserID]), nil
}

func (m *Mem) TouchSession(_ context.Context, h string, seen, exp time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[h]; ok {
		s.LastSeenAt, s.ExpiresAt = seen, exp
	}
	return nil
}

func (m *Mem) DeleteSession(_ context.Context, h string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, h)
	return nil
}

func (m *Mem) DeleteUserSessions(_ context.Context, userID int64, keep string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for h, s := range m.sessions {
		if s.UserID == userID && h != keep {
			delete(m.sessions, h)
			n++
		}
	}
	return n, nil
}

// SessionCount returns the number of stored sessions of userID.
func (m *Mem) SessionCount(userID int64) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, s := range m.sessions {
		if s.UserID == userID {
			n++
		}
	}
	return n
}

// ExpireSessions makes every session expire at t (test helper).
func (m *Mem) ExpireSessions(t time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		s.ExpiresAt = t
	}
}

func (m *Mem) DeleteExpiredSessions(_ context.Context, now time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for h, s := range m.sessions {
		if s.ExpiresAt.Before(now) {
			delete(m.sessions, h)
			n++
		}
	}
	return n, nil
}

func (m *Mem) tokenView(t *model.APIToken) *model.APIToken {
	c := *t
	if u := m.users[t.UserID]; u != nil {
		c.UserEmail, c.UserUsername = u.Email, u.Username
	}
	return &c
}

func (m *Mem) CreateToken(_ context.Context, t *model.APIToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, o := range m.tokens {
		if o.TokenHash == t.TokenHash {
			return repository.ErrConflict
		}
	}
	t.ID = m.id()
	t.CreatedAt = time.Now().UTC()
	m.tokens[t.ID] = m.tokenView(t)
	return nil
}

func (m *Mem) GetTokenByHash(_ context.Context, h string) (*model.APIToken, *model.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tokens {
		if t.TokenHash == h {
			return m.tokenView(t), m.view(m.users[t.UserID]), nil
		}
	}
	return nil, nil, repository.ErrNotFound
}

func (m *Mem) GetToken(_ context.Context, id int64) (*model.APIToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tokens[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return m.tokenView(t), nil
}

func (m *Mem) ListTokens(_ context.Context, userID int64) ([]*model.APIToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*model.APIToken{}
	for _, t := range m.tokens {
		if userID == 0 || t.UserID == userID {
			out = append(out, m.tokenView(t))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

func (m *Mem) RevokeToken(_ context.Context, id int64, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tokens[id]
	if !ok {
		return repository.ErrNotFound
	}
	if t.RevokedAt == nil {
		t.RevokedAt = &now
	}
	return nil
}

func (m *Mem) TouchToken(_ context.Context, id int64, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.tokens[id]; ok {
		t.LastUsedAt = &at
	}
	return nil
}

func (m *Mem) CreateLink(_ context.Context, l *model.UserLink) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	l.ID = m.id()
	l.CreatedAt = time.Now().UTC()
	c := *l
	m.links[l.ID] = &c
	return nil
}

func (m *Mem) GetLinkByHash(_ context.Context, h string) (*model.UserLink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, l := range m.links {
		if l.TokenHash == h {
			c := *l
			return &c, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *Mem) pendingInvites(now time.Time) []*model.UserLink {
	out := []*model.UserLink{}
	for _, l := range m.links {
		if l.Kind == model.LinkInvite && l.UsedAt == nil && l.ExpiresAt.After(now) {
			c := *l
			out = append(out, &c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

func (m *Mem) ListInvites(_ context.Context, now time.Time) ([]*model.UserLink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pendingInvites(now), nil
}

func (m *Mem) GetInviteByEmail(_ context.Context, email string, now time.Time) (*model.UserLink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, l := range m.pendingInvites(now) {
		if email != "" && l.Email == strings.ToLower(email) {
			return l, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *Mem) GetInviteByUsername(_ context.Context, username string, now time.Time) (*model.UserLink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, l := range m.pendingInvites(now) {
		if l.Username == strings.ToLower(username) {
			return l, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *Mem) ConsumeLink(_ context.Context, id int64, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.links[id]
	if !ok || l.UsedAt != nil || !l.ExpiresAt.After(now) {
		return repository.ErrNotFound
	}
	l.UsedAt = &now
	return nil
}

func (m *Mem) DeleteInvite(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l, ok := m.links[id]; !ok || l.Kind != model.LinkInvite {
		return repository.ErrNotFound
	}
	delete(m.links, id)
	return nil
}

func (m *Mem) DeleteExpiredLinks(_ context.Context, now time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for id, l := range m.links {
		if l.ExpiresAt.Before(now.Add(-30 * 24 * time.Hour)) {
			delete(m.links, id)
			n++
		}
	}
	return n, nil
}

func (m *Mem) InsertAudit(_ context.Context, e *model.AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e.ID = m.id()
	c := *e
	m.audit = append(m.audit, &c)
	return nil
}

// Audit returns a copy of the recorded events, oldest first.
func (m *Mem) Audit() []*model.AuditEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*model.AuditEvent(nil), m.audit...)
}

func (m *Mem) ListAudit(_ context.Context, q repository.AuditQuery) ([]*model.AuditEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*model.AuditEvent{}
	for i := len(m.audit) - 1; i >= 0; i-- {
		e := m.audit[i]
		if !q.Before.IsZero() && !e.Time.Before(q.Before) {
			continue
		}
		if q.UserID != 0 && (e.ActorUserID == nil || *e.ActorUserID != q.UserID) {
			continue
		}
		if q.Action != "" && e.Action != q.Action && !strings.HasPrefix(e.Action, q.Action+".") {
			continue
		}
		out = append(out, e)
		if q.Limit > 0 && len(out) >= q.Limit {
			break
		}
	}
	return out, nil
}

func (m *Mem) DeleteAuditBefore(_ context.Context, t time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	kept := m.audit[:0]
	for _, e := range m.audit {
		if e.Time.Before(t) {
			n++
			continue
		}
		kept = append(kept, e)
	}
	m.audit = kept
	return n, nil
}
