import { Component, ChangeDetectionStrategy, input } from '@angular/core';

/** "exit 0" / "exit 137" tag. Exit 0 is a normal end, so it stays neutral; any other code is a failure (danger). */
@Component({
  selector: 'app-exit-tag',
  standalone: true,
  template: `<span class="inline-block text-[11px] leading-4 border px-1.5 tabular-nums whitespace-nowrap"
    [class]="code() === 0 ? 'text-kymo-muted border-kymo-rule' : 'text-kymo-danger border-kymo-danger/40'">exit {{ code() }}</span>`,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ExitTagComponent {
  code = input.required<number>();
}
