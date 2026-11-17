import { Component, ChangeDetectionStrategy, input } from '@angular/core';

/**
 * KYMO mark: a square frame (rule colour) with an amber kymograph trace —
 * flat, a dip, a spike, then flat again. Scales to any `size` (px).
 */
@Component({
  selector: 'app-logo',
  standalone: true,
  template: `
    <svg
      [attr.width]="size()"
      [attr.height]="size()"
      viewBox="0 0 26 26"
      fill="none"
      [attr.role]="label() ? 'img' : null"
      [attr.aria-label]="label() || null"
      [attr.aria-hidden]="label() ? null : 'true'"
    >
      <rect x="0.5" y="0.5" width="25" height="25" stroke="var(--color-kymo-rule)" stroke-width="1" />
      <path
        d="M3 16h5l2-8 3 12 2-6h8"
        stroke="var(--color-kymo-amber)"
        stroke-width="1.8"
        stroke-linecap="round"
        stroke-linejoin="round"
      />
    </svg>
  `,
  host: { class: 'inline-flex flex-none' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class LogoComponent {
  size  = input(26);
  /** Accessible name; omit when the mark sits next to a visible wordmark. */
  label = input('');
}
