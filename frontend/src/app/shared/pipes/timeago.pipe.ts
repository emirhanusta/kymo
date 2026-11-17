import { Pipe, PipeTransform } from '@angular/core';

@Pipe({ name: 'timeago', standalone: true, pure: true })
export class TimeagoPipe implements PipeTransform {
  transform(value: string | null | undefined): string {
    if (!value) return '—';
    const ms = Date.now() - new Date(value).getTime();
    if (ms < 0) return 'just now';
    const s = Math.floor(ms / 1000);
    if (s < 60) return `${s}s ago`;
    const m = Math.floor(s / 60);
    if (m < 60) return `${m}m ago`;
    const h = Math.floor(m / 60);
    if (h < 24) return `${h}h ago`;
    const d = Math.floor(h / 24);
    return `${d}d ago`;
  }
}
