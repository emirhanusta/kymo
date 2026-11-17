import { Component, Input, ChangeDetectionStrategy } from '@angular/core';

@Component({
  selector: 'app-metric-card',
  standalone: true,
  templateUrl: './metric-card.component.html',
  styleUrl: './metric-card.component.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class MetricCardComponent {
  @Input() label = '';
  @Input() value: string | number = '—';
  @Input() unit?: string;
  @Input() sub?: string;
  /** Legacy names kept for callers: violet/amber → amber, emerald → running, rose → danger, slate → ink. */
  @Input() accent: 'violet' | 'emerald' | 'amber' | 'rose' | 'slate' = 'slate';
  /** Only apply the accent color when the value is "interesting" (e.g. crashes > 0). */
  @Input() muted = false;

  get valueClass(): string {
    if (this.muted) return 'text-kymo-ink';
    const map: Record<string, string> = {
      violet: 'text-kymo-amber',
      emerald: 'text-kymo-running',
      amber: 'text-kymo-amber',
      rose: 'text-kymo-danger',
      slate: 'text-kymo-ink',
    };
    return map[this.accent];
  }
}
