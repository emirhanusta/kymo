-- 002_add_source_name.sql
-- Adds a source_name column to every time-series table so that containers,
-- events, resource samples, and stability scores can be attributed to a named
-- collector source (e.g. "local-docker", "prod-k8s").
--
-- Run via: psql "$DSN" -f migrations/002_add_source_name.sql

ALTER TABLE containers
    ADD COLUMN IF NOT EXISTS source_name TEXT NOT NULL DEFAULT 'default';

ALTER TABLE lifecycle_events
    ADD COLUMN IF NOT EXISTS source_name TEXT NOT NULL DEFAULT 'default';

ALTER TABLE resource_samples
    ADD COLUMN IF NOT EXISTS source_name TEXT NOT NULL DEFAULT 'default';

ALTER TABLE stability_scores
    ADD COLUMN IF NOT EXISTS source_name TEXT NOT NULL DEFAULT 'default';

-- Indexes to support per-source queries.
CREATE INDEX IF NOT EXISTS idx_containers_source
    ON containers (source_name);

CREATE INDEX IF NOT EXISTS idx_lifecycle_source
    ON lifecycle_events (source_name, occurred_at DESC);

CREATE INDEX IF NOT EXISTS idx_resource_source
    ON resource_samples (source_name, sampled_at DESC);

CREATE INDEX IF NOT EXISTS idx_score_source
    ON stability_scores (source_name, calculated_at DESC);
