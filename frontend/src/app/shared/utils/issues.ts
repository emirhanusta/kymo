import { K8sEvent, LifecycleEvent, WorkloadSummary } from '../../core/models/container.model';
import { JobRow } from './jobs';
import { podBelongsTo, isJobKind } from './workload';
import { podNameFromId } from './source';
import { firstLine, occurrences } from './k8s-events';

export type IssueSeverity = 'critical' | 'warning';
export type IssueKind =
  | 'oom' | 'crashloop' | 'degraded' | 'job-failed'
  | 'scheduling' | 'image-pull' | 'evicted' | 'volume' | 'probe';

export interface Issue {
  id: string;
  severity: IssueSeverity;
  kind: IssueKind;
  /** Headline, e.g. "OOMKilled ×3 in 24h". */
  reason: string;
  /** Secondary facts, e.g. "1/3 pods ready". */
  detail: string;
  /** Further problems of the same workload that lost out on severity (e.g. "Probe failing ×4"). */
  also: string[];
  /** '' for Kubernetes-event issues whose object belongs to no known workload (shown as `Kind/name`, no link). */
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
  k8sWarnings: K8sEvent[] = [],
  now = Date.now(),
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
        ...base, id: `${w.id}#oom`, also: [], severity: 'critical', kind: 'oom', since,
        reason: `OOMKilled ×${oom} in 24h`,
        detail: crashing ? 'crash looping' : degraded ? ready : '',
      });
    } else if (crashing) {
      // Crashing with lifetime OOMs but none in the 24h window: still reported as an OOM kill (matches statusReason).
      const wasOom = w.oom_count > 0;
      out.push({
        ...base, id: `${w.id}#crash`, also: [], severity: 'critical', kind: wasOom ? 'oom' : 'crashloop', since,
        reason: wasOom ? 'OOMKilled' : ev?.lastExit != null ? `Crash loop · exit ${ev.lastExit}` : 'Crash loop',
        detail: degraded ? ready : '',
      });
    } else if (degraded) {
      out.push({
        ...base, id: `${w.id}#degraded`, also: [], severity: 'warning', kind: 'degraded', since,
        reason: `Degraded (${w.running_instances}/${w.active_instances} ready)`, detail: '',
      });
    }
  }

  for (const j of jobs) {
    if (j.w.kind !== 'CronJob' || j.health !== 'failing' || !j.lastFinished) continue;
    const code = j.lastFinished.exitCode;
    out.push({
      id: `${j.w.id}#job`, severity: 'critical', kind: 'job-failed', also: [],
      reason: code != null ? `Last run failed (exit ${code})` : 'Last run failed',
      detail: j.finished ? `${j.ok}/${j.finished} recent runs succeeded` : '',
      workloadId: j.w.id, name: j.w.name, workloadKind: j.w.kind, namespace: j.w.namespace ?? '',
      source: j.w.source_name, isSystem: j.w.is_system,
      since: j.lastFinished.at ?? j.w.last_seen_at,
    });
  }

  const all = mergeK8sIssues(out, deriveK8sIssues(k8sWarnings, workloads, now));
  const rank = (i: Issue) => (i.severity === 'critical' ? 0 : 1);
  return all.sort((a, b) => rank(a) - rank(b) || new Date(b.since).getTime() - new Date(a.since).getTime());
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


// ── Issues from Kubernetes Warning events ───────────────────────────────────────────────────────────────────────

/** Only Warning events seen this recently count as "open". */
export const K8S_ISSUE_WINDOW_MS = 30 * 60_000;

interface K8sProblem { kind: IssueKind; severity: IssueSeverity; reason: string; since: string; }

/** Display/priority order of the problem kinds (most actionable first). */
const K8S_PRIORITY: IssueKind[] = ['scheduling', 'image-pull', 'evicted', 'volume', 'probe'];

/**
 * Maps one Warning event to a problem kind, or null when it is not one we report.
 *  - FailedScheduling → scheduling; Evicted → evicted; FailedMount / FailedAttachVolume → volume; Unhealthy → probe.
 *  - ErrImagePull / ImagePullBackOff → image-pull. `Failed` and `BackOff` only when the message is about pulling an
 *    image (plain `Failed` also covers unrelated container errors; `BackOff` otherwise is the crash-loop issue).
 */
export function classifyK8sWarning(e: K8sEvent): IssueKind | null {
  const msg = e.message ?? '';
  switch (e.reason) {
    case 'FailedScheduling': return 'scheduling';
    case 'ErrImagePull':
    case 'ImagePullBackOff': return 'image-pull';
    case 'Failed': return /pull|image/i.test(msg) ? 'image-pull' : null;
    case 'BackOff': return /pull/i.test(msg) ? 'image-pull' : null;
    case 'Evicted': return 'evicted';
    case 'FailedMount':
    case 'FailedAttachVolume': return 'volume';
    case 'Unhealthy': return 'probe';
    default: return null;
  }
}

function k8sProblemText(kind: IssueKind, evs: K8sEvent[]): { severity: IssueSeverity; reason: string } {
  const n = evs.reduce((sum, e) => sum + occurrences(e), 0);
  switch (kind) {
    case 'scheduling': {
      const newest = evs.reduce((a, e) => (e.last_seen > a.last_seen ? e : a));
      const msg = firstLine(newest.message);
      return { severity: 'critical', reason: msg ? `Can't schedule — ${msg}` : "Can't schedule" };
    }
    case 'image-pull': return { severity: 'critical', reason: 'Image pull failing' };
    case 'evicted':    return { severity: 'critical', reason: `Evicted ×${n}` };
    case 'volume':     return { severity: 'critical', reason: 'Volume mount failing' };
    default:           return { severity: 'warning',  reason: `Probe failing ×${n}` };
  }
}

/**
 * One issue per workload (or per `Kind/name` object when the event has no known workload) from Warning events whose
 * `last_seen` is inside the window. Within a group the worst severity is the headline (ties: priority order above);
 * the other problems become `also`.
 */
export function deriveK8sIssues(events: K8sEvent[], workloads: WorkloadSummary[], now = Date.now()): Issue[] {
  const byId = new Map(workloads.map(w => [w.id, w]));
  const groups = new Map<string, { w: WorkloadSummary | null; ev: K8sEvent; byKind: Map<IssueKind, K8sEvent[]> }>();

  for (const e of events) {
    if (e.type !== 'Warning' || now - new Date(e.last_seen).getTime() > K8S_ISSUE_WINDOW_MS) continue;
    const kind = classifyK8sWarning(e);
    if (!kind) continue;
    const w = e.workload_id ? byId.get(e.workload_id) ?? null : null;
    const key = w ? w.id : `${e.source_name}\u0000${e.namespace}\u0000${e.kind}/${e.name}`;
    const g = groups.get(key) ?? groups.set(key, { w, ev: e, byKind: new Map() }).get(key)!;
    (g.byKind.get(kind) ?? g.byKind.set(kind, []).get(kind)!).push(e);
  }

  const out: Issue[] = [];
  for (const [key, g] of groups) {
    const problems: K8sProblem[] = K8S_PRIORITY.filter(k => g.byKind.has(k)).map(kind => {
      const evs = g.byKind.get(kind)!;
      const since = evs.reduce((a, e) => (e.first_seen < a ? e.first_seen : a), evs[0].first_seen);
      return { kind, since, ...k8sProblemText(kind, evs) };
    });
    // Stable: equal severities keep priority order.
    problems.sort((a, b) => (a.severity === b.severity ? 0 : a.severity === 'critical' ? -1 : 1));
    const [main, ...rest] = problems;
    const w = g.w;
    out.push({
      id: w ? `${w.id}#k8s` : `k8s:${key}`,
      severity: main.severity, kind: main.kind, reason: main.reason, detail: '', also: rest.map(p => p.reason),
      workloadId: w ? w.id : '',
      name: w ? w.name : g.ev.kind ? `${g.ev.kind}/${g.ev.name}` : g.ev.name,
      workloadKind: w ? w.kind : g.ev.kind,
      namespace: w ? w.namespace ?? '' : g.ev.namespace,
      source: w ? w.source_name : g.ev.source_name,
      isSystem: w ? w.is_system : false,
      since: main.since,
    });
  }
  return out;
}

/** Folds Kubernetes-derived issues into the status-derived ones: one issue per workload, worst severity wins. */
export function mergeK8sIssues(base: Issue[], k8s: Issue[]): Issue[] {
  const byWorkload = new Map(base.map(i => [i.workloadId, i]));
  const out = [...base];
  for (const k of k8s) {
    const cur = k.workloadId ? byWorkload.get(k.workloadId) : undefined;
    if (!cur) { out.push(k); continue; }
    const idx = out.indexOf(cur);
    // Critical k8s problems outrank a warning (e.g. degraded); otherwise the existing headline stays.
    out[idx] = k.severity === 'critical' && cur.severity === 'warning'
      ? { ...k, id: cur.id, also: [cur.reason, ...k.also], detail: cur.detail }
      : { ...cur, also: [...cur.also, k.reason, ...k.also] };
  }
  return out;
}
