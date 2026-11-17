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

/** Route to a container's detail page (Docker and Kubernetes alike). */
export function containerRoute(id: string, _runtime?: string | null): string[] {
  return ['/containers', id];
}

/** Shorten 64-char docker IDs for display fallbacks. */
export function shortId(id: string): string {
  return /^[0-9a-f]{64}$/i.test(id) ? id.slice(0, 12) : id;
}
