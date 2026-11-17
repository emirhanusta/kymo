import { LogCaptureState } from '../../core/models/error-log.model';

export const ERROR_WINDOWS = ['1h', '6h', '24h', '7d'] as const;

export const CAPTURE_PRIVACY_NOTE =
  'Only error-level lines are kept; secrets are masked (best effort).';

export interface TemplatePart { text: string; placeholder: boolean }

/** Splits a signature template into plain text and `<n>` / `<uuid>` style placeholders. */
export function templateParts(template: string): TemplatePart[] {
  const parts: TemplatePart[] = [];
  const re = /<(?:ts|uuid|hex|ip|n|str|email)>/g;
  let last = 0;
  for (const m of template.matchAll(re)) {
    const i = m.index ?? 0;
    if (i > last) parts.push({ text: template.slice(last, i), placeholder: false });
    parts.push({ text: m[0], placeholder: true });
    last = i + m[0].length;
  }
  if (last < template.length) parts.push({ text: template.slice(last), placeholder: false });
  return parts;
}

/** Why capture cannot be switched off from the workload page, or '' when the toggle is usable. */
export function captureLockReason(s: LogCaptureState | null, namespace?: string): string {
  if (!s || !s.enabled || s.via === 'workload') return '';
  switch (s.via) {
    case 'namespace':  return `enabled for namespace ${namespace || ''}`.trim();
    case 'annotation': return 'enabled by pod annotation kymo.io/capture-error-logs';
    case 'config':     return 'enabled in config.yaml';
    default:           return 'enabled elsewhere';
  }
}
