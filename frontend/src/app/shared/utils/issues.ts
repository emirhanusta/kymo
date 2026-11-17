import { WorkloadSummary } from '../../core/models/container.model';
import { Issue, IssueSeverity, IssueType } from '../../core/models/issue.model';

/** Issue types in display order, with human labels. */
export const ISSUE_TYPES: { type: IssueType; label: string }[] = [
  { type: 'crash_loop',    label: 'Crash loop' },
  { type: 'oom',           label: 'OOMKilled' },
  { type: 'degraded',      label: 'Degraded' },
  { type: 'job_failed',    label: 'Job run failed' },
  { type: 'cant_schedule', label: "Can't schedule" },
  { type: 'image_pull',    label: 'Image pull failing' },
  { type: 'evicted',       label: 'Evicted' },
  { type: 'volume_mount',  label: 'Volume mount failing' },
  { type: 'probe_failing', label: 'Probe failing' },
];

export function issueTypeLabel(type: IssueType | string): string {
  return ISSUE_TYPES.find(t => t.type === type)?.label ?? type;
}

export function severityLedClass(s: IssueSeverity): string {
  return s === 'critical' ? 'bg-kymo-danger' : 'bg-kymo-amber';
}

export function severityTextClass(s: IssueSeverity): string {
  return s === 'critical' ? 'text-kymo-danger' : 'text-kymo-amber';
}

/** Route of the issue's workload page. */
export function issueWorkloadRoute(i: Pick<Issue, 'workload_id'>): string[] {
  return ['/workloads', i.workload_id];
}

/** "OOMKilled" / "Crashing" / plain status, for the status badge of a workload. */
export function statusReason(w: WorkloadSummary): string {
  if (w.status === 'crashing') return w.oom_count > 0 ? 'OOMKilled' : 'Crashing';
  return w.status ? w.status.charAt(0).toUpperCase() + w.status.slice(1) : 'Unknown';
}
