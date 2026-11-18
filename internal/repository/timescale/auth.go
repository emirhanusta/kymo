package timescale

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/emirhanusta/kymo/internal/model"
	"github.com/emirhanusta/kymo/internal/repository"
)

// Auth returns the AuthRepository backed by this DB.
func (db *DB) Auth() repository.AuthRepository { return &authRepo{db} }

type authRepo struct{ db *DB }

const userSelect = `SELECT u.id, u.username, COALESCE(u.email, ''), u.name, COALESCE(u.password_hash, ''), u.status, u.created_at, u.updated_at,
  u.last_login_at, COALESCE(rb.role, ''), EXISTS (SELECT 1 FROM user_identities i WHERE i.user_id = u.id), u.must_change_password
  FROM users u LEFT JOIN role_bindings rb ON rb.user_id = u.id AND rb.scope = '*'`

func scanUser(row interface{ Scan(...any) error }) (*model.User, error) {
	var (
		u    model.User
		role string
	)
	if err := row.Scan(&u.ID, &u.Username, &u.Email, &u.Name, &u.PasswordHash, &u.Status, &u.CreatedAt, &u.UpdatedAt,
		&u.LastLoginAt, &role, &u.SSO, &u.MustChangePassword); err != nil {
		return nil, err
	}
	u.Role = model.Role(role)
	u.HasPassword = u.PasswordHash != ""
	return &u, nil
}

func isUnique(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (r *authRepo) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := r.db.pool.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("timescale: user count: %w", err)
	}
	return n, nil
}

func (r *authRepo) insertUser(ctx context.Context, tx pgx.Tx, u *model.User) error {
	err := tx.QueryRow(ctx, `INSERT INTO users (username, email, name, password_hash, status, must_change_password) VALUES ($1, $2, $3, $4, $5, $6)
	  RETURNING id, created_at, updated_at`, u.Username, nullStr(u.Email), u.Name, nullStr(u.PasswordHash), statusOrActive(u.Status), u.MustChangePassword).
		Scan(&u.ID, &u.CreatedAt, &u.UpdatedAt)
	if isUnique(err) {
		return repository.ErrConflict
	}
	if err != nil {
		return fmt.Errorf("timescale: user create: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO role_bindings (user_id, scope, role) VALUES ($1, '*', $2)`, u.ID, string(u.Role)); err != nil {
		if isUnique(err) {
			return repository.ErrConflict
		}
		return fmt.Errorf("timescale: role binding create: %w", err)
	}
	u.Status = statusOrActive(u.Status)
	u.HasPassword = u.PasswordHash != ""
	return nil
}

// emailArg is the value of an email update: nil/"" store NULL.
func emailArg(e *string) any {
	if e == nil || *e == "" {
		return nil
	}
	return strings.ToLower(*e)
}

func statusOrActive(s string) string {
	if s == "" {
		return model.UserActive
	}
	return s
}

func (r *authRepo) CreateUser(ctx context.Context, u *model.User) error {
	tx, err := r.db.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("timescale: user create: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := r.insertUser(ctx, tx, u); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *authRepo) CreateFirstOwner(ctx context.Context, u *model.User) error {
	tx, err := r.db.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("timescale: owner create: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	// Serialise concurrent first-run setups.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7461001)`); err != nil {
		return fmt.Errorf("timescale: owner create lock: %w", err)
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return fmt.Errorf("timescale: owner create: %w", err)
	}
	if n > 0 {
		return repository.ErrUsersExist
	}
	u.Role = model.RoleOwner
	if err := r.insertUser(ctx, tx, u); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *authRepo) GetUser(ctx context.Context, id int64) (*model.User, error) {
	u, err := scanUser(r.db.pool.QueryRow(ctx, userSelect+` WHERE u.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, repository.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("timescale: user get: %w", err)
	}
	return u, nil
}

func (r *authRepo) GetUserByEmail(ctx context.Context, email string) (*model.User, error) {
	u, err := scanUser(r.db.pool.QueryRow(ctx, userSelect+` WHERE u.email = $1`, strings.ToLower(email)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, repository.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("timescale: user get by email: %w", err)
	}
	return u, nil
}

func (r *authRepo) GetUserByUsername(ctx context.Context, username string) (*model.User, error) {
	u, err := scanUser(r.db.pool.QueryRow(ctx, userSelect+` WHERE u.username = $1`, strings.ToLower(username)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, repository.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("timescale: user get by username: %w", err)
	}
	return u, nil
}

func (r *authRepo) ListUsers(ctx context.Context) ([]*model.User, error) {
	rows, err := r.db.pool.Query(ctx, userSelect+` ORDER BY u.created_at, u.id`)
	if err != nil {
		return nil, fmt.Errorf("timescale: user list: %w", err)
	}
	defer rows.Close()
	out := []*model.User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("timescale: user scan: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (r *authRepo) UpdateUser(ctx context.Context, id int64, upd model.UserUpdate) error {
	tx, err := r.db.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("timescale: user update: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	tag, err := tx.Exec(ctx, `UPDATE users SET
	    name = COALESCE($2, name),
	    status = COALESCE($3, status),
	    password_hash = COALESCE($4, password_hash),
	    email = CASE WHEN $5::boolean THEN $6::text ELSE email END,
	    must_change_password = COALESCE($7, must_change_password),
	    updated_at = NOW()
	  WHERE id = $1`, id, upd.Name, upd.Status, upd.PasswordHash, upd.Email != nil, emailArg(upd.Email), upd.MustChangePassword)
	if isUnique(err) {
		return repository.ErrConflict
	}
	if err != nil {
		return fmt.Errorf("timescale: user update: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return repository.ErrNotFound
	}
	if upd.Role != nil {
		if _, err := tx.Exec(ctx, `INSERT INTO role_bindings (user_id, scope, role) VALUES ($1, '*', $2)
		  ON CONFLICT (user_id, scope) DO UPDATE SET role = EXCLUDED.role`, id, string(*upd.Role)); err != nil {
			if isUnique(err) {
				return repository.ErrConflict
			}
			return fmt.Errorf("timescale: role update: %w", err)
		}
	}
	return tx.Commit(ctx)
}

func (r *authRepo) SetLastLogin(ctx context.Context, id int64, t time.Time) error {
	_, err := r.db.pool.Exec(ctx, `UPDATE users SET last_login_at = $2 WHERE id = $1`, id, t)
	if err != nil {
		return fmt.Errorf("timescale: last login: %w", err)
	}
	return nil
}

func (r *authRepo) TransferOwnership(ctx context.Context, fromID, toID int64) error {
	tx, err := r.db.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("timescale: transfer ownership: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	// Demote first: the unique index allows a single owner.
	tag, err := tx.Exec(ctx, `UPDATE role_bindings SET role = 'admin' WHERE user_id = $1 AND scope = '*' AND role = 'owner'`, fromID)
	if err != nil {
		return fmt.Errorf("timescale: transfer ownership: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return repository.ErrNotFound
	}
	tag, err = tx.Exec(ctx, `UPDATE role_bindings SET role = 'owner' WHERE user_id = $1 AND scope = '*' AND role = 'admin'`, toID)
	if err != nil {
		return fmt.Errorf("timescale: transfer ownership: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return repository.ErrNotFound
	}
	return tx.Commit(ctx)
}

func (r *authRepo) GetUserByIdentity(ctx context.Context, provider, subject string) (*model.User, error) {
	u, err := scanUser(r.db.pool.QueryRow(ctx, userSelect+` WHERE u.id = (SELECT user_id FROM user_identities WHERE provider = $1 AND subject = $2)`, provider, subject))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, repository.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("timescale: identity get: %w", err)
	}
	return u, nil
}

func (r *authRepo) AddIdentity(ctx context.Context, userID int64, provider, subject, email string) error {
	_, err := r.db.pool.Exec(ctx, `INSERT INTO user_identities (user_id, provider, subject, email) VALUES ($1, $2, $3, $4)
	  ON CONFLICT (provider, subject) DO UPDATE SET email = EXCLUDED.email WHERE user_identities.user_id = EXCLUDED.user_id`,
		userID, provider, subject, email)
	if err != nil {
		return fmt.Errorf("timescale: identity add: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

func (r *authRepo) CreateSession(ctx context.Context, s *model.Session) error {
	_, err := r.db.pool.Exec(ctx, `INSERT INTO sessions (id_hash, user_id, method, created_at, last_seen_at, expires_at, ip, user_agent)
	  VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		s.IDHash, s.UserID, s.Method, s.CreatedAt, s.LastSeenAt, s.ExpiresAt, s.IP, s.UserAgent)
	if err != nil {
		return fmt.Errorf("timescale: session create: %w", err)
	}
	return nil
}

func (r *authRepo) GetSession(ctx context.Context, idHash string) (*model.Session, *model.User, error) {
	var s model.Session
	err := r.db.pool.QueryRow(ctx, `SELECT id_hash, user_id, method, created_at, last_seen_at, expires_at, ip, user_agent
	  FROM sessions WHERE id_hash = $1`, idHash).
		Scan(&s.IDHash, &s.UserID, &s.Method, &s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt, &s.IP, &s.UserAgent)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, repository.ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("timescale: session get: %w", err)
	}
	u, err := r.GetUser(ctx, s.UserID)
	if err != nil {
		return nil, nil, err
	}
	return &s, u, nil
}

func (r *authRepo) TouchSession(ctx context.Context, idHash string, lastSeen, expires time.Time) error {
	_, err := r.db.pool.Exec(ctx, `UPDATE sessions SET last_seen_at = $2, expires_at = $3 WHERE id_hash = $1`, idHash, lastSeen, expires)
	if err != nil {
		return fmt.Errorf("timescale: session touch: %w", err)
	}
	return nil
}

func (r *authRepo) DeleteSession(ctx context.Context, idHash string) error {
	_, err := r.db.pool.Exec(ctx, `DELETE FROM sessions WHERE id_hash = $1`, idHash)
	if err != nil {
		return fmt.Errorf("timescale: session delete: %w", err)
	}
	return nil
}

func (r *authRepo) DeleteUserSessions(ctx context.Context, userID int64, keepHash string) (int64, error) {
	tag, err := r.db.pool.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1 AND id_hash <> $2`, userID, keepHash)
	if err != nil {
		return 0, fmt.Errorf("timescale: user sessions delete: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (r *authRepo) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	tag, err := r.db.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at < $1`, now)
	if err != nil {
		return 0, fmt.Errorf("timescale: expired sessions delete: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ---------------------------------------------------------------------------
// API tokens
// ---------------------------------------------------------------------------

const tokenSelect = `SELECT t.id, t.user_id, COALESCE(u.email, ''), u.username, t.name, t.prefix, t.token_hash, t.role, t.created_at, t.expires_at,
  t.last_used_at, t.revoked_at FROM api_tokens t JOIN users u ON u.id = t.user_id`

func scanToken(row interface{ Scan(...any) error }) (*model.APIToken, error) {
	var (
		t    model.APIToken
		role string
	)
	if err := row.Scan(&t.ID, &t.UserID, &t.UserEmail, &t.UserUsername, &t.Name, &t.Prefix, &t.TokenHash, &role, &t.CreatedAt,
		&t.ExpiresAt, &t.LastUsedAt, &t.RevokedAt); err != nil {
		return nil, err
	}
	t.Role = model.Role(role)
	return &t, nil
}

func (r *authRepo) CreateToken(ctx context.Context, t *model.APIToken) error {
	err := r.db.pool.QueryRow(ctx, `INSERT INTO api_tokens (user_id, name, prefix, token_hash, role, expires_at)
	  VALUES ($1, $2, $3, $4, $5, $6) RETURNING id, created_at`,
		t.UserID, t.Name, t.Prefix, t.TokenHash, string(t.Role), t.ExpiresAt).Scan(&t.ID, &t.CreatedAt)
	if isUnique(err) {
		return repository.ErrConflict
	}
	if err != nil {
		return fmt.Errorf("timescale: token create: %w", err)
	}
	return nil
}

func (r *authRepo) GetTokenByHash(ctx context.Context, hash string) (*model.APIToken, *model.User, error) {
	t, err := scanToken(r.db.pool.QueryRow(ctx, tokenSelect+` WHERE t.token_hash = $1`, hash))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, repository.ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("timescale: token get: %w", err)
	}
	u, err := r.GetUser(ctx, t.UserID)
	if err != nil {
		return nil, nil, err
	}
	return t, u, nil
}

func (r *authRepo) GetToken(ctx context.Context, id int64) (*model.APIToken, error) {
	t, err := scanToken(r.db.pool.QueryRow(ctx, tokenSelect+` WHERE t.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, repository.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("timescale: token get: %w", err)
	}
	return t, nil
}

func (r *authRepo) ListTokens(ctx context.Context, userID int64) ([]*model.APIToken, error) {
	q, args := tokenSelect, []any{}
	if userID != 0 {
		q += ` WHERE t.user_id = $1`
		args = append(args, userID)
	}
	rows, err := r.db.pool.Query(ctx, q+` ORDER BY t.created_at DESC, t.id DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("timescale: token list: %w", err)
	}
	defer rows.Close()
	out := []*model.APIToken{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, fmt.Errorf("timescale: token scan: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *authRepo) RevokeToken(ctx context.Context, id int64, now time.Time) error {
	tag, err := r.db.pool.Exec(ctx, `UPDATE api_tokens SET revoked_at = COALESCE(revoked_at, $2) WHERE id = $1`, id, now)
	if err != nil {
		return fmt.Errorf("timescale: token revoke: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (r *authRepo) TouchToken(ctx context.Context, id int64, t time.Time) error {
	_, err := r.db.pool.Exec(ctx, `UPDATE api_tokens SET last_used_at = $2 WHERE id = $1`, id, t)
	if err != nil {
		return fmt.Errorf("timescale: token touch: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Links
// ---------------------------------------------------------------------------

const linkCols = `id, token_hash, kind, username, email, user_id, role, created_by, created_at, expires_at, used_at`

func scanLink(row interface{ Scan(...any) error }) (*model.UserLink, error) {
	var (
		l    model.UserLink
		role string
	)
	if err := row.Scan(&l.ID, &l.TokenHash, &l.Kind, &l.Username, &l.Email, &l.UserID, &role, &l.CreatedBy, &l.CreatedAt, &l.ExpiresAt, &l.UsedAt); err != nil {
		return nil, err
	}
	l.Role = model.Role(role)
	return &l, nil
}

func (r *authRepo) CreateLink(ctx context.Context, l *model.UserLink) error {
	err := r.db.pool.QueryRow(ctx, `INSERT INTO user_links (token_hash, kind, username, email, user_id, role, created_by, expires_at)
	  VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id, created_at`,
		l.TokenHash, l.Kind, l.Username, l.Email, l.UserID, string(l.Role), l.CreatedBy, l.ExpiresAt).Scan(&l.ID, &l.CreatedAt)
	if isUnique(err) {
		return repository.ErrConflict
	}
	if err != nil {
		return fmt.Errorf("timescale: link create: %w", err)
	}
	return nil
}

func (r *authRepo) GetLinkByHash(ctx context.Context, hash string) (*model.UserLink, error) {
	l, err := scanLink(r.db.pool.QueryRow(ctx, `SELECT `+linkCols+` FROM user_links WHERE token_hash = $1`, hash))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, repository.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("timescale: link get: %w", err)
	}
	return l, nil
}

func (r *authRepo) ListInvites(ctx context.Context, now time.Time) ([]*model.UserLink, error) {
	rows, err := r.db.pool.Query(ctx, `SELECT `+linkCols+` FROM user_links
	  WHERE kind = 'invite' AND used_at IS NULL AND expires_at > $1 ORDER BY created_at DESC, id DESC`, now)
	if err != nil {
		return nil, fmt.Errorf("timescale: invite list: %w", err)
	}
	defer rows.Close()
	out := []*model.UserLink{}
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, fmt.Errorf("timescale: invite scan: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (r *authRepo) GetInviteByEmail(ctx context.Context, email string, now time.Time) (*model.UserLink, error) {
	if email == "" {
		return nil, repository.ErrNotFound
	}
	l, err := scanLink(r.db.pool.QueryRow(ctx, `SELECT `+linkCols+` FROM user_links
	  WHERE kind = 'invite' AND email = $1 AND used_at IS NULL AND expires_at > $2 ORDER BY created_at DESC, id DESC LIMIT 1`,
		strings.ToLower(email), now))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, repository.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("timescale: invite get: %w", err)
	}
	return l, nil
}

func (r *authRepo) GetInviteByUsername(ctx context.Context, username string, now time.Time) (*model.UserLink, error) {
	l, err := scanLink(r.db.pool.QueryRow(ctx, `SELECT `+linkCols+` FROM user_links
	  WHERE kind = 'invite' AND username = $1 AND used_at IS NULL AND expires_at > $2 ORDER BY created_at DESC, id DESC LIMIT 1`,
		strings.ToLower(username), now))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, repository.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("timescale: invite get: %w", err)
	}
	return l, nil
}

func (r *authRepo) ConsumeLink(ctx context.Context, id int64, now time.Time) error {
	tag, err := r.db.pool.Exec(ctx, `UPDATE user_links SET used_at = $2 WHERE id = $1 AND used_at IS NULL AND expires_at > $2`, id, now)
	if err != nil {
		return fmt.Errorf("timescale: link consume: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (r *authRepo) DeleteInvite(ctx context.Context, id int64) error {
	tag, err := r.db.pool.Exec(ctx, `DELETE FROM user_links WHERE id = $1 AND kind = 'invite'`, id)
	if err != nil {
		return fmt.Errorf("timescale: invite delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (r *authRepo) DeleteExpiredLinks(ctx context.Context, now time.Time) (int64, error) {
	tag, err := r.db.pool.Exec(ctx, `DELETE FROM user_links WHERE expires_at < $1::timestamptz - INTERVAL '30 days'`, now)
	if err != nil {
		return 0, fmt.Errorf("timescale: expired links delete: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ---------------------------------------------------------------------------
// Audit
// ---------------------------------------------------------------------------

func (r *authRepo) InsertAudit(ctx context.Context, e *model.AuditEvent) error {
	details := e.Details
	if len(details) == 0 {
		details = json.RawMessage(`{}`)
	}
	t := e.Time
	if t.IsZero() {
		t = time.Now().UTC()
	}
	err := r.db.pool.QueryRow(ctx, `INSERT INTO audit_events (time, actor_user_id, actor_label, action, target_type, target_id, details, ip)
	  VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id, time`,
		t, e.ActorUserID, e.ActorLabel, e.Action, e.TargetType, e.TargetID, []byte(details), e.IP).Scan(&e.ID, &e.Time)
	if err != nil {
		return fmt.Errorf("timescale: audit insert: %w", err)
	}
	return nil
}

func (r *authRepo) ListAudit(ctx context.Context, q repository.AuditQuery) ([]*model.AuditEvent, error) {
	where := []string{"time < $1"}
	before := q.Before
	if before.IsZero() {
		before = time.Now().UTC().Add(time.Minute)
	}
	args := []any{before}
	if q.UserID != 0 {
		args = append(args, q.UserID)
		where = append(where, fmt.Sprintf("actor_user_id = $%d", len(args)))
	}
	if q.Action != "" {
		args = append(args, q.Action)
		where = append(where, fmt.Sprintf("(action = $%d OR action LIKE $%d || '.%%')", len(args), len(args)))
	}
	limit := q.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	args = append(args, limit)
	rows, err := r.db.pool.Query(ctx, `SELECT id, time, actor_user_id, actor_label, action, target_type, target_id, details, ip
	  FROM audit_events WHERE `+strings.Join(where, " AND ")+fmt.Sprintf(` ORDER BY time DESC, id DESC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("timescale: audit list: %w", err)
	}
	defer rows.Close()
	out := []*model.AuditEvent{}
	for rows.Next() {
		var (
			e       model.AuditEvent
			details []byte
		)
		if err := rows.Scan(&e.ID, &e.Time, &e.ActorUserID, &e.ActorLabel, &e.Action, &e.TargetType, &e.TargetID, &details, &e.IP); err != nil {
			return nil, fmt.Errorf("timescale: audit scan: %w", err)
		}
		e.Details = json.RawMessage(details)
		out = append(out, &e)
	}
	return out, rows.Err()
}

func (r *authRepo) DeleteAuditBefore(ctx context.Context, t time.Time) (int64, error) {
	tag, err := r.db.pool.Exec(ctx, `DELETE FROM audit_events WHERE time < $1`, t)
	if err != nil {
		return 0, fmt.Errorf("timescale: audit delete: %w", err)
	}
	return tag.RowsAffected(), nil
}
