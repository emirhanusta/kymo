import { Component, ChangeDetectionStrategy } from '@angular/core';
import { RouterOutlet } from '@angular/router';

import { LogoComponent } from '../../shared/components/logo/logo.component';
import { ToastComponent } from '../../shared/components/toast/toast.component';

/** Minimal shell for login / change-password / invite / reset: centered card, no topbar or event tape. */
@Component({
  selector: 'app-auth-layout',
  standalone: true,
  imports: [RouterOutlet, LogoComponent, ToastComponent],
  template: `
    <div class="min-h-screen bg-kymo-bg flex flex-col items-center justify-center px-4 py-10">
      <div class="flex items-center gap-2.5 mb-6 text-kymo-ink">
        <app-logo [size]="28" />
        <span class="text-base font-semibold tracking-[0.3em]">KYMO</span>
      </div>
      <div class="k-panel w-full max-w-sm border-t-kymo-amber p-6">
        <router-outlet />
      </div>
    </div>
    <app-toast />
  `,
  host: { class: 'block' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class AuthLayoutComponent {}
