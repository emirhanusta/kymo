/** Canonical container status keys used for colouring across the UI. */
export type StatusKey = 'running' | 'stopped' | 'crashing' | 'paused' | 'unknown';

/** Maps any runtime/backend status string onto a StatusKey. */
export function normalizeStatus(status: string | null | undefined): StatusKey {
  switch ((status ?? '').toLowerCase().trim()) {
    case 'running':
      return 'running';
    case 'stopped':
    case 'exited':
      return 'stopped';
    case 'crashing':
    case 'crashed':
    case 'dead':
    case 'oomkilled':
      return 'crashing';
    case 'paused':
      return 'paused';
    default:
      return 'unknown';
  }
}

export const STATUS_DOT_CLASS: Record<StatusKey, string> = {
  running: 'bg-kymo-running',
  stopped: 'bg-kymo-stopped',
  crashing: 'bg-kymo-danger',
  paused: 'bg-kymo-paused',
  unknown: 'bg-kymo-unknown',
};

export const STATUS_TEXT_CLASS: Record<StatusKey, string> = {
  running: 'text-kymo-running',
  stopped: 'text-kymo-stopped',
  crashing: 'text-kymo-danger',
  paused: 'text-kymo-paused',
  unknown: 'text-kymo-unknown',
};

/** Severity order, worst first, used to summarise several containers (e.g. a pod). */
const SEVERITY: StatusKey[] = ['crashing', 'unknown', 'stopped', 'paused', 'running'];

/**
 * Returns the worst status among the given statuses, so a pod with one
 * crashing container reads as crashing. An empty list is 'unknown'.
 */
export function worstStatus(statuses: readonly string[]): StatusKey {
  if (!statuses.length) return 'unknown';
  const keys = new Set(statuses.map(normalizeStatus));
  return SEVERITY.find(k => keys.has(k)) ?? 'unknown';
}
