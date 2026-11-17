-- 005_retention_and_indexes.sql
-- Data-retention policies, a refresh policy for the resource_5m continuous
-- aggregate, and supporting indexes.
--
-- Retention policies need the TimescaleDB community (TSL) edition. Each policy
-- is wrapped in its own DO block so that the migration still succeeds on an
-- Apache-only build (the policy is simply skipped with a NOTICE).
-- Adjust the intervals below to taste and re-run:
--   SELECT remove_retention_policy('resource_samples');  -- then re-add

DO $$
BEGIN
    PERFORM add_retention_policy('resource_samples', INTERVAL '14 days', if_not_exists => TRUE);
EXCEPTION WHEN OTHERS THEN
    RAISE NOTICE 'retention policy for resource_samples skipped: %', SQLERRM;
END
$$;

DO $$
BEGIN
    PERFORM add_retention_policy('stability_scores', INTERVAL '30 days', if_not_exists => TRUE);
EXCEPTION WHEN OTHERS THEN
    RAISE NOTICE 'retention policy for stability_scores skipped: %', SQLERRM;
END
$$;

DO $$
BEGIN
    PERFORM add_retention_policy('lifecycle_events', INTERVAL '180 days', if_not_exists => TRUE);
EXCEPTION WHEN OTHERS THEN
    RAISE NOTICE 'retention policy for lifecycle_events skipped: %', SQLERRM;
END
$$;

DO $$
BEGIN
    PERFORM add_retention_policy('cronjob_executions', INTERVAL '90 days', if_not_exists => TRUE);
EXCEPTION WHEN OTHERS THEN
    RAISE NOTICE 'retention policy for cronjob_executions skipped: %', SQLERRM;
END
$$;

DO $$
BEGIN
    PERFORM add_continuous_aggregate_policy('resource_5m',
        start_offset      => INTERVAL '2 days',
        end_offset        => INTERVAL '5 minutes',
        schedule_interval => INTERVAL '5 minutes',
        if_not_exists     => TRUE);
EXCEPTION WHEN OTHERS THEN
    RAISE NOTICE 'refresh policy for resource_5m skipped: %', SQLERRM;
END
$$;

-- Supports the per-CronJob execution history query
-- (namespace + cronjob_name, newest first).
CREATE INDEX IF NOT EXISTS idx_cronjob_ns_name_time
    ON cronjob_executions (namespace, cronjob_name, sampled_at DESC);

-- Supports filtering containers by status (dashboard / list filters).
CREATE INDEX IF NOT EXISTS idx_containers_status
    ON containers (current_status);
