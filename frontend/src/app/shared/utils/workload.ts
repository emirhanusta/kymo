import { WorkloadSummary } from '../../core/models/container.model';

/** Kinds that get their own chip on the Workloads page; everything else non-job is "Other". */
export const LONG_RUNNING_KINDS = ['Deployment', 'StatefulSet', 'DaemonSet'] as const;

/** CronJobs and Jobs live on the Jobs page, never on Workloads. */
export const isJobKind = (kind: string | null | undefined): boolean => kind === 'CronJob' || kind === 'Job';

/** Deployment, StatefulSet, DaemonSet, ReplicaSet, Pod, … — anything that is not a job. */
export const isLongRunning = (w: Pick<WorkloadSummary, 'kind'>): boolean => !isJobKind(w.kind);

/** What the remainder of a pod name looks like after its owning workload's name, per kind. */
const POD_SUFFIX: Record<string, RegExp> = {
  Deployment:  /^[a-z0-9]+-[a-z0-9]{5}$/,   // <name>-<replicaset hash>-<5 chars>
  ReplicaSet:  /^[a-z0-9]{5}$/,
  StatefulSet: /^\d+$/,                      // <name>-<ordinal>
  DaemonSet:   /^[a-z0-9]{5}$/,
  Job:         /^[a-z0-9]{5}$/,
  CronJob:     /^\d+-[a-z0-9]{5}$/,          // <name>-<scheduled minute>-<5 chars>
};

/**
 * Finds the workload a pod belongs to from names alone (events carry only `namespace/pod/container`).
 * Longest matching workload name wins; the rest of the pod name must look like that kind's pod suffix.
 */
export function workloadResolver(workloads: WorkloadSummary[]): (source: string | undefined, namespace: string, pod: string) => WorkloadSummary | null {
  const byScope = new Map<string, WorkloadSummary[]>();
  for (const w of workloads) {
    const key = `${w.source_name}\u0000${w.namespace ?? ''}`;
    const list = byScope.get(key);
    if (list) list.push(w); else byScope.set(key, [w]);
  }
  for (const list of byScope.values()) list.sort((a, b) => b.name.length - a.name.length);
  return (source, namespace, pod) => {
    if (!pod) return null;
    const list = byScope.get(`${source ?? ''}\u0000${namespace}`);
    if (!list) return null;
    for (const w of list) {
      if (pod === w.name) return w;
      if (!pod.startsWith(w.name + '-')) continue;
      const re = POD_SUFFIX[w.kind];
      if (re && re.test(pod.slice(w.name.length + 1))) return w;
    }
    return null;
  };
}
