import { Component, ChangeDetectionStrategy, computed, inject } from '@angular/core';
import { RouterLink, RouterLinkActive } from '@angular/router';
import { AuthService } from '../../core/services/auth.service';

interface NavGroup { label: string; links: { path: string; label: string }[]; }

/** Settings sub-navigation: grouped vertical list on lg+, horizontal scrollable strip below. Hides pages the user cannot open. */
@Component({
  selector: 'app-settings-nav',
  standalone: true,
  imports: [RouterLink, RouterLinkActive],
  template: `
    <nav class="flex lg:flex-col gap-x-1 lg:gap-y-5 overflow-x-auto lg:overflow-visible scrollbar-none border-b border-kymo-rule lg:border-b-0" aria-label="Settings sections">
      @for (g of groups(); track g.label) {
        <div class="flex lg:flex-col flex-none" role="group" [attr.aria-label]="g.label">
          <p class="k-label hidden lg:block px-3 mb-1">{{ g.label }}</p>
          @for (l of g.links; track l.path) {
            <a [routerLink]="l.path" routerLinkActive="!border-kymo-amber !text-kymo-amber" #rla="routerLinkActive"
               [attr.aria-current]="rla.isActive ? 'page' : null"
               class="px-3 h-9 lg:h-8 inline-flex items-center text-xs font-medium whitespace-nowrap border-b-2 -mb-px lg:mb-0 lg:border-b-0 lg:border-l-2 border-transparent text-kymo-muted hover:text-kymo-ink transition-colors">{{ l.label }}</a>
          }
        </div>
      }
    </nav>
  `,
  host: { class: 'block' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class SettingsNavComponent {
  private auth = inject(AuthService);
  readonly groups = computed<NavGroup[]>(() => {
    const access = [
      ...(this.auth.can('users:manage') ? [{ path: '/settings/users', label: 'Users' }] : []),
      { path: '/settings/tokens', label: 'API tokens' },
      ...(this.auth.can('audit:read') ? [{ path: '/settings/audit', label: 'Audit log' }] : []),
    ];
    return [
      { label: 'Alerts', links: [
        { path: '/settings/notifications', label: 'Notifications' },
        { path: '/settings/log-capture', label: 'Log capture' },
      ] },
      { label: 'Access', links: access },
    ];
  });
}
