package timescale

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

func mkUser(t *testing.T, repo repository.AuthRepository, email string, role model.Role, pwHash string) *model.User {
	t.Helper()
	u := &model.User{Username: testUsername(email), Email: email, Name: "N " + email, Role: role, PasswordHash: pwHash}
	if err := repo.CreateUser(context.Background(), u); err != nil {
		t.Fatalf("create %s: %v", email, err)
	}
	return u
}

// testUsername derives a unique valid username from an email-like string.
func testUsername(email string) string {
	local, _, _ := strings.Cut(email, "@")
	return strings.ReplaceAll(strings.ToLower(local), "+", "-") + "-" + strconv.Itoa(len(email))
}

func TestAuthUsers(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	repo := db.Auth()

	if n, _ := repo.CountUsers(ctx); n != 0 {
		t.Fatalf("count = %d", n)
	}
	owner := &model.User{Username: "owner", Email: "owner@example.com", Name: "Owner", PasswordHash: "$argon2id$x"}
	if err := repo.CreateFirstOwner(ctx, owner); err != nil || owner.ID == 0 || owner.Role != model.RoleOwner || owner.CreatedAt.IsZero() {
		t.Fatalf("first owner: %+v %v", owner, err)
	}
	if err := repo.CreateFirstOwner(ctx, &model.User{Username: "xx", Email: "x@example.com"}); !errors.Is(err, repository.ErrUsersExist) {
		t.Errorf("second first-owner: %v", err)
	}
	got, err := repo.GetUserByEmail(ctx, "OWNER@example.com")
	if err != nil || got.Role != model.RoleOwner || !got.HasPassword || got.SSO || got.Status != "active" || got.LastLoginAt != nil {
		t.Fatalf("get: %+v %v", got, err)
	}

	// Unique lower-cased email; the database refuses mixed case.
	if err := repo.CreateUser(ctx, &model.User{Username: "owner2", Email: "owner@example.com", Role: model.RoleViewer}); !errors.Is(err, repository.ErrConflict) {
		t.Errorf("duplicate email: %v", err)
	}
	if _, err := db.pool.Exec(ctx, `INSERT INTO users (username, email) VALUES ('mixed', 'Mixed@Case.com')`); err == nil {
		t.Error("mixed-case email must be rejected by the CHECK constraint")
	}
	// Exactly one owner.
	if err := repo.CreateUser(ctx, &model.User{Username: "second", Email: "second@example.com", Role: model.RoleOwner}); !errors.Is(err, repository.ErrConflict) {
		t.Errorf("second owner: %v", err)
	}
	// A failed create leaves no orphan user.
	if _, err := repo.GetUserByEmail(ctx, "second@example.com"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("orphan user after failed create: %v", err)
	}

	admin := mkUser(t, repo, "admin@example.com", model.RoleAdmin, "")
	viewer := mkUser(t, repo, "viewer@example.com", model.RoleViewer, "")
	if admin.HasPassword {
		t.Error("no password expected")
	}

	// Update: partial fields, role change, password.
	name, st, role, hash := "Renamed", model.UserDisabled, model.RoleOperator, "$argon2id$new"
	if err := repo.UpdateUser(ctx, viewer.ID, model.UserUpdate{Name: &name}); err != nil {
		t.Fatal(err)
	}
	g, _ := repo.GetUser(ctx, viewer.ID)
	if g.Name != "Renamed" || g.Role != model.RoleViewer || g.Status != "active" {
		t.Errorf("name-only update: %+v", g)
	}
	if err := repo.UpdateUser(ctx, viewer.ID, model.UserUpdate{Role: &role, Status: &st, PasswordHash: &hash}); err != nil {
		t.Fatal(err)
	}
	g, _ = repo.GetUser(ctx, viewer.ID)
	if g.Role != model.RoleOperator || g.Status != "disabled" || g.PasswordHash != hash || !g.HasPassword || g.Name != "Renamed" || !g.UpdatedAt.After(g.CreatedAt.Add(-time.Second)) {
		t.Errorf("update: %+v", g)
	}
	if err := repo.UpdateUser(ctx, 99999, model.UserUpdate{Name: &name}); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("update missing: %v", err)
	}
	owner2 := model.RoleOwner
	if err := repo.UpdateUser(ctx, admin.ID, model.UserUpdate{Role: &owner2}); !errors.Is(err, repository.ErrConflict) {
		t.Errorf("promote to second owner: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	_ = repo.SetLastLogin(ctx, admin.ID, now)
	if g, _ := repo.GetUser(ctx, admin.ID); g.LastLoginAt == nil || !g.LastLoginAt.Equal(now) {
		t.Errorf("last login: %v", g.LastLoginAt)
	}

	users, err := repo.ListUsers(ctx)
	if err != nil || len(users) != 3 || users[0].Email != "owner@example.com" {
		t.Errorf("list: %d %v", len(users), err)
	}

	// Ownership transfer is atomic and keeps a single owner.
	if err := repo.TransferOwnership(ctx, owner.ID, admin.ID); err != nil {
		t.Fatal(err)
	}
	a, _ := repo.GetUser(ctx, admin.ID)
	o, _ := repo.GetUser(ctx, owner.ID)
	if a.Role != model.RoleOwner || o.Role != model.RoleAdmin {
		t.Errorf("transfer: %s / %s", a.Role, o.Role)
	}
	if err := repo.TransferOwnership(ctx, owner.ID, admin.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("transfer by non-owner: %v", err)
	}
	if err := repo.TransferOwnership(ctx, admin.ID, viewer.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("transfer to non-admin: %v", err)
	}
	if o2, _ := repo.GetUser(ctx, admin.ID); o2.Role != model.RoleOwner {
		t.Error("failed transfer must roll back")
	}
}

func TestAuthIdentities(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	repo := db.Auth()
	u := mkUser(t, repo, "sso@example.com", model.RoleViewer, "")
	other := mkUser(t, repo, "other@example.com", model.RoleViewer, "")

	if _, err := repo.GetUserByIdentity(ctx, "oidc:https://idp", "sub-1"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("unknown identity: %v", err)
	}
	if err := repo.AddIdentity(ctx, u.ID, "oidc:https://idp", "sub-1", "sso@example.com"); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetUserByIdentity(ctx, "oidc:https://idp", "sub-1")
	if err != nil || got.ID != u.ID || !got.SSO {
		t.Fatalf("identity: %+v %v", got, err)
	}
	// The same subject at another issuer is a different identity.
	if _, err := repo.GetUserByIdentity(ctx, "oidc:https://other", "sub-1"); !errors.Is(err, repository.ErrNotFound) {
		t.Error("issuer qualification")
	}
	// Re-adding for the same user is idempotent; stealing it for another user is not.
	if err := repo.AddIdentity(ctx, u.ID, "oidc:https://idp", "sub-1", "new@example.com"); err != nil {
		t.Errorf("idempotent add: %v", err)
	}
	_ = repo.AddIdentity(ctx, other.ID, "oidc:https://idp", "sub-1", "x")
	if g, _ := repo.GetUserByIdentity(ctx, "oidc:https://idp", "sub-1"); g.ID != u.ID {
		t.Error("identity must not move to another user")
	}
	// Deleting the user removes identities, sessions and bindings.
	if _, err := db.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
		t.Fatal(err)
	}
	if count(t, db, "user_identities", "user_id = $1", u.ID) != 0 || count(t, db, "role_bindings", "user_id = $1", u.ID) != 0 {
		t.Error("cascade")
	}
}

func TestAuthSessions(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	repo := db.Auth()
	u := mkUser(t, repo, "a@example.com", model.RoleOperator, "")
	v := mkUser(t, repo, "b@example.com", model.RoleViewer, "")
	now := time.Now().UTC().Truncate(time.Microsecond)

	mk := func(h string, user int64, exp time.Time) {
		if err := repo.CreateSession(ctx, &model.Session{IDHash: h, UserID: user, Method: model.SessionPassword, CreatedAt: now, LastSeenAt: now, ExpiresAt: exp, IP: "1.2.3.4", UserAgent: "ua"}); err != nil {
			t.Fatal(err)
		}
	}
	mk("h1", u.ID, now.Add(time.Hour))
	mk("h2", u.ID, now.Add(time.Hour))
	mk("h3", v.ID, now.Add(time.Hour))
	mk("old", v.ID, now.Add(-time.Hour))

	s, usr, err := repo.GetSession(ctx, "h1")
	if err != nil || usr.ID != u.ID || usr.Role != model.RoleOperator || s.IP != "1.2.3.4" || s.Method != "password" || !s.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("get session: %+v %+v %v", s, usr, err)
	}
	if _, _, err := repo.GetSession(ctx, "nope"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	if err := repo.CreateSession(ctx, &model.Session{IDHash: "bad", UserID: u.ID, Method: "magic", ExpiresAt: now}); err == nil {
		t.Error("unknown method must be rejected")
	}
	later := now.Add(2 * time.Hour)
	_ = repo.TouchSession(ctx, "h1", later, later)
	if s, _, _ := repo.GetSession(ctx, "h1"); !s.LastSeenAt.Equal(later) || !s.ExpiresAt.Equal(later) {
		t.Errorf("touch: %+v", s)
	}
	if n, _ := repo.DeleteUserSessions(ctx, u.ID, "h2"); n != 1 {
		t.Errorf("revoke-all-but-one deleted %d", n)
	}
	if _, _, err := repo.GetSession(ctx, "h1"); !errors.Is(err, repository.ErrNotFound) {
		t.Error("h1 should be gone")
	}
	if _, _, err := repo.GetSession(ctx, "h2"); err != nil {
		t.Error("h2 should survive")
	}
	if n, _ := repo.DeleteExpiredSessions(ctx, now); n != 1 {
		t.Errorf("expired deleted %d", n)
	}
	_ = repo.DeleteSession(ctx, "h3")
	if count(t, db, "sessions", "user_id = $1", v.ID) != 0 {
		t.Error("delete session")
	}
	if n, _ := repo.DeleteUserSessions(ctx, u.ID, ""); n != 1 {
		t.Errorf("revoke all: %d", n)
	}
}

func TestAuthTokens(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	repo := db.Auth()
	u := mkUser(t, repo, "t@example.com", model.RoleAdmin, "")
	v := mkUser(t, repo, "v@example.com", model.RoleViewer, "")
	exp := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Microsecond)

	tk := &model.APIToken{UserID: u.ID, Name: "ci", Prefix: "abcd1234", TokenHash: "hash-1", Role: model.RoleOperator, ExpiresAt: &exp}
	if err := repo.CreateToken(ctx, tk); err != nil || tk.ID == 0 || tk.CreatedAt.IsZero() {
		t.Fatalf("create: %+v %v", tk, err)
	}
	if err := repo.CreateToken(ctx, &model.APIToken{UserID: u.ID, Name: "dup", Prefix: "p", TokenHash: "hash-1", Role: model.RoleViewer}); !errors.Is(err, repository.ErrConflict) {
		t.Errorf("duplicate hash: %v", err)
	}
	_ = repo.CreateToken(ctx, &model.APIToken{UserID: v.ID, Name: "mine", Prefix: "p2", TokenHash: "hash-2", Role: model.RoleViewer})

	got, owner, err := repo.GetTokenByHash(ctx, "hash-1")
	if err != nil || got.Name != "ci" || got.Role != model.RoleOperator || got.UserEmail != "t@example.com" || got.ExpiresAt == nil || !got.ExpiresAt.Equal(exp) || owner.ID != u.ID {
		t.Fatalf("by hash: %+v %+v %v", got, owner, err)
	}
	if _, _, err := repo.GetTokenByHash(ctx, "zzz"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	if mine, _ := repo.ListTokens(ctx, v.ID); len(mine) != 1 || mine[0].Name != "mine" {
		t.Errorf("own: %+v", mine)
	}
	if all, _ := repo.ListTokens(ctx, 0); len(all) != 2 || all[0].Name != "mine" {
		t.Errorf("all (newest first): %+v", all)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	_ = repo.TouchToken(ctx, tk.ID, now)
	if err := repo.RevokeToken(ctx, tk.ID, now); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Hour)
	_ = repo.RevokeToken(ctx, tk.ID, later) // idempotent: keeps the first timestamp
	g, _ := repo.GetToken(ctx, tk.ID)
	if g.LastUsedAt == nil || g.RevokedAt == nil || !g.RevokedAt.Equal(now) {
		t.Errorf("revoked: %+v", g)
	}
	if err := repo.RevokeToken(ctx, 99999, now); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("revoke missing: %v", err)
	}
	b, _ := json.Marshal(g)
	if string(b) == "" || json.Valid(b) == false || contains(string(b), "hash-1") {
		t.Errorf("token JSON leaks the hash: %s", b)
	}
	if _, err := db.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, v.ID); err != nil {
		t.Fatal(err)
	}
	if count(t, db, "api_tokens", "user_id = $1", v.ID) != 0 {
		t.Error("tokens must cascade with the user")
	}
}

func TestAuthLinks(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	repo := db.Auth()
	admin := mkUser(t, repo, "admin@example.com", model.RoleAdmin, "")
	now := time.Now().UTC().Truncate(time.Microsecond)

	inv := &model.UserLink{TokenHash: "inv-1", Kind: model.LinkInvite, Email: "new@example.com", Role: model.RoleOperator, CreatedBy: &admin.ID, ExpiresAt: now.Add(7 * 24 * time.Hour)}
	if err := repo.CreateLink(ctx, inv); err != nil || inv.ID == 0 {
		t.Fatalf("create: %v", err)
	}
	reset := &model.UserLink{TokenHash: "rst-1", Kind: model.LinkPasswordReset, UserID: &admin.ID, Email: admin.Email, CreatedBy: &admin.ID, ExpiresAt: now.Add(time.Hour)}
	if err := repo.CreateLink(ctx, reset); err != nil {
		t.Fatal(err)
	}
	expired := &model.UserLink{TokenHash: "inv-old", Kind: model.LinkInvite, Email: "old@example.com", Role: model.RoleViewer, ExpiresAt: now.Add(-time.Hour)}
	_ = repo.CreateLink(ctx, expired)
	if err := repo.CreateLink(ctx, &model.UserLink{TokenHash: "inv-1", Kind: model.LinkInvite, Email: "d@example.com", ExpiresAt: now}); !errors.Is(err, repository.ErrConflict) {
		t.Errorf("duplicate hash: %v", err)
	}
	if err := repo.CreateLink(ctx, &model.UserLink{TokenHash: "k", Kind: "magic", ExpiresAt: now}); err == nil {
		t.Error("unknown kind must be rejected")
	}

	g, err := repo.GetLinkByHash(ctx, "inv-1")
	if err != nil || g.Kind != model.LinkInvite || g.Role != model.RoleOperator || g.Email != "new@example.com" || g.CreatedBy == nil || *g.CreatedBy != admin.ID || g.UsedAt != nil {
		t.Fatalf("get: %+v %v", g, err)
	}
	if g, _ := repo.GetLinkByHash(ctx, "rst-1"); g.UserID == nil || *g.UserID != admin.ID {
		t.Error("reset link user")
	}
	if _, err := repo.GetLinkByHash(ctx, "none"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	if list, _ := repo.ListInvites(ctx, now); len(list) != 1 || list[0].Email != "new@example.com" {
		t.Errorf("pending invites: %+v", list)
	}
	if l, err := repo.GetInviteByEmail(ctx, "NEW@example.com", now); err != nil || l.ID != inv.ID {
		t.Errorf("by email: %v", err)
	}
	if _, err := repo.GetInviteByEmail(ctx, "old@example.com", now); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("expired by email: %v", err)
	}

	// Consume is atomic and single-use; expired links cannot be consumed.
	if err := repo.ConsumeLink(ctx, inv.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.ConsumeLink(ctx, inv.ID, now); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("second consume: %v", err)
	}
	if err := repo.ConsumeLink(ctx, expired.ID, now); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("expired consume: %v", err)
	}
	if list, _ := repo.ListInvites(ctx, now); len(list) != 0 {
		t.Errorf("used invite listed: %+v", list)
	}
	in2 := &model.UserLink{TokenHash: "inv-2", Kind: model.LinkInvite, Email: "z@example.com", Role: model.RoleViewer, ExpiresAt: now.Add(time.Hour)}
	_ = repo.CreateLink(ctx, in2)
	if err := repo.DeleteInvite(ctx, reset.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("delete non-invite: %v", err)
	}
	if err := repo.DeleteInvite(ctx, in2.ID); err != nil {
		t.Errorf("delete invite: %v", err)
	}
	if n, _ := repo.DeleteExpiredLinks(ctx, now.Add(60*24*time.Hour)); n < 2 {
		t.Errorf("expired cleanup: %d", n)
	}
}

func TestAuthAudit(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	repo := db.Auth()
	u := mkUser(t, repo, "a@example.com", model.RoleAdmin, "")
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-10 * time.Hour)

	ins := func(offset time.Duration, uid *int64, label, action string, details string) {
		e := &model.AuditEvent{Time: base.Add(offset), ActorUserID: uid, ActorLabel: label, Action: action, TargetType: "x", TargetID: "1", IP: "9.9.9.9"}
		if details != "" {
			e.Details = json.RawMessage(details)
		}
		if err := repo.InsertAudit(ctx, e); err != nil || e.ID == 0 {
			t.Fatalf("insert: %v", err)
		}
	}
	ins(0, &u.ID, "a@example.com", "login.success", `{"method":"password"}`)
	ins(time.Hour, &u.ID, "a@example.com", "pod.restart", `{"source":"prod"}`)
	ins(2*time.Hour, nil, "system", "source.config_reload", "")
	ins(3*time.Hour, &u.ID, "a@example.com", "user.update", "")
	ins(3*time.Hour, nil, "token:ci", "user.role_change", "")

	all, err := repo.ListAudit(ctx, repository.AuditQuery{})
	if err != nil || len(all) != 5 {
		t.Fatalf("all: %d %v", len(all), err)
	}
	if all[len(all)-1].Action != "login.success" || all[0].Time.Before(all[1].Time) {
		t.Errorf("order: %+v", all)
	}
	var d map[string]any
	if err := json.Unmarshal(all[len(all)-1].Details, &d); err != nil || d["method"] != "password" {
		t.Errorf("details: %s", all[len(all)-1].Details)
	}
	if string(all[2].Details) != "{}" || all[2].ActorUserID != nil || all[2].ActorLabel != "system" || all[2].IP != "9.9.9.9" {
		t.Errorf("system event: %+v", all[2])
	}
	if got, _ := repo.ListAudit(ctx, repository.AuditQuery{UserID: u.ID}); len(got) != 3 {
		t.Errorf("by user: %d", len(got))
	}
	if got, _ := repo.ListAudit(ctx, repository.AuditQuery{Action: "user"}); len(got) != 2 {
		t.Errorf("prefix match: %d", len(got))
	}
	if got, _ := repo.ListAudit(ctx, repository.AuditQuery{Action: "pod.restart"}); len(got) != 1 {
		t.Errorf("exact match: %d", len(got))
	}
	if got, _ := repo.ListAudit(ctx, repository.AuditQuery{Action: "us"}); len(got) != 0 {
		t.Errorf("a partial word must not match: %d", len(got))
	}
	if got, _ := repo.ListAudit(ctx, repository.AuditQuery{Before: base.Add(2 * time.Hour)}); len(got) != 2 {
		t.Errorf("before: %d", len(got))
	}
	if got, _ := repo.ListAudit(ctx, repository.AuditQuery{Limit: 2}); len(got) != 2 {
		t.Errorf("limit: %d", len(got))
	}
	// History survives user deletion (no FK).
	if _, err := db.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.ListAudit(ctx, repository.AuditQuery{}); len(got) != 5 {
		t.Errorf("audit after user delete: %d", len(got))
	}
	if n, err := repo.DeleteAuditBefore(ctx, base.Add(2*time.Hour)); err != nil || n != 2 {
		t.Errorf("delete before: %d %v", n, err)
	}
	// 1-year retention policy is registered (when the extension supports it).
	var n int
	_ = db.pool.QueryRow(ctx, `SELECT COUNT(*) FROM timescaledb_information.jobs WHERE hypertable_name = 'audit_events' AND proc_name = 'policy_retention'`).Scan(&n)
	if n != 1 {
		t.Errorf("retention policy jobs = %d", n)
	}
}

func TestAuthUsernamesAndOptionalEmail(t *testing.T) {
	db := reset(t)
	ctx := context.Background()
	repo := db.Auth()

	a := &model.User{Username: "alice", Name: "Alice", Role: model.RoleViewer, MustChangePassword: true}
	if err := repo.CreateUser(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetUserByUsername(ctx, "ALICE")
	if err != nil || got.ID != a.ID || got.Email != "" || !got.MustChangePassword {
		t.Fatalf("by username: %+v %v", got, err)
	}
	if _, err := repo.GetUserByUsername(ctx, "nobody"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("missing username: %v", err)
	}
	// Several users without email are fine (NULL is not unique-constrained).
	if err := repo.CreateUser(ctx, &model.User{Username: "bob", Role: model.RoleViewer}); err != nil {
		t.Errorf("second user without email: %v", err)
	}
	// Username is unique.
	if err := repo.CreateUser(ctx, &model.User{Username: "alice", Email: "x@example.com", Role: model.RoleViewer}); !errors.Is(err, repository.ErrConflict) {
		t.Errorf("duplicate username: %v", err)
	}
	// Invalid usernames are refused by the database too.
	for _, bad := range []string{"A", "x", "-lead", "has space", "Upper", strings.Repeat("a", 65)} {
		if _, err := db.pool.Exec(ctx, `INSERT INTO users (username) VALUES ($1)`, bad); err == nil {
			t.Errorf("username %q accepted", bad)
		}
	}
	// Email uniqueness only applies when set; GetUserByEmail("") finds nobody.
	em := "Alice@Example.com"
	if err := repo.UpdateUser(ctx, a.ID, model.UserUpdate{Email: &em}); err != nil {
		t.Fatal(err)
	}
	if g, err := repo.GetUserByEmail(ctx, "alice@example.com"); err != nil || g.ID != a.ID || g.Email != "alice@example.com" {
		t.Errorf("email stored lower-cased: %+v %v", g, err)
	}
	b, _ := repo.GetUserByUsername(ctx, "bob")
	if err := repo.UpdateUser(ctx, b.ID, model.UserUpdate{Email: &em}); !errors.Is(err, repository.ErrConflict) {
		t.Errorf("duplicate email on update: %v", err)
	}
	if err := repo.CreateUser(ctx, &model.User{Username: "carl", Email: "ALICE@example.com", Role: model.RoleViewer}); err == nil {
		t.Error("duplicate email (other case) on create must conflict")
	}
	if _, err := repo.GetUserByEmail(ctx, ""); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("empty email lookup: %v", err)
	}
	// Clearing the email frees it; flag updates round-trip.
	empty, no := "", false
	if err := repo.UpdateUser(ctx, a.ID, model.UserUpdate{Email: &empty, MustChangePassword: &no}); err != nil {
		t.Fatal(err)
	}
	g, _ := repo.GetUser(ctx, a.ID)
	if g.Email != "" || g.MustChangePassword {
		t.Errorf("cleared: %+v", g)
	}
	if err := repo.UpdateUser(ctx, b.ID, model.UserUpdate{Email: &em}); err != nil {
		t.Errorf("freed email: %v", err)
	}
	// A nil Email keeps the stored one.
	name := "Bobby"
	_ = repo.UpdateUser(ctx, b.ID, model.UserUpdate{Name: &name})
	if g, _ := repo.GetUser(ctx, b.ID); g.Email != "alice@example.com" {
		t.Errorf("email kept: %q", g.Email)
	}

	// Invites carry username + optional email.
	now := time.Now().UTC()
	exp := now.Add(time.Hour)
	if err := repo.CreateLink(ctx, &model.UserLink{TokenHash: "u-1", Kind: model.LinkInvite, Username: "newbie", Role: model.RoleViewer, ExpiresAt: exp}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateLink(ctx, &model.UserLink{TokenHash: "u-2", Kind: model.LinkInvite, Username: "withmail", Email: "w@example.com", Role: model.RoleViewer, ExpiresAt: exp}); err != nil {
		t.Fatal(err)
	}
	if l, err := repo.GetInviteByUsername(ctx, "NEWBIE", now); err != nil || l.Username != "newbie" || l.Email != "" {
		t.Errorf("invite by username: %+v %v", l, err)
	}
	if _, err := repo.GetInviteByEmail(ctx, "", now); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("empty email must not match username-only invites: %v", err)
	}
	if l, err := repo.GetInviteByEmail(ctx, "w@example.com", now); err != nil || l.Username != "withmail" {
		t.Errorf("invite by email: %+v %v", l, err)
	}
	// Tokens expose the owner's username.
	tok := &model.APIToken{UserID: a.ID, Name: "t", Prefix: "abcd1234", TokenHash: "th", Role: model.RoleViewer}
	_ = repo.CreateToken(ctx, tok)
	if gt, _ := repo.GetToken(ctx, tok.ID); gt.UserUsername != "alice" || gt.UserEmail != "" {
		t.Errorf("token owner: %+v", gt)
	}
}
