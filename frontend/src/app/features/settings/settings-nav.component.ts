import { Component, ChangeDetectionStrategy } from '@angular/core';
import { RouterLink, RouterLinkActive } from '@angular/router';

/** Sub-navigation shared by the Settings pages. */
@Component({
  selector: 'app-settings-nav',
  standalone: true,
  imports: [RouterLink, RouterLinkActive],
  template: `
    <nav class="flex border-b border-kymo-rule" aria-label="Settings sections">
      @for (l of links; track l.path) {
        <a [routerLink]="l.path" routerLinkActive="!border-kymo-amber !text-kymo-amber" #rla="routerLinkActive"
           [attr.aria-current]="rla.isActive ? 'page' : null"
           class="px-4 h-9 inline-flex items-center text-xs font-medium whitespace-nowrap border-b-2 border-transparent -mb-px text-kymo-muted hover:text-kymo-ink transition-colors">{{ l.label }}</a>
      }
    </nav>
  `,
  host: { class: 'block' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class SettingsNavComponent {
  readonly links = [
    { path: '/settings/notifications', label: 'Notifications' },
    { path: '/settings/log-capture',   label: 'Log capture' },
  ];
}
