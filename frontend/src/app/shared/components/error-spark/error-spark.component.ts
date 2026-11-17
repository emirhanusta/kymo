import { Component, ChangeDetectionStrategy, input, computed } from '@angular/core';

/** Tiny inline-SVG bar sparkline of error counts per bucket (oldest first). */
@Component({
  selector: 'app-error-spark',
  standalone: true,
  template: `
    <svg [attr.viewBox]="'0 0 ' + w + ' ' + h" [attr.width]="w" [attr.height]="h" class="block flex-none" role="img"
         [attr.aria-label]="label()">
      @for (b of bars(); track $index) {
        <rect [attr.x]="b.x" [attr.y]="b.y" [attr.width]="b.w" [attr.height]="b.h"
              [attr.fill]="b.v > 0 ? 'var(--color-kymo-amber)' : 'var(--color-kymo-rule)'" />
      }
    </svg>
  `,
  host: { class: 'inline-block leading-none' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ErrorSparkComponent {
  buckets = input<number[]>([]);
  readonly w = 72;
  readonly h = 18;

  private max = computed(() => Math.max(1, ...(this.buckets() ?? [])));

  bars = computed(() => {
    const b = this.buckets() ?? [];
    if (!b.length) return [];
    const slot = this.w / b.length;
    return b.map((v, i) => {
      const bh = v > 0 ? Math.max(2, (v / this.max()) * this.h) : 1;
      return { v, x: +(i * slot).toFixed(2), w: +Math.max(1, slot - 1).toFixed(2), y: this.h - bh, h: bh };
    });
  });

  label = computed(() => `Errors over time, oldest to newest, ${(this.buckets() ?? []).reduce((a, n) => a + n, 0)} total`);
}
