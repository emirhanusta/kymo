import { LifecycleEvent, Revision } from '../../core/models/container.model';
import { isProblemEvent } from './events';
import { shortImage } from './format';

export type TimelineFilter = 'all' | 'problems' | 'rollouts';

/** A revision that replaced an earlier one. */
export interface Rollout {
  revision: Revision;
  /** The revision it replaced. */
  prev: Revision;
  /** "a → b" for the images that differ; '' when they are the same or unknown. */
  imageChange: string;
}

export interface EventRow {
  kind: 'event';
  key: string;
  /** ISO time of the newest event in the row. */
  at: string;
  ev: LifecycleEvent;
  /** >1 when identical consecutive start events were collapsed. */
  count: number;
  /** ISO time of the oldest collapsed event. */
  firstAt: string;
}
export interface RolloutRow { kind: 'rollout'; key: string; at: string; rollout: Rollout; }
export type TimelineRow = EventRow | RolloutRow;

const t = (s: string) => new Date(s).getTime();

/** "5d8f7b9c4" → "5d8f7b9"; '' → "unknown". */
export function shortRev(rev: string): string {
  return rev ? rev.slice(0, 7) : 'unknown';
}

/** Images of `cur` that `prev` did not have, as "old → new". */
export function imageChange(prev: string[], cur: string[]): string {
  const removed = prev.filter(i => !cur.includes(i));
  const added = cur.filter(i => !prev.includes(i));
  if (!removed.length || !added.length) return '';
  return `${removed.map(shortImage).join(', ')} → ${added.map(shortImage).join(', ')}`;
}

/** Every known revision except the oldest (that one is just where recording started), newest first. */
export function rolloutsOf(revisions: Revision[]): Rollout[] {
  const known = revisions.filter(r => r.revision).sort((a, b) => t(a.first_seen_at) - t(b.first_seen_at));
  const out: Rollout[] = [];
  for (let i = 1; i < known.length; i++) {
    out.push({ revision: known[i], prev: known[i - 1], imageChange: imageChange(known[i - 1].images ?? [], known[i].images ?? []) });
  }
  return out.reverse();
}

/** The newest rollout that happened before `atMs`, or null (no rollout / revision unknown). */
export function rolloutBefore(revisions: Revision[], atMs: number): Rollout | null {
  return rolloutsOf(revisions).find(r => t(r.revision.first_seen_at) <= atMs) ?? null;
}

/**
 * Merges events (newest first) with rollout markers into one newest-first list. Consecutive identical start
 * events of one container collapse into a single row. While older pages remain unloaded, markers older than the
 * oldest loaded event are held back so they do not float above events that are not loaded yet.
 */
export function buildTimeline(
  events: LifecycleEvent[], revisions: Revision[], filter: TimelineFilter, hasMore: boolean,
): TimelineRow[] {
  const sorted = [...events].sort((a, b) => t(b.occurred_at) - t(a.occurred_at) || b.id - a.id);
  const oldestLoaded = sorted.length ? t(sorted[sorted.length - 1].occurred_at) : Infinity;

  const rows: TimelineRow[] = [];
  if (filter !== 'rollouts') {
    for (const ev of sorted) {
      if (filter === 'problems' && !isProblemEvent(ev)) continue;
      const prev = rows[rows.length - 1];
      if (prev && prev.kind === 'event' && ev.event_type === 'start' && prev.ev.event_type === 'start'
          && prev.ev.container_id === ev.container_id) {
        prev.count++;
        prev.firstAt = ev.occurred_at;
        continue;
      }
      rows.push({ kind: 'event', key: `e${ev.id}`, at: ev.occurred_at, ev, count: 1, firstAt: ev.occurred_at });
    }
  }
  const markers: RolloutRow[] = rolloutsOf(revisions)
    .filter(r => filter === 'rollouts' || !hasMore || t(r.revision.first_seen_at) >= oldestLoaded)
    .map(r => ({ kind: 'rollout' as const, key: `r${r.revision.revision}`, at: r.revision.first_seen_at, rollout: r }));
  if (filter === 'rollouts') return markers;

  // Merge: both lists are newest first.
  const out: TimelineRow[] = [];
  let i = 0, j = 0;
  while (i < rows.length || j < markers.length) {
    if (j >= markers.length || (i < rows.length && t(rows[i].at) >= t(markers[j].at))) out.push(rows[i++]);
    else out.push(markers[j++]);
  }
  return filter === 'problems' ? rows : out;
}
