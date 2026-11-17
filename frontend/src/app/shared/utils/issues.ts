import { LifecycleEvent, WorkloadSummary } from '../../core/models/container.model';
import { JobRow } from './jobs';
import { podBelongsTo, isJobKind } from './workload';
import { podNameFromId } from './source';

export type IssueSeverity = 'critical' | 'warning';
export type IssueKind = 'oom' | 'crashloop' | 'degraded' | 'job-failed';

export interface Issue {
  id: string;
  severity: IssueSeverity;
  kind: IssueKind;
  /** Headline, e.g. "OOMKilled ×3 in 24h". */
  reason: string;
  /** Secondary facts, e.g. "1/3 pods ready". */
  detail: string;
  workloadId: string;
  name: string;
  workloadKind: string;
  namespace: string;
  source: string;
  isSystem: boolean;
  /** ISO timestamp the problem started (first relevant event, else last seen). */
  since: string;
}

/** Recent-event facts per workload. */
export interface WorkloadEventInfo {
  oom24: number;
  crash24: number;
  /** Exit code of the newest crash event that carried one. */
  lastExit: number | null;
  /** Earliest relevant event inside the 24h window (ISO). */
  firstAt: string | null;
}

const DAY = 24 * 3600_000;

/**
 * Attributes recent events to workloads.
 *  1. Pod-name shape: `<ns>/<pod>/<container>` + workload (same cluster & namespace) whose name/kind produce that pod name
 *     (longest name wins, so `api-gateway` beats `api`).
 *  2. Otherwise the exact `workload_id` of the container, looked up via GET /containers/:id (`containerLookup`).
 */
export function attributeEvents(
  events: LifecycleEvent[],
  workloads: WorkloadSummary[],
  containerLookup: ReadonlyMap<string, string | null>,
  now = Date.now(),
): Map<string, WorkloadEventInfo> {
  const byScope = new Map<string, WorkloadSummary[]>();
  const byId = new Set<string>();
  for (const w of workloads) {
    byId.add(w.id);
    const key = `${w.source_name}\u0000${w.namespace ?? ''}`;
    (byScope.get(key) ?? byScope.set(key, []).get(key)!).push(w);
  }

  const out = new Map<string, WorkloadEventInfo>();
  const newestExit = new Map<string, number>();
  for (const ev of events) {
    const at = new Date(ev.occurred_at).getTime();
    if (!at || now - at > DAY) continue;
    const type = ev.event_type.toLowerCase();
    const isOom = type === 'oom' || type === 'oomkilled';
    const isCrash = type === 'crash' || type === 'crashed';
    if (!isOom && !isCrash) continue;

    const wid = matchWorkload(ev, byScope, containerLookup, byId);
    if (!wid) continue;
    const info = out.get(wid) ?? out.set(wid, { oom24: 0, crash24: 0, lastExit: null, firstAt: null }).get(wid)!;
    if (isOom) info.oom24++; else info.crash24++;
    if (!info.firstAt || at < new Date(info.firstAt).getTime()) info.firstAt = ev.occurred_at;
    if (isCrash && ev.exit_code != null && at >= (newestExit.get(wid) ?? 0)) {
      newestExit.set(wid, at);
      info.lastExit = ev.exit_code;
    }
  }
  return out;
}

function matchWorkload(
  ev: LifecycleEvent,
  byScope: Map<string, WorkloadSummary[]>,
  lookup: ReadonlyMap<string, string | null>,
  known: Set<string>,
): string | null {
  const parts = ev.container_id.split('/');
  const pod = podNameFromId(ev.container_id);
  if (pod && ev.source_name) {
    const cands = byScope.get(`${ev.source_name}\u0000${parts[0]}`) ?? [];
    let best: WorkloadSummary | null = null;
    for (const w of cands) {
      if (podBelongsTo(pod, w.kind, w.name) && (!best || w.name.length > best.name.length)) best = w;
    }
    if (best) return best.id;
  }
  const id = lookup.get(ev.container_id);
  return id && known.has(id) ? id : null;
}

/** Events that pod-name matching cannot place (the caller resolves them through the container API). */
export function unmatchedContainerIds(
  events: LifecycleEvent[], workloads: WorkloadSummary[], lookup: ReadonlyMap<string, string | null>,
): string[] {
  const attributable = attributeEventsRaw(events, workloads);
  return [...new Set(events.filter(e => !attributable.has(e)).map(e => e.container_id))].filter(id => !lookup.has(id));
}

function attributeEventsRaw(events: LifecycleEvent[], workloads: WorkloadSummary[]): Set<LifecycleEvent> {
  const ok = new Set<LifecycleEvent>();
  for (const ev of events) {
    const pod = podNameFromId(ev.container_id);
    if (!pod || !ev.source_name) continue;
    const ns = ev.container_id.split('/')[0];
    if (workloads.some(w => w.source_name === ev.source_name && (w.namespace ?? '') === ns && podBelongsTo(pod, w.kind, w.name))) ok.add(ev);
  }
  return ok;
}

export function deriveIssues(
  workloads: WorkloadSummary[],
  jobs: JobRow[],
  info: ReadonlyMap<string, WorkloadEventInfo>,
): Issue[] {
  const out: Issue[] = [];

  for (const w of workloads) {
    if (isJobKind(w.kind)) continue;
    const ev = info.get(w.id);
    const oom = ev?.oom24 ?? 0;
    const crashing = w.status === 'crashing';
    const degraded = w.active_instances > 0 && w.running_instances < w.active_instances;
    const ready = `${w.running_instances}/${w.active_instances} pods ready`;
    const base = {
      workloadId: w.id, name: w.name, workloadKind: w.kind, namespace: w.namespace ?? '',
      source: w.source_name, isSystem: w.is_system,
    };
    const since = ev?.firstAt ?? w.last_seen_at;

    if (oom > 0) {
      out.push({
        ...base, id: `${w.id}#oom`, severity: 'critical', kind: 'oom', since,
        reason: `OOMKilled ×${oom} in 24h`,
        detail: crashing ? 'crash looping' : degraded ? ready : '',
      });
    } else if (crashing) {
      // Crashing with lifetime OOMs but none in the 24h window: still reported as an OOM kill (matches statusReason).
      const wasOom = w.oom_count > 0;
      out.push({
        ...base, id: `${w.id}#crash`, severity: 'critical', kind: wasOom ? 'oom' : 'crashloop', since,
        reason: wasOom ? 'OOMKilled' : ev?.lastExit != null ? `Crash loop · exit ${ev.lastExit}` : 'Crash loop',
        detail: degraded ? ready : '',
      });
    } else if (degraded) {
      out.push({
        ...base, id: `${w.id}#degraded`, severity: 'warning', kind: 'degraded', since,
        reason: `Degraded (${w.running_instances}/${w.active_instances} ready)`, detail: '',
      });
    }
  }

  for (const j of jobs) {
    if (j.w.kind !== 'CronJob' || j.health !== 'failing' || !j.lastFinished) continue;
    const code = j.lastFinished.exitCode;
    out.push({
      id: `${j.w.id}#job`, severity: 'critical', kind: 'job-failed',
      reason: code != null ? `Last run failed (exit ${code})` : 'Last run failed',
      detail: j.finished ? `${j.ok}/${j.finished} recent runs succeeded` : '',
      workloadId: j.w.id, name: j.w.name, workloadKind: j.w.kind, namespace: j.w.namespace ?? '',
      source: j.w.source_name, isSystem: j.w.is_system,
      since: j.lastFinished.at ?? j.w.last_seen_at,
    });
  }

  const rank = (i: Issue) => (i.severity === 'critical' ? 0 : 1);
  return out.sort((a, b) => rank(a) - rank(b) || new Date(b.since).getTime() - new Date(a.since).getTime());
}

/** Most specific status text for a workload: OOMKilled › CrashLoop · exit N › Crashing › plain status. */
export function statusReason(w: WorkloadSummary, info?: WorkloadEventInfo | null): string {
  if (w.status === 'crashing') {
    if ((info?.oom24 ?? 0) > 0 || w.oom_count > 0) return 'OOMKilled';
    if (info?.lastExit != null) return `CrashLoop · exit ${info.lastExit}`;
    return 'Crashing';
  }
  return w.status ? w.status.charAt(0).toUpperCase() + w.status.slice(1) : 'Unknown';
}
