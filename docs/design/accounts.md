# Accounts & access control — design (Phase 1)

Status: implemented (Phase 1). Revised: username is the login identity, email is
optional, and the setup code was replaced by an initial admin account.

## Decisions
- **Single organization per installation.** No multi-tenancy; every KYMO install
  (Helm / compose) has its own users.
- **Login: local password + SSO (OIDC) in Phase 1.** Sign-in is by **username**
  (or email, when the user has one). KYMO is self-hosted and sends no email, so
  email is optional profile data, never required.
- **No setup code.** The first owner is an initial admin account created at
  startup (Argo CD style), see "Bootstrap".
- **Fixed roles**, global for all clusters in Phase 1. The data model already
  carries a scope so per-cluster access (Phase 3) needs no schema change.
- **Viewers can read logs** (live pod logs, crash logs, captured error logs).

## Roles
| Role | Can |
|---|---|
| viewer | Read everything: overview, workloads, jobs, events, issues, errors, logs, clusters, settings pages (read-only). |
| operator | viewer + restart pod / scale / rollout restart on non-read-only clusters, toggle log capture targets. |
| admin | operator + add/edit/remove/purge clusters, reload config, notification channels, users, invites, API tokens of others, audit log. |
| owner | admin + promote/demote admins, transfer ownership. Exactly one owner. |

Checks happen in the backend middleware per route (`require(role)`); the UI hides
or disables what the current role cannot do.

## Data model (migrations 014, 015)
- `users` — id, username (unique, lower-case, `^[a-z0-9][a-z0-9._-]{1,63}$`),
  email (nullable; lower-cased; unique via a partial index on lower(email) where
  not null), name, password_hash (argon2id, NULL for SSO-only users),
  must_change_password (default false), status (`active` | `disabled`),
  created_at, updated_at, last_login_at. Migration 015 backfills usernames of
  existing rows from the email local part (sanitized; `-2`, `-3` on clashes).
- `role_bindings` — user_id, scope (`*` in Phase 1, later a source name), role;
  PK (user_id, scope).
- `user_identities` — user_id, provider (`oidc`), subject, email;
  UNIQUE (provider, subject).
- `sessions` — id_hash (sha256 of the cookie value), user_id, method
  (`password` | `oidc`), created_at, last_seen_at, expires_at, ip, user_agent.
- `api_tokens` — id, user_id, name, prefix (shown in UI), token_hash, role
  (≤ the creator's role), created_at, expires_at, last_used_at, revoked_at.
- `user_links` — token_hash, kind (`invite` | `password_reset`), username +
  optional email (invites) / user_id (resets), role (invites), created_by, expires_at (7d invite, 1h reset), used_at.
- `audit_events` — id, time, actor_user_id, actor_label (username / token name /
  "system"), action, target_type, target_id, details JSONB, ip. Kept 1 year.

## Authentication
- **Password:** argon2id; min 12 chars; login rate-limited per IP (50 / 15 min) and per
  normalized login, i.e. the lower-cased username or email typed (10 / 15 min),
  with a generic error message.
- **Sessions:** random 32-byte cookie `kymo_session`, HttpOnly, SameSite=Lax,
  Secure when `server.public_url` is https. Sliding 7 days, absolute 30 days.
  Session id rotated at login; all sessions of a user revoked on password change,
  disable or role change.
- **CSRF:** CORS `*` is removed (same-origin only, optional
  `server.allowed_origins`). Mutating requests must carry the `X-KYMO-Request: 1`
  header and a matching `Origin`/`Referer` when present.
- **API tokens:** `Authorization: Bearer kymo_<prefix>_<secret>`; only the hash
  is stored; the secret is shown once.
- **SSO (OIDC):** authorization code flow with PKCE, state and nonce
  (`coreos/go-oidc` + `x/oauth2`). Config:
  ```yaml
  auth:
    oidc:
      enabled: true
      name: "Google"                 # button label
      issuer: "https://accounts.google.com"
      client_id: "..."
      client_secret: "..."           # or KYMO_AUTH_OIDC_CLIENT_SECRET
      scopes: ["openid", "email", "profile"]
      allowed_domains: ["example.com"]  # optional, on the verified email
      groups_claim: "groups"            # optional
      role_mapping:                     # optional, first match wins (highest role)
        admin: ["kymo-admins"]
        operator: ["sre"]
      default_role: "viewer"            # for new users without a mapped group
      auto_create: true                 # JIT users; false = invite required
      disable_password_login: false
  ```
  Redirect URL: `<public_url>/api/v1/auth/oidc/callback`. Users are matched by
  (issuer, subject); first login links to an existing user with the same
  **verified** email. When role_mapping is set, the role is re-synced at every
  login (owner is never demoted by SSO).
- **Public endpoints:** `/api/v1/healthz`, `/api/v1/auth/*` needed for login /
  invites, and the SPA static files. Everything else requires a session
  or token.

## Bootstrap & user management
- At startup, when **no users exist**, KYMO creates the initial owner:
  * `KYMO_ADMIN_PASSWORD` set → username `KYMO_ADMIN_USERNAME` (default `admin`),
    optional `KYMO_ADMIN_EMAIL` / `KYMO_ADMIN_NAME`, `must_change_password` =
    `KYMO_ADMIN_MUST_CHANGE_PASSWORD` (default false).
  * otherwise → owner `admin` with a random 24-character password and
    `must_change_password = true`; one INFO line is logged:
    `initial admin account created: username "admin", password "<pw>" — sign in and change it`
    (grep "initial admin").
  * The variables are ignored once any user exists.
- **must_change_password gate:** such a session may only call `GET /auth/config`,
  `GET /auth/me`, `POST /auth/logout`, `POST /me/password` and `PATCH /me`;
  everything else answers `403 {code: "password_change_required"}` (the guard is
  part of `Require`, so new routes are gated by default). API tokens cannot be
  created meanwhile. Changing the password clears the flag. Admin-created reset
  links, invites and the CLI reset never set the flag (a reset clears it).
- Helm: the chart Secret holds a generated `admin-password` (kept across
  upgrades via `lookup`, or `auth.admin.password` / `existingSecret`), passed as
  `KYMO_ADMIN_PASSWORD` with `KYMO_ADMIN_MUST_CHANGE_PASSWORD=true`.
- Invites: admin creates an invite (username + role, optional email) → link valid
  7 days, copied and shared manually (no email sending). The invitee sets a name
  and password.
- Password reset: admin creates a 1-hour reset link; lockout recovery via
  `kymo admin reset-password --username ... | --email ...` (CLI subcommand of
  the same binary).
- Users edit their own name / email (`PATCH /me`); admins may set a user's email.
- Disable instead of delete (keeps audit history); last owner cannot be disabled.

## Audit
Recorded: login success/failure, logout, bootstrap, user/role/invite/token changes,
pod/deployment actions, source add/update/delete/purge/reload, notification
channel changes, log capture target changes. UI: Settings → Audit (filter by user,
action, time).

## API (under /api/v1)
Errors: `{error: "<human message>", code: "<machine code>"}`. 401 `unauthenticated`,
403 `forbidden` (+ `required_role`), 403 `password_change_required`,
429 `rate_limited`.

Shapes:
- `User` = {id, username, email ("" when none), name, role, status, has_password,
  sso, must_change_password, created_at, last_login_at}
- `Permission` strings: `workloads:manage` (operator), `logcapture:manage`
  (operator), `sources:manage`, `channels:manage`, `users:manage`,
  `tokens:manage_all`, `audit:read` (admin), `owner` (owner).

Public:
- `GET /auth/config` → {auth_enabled, password_login, oidc: {enabled, name}}
- `POST /auth/login` {login, password} → {user} + cookie (401 `invalid_credentials`);
  `login` is the username or the email, case-insensitive.
- `GET /auth/oidc/login?return_to=/path` → 302 to the provider;
  `GET /auth/oidc/callback` → 302 to return_to, or to
  `/login?error=<domain_not_allowed|not_invited|disabled|oidc_failed>`
- `GET /auth/links/:token` → {kind: invite|password_reset, username, email, role, expires_at}
  (404 / 410 expired or used); `POST /auth/links/:token` {name?, password}
  → {user} + cookie

Session / token required:
- `POST /auth/logout` → 204; `GET /auth/me` → {auth_enabled, user, permissions}
  (auth disabled: user = {id: 0, name: "local", role: "owner", ...}, all permissions)
- `PATCH /me` {name?, email?} → {user} (email "" clears it; 400 `invalid_email`,
  409 `email_taken`). Also allowed while the password must be changed.
- `POST /me/password` {current_password, new_password} → 204 (other sessions
  revoked, `must_change_password` cleared; 400 `same_password` if unchanged)
- `GET /users` → [User] (any role); `PATCH /users/:id` {name?, email?, role?, status?}
  (admin; only owner changes admins; cannot disable/demote the owner or yourself)
- `POST /users/:id/reset-link` → {url, expires_at} (admin, local-password users)
- `POST /users/:id/transfer-ownership` → 204 (owner; target must be admin+active)
- `GET /users/invites` → [{id, username, email, role, created_by, created_by_id,
  created_at, expires_at}];
  `POST /users/invites` {username, role, email?} → {id, username, email, role, url,
  expires_at} (400 `invalid_username` / `invalid_email`, 409 `username_taken` /
  `email_taken`);
  `DELETE /users/invites/:id` (admin)
- `GET /tokens[?all=1]` → [{id, name, prefix, role, user_id, user_username, user_email,
  created_at, expires_at, last_used_at, revoked_at}];
  `POST /tokens` {name, role, expires_in_days: number|null} →
  {token: {...}, secret: "kymo_..."}; `DELETE /tokens/:id` (own, or admin)
- `GET /audit?user_id&action&before&limit` → [{id, time, actor_user_id,
  actor_label, action, target_type, target_id, details, ip}] (admin)

Existing routes get role requirements: GETs = viewer; pod/deployment management =
operator; log-capture target/toggle writes = operator; sources writes/reload/purge,
notification channel writes + test = admin.

- `auth.enabled: false` is allowed for local development only; KYMO then logs a
  loud warning and the UI shows an "auth disabled" banner.

## UI
Login page (one "Username or email" field + "Sign in with <SSO>" button), a
forced Change-password page (`/change-password`, shown for every route while
`must_change_password`), invite / reset pages, user menu (top-right: username,
name, role, Profile, change password, logout),
Settings → Users, API tokens, Audit. 401 → redirect to login keeping the return
URL; 403 → toast "Your role (viewer) cannot do this".

## Later phases
- Phase 2: auth proxy mode (trust `X-Forwarded-Email` from oauth2-proxy within
  a trusted CIDR), SMTP for invites, optional TOTP 2FA for local accounts.
- Phase 3: per-cluster role bindings (scope = source name) and filtered queries.
