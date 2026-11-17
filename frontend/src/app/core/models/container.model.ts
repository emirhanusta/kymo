export interface Page<T> {
  data: T[];
  total: number;
  page: number;
  limit: number;
  total_pages: number;
}

export interface Container {
  id: string;
  name: string;
  image: string;
  runtime: 'docker' | 'kubernetes';
  source_name: string;
  current_status: string;
  namespace?: string;
  node_name?: string;
  workload_kind?: string;
  workload_name?: string;
  is_system: boolean;
  start_count: number;
  crash_count: number;
  oom_count: number;
  first_seen_at: string;
  last_seen_at: string;
  deleted_at?: string;
  ignored: boolean;
  /** Latest stability score 0–100; null when not yet computed. */
  score: number | null;
}

export type ContainerStatusFilter = '' | 'running' | 'stopped' | 'crashing' | 'paused' | 'unknown';

export type ContainerSortField =
  | 'last_seen_at' | 'current_status' | 'crash_count' | 'oom_count' | 'name' | 'score';

export interface ContainerQuery {
  page: number;
  limit: number;
  sort?: ContainerSortField;
  order?: 'asc' | 'desc';
  runtime?: 'docker' | 'kubernetes';
  source?: string;
  q?: string;
  status?: ContainerStatusFilter;
}

export interface ResourceSample {
  id: number;
  container_id: string;
  cpu_percent: number;
  mem_bytes: number;
  mem_percent: number;
  /** Cumulative counters (may be absent on older backends). */
  net_rx_bytes?: number;
  net_tx_bytes?: number;
  block_read_bytes?: number;
  block_write_bytes?: number;
  sampled_at: string;
}

export interface StabilityScore {
  id: number;
  container_id: string;
  score: number;
  restart_penalty: number;
  crash_penalty: number;
  oom_penalty: number;
  volatility_penalty: number;
  availability_penalty: number;
  calculated_at: string;
}

export interface EventMetadata {
  reason?: string;
  message?: string;
  [key: string]: unknown;
}

export interface LifecycleEvent {
  id: number;
  container_id: string;
  event_type: string;
  exit_code?: number | null;
  occurred_at: string;
  metadata?: EventMetadata | null;
  /** Present on /events/recent. */
  container_name?: string;
  runtime?: 'docker' | 'kubernetes';
  source_name?: string;
}

export interface DeploymentInfo {
  name: string;
  namespace: string;
  replicas: number;
  ready_replicas: number;
  image: string;
  created_at: string;
}

export interface ActionResult {
  success: boolean;
  message: string;
}

export interface SourceStatus {
  name: string;
  type: 'docker' | 'kubernetes';
  status: 'connected' | 'connecting' | 'error';
  last_error?: string;
  container_count: number;
  running_count: number;
}

export interface CronJobExecution {
  id: number;
  source_name: string;
  namespace: string;
  cronjob_name: string;
  job_name: string;
  pod_name: string;
  started_at?: string;
  completed_at?: string;
  duration_seconds?: number;
  succeeded: boolean;
  exit_code?: number;
  sampled_at: string;
}

export interface CronJobSummary {
  namespace: string;
  cronjob_name: string;
  source_name: string;
  last_execution?: CronJobExecution;
}

export interface PodDetail {
  pod_name: string;
  namespace: string;
  node_name?: string;
  workload_kind?: string;
  workload_name?: string;
  source_name: string;
  containers: Container[];
}

export interface DeploymentPod {
  pod_name: string;
  namespace: string;
  node_name?: string;
  status: string;
  containers: Container[];
}

export interface DeploymentDetail {
  deployment: DeploymentInfo | null;
  pods: DeploymentPod[];
}
