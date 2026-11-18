-- 014_accounts.sql
-- Accounts & access control (Phase 1): users, role bindings, SSO identities,
-- sessions, API tokens, one-time links (invites / password resets) and the
-- audit log. See docs/design/accounts.md.

CREATE TABLE IF NOT EXISTS users (
    id            BIGSERIAL PRIMARY KEY,
    -- Always stored lower-cased (enforced); unique.
    email         TEXT        NOT NULL UNIQUE CHECK (email = lower(email) AND email <> ''),
    name          TEXT        NOT NULL DEFAULT '',
    -- argon2id (encoded); NULL for SSO-only users.
    password_hash TEXT,
    status        TEXT        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_login_at TIMESTAMPTZ
);

-- scope '*' = every cluster (Phase 1); Phase 3 uses a source name.
CREATE TABLE IF NOT EXISTS role_bindings (
    user_id BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    scope   TEXT   NOT NULL DEFAULT '*',
    role    TEXT   NOT NULL CHECK (role IN ('viewer', 'operator', 'admin', 'owner')),
    PRIMARY KEY (user_id, scope)
);

-- Exactly one owner installation-wide.
CREATE UNIQUE INDEX IF NOT EXISTS idx_role_bindings_single_owner
    ON role_bindings (role) WHERE role = 'owner' AND scope = '*';

CREATE TABLE IF NOT EXISTS user_identities (
    user_id    BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- "oidc:<issuer>" so identities of different issuers never collide.
    provider   TEXT        NOT NULL,
    subject    TEXT        NOT NULL,
    email      TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (provider, subject)
);

CREATE INDEX IF NOT EXISTS idx_user_identities_user ON user_identities (user_id);

CREATE TABLE IF NOT EXISTS sessions (
    -- hex sha256 of the cookie value
    id_hash      TEXT        PRIMARY KEY,
    user_id      BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    method       TEXT        NOT NULL CHECK (method IN ('password', 'oidc')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at   TIMESTAMPTZ NOT NULL,
    ip           TEXT        NOT NULL DEFAULT '',
    user_agent   TEXT        NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions (user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions (expires_at);

CREATE TABLE IF NOT EXISTS api_tokens (
    id           BIGSERIAL   PRIMARY KEY,
    user_id      BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name         TEXT        NOT NULL,
    -- shown in the UI (kymo_<prefix>_...)
    prefix       TEXT        NOT NULL,
    -- hex sha256 of the full token
    token_hash   TEXT        NOT NULL UNIQUE,
    role         TEXT        NOT NULL CHECK (role IN ('viewer', 'operator', 'admin', 'owner')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at   TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_api_tokens_user ON api_tokens (user_id);

CREATE TABLE IF NOT EXISTS user_links (
    id         BIGSERIAL   PRIMARY KEY,
    token_hash TEXT        NOT NULL UNIQUE,
    kind       TEXT        NOT NULL CHECK (kind IN ('invite', 'password_reset')),
    email      TEXT        NOT NULL DEFAULT '',
    user_id    BIGINT      REFERENCES users (id) ON DELETE CASCADE,
    role       TEXT        NOT NULL DEFAULT '' CHECK (role IN ('', 'viewer', 'operator', 'admin', 'owner')),
    created_by BIGINT      REFERENCES users (id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_user_links_email ON user_links (email) WHERE kind = 'invite';

-- Audit log. No foreign key on actor_user_id so history survives user removal.
-- A hypertable (like the other time-series tables) so the 1-year retention is
-- a policy; the primary key therefore includes the time column.
CREATE TABLE IF NOT EXISTS audit_events (
    id            BIGSERIAL,
    time          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    actor_user_id BIGINT,
    actor_label   TEXT        NOT NULL DEFAULT '',
    action        TEXT        NOT NULL,
    target_type   TEXT        NOT NULL DEFAULT '',
    target_id     TEXT        NOT NULL DEFAULT '',
    details       JSONB       NOT NULL DEFAULT '{}',
    ip            TEXT        NOT NULL DEFAULT '',
    PRIMARY KEY (id, time)
);

SELECT create_hypertable(
    'audit_events',
    'time',
    if_not_exists       => TRUE,
    chunk_time_interval => INTERVAL '30 days'
);

CREATE INDEX IF NOT EXISTS idx_audit_events_time ON audit_events (time DESC);
CREATE INDEX IF NOT EXISTS idx_audit_events_actor ON audit_events (actor_user_id, time DESC);
CREATE INDEX IF NOT EXISTS idx_audit_events_action ON audit_events (action, time DESC);

-- Audit events are kept one year.
DO $$
BEGIN
    PERFORM add_retention_policy('audit_events', INTERVAL '365 days', if_not_exists => TRUE);
EXCEPTION WHEN OTHERS THEN
    RAISE NOTICE 'retention policy for audit_events skipped: %', SQLERRM;
END
$$;
