/** One hour of lifecycle events (GET /overview/activity). */
export interface ActivityBucket {
  start: string;
  starts: number;
  crashes: number;
  ooms: number;
}

export interface Activity {
  /** Consecutive hours, oldest first. */
  buckets: ActivityBucket[];
  /** Crash + OOM events in the window just before the first bucket. */
  previous_crashes: number;
}

/** A rollout of a new pod-template revision (GET /overview/changes). */
export interface OverviewChange {
  workload_id: string;
  kind: string;
  name: string;
  namespace: string;
  source_name: string;
  revision: string;
  previous_revision: string;
  image: string;
  previous_image: string;
  at: string;
}
