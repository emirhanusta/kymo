-- 001_initial_schema.sql
-- Initial TimescaleDB schema for KYMO.
-- Run via: psql "$DSN" -f migrations/001_initial_schema.sql
--          or mount this file into the TimescaleDB container's
--          /docker-entrypoint-initdb.d/ directory for automatic execution.

-- Enable the TimescaleDB extension (no-op if already installed).
CREATE EXTENSION IF NOT EXISTS timescaledb;

-- ---------------------------------------------------------------------------
-- containers
-- ---------------------------------------------------------------------------
-- Stores the aggregated identity and lifetime statistics for every observed
-- container. A single row per container is maintained via upsert, so this
-- table always reflects the current known state rather than historical snapshots.

CREATE TABLE IF NOT EXISTS containers (
    id             TEXT        NOT NULL PRIMARY KEY,
    runtime        TEXT        NOT NULL CHECK (runtime IN ('docker', 'kubernetes')),
    name           TEXT        NOT NULL,
    image          TEXT        NOT NULL,
    namespace      TEXT        NOT NULL DEFAULT '',
    node_name      TEXT        NOT NULL DEFAULT '',
    first_seen_at  TIMESTAMPTZ NOT NULL,
    last_seen_at   TIMESTAMPTZ NOT NULL,
    start_count    INTEGER     NOT NULL DEFAULT 0,
    crash_count    INTEGER     NOT NULL DEFAULT 0,
    oom_count      INTEGER     NOT NULL DEFAULT 0,
    current_status TEXT        NOT NULL DEFAULT 'unknown'
);

CREATE INDEX IF NOT EXISTS idx_containers_runtime
    ON containers (runtime);

CREATE INDEX IF NOT EXISTS idx_containers_last_seen
    ON containers (last_seen_at DESC);

-- ---------------------------------------------------------------------------
-- lifecycle_events
-- ---------------------------------------------------------------------------
-- Append-only time-series table that records every container state-change
-- (start, stop, crash, OOM, pause, resume). Converting it to a hypertable
-- on occurred_at enables efficient range queries and data retention policies.

CREATE TABLE IF NOT EXISTS lifecycle_events (
    id           BIGSERIAL,
    container_id TEXT        NOT NULL REFERENCES containers(id) ON DELETE CASCADE,
    event_type   TEXT        NOT NULL,
    exit_code    INTEGER     NOT NULL DEFAULT 0,
    node_name    TEXT        NOT NULL DEFAULT '',
    metadata     JSONB,
    occurred_at  TIMESTAMPTZ NOT NULL
);

SELECT create_hypertable(
    'lifecycle_events',
    'occurred_at',
    if_not_exists       => TRUE,
    chunk_time_interval => INTERVAL '1 day'
);

-- Composite index supporting per-container event history queries.
CREATE INDEX IF NOT EXISTS idx_lifecycle_container_time
    ON lifecycle_events (container_id, occurred_at DESC);

-- Index supporting global event-type queries (e.g. "all recent OOMs").
CREATE INDEX IF NOT EXISTS idx_lifecycle_event_type
    ON lifecycle_events (event_type, occurred_at DESC);

-- ---------------------------------------------------------------------------
-- resource_samples
-- ---------------------------------------------------------------------------
-- High-frequency time-series table storing per-container CPU, memory, network,
-- and block I/O snapshots. Partitioned hourly by sampled_at to allow fast
-- range scans and granular time-based retention policies.

CREATE TABLE IF NOT EXISTS resource_samples (
    id                BIGSERIAL,
    container_id      TEXT             NOT NULL REFERENCES containers(id) ON DELETE CASCADE,
    cpu_percent       DOUBLE PRECISION NOT NULL DEFAULT 0,
    mem_bytes         BIGINT           NOT NULL DEFAULT 0,
    mem_percent       DOUBLE PRECISION NOT NULL DEFAULT 0,
    net_rx_bytes      BIGINT           NOT NULL DEFAULT 0,
    net_tx_bytes      BIGINT           NOT NULL DEFAULT 0,
    block_read_bytes  BIGINT           NOT NULL DEFAULT 0,
    block_write_bytes BIGINT           NOT NULL DEFAULT 0,
    sampled_at        TIMESTAMPTZ      NOT NULL
);

SELECT create_hypertable(
    'resource_samples',
    'sampled_at',
    if_not_exists       => TRUE,
    chunk_time_interval => INTERVAL '1 hour'
);

CREATE INDEX IF NOT EXISTS idx_resource_container_time
    ON resource_samples (container_id, sampled_at DESC);

-- Continuous aggregate: 5-minute CPU and memory rollups per container.
-- Used by dashboard queries to avoid scanning raw high-frequency data.
-- Refresh policy should be configured separately via add_continuous_aggregate_policy().
CREATE MATERIALIZED VIEW IF NOT EXISTS resource_5m
WITH (timescaledb.continuous) AS
SELECT
    container_id,
    time_bucket('5 minutes', sampled_at) AS bucket,
    AVG(cpu_percent)                     AS avg_cpu_percent,
    MAX(cpu_percent)                     AS max_cpu_percent,
    AVG(mem_bytes)                       AS avg_mem_bytes,
    MAX(mem_bytes)                       AS max_mem_bytes,
    SUM(net_rx_bytes)                    AS total_net_rx_bytes,
    SUM(net_tx_bytes)                    AS total_net_tx_bytes
FROM resource_samples
GROUP BY container_id, bucket
WITH NO DATA;

-- ---------------------------------------------------------------------------
-- stability_scores
-- ---------------------------------------------------------------------------
-- Periodic computed stability scores for each container. Each row captures
-- the individual penalty components alongside the final score so breakdowns
-- can be surfaced to operators without re-running the scoring algorithm.

CREATE TABLE IF NOT EXISTS stability_scores (
    id                   BIGSERIAL,
    container_id         TEXT             NOT NULL REFERENCES containers(id) ON DELETE CASCADE,
    score                DOUBLE PRECISION NOT NULL,
    restart_penalty      DOUBLE PRECISION NOT NULL DEFAULT 0,
    crash_penalty        DOUBLE PRECISION NOT NULL DEFAULT 0,
    oom_penalty          DOUBLE PRECISION NOT NULL DEFAULT 0,
    volatility_penalty   DOUBLE PRECISION NOT NULL DEFAULT 0,
    availability_penalty DOUBLE PRECISION NOT NULL DEFAULT 0,
    calculated_at        TIMESTAMPTZ      NOT NULL
);

SELECT create_hypertable(
    'stability_scores',
    'calculated_at',
    if_not_exists       => TRUE,
    chunk_time_interval => INTERVAL '7 days'
);

CREATE INDEX IF NOT EXISTS idx_score_container_time
    ON stability_scores (container_id, calculated_at DESC);
