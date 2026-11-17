ALTER TABLE containers ADD COLUMN IF NOT EXISTS workload_kind TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE containers ADD COLUMN IF NOT EXISTS workload_name TEXT NOT NULL DEFAULT '';
ALTER TABLE containers ADD COLUMN IF NOT EXISTS is_system BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS cronjob_executions (
    id               BIGSERIAL,
    source_name      TEXT        NOT NULL,
    namespace        TEXT        NOT NULL,
    cronjob_name     TEXT        NOT NULL,
    job_name         TEXT        NOT NULL,
    pod_name         TEXT        NOT NULL DEFAULT '',
    started_at       TIMESTAMPTZ,
    completed_at     TIMESTAMPTZ,
    duration_seconds DOUBLE PRECISION,
    succeeded        BOOLEAN,
    exit_code        INTEGER     NOT NULL DEFAULT 0,
    sampled_at       TIMESTAMPTZ NOT NULL
);

SELECT create_hypertable('cronjob_executions', 'sampled_at', if_not_exists => TRUE, chunk_time_interval => INTERVAL '7 days');

CREATE INDEX IF NOT EXISTS idx_cronjob_source_name ON cronjob_executions (source_name, cronjob_name, sampled_at DESC);
CREATE INDEX IF NOT EXISTS idx_cronjob_namespace ON cronjob_executions (namespace, cronjob_name);
