import { SourceStatus } from '../../core/models/container.model';

export function sourceDotClass(src: SourceStatus): string {
  if (src.enabled === false && src.status !== 'connecting') return 'bg-kymo-unknown';
  switch (src.status) {
    case 'connected':  return 'bg-kymo-running';
    case 'connecting': return 'bg-kymo-amber animate-pulse';
    case 'error':      return 'bg-kymo-danger';
    case 'disabled':   return 'bg-kymo-unknown';
    default:           return 'bg-kymo-unknown';
  }
}

export function sourceStatusLabel(src: SourceStatus): string {
  switch (src.status) {
    case 'connected':  return 'Connected';
    case 'connecting': return 'Connecting…';
    case 'error':      return src.last_error ? `Error: ${src.last_error}` : 'Error';
    case 'disabled':   return 'Disabled';
    default:           return src.enabled === false ? 'Disabled' : 'Unknown';
  }
}

export const READ_ONLY_HINT = 'Read-only cluster: management actions (restart pod, scale, rollout restart) are disabled.';
export const REMOVED_HINT = 'Removed cluster: recorded history only. Management actions and live logs are unavailable.';
export const CONFIG_ORIGIN_HINT = 'Managed in config.yaml — edit the file; changes apply automatically';

/** Concise connection label for lists: "kubeconfig · ctx", "uploaded · ctx" or "in-cluster". Never a file path. */
export function sourceConnectionSummary(src: SourceStatus): string {
  const s = src.settings;
  if (!s) return '—';
  const kind = s.kubeconfig_uploaded ? 'uploaded' : s.kubeconfig_path && s.kubeconfig_path !== 'in-cluster' ? 'kubeconfig' : 'in-cluster';
  return s.kube_context && kind !== 'in-cluster' ? `${kind} · ${s.kube_context}` : kind;
}

/** Full detail for the connection tooltip (may include the server-side path). */
export function sourceConnectionTitle(src: SourceStatus): string {
  const s = src.settings;
  if (!s) return '';
  const parts = [
    s.kubeconfig_uploaded ? 'Uploaded kubeconfig' : s.kubeconfig_path === 'in-cluster' ? 'In-cluster (service account)' : s.kubeconfig_path ? `Kubeconfig: ${s.kubeconfig_path}` : 'In-cluster / default credentials',
    s.kube_context ? `Context: ${s.kube_context}` : '',
    s.poll_interval_seconds ? `Poll every ${s.poll_interval_seconds}s` : '',
  ];
  return parts.filter(Boolean).join('\n');
}

/** Kubernetes container IDs are `namespace/pod/container`. */
export function podNameFromId(id: string): string {
  const parts = id.split('/');
  return parts.length >= 3 ? parts[1] : '';
}
