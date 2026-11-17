import { Component, ChangeDetectionStrategy, input } from '@angular/core';
import { READ_ONLY_HINT } from '../../utils/source';

/** Small "READ-ONLY" tag shown in place of management actions for read-only sources. */
@Component({
  selector: 'app-read-only-tag',
  standalone: true,
  template: `
    <span
      class="inline-flex items-center gap-1 h-5 px-1.5 text-[10px] font-medium tracking-[0.08em] border border-kymo-amber/50 text-kymo-amber cursor-help whitespace-nowrap"
      tabindex="0"
      [title]="hint()"
      [attr.aria-label]="'Read-only. ' + hint()"
    >
      <svg class="w-2.5 h-2.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2.5" aria-hidden="true">
        <rect x="5" y="11" width="14" height="10" /><path stroke-linecap="square" d="M8 11V7a4 4 0 018 0v4" />
      </svg>
      READ-ONLY
    </span>
  `,
  host: { class: 'inline-flex' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ReadOnlyTagComponent {
  hint = input(READ_ONLY_HINT);
}
