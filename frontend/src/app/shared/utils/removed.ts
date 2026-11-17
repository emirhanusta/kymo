import { RemovedSource } from '../../core/models/container.model';

/** Body text of the "Delete history" confirmation. */
export function purgeMessage(r: RemovedSource): string {
  const n = (count: number, noun: string) => `${count} ${noun}${count === 1 ? '' : 's'}`;
  return `Permanently delete all recorded history of ${r.name} — ${n(r.containers, 'container')}, ` +
    `${n(r.workloads, 'workload')}, ${n(r.cronjob_runs, 'CronJob run')}` +
    `${r.k8s_events ? ` and ${n(r.k8s_events, 'Kubernetes event')}` : ''}, with their lifecycle events, ` +
    'scores and resource samples. This can\'t be undone.';
}
