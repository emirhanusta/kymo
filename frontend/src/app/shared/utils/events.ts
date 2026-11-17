import { LifecycleEvent } from '../../core/models/container.model';

export type EventTone = 'good' | 'neutral' | 'bad' | 'warn' | 'info';

const FAILURE_TYPES = new Set(['die', 'crash', 'crashed']);

/** Exit codes are only meaningful for die/crash events. */
export function showsExitCode(ev: LifecycleEvent): boolean {
  return FAILURE_TYPES.has(ev.event_type.toLowerCase()) && ev.exit_code != null;
}

export function eventTone(ev: LifecycleEvent): EventTone {
  switch (ev.event_type.toLowerCase()) {
    case 'start':
    case 'unpause':
    case 'create':
      return 'good';
    case 'die':
      return ev.exit_code ? 'bad' : 'neutral';
    case 'crash':
    case 'crashed':
    case 'kill':
      return 'bad';
    case 'oom':
    case 'oomkilled':
      return 'warn';
    case 'restart':
    case 'pause':
      return 'info';
    default:
      return 'neutral';
  }
}

const LABELS: Record<string, string> = {
  start: 'Started', stop: 'Stopped', die: 'Exited', crash: 'Crashed', crashed: 'Crashed',
  oom: 'OOM killed', oomkilled: 'OOM killed', kill: 'Killed', restart: 'Restarted',
  pause: 'Paused', unpause: 'Resumed', create: 'Created', destroy: 'Removed', exited: 'Exited',
};

export function eventLabel(ev: LifecycleEvent): string {
  const t = ev.event_type.toLowerCase();
  const base = LABELS[t] ?? (t ? t.charAt(0).toUpperCase() + t.slice(1) : 'Event');
  if (t === 'die' && !ev.exit_code) return 'Exited cleanly';
  return base;
}

/** Human-readable reason, e.g. "CrashLoopBackOff — back-off 5m0s restarting failed container". */
export function eventReason(ev: LifecycleEvent): string {
  const m = ev.metadata;
  if (!m) return '';
  const reason  = typeof m.reason === 'string' ? m.reason.trim() : '';
  const message = typeof m.message === 'string' ? m.message.trim() : '';
  if (reason && message) return `${reason} — ${message}`;
  return reason || message;
}

const TONE_DOT: Record<EventTone, string> = {
  good: 'bg-kymo-running', neutral: 'bg-kymo-stopped', bad: 'bg-kymo-danger', warn: 'bg-kymo-amber', info: 'bg-kymo-paused',
};
const TONE_TEXT: Record<EventTone, string> = {
  good: 'text-kymo-running', neutral: 'text-kymo-stopped', bad: 'text-kymo-danger', warn: 'text-kymo-amber', info: 'text-kymo-paused',
};
const TONE_CHIP: Record<EventTone, string> = {
  good: 'border border-kymo-running/40 text-kymo-running', neutral: 'border border-kymo-rule text-kymo-stopped',
  bad: 'border border-kymo-danger/50 text-kymo-danger', warn: 'border border-kymo-amber/50 text-kymo-amber',
  info: 'border border-kymo-paused/40 text-kymo-paused',
};

export const eventDotClass  = (ev: LifecycleEvent) => TONE_DOT[eventTone(ev)];
export const eventTextClass = (ev: LifecycleEvent) => TONE_TEXT[eventTone(ev)];
export const eventChipClass = (ev: LifecycleEvent) => TONE_CHIP[eventTone(ev)];

export function eventIcon(ev: LifecycleEvent): string {
  switch (eventTone(ev)) {
    case 'good': return '▶';
    case 'bad':  return '✕';
    case 'warn': return '⚠';
    case 'info': return '↻';
    default:     return '■';
  }
}

/** Crash and OOM terminations: the events that can carry a log snapshot. */
export function isProblemEvent(ev: LifecycleEvent): boolean {
  return ['crash', 'crashed', 'oom', 'oomkilled'].includes(ev.event_type.toLowerCase());
}

/** A crash/OOM event with a captured log snapshot. */
export const hasCrashLogs = (ev: LifecycleEvent): boolean => !!ev.has_logs && isProblemEvent(ev);

/** Stop caused by the pod being deleted/replaced (rollout, scale-down, eviction), not by the app failing. */
export function isPodReplaced(ev: LifecycleEvent): boolean {
  const v = ev.metadata?.['pod_terminating'];
  return v === true || v === 'true' || v === 1;
}

/** Container name from a Kubernetes id `namespace/pod/container` (falls back to the event's own name). */
export function eventContainerName(ev: LifecycleEvent): string {
  if (ev.container_name) return ev.container_name;
  const parts = ev.container_id.split('/');
  return parts.length >= 3 ? parts.slice(2).join('/') : ev.container_id;
}
