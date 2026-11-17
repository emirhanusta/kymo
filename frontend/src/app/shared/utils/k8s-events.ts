import { K8sEvent } from '../../core/models/container.model';

export const isWarning = (e: K8sEvent): boolean => e.type === 'Warning';

/** "Pod/api-7d9f" (Kind/name). */
export const k8sObject = (e: K8sEvent): string => (e.kind ? `${e.kind}/${e.name}` : e.name);

/** Total occurrences (aggregated rows carry `count`). */
export const occurrences = (e: K8sEvent): number => Math.max(1, e.count || 1);

/** The first and last sighting differ by more than a second. */
export const spansTime = (e: K8sEvent): boolean =>
  Math.abs(new Date(e.last_seen).getTime() - new Date(e.first_seen).getTime()) > 1000;

/** Newest first, ties broken by id. */
export const byLastSeen = (a: K8sEvent, b: K8sEvent): number =>
  new Date(b.last_seen).getTime() - new Date(a.last_seen).getTime() || b.id - a.id;

/** Warning events seen at or after `sinceMs`. */
export const recentWarnings = (list: K8sEvent[], sinceMs: number): K8sEvent[] =>
  list.filter(e => isWarning(e) && new Date(e.last_seen).getTime() >= sinceMs);

export const TYPE_BADGE = {
  Warning: 'border border-kymo-amber/50 text-kymo-amber',
  Normal: 'border border-kymo-rule text-kymo-muted',
} as const;

const HOUR = 3600_000;

/** Warnings (counting repeats) seen in the last hour; 0 when there are none. */
export function warningsLastHour(list: K8sEvent[], now = Date.now()): number {
  return recentWarnings(list, now - HOUR).reduce((n, e) => n + occurrences(e), 0);
}

/** RFC3339 timestamp one hour ago, for `since=`. */
export const hourAgoIso = (now = Date.now()): string => new Date(now - HOUR).toISOString();
