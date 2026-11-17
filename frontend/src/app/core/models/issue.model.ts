export type IssueType =
  | 'crash_loop' | 'oom' | 'degraded' | 'job_failed' | 'cant_schedule'
  | 'image_pull' | 'evicted' | 'volume_mount' | 'probe_failing';
export type IssueSeverity = 'critical' | 'warning';
export type IssueStatus = 'open' | 'resolved';

/** A server-detected problem (see GET /api/v1/issues). */
export interface Issue {
  id: number;
  source_name: string;
  workload_id: string;
  namespace: string;
  kind: string;
  name: string;
  type: IssueType;
  severity: IssueSeverity;
  title: string;
  detail: string;
  status: IssueStatus;
  first_seen: string;
  last_seen: string;
  resolved_at: string | null;
  /** How often the issue resolved and re-opened. */
  flaps: number;
}

export interface IssueSummary {
  open: number;
  critical: number;
  warning: number;
  by_source: Record<string, number>;
}

export interface IssueQuery {
  status?: IssueStatus | 'all';
  source?: string;
  severity?: IssueSeverity;
  type?: IssueType;
  limit?: number;
  /** Cursor: only issues first seen before this ISO timestamp. */
  before?: string;
}

export type ChannelType = 'slack' | 'discord' | 'webhook';
export type DeliveryEvent = 'opened' | 'resolved' | 'reminder' | 'test';

export interface ChannelDelivery {
  sent_at: string;
  ok: boolean;
  error: string;
  event: DeliveryEvent;
}

export interface NotificationChannel {
  id: number;
  name: string;
  type: ChannelType;
  origin: 'user' | 'config';
  enabled: boolean;
  min_severity: IssueSeverity;
  sources: string[];
  namespaces: string[];
  types: IssueType[];
  notify_resolved: boolean;
  include_logs: boolean;
  repeat_minutes: number;
  url_hint: string;
  has_secret: boolean;
  created_at: string;
  updated_at: string;
  last_delivery: ChannelDelivery | null;
}

/** Body of POST / PUT /notifications/channels. `url` / `secret`: empty on PUT = keep the stored value. */
export interface ChannelInput {
  name: string;
  type: ChannelType;
  enabled: boolean;
  min_severity: IssueSeverity;
  sources: string[];
  namespaces: string[];
  types: IssueType[];
  notify_resolved: boolean;
  include_logs: boolean;
  repeat_minutes: number;
  url?: string;
  secret?: string;
}

export interface ChannelTestResult {
  ok: boolean;
  status_code: number;
  error: string;
}

export interface NotificationDelivery {
  id: number;
  channel_id: number;
  issue_id: number | null;
  event: DeliveryEvent;
  ok: boolean;
  status_code: number;
  error: string;
  attempts: number;
  sent_at: string;
}
