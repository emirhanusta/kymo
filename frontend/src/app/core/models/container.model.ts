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
  /** `source:namespace:kind:name` of the aggregating workload (when known). */
  workload_id?: string;
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
  /** Pod revision (Deployment pod-template hash etc.); "" / absent = unknown. */
  revision?: string;
}

export type ContainerStatusFilter = '' | 'running' | 'stopped' | 'crashing' | 'paused' | 'unknown';

export type ContainerSortField =
  | 'last_seen_at' | 'current_status' | 'crash_count' | 'oom_count' | 'name' | 'score';

export interface ContainerQuery {
  page: number;
  limit: number;
  sort?: ContainerSortField;
  order?: 'asc' | 'desc';
  source?: string;
  q?: string;
  status?: ContainerStatusFilter;
  /** Exact match on workload_kind (e.g. 'Deployment'). */
  workload_kind?: string;
  /** Exact match on workload_name. */
  workload?: string;
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
  /** A crash-log snapshot exists for this termination (crash and OOM events of one termination share it). */
  has_logs?: boolean;
}

/** Log tail captured when a container terminated abnormally. Static text, never streamed. */
export interface CrashLog {
  id: number;
  container_id: string;
  source_name: string;
  occurred_at: string;
  reason: string;
  exit_code: number;
  /** Lines come from the previous (terminated) container instance. */
  previous: boolean;
  lines: string;
  line_count: number;
  truncated: boolean;
  captured_at: string;
}

/** One rollout generation of a workload (e.g. a ReplicaSet), newest first from the API. */
export interface Revision {
  /** "" = unknown. */
  revision: string;
  first_seen_at: string;
  last_seen_at: string;
  images: string[];
  pods: number;
  active_pods: number;
  current: boolean;
}

export interface DeploymentInfo {
  name: string;
  /** Not sent by every backend version; the UI falls back to the selected / default source. */
  source_name?: string;
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

export type SourceType = 'docker' | 'kubernetes';

export interface SourceSettings {
  kubeconfig_path?: string;
  kube_context?: string;
  kubeconfig_uploaded: boolean;
  poll_interval_seconds?: number;
}

export interface SourceStatus {
  name: string;
  type: SourceType;
  /** `config` = defined in config.yaml (read-only in the UI); `user` = created through the UI/API. */
  origin: 'config' | 'user';
  enabled: boolean;
  /** Management actions (restart/scale/…) are refused for read-only clusters. */
  read_only: boolean;
  status: 'connecting' | 'connected' | 'error' | 'disabled';
  last_error?: string;
  connected_at?: string;
  container_count: number;
  running_count: number;
  settings: SourceSettings;
}

/** A cluster with recorded history that is not registered any more (GET /sources/removed). */
export interface RemovedSource {
  name: string;
  workloads: number;
  containers: number;
  cronjob_runs: number;
  /** Recorded Kubernetes events (absent on older backends). */
  k8s_events?: number;
  first_seen_at: string;
  last_seen_at: string;
}

/** Result of DELETE /sources/removed/:name. */
export interface PurgeRemovedResult extends ActionResult {
  deleted_containers: number;
}

/** Body for POST /sources, PUT /sources/:name and POST /sources/test. */
export interface SourceInput {
  name: string;
  type: SourceType;
  enabled?: boolean;
  read_only: boolean;
  kubeconfig_path?: string;
  /** Raw kubeconfig YAML. Omit on update to keep the stored one. */
  kubeconfig?: string;
  kube_context?: string;
  poll_interval_seconds?: number;
}

export interface SourceCheck {
  name: string;
  ok: boolean;
  warning?: boolean;
  detail?: string;
}

export interface SourceTestResult {
  ok: boolean;
  checks: SourceCheck[];
}

export interface KubeContext {
  name: string;
  cluster: string;
  server: string;
  namespace?: string;
}

export interface KubeContextList {
  contexts: KubeContext[];
  current: string;
}

export interface ServerMeta {
  version: string;
  secret_key_configured: boolean;
  /** Path of the config file in use, or "" when none. */
  config_file: string;
  /** Last automatic/manual config.yaml sync (null before the first one). */
  config_sync: ConfigSync | null;
}

export interface SyncResult {
  added: string[];
  removed: string[];
  updated: string[];
  unchanged: string[];
  conflicts: string[];
  reloaded_at: string;
}

export interface ConfigSync {
  reloaded_at: string;
  result: SyncResult | null;
  error?: string;
}

/** Lifecycle event types accepted by GET /events/recent?type=. */
export type LifecycleEventType = 'start' | 'stop' | 'die' | 'crash' | 'oom' | 'pause' | 'resume';

export interface RecentEventsQuery {
  limit?: number;
  source?: string;
  type?: LifecycleEventType | '';
  /** RFC3339; only events strictly older than this are returned. */
  before?: string;
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

// --- Workloads (aggregated over every recorded instance) ---

export type WorkloadStatus = 'running' | 'crashing' | 'stopped' | 'unknown';

export interface WorkloadSummary {
  id: string;
  source_name: string;
  runtime: 'docker' | 'kubernetes';
  namespace?: string;
  kind: string;
  name: string;
  is_system: boolean;
  containers: string[];
  images: string[];
  instances: number;
  active_instances: number;
  running_instances: number;
  status: WorkloadStatus;
  start_count: number;
  /** Lifetime restarts: starts minus each container's first boot. */
  restart_count: number;
  crash_count: number;
  oom_count: number;
  first_seen_at: string;
  last_seen_at: string;
  score: number | null;
}

export interface WorkloadScore {
  id: number;
  workload_id: string;
  source_name: string;
  score: number;
  restart_penalty: number;
  crash_penalty: number;
  oom_penalty: number;
  volatility_penalty: number;
  availability_penalty: number;
  /** Counts inside the scoring window (24h). */
  restarts: number;
  crashes: number;
  ooms: number;
  calculated_at: string;
}

export interface WorkloadDetail {
  workload: WorkloadSummary;
  score: WorkloadScore | null;
  instances: Container[];
}

export interface WorkloadQuery {
  source?: string;
  namespace?: string;
  kind?: string;
  q?: string;
  status?: string;
  system?: boolean;
}

export interface WorkloadEventsQuery {
  limit?: number;
  before?: string;
  type?: LifecycleEventType | '';
}

/** A persisted Kubernetes Event (repeats are aggregated into one row with `count`). */
export interface K8sEvent {
  id: number;
  source_name: string;
  uid: string;
  namespace: string;
  kind: string;
  name: string;
  /** Owning workload; "" when unknown. */
  workload_id: string;
  type: 'Normal' | 'Warning';
  reason: string;
  message: string;
  count: number;
  first_seen: string;
  last_seen: string;
  component: string;
}

export type K8sEventType = 'Normal' | 'Warning';

/** GET /k8s-events. Newest `last_seen` first; `before` is a `last_seen` cursor. */
export interface K8sEventsQuery {
  source?: string;
  namespace?: string;
  type?: K8sEventType | '';
  reason?: string;
  workload_id?: string;
  q?: string;
  /** RFC3339; only events seen at or after this. */
  since?: string;
  before?: string;
  /** Default 100, max 500. */
  limit?: number;
}

/** GET /workloads/:id/k8s-events. */
export interface WorkloadK8sEventsQuery {
  since?: string;
  before?: string;
  limit?: number;
}

/** GET /k8s-events/reasons. */
export interface K8sReasonCount {
  reason: string;
  type: K8sEventType;
  count: number;
}
