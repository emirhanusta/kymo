import { Component, ChangeDetectionStrategy, input } from '@angular/core';

@Component({
  selector: 'app-role-chip',
  standalone: true,
  template: `<span class="inline-flex items-center h-5 px-1.5 text-[10px] tracking-[0.06em] border"
    [class]="role() === 'owner' || role() === 'admin' ? 'border-kymo-amber/60 text-kymo-amber' : 'border-kymo-rule text-kymo-muted'">{{ role() }}</span>`,
  host: { class: 'inline-flex' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class RoleChipComponent {
  role = input.required<string>();
}
