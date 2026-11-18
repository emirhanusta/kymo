import { Component, ChangeDetectionStrategy, inject, signal, computed } from '@angular/core';
import { FormsModule } from '@angular/forms';
import { ActivatedRoute, Router } from '@angular/router';
import { HttpErrorResponse } from '@angular/common/http';

import { AuthService } from '../../core/services/auth.service';
import { safeReturn } from '../../core/guards/auth.guards';

const OIDC_ERRORS: Record<string, string> = {
  domain_not_allowed: 'Your email domain is not allowed to sign in.',
  not_invited: 'No account exists for this identity. Ask an admin for an invite.',
  disabled: 'This account is disabled.',
  oidc_failed: 'Single sign-on failed. Please try again.',
};

@Component({
  selector: 'app-login',
  standalone: true,
  imports: [FormsModule],
  template: `
    <h1 class="text-sm font-semibold uppercase tracking-[0.08em] text-kymo-ink mb-4">Sign in</h1>

    @if (message(); as m) {
      <p class="text-xs text-kymo-danger border border-kymo-danger/50 px-3 py-2 mb-4" role="alert">{{ m }}</p>
    }

    @if (cfg()?.password_login !== false) {
      <form class="space-y-3" (ngSubmit)="submit()" aria-label="Sign in">
        <label class="flex flex-col gap-1">
          <span class="k-label">Username or email</span>
          <input class="k-input w-full" type="text" name="login" autocomplete="username" autocapitalize="none" spellcheck="false" required autofocus
                 [ngModel]="login()" (ngModelChange)="login.set($event)" />
        </label>
        <label class="flex flex-col gap-1">
          <span class="k-label">Password</span>
          <input class="k-input w-full" type="password" name="password" autocomplete="current-password" required
                 [ngModel]="password()" (ngModelChange)="password.set($event)" />
        </label>
        <button type="submit" class="k-btn k-btn-primary w-full" [disabled]="busy() || !login().trim() || !password()">
          {{ busy() ? 'Signing in…' : 'Sign in' }}
        </button>
      </form>
    }

    @if (cfg()?.oidc?.enabled) {
      @if (cfg()?.password_login !== false) {
        <div class="flex items-center gap-3 my-4 text-[11px] text-kymo-faint"><span class="flex-1 border-t border-kymo-rule"></span>or<span class="flex-1 border-t border-kymo-rule"></span></div>
      }
      <button type="button" class="k-btn w-full" [class.k-btn-primary]="cfg()?.password_login === false" (click)="sso()">
        Sign in with {{ cfg()?.oidc?.name || 'SSO' }}
      </button>
    }

    @if (cfg()?.password_login === false && !cfg()?.oidc?.enabled) {
      <p class="text-xs text-kymo-muted">No sign-in method is enabled. Check the KYMO auth configuration.</p>
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class LoginComponent {
  private auth = inject(AuthService);
  private router = inject(Router);
  private route = inject(ActivatedRoute);

  cfg = this.auth.config;
  login = signal('');
  password = signal('');
  busy = signal(false);
  private loginError = signal('');
  private oidcCode = this.route.snapshot.queryParamMap.get('error') ?? '';
  private returnTo = safeReturn(this.route.snapshot.queryParamMap.get('return'));

  message = computed(() => this.loginError() || (this.oidcCode ? OIDC_ERRORS[this.oidcCode] ?? 'Sign-in failed.' : ''));

  submit(): void {
    if (this.busy()) return;
    this.busy.set(true);
    this.loginError.set('');
    this.auth.login(this.login().trim(), this.password()).subscribe({
      next: async () => {
        await this.auth.refreshSession();
        this.busy.set(false);
        this.router.navigateByUrl(this.returnTo);
      },
      error: (err: HttpErrorResponse) => {
        this.busy.set(false);
        this.password.set('');
        this.loginError.set(
          err.status === 429 ? 'Too many attempts. Try again in a few minutes.'
          : err.status === 401 ? 'Invalid username/email or password.'
          : 'Sign-in failed. Please try again.');
      },
    });
  }

  sso(): void {
    this.auth.oidcLogin(this.returnTo);
  }
}
