-- 011_k8s_events.sql
-- Persisted Kubernetes Events (they expire after ~1h in the cluster).

CREATE TABLE IF NOT EXISTS k8s_events (
    id          BIGSERIAL,
    source_name TEXT        NOT NULL,
    uid         TEXT        NOT NULL,
    namespace   TEXT        NOT NULL DEFAULT '',
    kind        TEXT        NOT NULL DEFAULT '',
    name        TEXT        NOT NULL DEFAULT '',
    workload_id TEXT        NOT NULL DEFAULT '',
    type        TEXT        NOT NULL,
    reason      TEXT        NOT NULL DEFAULT '',
    message     TEXT        NOT NULL DEFAULT '',
    count       INT         NOT NULL DEFAULT 1,
    first_seen  TIMESTAMPTZ NOT NULL,
    last_seen   TIMESTAMPTZ NOT NULL,
    component   TEXT        NOT NULL DEFAULT '',
    UNIQUE (source_name, uid, first_seen)
);

SELECT create_hypertable(
    'k8s_events',
    'first_seen',
    if_not_exists       => TRUE,
    chunk_time_interval => INTERVAL '7 days'
);

CREATE INDEX IF NOT EXISTS idx_k8s_events_source_last
    ON k8s_events (source_name, last_seen DESC);
CREATE INDEX IF NOT EXISTS idx_k8s_events_workload_last
    ON k8s_events (workload_id, last_seen DESC) WHERE workload_id <> '';
CREATE INDEX IF NOT EXISTS idx_k8s_events_type_last
    ON k8s_events (type, last_seen DESC);
CREATE INDEX IF NOT EXISTS idx_k8s_events_reason
    ON k8s_events (reason);

DO $$
BEGIN
    PERFORM add_retention_policy('k8s_events', INTERVAL '90 days', if_not_exists => TRUE);
EXCEPTION WHEN OTHERS THEN
    RAISE NOTICE 'retention policy for k8s_events skipped: %', SQLERRM;
END
$$;
