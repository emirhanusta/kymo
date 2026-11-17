import { Component, ChangeDetectionStrategy, input, computed } from '@angular/core';

/** Cheap pulse placeholders: `table` rows inside a card, or a grid of `cards`. */
@Component({
  selector: 'app-skeleton',
  standalone: true,
  template: `
    @if (variant() === 'cards') {
      <div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
        @for (i of items(); track i) {
          <div class="k-paper border border-kymo-rule p-4 flex gap-4 items-center animate-pulse">
            <div class="w-16 h-16 border border-kymo-rule bg-kymo-raised flex-none"></div>
            <div class="flex-1 space-y-2">
              <div class="h-3 bg-kymo-raised w-1/2"></div>
              <div class="h-2.5 bg-kymo-raised w-1/3"></div>
              <div class="h-1 bg-kymo-raised w-full"></div>
            </div>
          </div>
        }
      </div>
    } @else {
      <div class="k-panel overflow-hidden animate-pulse">
        <div class="h-9 border-b border-kymo-rule"></div>
        @for (i of items(); track i) {
          <div class="flex items-center gap-6 px-4 py-3 border-b border-kymo-rule last:border-b-0">
            <div class="h-2.5 bg-kymo-raised w-1/4"></div>
            <div class="h-2.5 bg-kymo-raised w-1/6 hidden md:block"></div>
            <div class="h-2.5 bg-kymo-raised w-16"></div>
            <div class="flex-1"></div>
            <div class="h-2.5 bg-kymo-raised w-12"></div>
          </div>
        }
      </div>
    }
  `,
  host: { class: 'block', 'aria-busy': 'true', 'aria-label': 'Loading' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class SkeletonComponent {
  variant = input<'table' | 'cards'>('table');
  rows    = input(6);
  items   = computed(() => Array.from({ length: this.rows() }, (_, i) => i));
}
