-- 013_error_logs.sql
-- Opt-in error-log capture: targets (what to follow), error signatures
-- (grouped error messages per workload), per-minute counts and the newest
-- masked occurrences per signature.

CREATE TABLE IF NOT EXISTS log_capture_targets (
    id          BIGSERIAL PRIMARY KEY,
    source_name TEXT        NOT NULL,
    namespace   TEXT        NOT NULL,
    -- '' = every workload of the namespace.
    workload_id TEXT        NOT NULL DEFAULT '',
    -- 'ui' targets are created through the API; 'config' targets mirror
    -- log_capture.targets in config.yaml (read-only).
    origin      TEXT        NOT NULL DEFAULT 'ui' CHECK (origin IN ('ui', 'config')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (source_name, namespace, workload_id)
);

CREATE TABLE IF NOT EXISTS error_signatures (
    -- first 16 hex chars of sha1(workload_id || 0x00 || template)
    id          TEXT        PRIMARY KEY,
    source_name TEXT        NOT NULL,
    workload_id TEXT        NOT NULL,
    namespace   TEXT        NOT NULL DEFAULT '',
    kind        TEXT        NOT NULL DEFAULT '',
    name        TEXT        NOT NULL DEFAULT '',
    level       TEXT        NOT NULL DEFAULT 'error',
    template    TEXT        NOT NULL,
    sample      TEXT        NOT NULL DEFAULT '',
    total_count BIGINT      NOT NULL DEFAULT 0,
    first_seen  TIMESTAMPTZ NOT NULL,
    last_seen   TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_error_signatures_workload_last
    ON error_signatures (workload_id, last_seen DESC);
CREATE INDEX IF NOT EXISTS idx_error_signatures_source_last
    ON error_signatures (source_name, last_seen DESC);
CREATE INDEX IF NOT EXISTS idx_error_signatures_last
    ON error_signatures (last_seen DESC);

-- Occurrences per signature and minute; pods = distinct pods in that minute.
CREATE TABLE IF NOT EXISTS error_counts (
    signature_id TEXT        NOT NULL REFERENCES error_signatures (id) ON DELETE CASCADE,
    bucket       TIMESTAMPTZ NOT NULL,
    count        INT         NOT NULL,
    pods         TEXT[]      NOT NULL DEFAULT '{}',
    PRIMARY KEY (signature_id, bucket)
);

SELECT create_hypertable(
    'error_counts',
    'bucket',
    if_not_exists       => TRUE,
    chunk_time_interval => INTERVAL '1 day'
);

CREATE INDEX IF NOT EXISTS idx_error_counts_bucket
    ON error_counts (bucket DESC);

-- Newest N masked occurrences per signature (trimmed by the application).
CREATE TABLE IF NOT EXISTS error_occurrences (
    id           BIGSERIAL,
    signature_id TEXT        NOT NULL REFERENCES error_signatures (id) ON DELETE CASCADE,
    time         TIMESTAMPTZ NOT NULL,
    container_id TEXT        NOT NULL DEFAULT '',
    pod          TEXT        NOT NULL DEFAULT '',
    container    TEXT        NOT NULL DEFAULT '',
    level        TEXT        NOT NULL DEFAULT 'error',
    message      TEXT        NOT NULL DEFAULT '',
    PRIMARY KEY (id, time)
);

SELECT create_hypertable(
    'error_occurrences',
    'time',
    if_not_exists       => TRUE,
    chunk_time_interval => INTERVAL '1 day'
);

CREATE INDEX IF NOT EXISTS idx_error_occurrences_sig_time
    ON error_occurrences (signature_id, time DESC);

-- Default retention 7 days; log_capture.retention re-applies it at startup.
DO $$
BEGIN
    PERFORM add_retention_policy('error_counts', INTERVAL '7 days', if_not_exists => TRUE);
    PERFORM add_retention_policy('error_occurrences', INTERVAL '7 days', if_not_exists => TRUE);
EXCEPTION WHEN OTHERS THEN
    RAISE NOTICE 'retention policy for error logs skipped: %', SQLERRM;
END
$$;
