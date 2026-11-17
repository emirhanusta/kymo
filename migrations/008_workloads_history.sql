-- 008_workloads_history.sql
-- Workload-level history: every container row carries the ID of the workload
-- it belongs to ("source:namespace:kind:name"), so history and stability can
-- be aggregated across pod replacements (rollouts, rescheduling, crashes).

ALTER TABLE containers ADD COLUMN IF NOT EXISTS workload_id TEXT NOT NULL DEFAULT '';

-- Back-fill rows recorded before this migration.
UPDATE containers
SET workload_id = source_name || ':' || namespace || ':' || workload_kind || ':' || workload_name
WHERE workload_id = ''
  AND workload_name <> ''
  AND workload_kind NOT IN ('', 'unknown');

CREATE INDEX IF NOT EXISTS idx_containers_workload_id
    ON containers (workload_id) WHERE workload_id <> '';

-- Stability scores computed per workload (all instances together).
CREATE TABLE IF NOT EXISTS workload_scores (
    id                   BIGSERIAL,
    workload_id          TEXT             NOT NULL,
    source_name          TEXT             NOT NULL DEFAULT '',
    score                DOUBLE PRECISION NOT NULL,
    restart_penalty      DOUBLE PRECISION NOT NULL DEFAULT 0,
    crash_penalty        DOUBLE PRECISION NOT NULL DEFAULT 0,
    oom_penalty          DOUBLE PRECISION NOT NULL DEFAULT 0,
    volatility_penalty   DOUBLE PRECISION NOT NULL DEFAULT 0,
    availability_penalty DOUBLE PRECISION NOT NULL DEFAULT 0,
    restarts             INTEGER          NOT NULL DEFAULT 0,
    crashes              INTEGER          NOT NULL DEFAULT 0,
    ooms                 INTEGER          NOT NULL DEFAULT 0,
    calculated_at        TIMESTAMPTZ      NOT NULL
);

SELECT create_hypertable('workload_scores', 'calculated_at',
    if_not_exists => TRUE, chunk_time_interval => INTERVAL '7 days');

CREATE INDEX IF NOT EXISTS idx_workload_scores_time
    ON workload_scores (workload_id, calculated_at DESC);

DO $$
BEGIN
    PERFORM add_retention_policy('workload_scores', INTERVAL '30 days', if_not_exists => TRUE);
EXCEPTION WHEN OTHERS THEN
    RAISE NOTICE 'retention policy for workload_scores skipped: %', SQLERRM;
END
$$;
