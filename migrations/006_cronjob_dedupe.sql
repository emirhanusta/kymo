-- 006_cronjob_dedupe.sql
-- CronJob executions are now keyed on their completion time (sampled_at =
-- completed_at), so the same execution observed more than once — e.g. replayed
-- when KYMO restarts — is stored only once.

-- Remove duplicates recorded by earlier versions (same pod + completion time),
-- keeping the earliest row.
DELETE FROM cronjob_executions a
USING cronjob_executions b
WHERE a.source_name = b.source_name
  AND a.namespace   = b.namespace
  AND a.job_name    = b.job_name
  AND a.pod_name    = b.pod_name
  AND a.completed_at IS NOT DISTINCT FROM b.completed_at
  AND a.id > b.id;

-- Unique indexes on a hypertable must include the partitioning column.
CREATE UNIQUE INDEX IF NOT EXISTS uq_cronjob_execution
    ON cronjob_executions (source_name, namespace, job_name, pod_name, sampled_at);
