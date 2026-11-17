export type ErrorLevel = 'error' | 'fatal';
export type ErrorWindow = '1h' | '6h' | '24h' | '7d';

/** One normalized error message (signature) of a workload. See GET /api/v1/errors. */
export interface ErrorSignature {
  id: string;
  source_name: string;
  workload_id: string;
  namespace: string;
  kind: string;
  name: string;
  level: ErrorLevel;
  /** Normalized first line; placeholders look like `<n>`, `<uuid>`, `<str>`. */
  template: string;
  /** Newest masked first line. */
  sample: string;
  /** Occurrences in the window. */
  count: number;
  total_count: number;
  first_seen: string;
  last_seen: string;
  /** first_seen lies within the window. */
  is_new: boolean;
  /** Distinct pods in the window. */
  pods: number;
  /** 24 equal buckets across the window, oldest first. */
  buckets: number[];
}

export interface ErrorOccurrence {
  time: string;
  container_id: string;
  pod: string;
  container: string;
  level: ErrorLevel;
  /** Full, multi-line, masked message (<= 8KB). */
  message: string;
}

export interface ErrorQuery {
  source?: string;
  namespace?: string;
  workload_id?: string;
  since?: ErrorWindow;
  limit?: number;
}

export type LogCaptureVia = 'workload' | 'namespace' | 'annotation' | 'config';

export interface LogCaptureState {
  enabled: boolean;
  via: LogCaptureVia | null;
  target_id: number | null;
  /** Containers currently streamed for this workload. */
  streams: number;
  global_enabled: boolean;
}

export interface LogCaptureTarget {
  id: number;
  source_name: string;
  namespace: string;
  /** '' = the whole namespace. */
  workload_id: string;
  origin: 'ui' | 'config';
  created_at: string;
}

export interface LogCaptureTargetInput {
  source_name: string;
  namespace: string;
  workload_id?: string;
}

export interface LogCaptureSourceStatus {
  source_name: string;
  streams: number;
  max_streams: number;
  dropped_lines: number;
  last_error: string;
}

export interface LogCaptureStatus {
  enabled: boolean;
  sources: LogCaptureSourceStatus[];
}
