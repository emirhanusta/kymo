import { WorkloadSummary } from '../../core/models/container.model';

/** Kinds that get their own chip on the Workloads page; everything else non-job is "Other". */
export const LONG_RUNNING_KINDS = ['Deployment', 'StatefulSet', 'DaemonSet'] as const;

/** CronJobs and Jobs live on the Jobs page, never on Workloads. */
export const isJobKind = (kind: string | null | undefined): boolean => kind === 'CronJob' || kind === 'Job';

/** Deployment, StatefulSet, DaemonSet, ReplicaSet, Pod, … — anything that is not a job. */
export const isLongRunning = (w: Pick<WorkloadSummary, 'kind'>): boolean => !isJobKind(w.kind);

/**
 * Does a Kubernetes pod name belong to a workload of this kind/name?
 *   Deployment  `<name>-<rs-hash>-<rand>`   StatefulSet `<name>-<ordinal>`
 *   DaemonSet / ReplicaSet `<name>-<rand>`  Job `<name>-<rand>`  CronJob `<name>-<minutes>-<rand>`
 * Used to attribute recent events to a workload without extra requests.
 */
export function podBelongsTo(pod: string, kind: string, name: string): boolean {
  if (!pod.startsWith(name + '-')) return false;
  const rest = pod.slice(name.length + 1);
  const segs = rest.split('-');
  switch (kind) {
    case 'Deployment':
    case 'CronJob':    return segs.length === 2 && segs.every(Boolean);
    case 'StatefulSet': return segs.length === 1 && /^\d+$/.test(segs[0]);
    case 'DaemonSet':
    case 'ReplicaSet':
    case 'Job':        return segs.length === 1 && !!segs[0];
    case 'Pod':        return false;
    default:           return false;
  }
}
