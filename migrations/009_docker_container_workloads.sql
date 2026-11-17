-- 009_docker_container_workloads.sql
-- Every recorded container belongs to a workload. Standalone Docker containers
-- (no Compose project) become a "Container" workload named after the container.
-- Idempotent: only touches Docker rows that still have no workload.

UPDATE containers
SET workload_kind = 'Container',
    workload_name = name,
    workload_id   = source_name || ':' || namespace || ':Container:' || name
WHERE runtime = 'docker'
  AND (workload_name = '' OR workload_kind IN ('', 'unknown'));
