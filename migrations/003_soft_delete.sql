-- Migration 003: soft delete and ignore support for containers
-- Run: psql "$DSN" -f migrations/003_soft_delete.sql

ALTER TABLE containers ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
ALTER TABLE containers ADD COLUMN IF NOT EXISTS ignored BOOLEAN NOT NULL DEFAULT false;

-- Partial index for the hot path: only active (non-deleted, non-ignored) containers
CREATE INDEX IF NOT EXISTS idx_containers_active  ON containers (deleted_at, ignored) WHERE deleted_at IS NULL AND ignored = false;
-- Index to efficiently list archived containers ordered by deletion time
CREATE INDEX IF NOT EXISTS idx_containers_deleted ON containers (deleted_at) WHERE deleted_at IS NOT NULL;
