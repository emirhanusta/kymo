-- 015_usernames.sql
-- Accounts: the username is the login identity; email becomes optional.
-- Also adds must_change_password (initial admin account) and stores a username
-- (plus optional email) on invites. Idempotent.

ALTER TABLE users ADD COLUMN IF NOT EXISTS username TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS must_change_password BOOLEAN NOT NULL DEFAULT false;

-- Backfill: username from the email local part (sanitized), de-duplicated with
-- -2, -3, ... suffixes. Only rows without a username are touched.
DO $$
DECLARE
    r RECORD;
    base TEXT;
    cand TEXT;
    n INT;
BEGIN
    FOR r IN SELECT id, email FROM users WHERE username IS NULL ORDER BY id LOOP
        base := lower(split_part(COALESCE(r.email, ''), '@', 1));
        base := regexp_replace(base, '[^a-z0-9._-]', '-', 'g');
        base := regexp_replace(base, '^[^a-z0-9]+', '');
        base := left(base, 60);
        IF length(base) < 2 THEN
            base := 'user';
        END IF;
        cand := base;
        n := 1;
        WHILE EXISTS (SELECT 1 FROM users WHERE username = cand) LOOP
            n := n + 1;
            cand := base || '-' || n;
        END LOOP;
        UPDATE users SET username = cand WHERE id = r.id;
    END LOOP;
END
$$;

ALTER TABLE users ALTER COLUMN username SET NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username ON users (username);

DO $$
BEGIN
    ALTER TABLE users ADD CONSTRAINT users_username_format CHECK (username ~ '^[a-z0-9][a-z0-9._-]{1,63}$');
EXCEPTION WHEN duplicate_object THEN
    NULL;
END
$$;

-- Email is optional: NULL = none. The old column-level UNIQUE is replaced by a
-- partial unique index on lower(email). The lower-case CHECK stays (NULL passes).
ALTER TABLE users ALTER COLUMN email DROP NOT NULL;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_email_key;
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email_lower ON users (lower(email)) WHERE email IS NOT NULL;

-- Invites carry the username of the account to create (email optional).
ALTER TABLE user_links ADD COLUMN IF NOT EXISTS username TEXT NOT NULL DEFAULT '';
