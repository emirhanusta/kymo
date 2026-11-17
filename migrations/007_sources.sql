-- 007_sources.sql
-- Collector sources created through the API / UI. Sources declared in
-- config.yaml are not stored here. Uploaded kubeconfigs are stored encrypted
-- (AES-256-GCM, key derived from KYMO_SECRET_KEY) in kubeconfig_enc.

CREATE TABLE IF NOT EXISTS sources (
    name                  TEXT        PRIMARY KEY,
    type                  TEXT        NOT NULL CHECK (type IN ('docker', 'kubernetes')),
    enabled               BOOLEAN     NOT NULL DEFAULT TRUE,
    read_only             BOOLEAN     NOT NULL DEFAULT FALSE,
    socket                TEXT        NOT NULL DEFAULT '',
    kubeconfig_path       TEXT        NOT NULL DEFAULT '',
    kube_context          TEXT        NOT NULL DEFAULT '',
    kubeconfig_enc        BYTEA,
    poll_interval_seconds INTEGER     NOT NULL DEFAULT 0,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Supports filtering containers by workload (e.g. a Compose project).
CREATE INDEX IF NOT EXISTS idx_containers_workload
    ON containers (workload_kind, workload_name);
