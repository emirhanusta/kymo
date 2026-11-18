import { Issue } from '../../core/models/issue.model';
import { ActivityBucket, OverviewChange } from '../../core/models/overview.model';

/** Critical first, then most recently seen. */
export function sortIssues(list: Issue[]): Issue[] {
  const rank = (i: Issue) => (i.severity === 'critical' ? 0 : 1);
  return [...list].sort((a, b) => rank(a) - rank(b) || b.last_seen.localeCompare(a.last_seen));
}

/** "2 critical · 1 warning" (zero parts are left out). */
export function severityBreakdown(critical: number, warning: number): string {
  return [critical ? `${critical} critical` : '', warning ? `${warning} warning` : ''].filter(Boolean).join(' · ');
}

/** Tag (or short digest) of an image reference: "repo/app:1.2" → "1.2", "app@sha256:abcdef…" → "abcdef1". */
export function imageTag(image: string): string {
  if (!image) return '';
  const at = image.indexOf('@sha256:');
  if (at >= 0) return image.slice(at + 8, at + 15);
  const last = image.slice(image.lastIndexOf('/') + 1);
  const colon = last.lastIndexOf(':');
  return colon >= 0 ? last.slice(colon + 1) : 'latest';
}

/** "1.1 → 1.2" for an image bump, else the revision hashes. */
export function changeLabel(c: Pick<OverviewChange, 'image' | 'previous_image' | 'revision' | 'previous_revision'>): { from: string; to: string; kind: 'image' | 'revision' } {
  const from = imageTag(c.previous_image), to = imageTag(c.image);
  if (from && to && c.previous_image !== c.image) return { from, to, kind: 'image' };
  const short = (r: string) => r.slice(0, 8);
  return { from: short(c.previous_revision), to: short(c.revision), kind: 'revision' };
}

/** Crashes + OOM kills of a bucket. */
export const bucketCrashes = (b: ActivityBucket): number => b.crashes + b.ooms;

/** "+3", "−2" or "no change" against the previous window. */
export function deltaLabel(now: number, previous: number): string {
  const d = now - previous;
  if (d === 0) return 'same as previous 24h';
  return `${d > 0 ? '+' : '−'}${Math.abs(d)} vs previous 24h`;
}

/** Upper bound for the chart's y axis: 1, 2, 5 × 10^n above `max`. */
export function niceMax(max: number): number {
  if (max <= 4) return Math.max(max, 1);
  const pow = Math.pow(10, Math.floor(Math.log10(max)));
  for (const m of [1, 2, 5, 10]) if (m * pow >= max) return m * pow;
  return max;
}
