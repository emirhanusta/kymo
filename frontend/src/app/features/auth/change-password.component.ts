import { Component, ChangeDetectionStrategy, inject, signal, computed } from '@angular/core';
import { FormsModule } from '@angular/forms';
import { ActivatedRoute, Router } from '@angular/router';
import { HttpErrorResponse } from '@angular/common/http';
import { firstValueFrom } from 'rxjs';

import { AuthService } from '../../core/services/auth.service';
import { safeReturn } from '../../core/guards/auth.guards';
import { MIN_PASSWORD, hintClass, passwordHint } from './password-utils';

const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

/** /change-password — shown while the account is flagged must_change_password (e.g. a bootstrap admin). */
@Component({
  selector: 'app-change-password',
  standalone: true,
  imports: [FormsModule],
  template: `
    <h1 class="text-sm font-semibold uppercase tracking-[0.08em] text-kymo-ink mb-1">Change your password</h1>
    <p class="text-xs text-kymo-muted mb-4">
      @if (user(); as u) { Signed in as <span class="text-kymo-ink">{{ u.username }}</span>. }
      You must choose a new password before continuing.
    </p>

    @if (error()) {
      <p class="text-xs text-kymo-danger border border-kymo-danger/50 px-3 py-2 mb-4" role="alert">{{ error() }}</p>
    }

    <form class="space-y-3" (ngSubmit)="submit()" aria-label="Change password">
      <label class="flex flex-col gap-1">
        <span class="k-label">Current password</span>
        <input class="k-input w-full" type="password" name="cur" autocomplete="current-password" required autofocus
               [ngModel]="current()" (ngModelChange)="current.set($event)" />
      </label>
      <label class="flex flex-col gap-1">
        <span class="k-label">New password</span>
        <input class="k-input w-full" type="password" name="new" autocomplete="new-password" required
               [ngModel]="next()" (ngModelChange)="next.set($event)" />
        @if (hint(); as h) { <span class="text-[11px]" [class]="cls(h.tone)">{{ h.label }}</span> }
        @else { <span class="text-[11px] text-kymo-faint">At least {{ min }} characters; mix letters, numbers and symbols.</span> }
      </label>
      <label class="flex flex-col gap-1">
        <span class="k-label">Confirm new password</span>
        <input class="k-input w-full" type="password" name="conf" autocomplete="new-password" required
               [ngModel]="confirm()" (ngModelChange)="confirm.set($event)" />
        @if (confirm() && confirm() !== next()) { <span class="text-[11px] text-kymo-danger">Passwords do not match</span> }
      </label>
      <label class="flex flex-col gap-1">
        <span class="k-label">Email (optional)</span>
        <input class="k-input w-full" type="email" name="email" autocomplete="email"
               [ngModel]="email()" (ngModelChange)="email.set($event)" />
        @if (emailInvalid()) { <span class="text-[11px] text-kymo-danger">Enter a valid email address</span> }
      </label>
      <button type="submit" class="k-btn k-btn-primary w-full" [disabled]="!valid() || busy()">
        {{ busy() ? 'Saving…' : 'Change password' }}
      </button>
    </form>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ChangePasswordComponent {
  private auth = inject(AuthService);
  private router = inject(Router);
  private route = inject(ActivatedRoute);
  private returnTo = safeReturn(this.route.snapshot.queryParamMap.get('returnUrl'));

  min = MIN_PASSWORD;
  cls = hintClass;
  user = this.auth.user;
  current = signal(''); next = signal(''); confirm = signal('');
  email = signal(this.auth.user()?.email ?? '');
  busy = signal(false);
  error = signal('');
  hint = computed(() => passwordHint(this.next()));
  emailInvalid = computed(() => { const e = this.email().trim(); return !!e && !EMAIL_RE.test(e); });
  valid = computed(() =>
    !!this.current() && this.next().length >= MIN_PASSWORD && this.next() === this.confirm() && !this.emailInvalid());

  async submit(): Promise<void> {
    if (!this.valid() || this.busy()) return;
    this.busy.set(true);
    this.error.set('');
    const email = this.email().trim();
    try {
      if (email && email !== (this.auth.user()?.email ?? '')) {
        await firstValueFrom(this.auth.updateProfile({ email }));
      }
      await firstValueFrom(this.auth.changePassword(this.current(), this.next()));
    } catch (e) {
      this.busy.set(false);
      this.error.set(this.message(e as HttpErrorResponse));
      return;
    }
    await this.auth.refreshSession();
    this.busy.set(false);
    this.router.navigateByUrl(this.returnTo === '/' ? '/dashboard' : this.returnTo);
  }

  private message(err: HttpErrorResponse): string {
    const code = err.error?.code;
    if (code === 'email_taken') return 'That email address is already in use.';
    if (code === 'invalid_email') return 'That email address is not valid.';
    if (err.status === 401 || code === 'invalid_credentials') return 'Current password is incorrect.';
    return err.error?.error || 'Could not change password.';
  }
}
