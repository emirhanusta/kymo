import { Component, ChangeDetectionStrategy, input } from '@angular/core';

export type EmptyIcon = 'box' | 'search' | 'chart' | 'events' | 'check' | 'archive' | 'clock';

const ICONS: Record<EmptyIcon, string> = {
  box:     'M20 7l-8-4-8 4m16 0l-8 4m8-4v10l-8 4m0-10L4 7m8 4v10M4 7v10l8 4',
  search:  'M21 21l-4.35-4.35M17 10.5a6.5 6.5 0 11-13 0 6.5 6.5 0 0113 0z',
  chart:   'M7 12l3-3 3 3 4-4M8 21l4-4 4 4M3 4h18M4 4h16v12a1 1 0 01-1 1H5a1 1 0 01-1-1V4z',
  events:  'M13 10V3L4 14h7v7l9-11h-7z',
  check:   'M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z',
  archive: 'M5 8h14M5 8a2 2 0 110-4h14a2 2 0 110 4M5 8l1 12a2 2 0 002 2h8a2 2 0 002-2L19 8',
  clock:   'M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z',
};

@Component({
  selector: 'app-empty-state',
  standalone: true,
  template: `
    <div class="flex flex-col items-center justify-center text-center px-6" [class.py-14]="!compact()" [class.py-8]="compact()">
      <svg class="w-5 h-5 mb-3" [class]="tone() === 'success' ? 'text-kymo-running' : 'text-kymo-faint'"
           fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5" aria-hidden="true">
        <path stroke-linecap="square" stroke-linejoin="miter" [attr.d]="path()" />
      </svg>
      <p class="text-xs uppercase tracking-[0.08em]" [class]="tone() === 'success' ? 'text-kymo-running' : 'text-kymo-ink'">{{ message() }}</p>
      @if (hint()) {
        <p class="text-xs text-kymo-muted mt-1.5 max-w-sm">{{ hint() }}</p>
      }
      <ng-content />
    </div>
  `,
  host: { class: 'block' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class EmptyStateComponent {
  icon    = input<EmptyIcon>('box');
  message = input.required<string>();
  hint    = input<string>('');
  compact = input(false);
  tone    = input<'default' | 'success'>('default');

  path() { return ICONS[this.icon()]; }
}
