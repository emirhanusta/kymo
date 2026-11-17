import { SourceStatus } from '../../core/models/container.model';

export function sourceDotClass(src: SourceStatus): string {
  switch (src.status) {
    case 'connected':  return 'bg-kymo-running';
    case 'connecting': return 'bg-kymo-amber animate-pulse';
    case 'error':      return 'bg-kymo-danger';
    default:           return 'bg-kymo-unknown';
  }
}

export function sourceStatusLabel(src: SourceStatus): string {
  switch (src.status) {
    case 'connected':  return 'Connected';
    case 'connecting': return 'Connecting…';
    case 'error':      return src.last_error ? `Error: ${src.last_error}` : 'Error';
    default:           return 'Unknown';
  }
}
