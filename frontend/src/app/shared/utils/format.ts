export function formatBytes(bytes: number | null | undefined, digits = 1): string {
  if (bytes == null || isNaN(bytes)) return '—';
  const abs = Math.abs(bytes);
  if (abs >= 1_073_741_824) return `${(bytes / 1_073_741_824).toFixed(digits)} GB`;
  if (abs >= 1_048_576)     return `${(bytes / 1_048_576).toFixed(digits)} MB`;
  if (abs >= 1_024)         return `${(bytes / 1_024).toFixed(digits)} KB`;
  return `${Math.round(bytes)} B`;
}

export function formatRate(bytesPerSec: number | null | undefined): string {
  const b = formatBytes(bytesPerSec);
  return b === '—' ? b : `${b}/s`;
}

/** Route to a container's detail page. */
export function containerRoute(id: string, _runtime?: string | null): string[] {
  return ['/containers', id];
}

/** Shorten 64-char hex IDs for display fallbacks. */
export function shortId(id: string): string {
  return /^[0-9a-f]{64}$/i.test(id) ? id.slice(0, 12) : id;
}

/** Seconds → "850ms", "6.2s", "1m 12s", "2h 3m". */
export function formatDuration(seconds: number | null | undefined): string {
  if (seconds == null || isNaN(seconds)) return '—';
  if (seconds < 1) return `${Math.round(seconds * 1000)}ms`;
  if (seconds < 60) return `${seconds < 10 ? seconds.toFixed(1) : Math.round(seconds)}s`;
  const m = Math.floor(seconds / 60);
  if (m < 60) return `${m}m ${Math.round(seconds % 60)}s`;
  return `${Math.floor(m / 60)}h ${m % 60}m`;
}

/** Millisecond span → "45s", "4 min", "3 h", "2 d". */
export function formatSpan(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return `${s}s`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m} min`;
  const h = Math.round(m / 60);
  if (h < 48) return `${h} h`;
  return `${Math.round(h / 24)} d`;
}

/** Drops the registry host from an image reference: "reg.io:5000/team/api:1.2" → "team/api:1.2". */
export function shortImage(image: string): string {
  const i = image.indexOf('/');
  if (i > 0 && /[.:]|^localhost$/.test(image.slice(0, i))) return image.slice(i + 1);
  return image;
}

/** "quay.io/argoproj/argocd:v2.11.3@sha256:…" → "argocd:v2.11.3" (last path segment, digest dropped). */
export function imageNameTag(image: string): string {
  const noDigest = image.split('@')[0];
  return noDigest.slice(noDigest.lastIndexOf('/') + 1);
}

/** The tag of an image reference ("latest" when none, '' for a bare digest). */
export function imageTagOf(image: string): string {
  const nt = imageNameTag(image);
  const i = nt.lastIndexOf(':');
  return i >= 0 ? nt.slice(i + 1) : (nt ? 'latest' : '');
}
