-- 010_crash_logs.sql
-- Crash-time log snapshots and per-container rollout revisions.

CREATE TABLE IF NOT EXISTS crash_logs (
    id           BIGSERIAL,
    container_id TEXT        NOT NULL REFERENCES containers(id) ON DELETE CASCADE,
    source_name  TEXT        NOT NULL DEFAULT '',
    occurred_at  TIMESTAMPTZ NOT NULL,
    reason       TEXT        NOT NULL DEFAULT '',
    exit_code    INT         NOT NULL DEFAULT 0,
    previous     BOOLEAN     NOT NULL DEFAULT FALSE,
    lines        TEXT        NOT NULL,
    line_count   INT         NOT NULL DEFAULT 0,
    truncated    BOOLEAN     NOT NULL DEFAULT FALSE,
    captured_at  TIMESTAMPTZ NOT NULL,
    UNIQUE (container_id, occurred_at)
);

SELECT create_hypertable(
    'crash_logs',
    'occurred_at',
    if_not_exists       => TRUE,
    chunk_time_interval => INTERVAL '7 days'
);

CREATE INDEX IF NOT EXISTS idx_crash_logs_container_time
    ON crash_logs (container_id, occurred_at DESC);

DO $$
BEGIN
    PERFORM add_retention_policy('crash_logs', INTERVAL '30 days', if_not_exists => TRUE);
EXCEPTION WHEN OTHERS THEN
    RAISE NOTICE 'retention policy for crash_logs skipped: %', SQLERRM;
END
$$;

ALTER TABLE containers ADD COLUMN IF NOT EXISTS revision TEXT NOT NULL DEFAULT '';
