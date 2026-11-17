import { WorkloadSummary } from '../../core/models/container.model';

/** Kinds that get their own chip on the Workloads page; everything else non-job is "Other". */
export const LONG_RUNNING_KINDS = ['Deployment', 'StatefulSet', 'DaemonSet'] as const;

/** CronJobs and Jobs live on the Jobs page, never on Workloads. */
export const isJobKind = (kind: string | null | undefined): boolean => kind === 'CronJob' || kind === 'Job';

/** Deployment, StatefulSet, DaemonSet, ReplicaSet, Pod, … — anything that is not a job. */
export const isLongRunning = (w: Pick<WorkloadSummary, 'kind'>): boolean => !isJobKind(w.kind);
