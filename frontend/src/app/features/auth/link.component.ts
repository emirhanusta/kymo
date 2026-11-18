import { Component, ChangeDetectionStrategy, inject, signal, computed, OnInit } from '@angular/core';
import { FormsModule } from '@angular/forms';
import { ActivatedRoute, Router, RouterLink } from '@angular/router';
import { HttpErrorResponse } from '@angular/common/http';

import { AuthService } from '../../core/services/auth.service';
import { LinkInfo } from '../../core/models/auth.model';
import { MIN_PASSWORD, hintClass, passwordHint } from './password-utils';

/** /invite/:token and /reset/:token — accept an invite or set a new password. */
@Component({
  selector: 'app-link',
  standalone: true,
  imports: [FormsModule, RouterLink],
  template: `
    @if (state() === 'loading') {
      <p class="text-xs text-kymo-muted">Checking link…</p>
    } @else if (state() === 'invalid') {
      <h1 class="text-sm font-semibold uppercase tracking-[0.08em] text-kymo-ink mb-2">Link not valid</h1>
      <p class="text-xs text-kymo-muted mb-4">{{ invalidMsg() }}</p>
      <a routerLink="/login" class="k-btn w-full">Go to sign in</a>
    } @else if (info(); as i) {
      <h1 class="text-sm font-semibold uppercase tracking-[0.08em] text-kymo-ink mb-1">
        {{ i.kind === 'invite' ? 'Accept invitation' : 'Reset password' }}
      </h1>
      <dl class="text-xs mb-4 space-y-1">
        <div class="flex gap-2"><dt class="text-kymo-muted w-20">Username</dt><dd class="text-kymo-ink">{{ i.username }}</dd></div>
        @if (i.email) {
          <div class="flex gap-2"><dt class="text-kymo-muted w-20">Email</dt><dd class="text-kymo-ink">{{ i.email }}</dd></div>
        }
        @if (i.kind === 'invite' && i.role) {
          <div class="flex gap-2"><dt class="text-kymo-muted w-20">Role</dt><dd class="text-kymo-amber">{{ i.role }}</dd></div>
        }
      </dl>
      @if (error()) {
        <p class="text-xs text-kymo-danger border border-kymo-danger/50 px-3 py-2 mb-4" role="alert">{{ error() }}</p>
      }
      <form class="space-y-3" (ngSubmit)="submit()" aria-label="Set password">
        @if (i.kind === 'invite') {
          <label class="flex flex-col gap-1">
            <span class="k-label">Name</span>
            <input class="k-input w-full" name="name" autocomplete="name" required [ngModel]="name()" (ngModelChange)="name.set($event)" />
          </label>
        }
        <label class="flex flex-col gap-1">
          <span class="k-label">Password</span>
          <input class="k-input w-full" type="password" name="pw" autocomplete="new-password" required [ngModel]="password()" (ngModelChange)="password.set($event)" />
          @if (hint(); as h) { <span class="text-[11px]" [class]="cls(h.tone)">{{ h.label }}</span> }
        </label>
        <label class="flex flex-col gap-1">
          <span class="k-label">Confirm password</span>
          <input class="k-input w-full" type="password" name="pw2" autocomplete="new-password" required [ngModel]="confirm()" (ngModelChange)="confirm.set($event)" />
          @if (confirm() && confirm() !== password()) { <span class="text-[11px] text-kymo-danger">Passwords do not match</span> }
        </label>
        <button type="submit" class="k-btn k-btn-primary w-full" [disabled]="!valid() || busy()">
          {{ busy() ? 'Saving…' : i.kind === 'invite' ? 'Create account' : 'Set password' }}
        </button>
      </form>
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class LinkComponent implements OnInit {
  private auth = inject(AuthService);
  private router = inject(Router);
  private route = inject(ActivatedRoute);
  private token = this.route.snapshot.paramMap.get('token') ?? '';

  state = signal<'loading' | 'ready' | 'invalid'>('loading');
  invalidMsg = signal('');
  info = signal<LinkInfo | null>(null);
  name = signal(''); password = signal(''); confirm = signal('');
  busy = signal(false);
  error = signal('');
  hint = computed(() => passwordHint(this.password()));
  cls = hintClass;
  valid = computed(() =>
    this.password().length >= MIN_PASSWORD && this.password() === this.confirm()
    && (this.info()?.kind !== 'invite' || !!this.name().trim()));

  ngOnInit(): void {
    this.auth.link(this.token).subscribe({
      next: i => { this.info.set(i); this.state.set('ready'); },
      error: (err: HttpErrorResponse) => {
        this.invalidMsg.set(err.status === 410
          ? 'This link has expired or was already used. Ask an admin for a new one.'
          : 'This link does not exist.');
        this.state.set('invalid');
      },
    });
  }

  submit(): void {
    if (!this.valid() || this.busy()) return;
    this.busy.set(true);
    this.error.set('');
    const body = this.info()?.kind === 'invite' ? { name: this.name().trim(), password: this.password() } : { password: this.password() };
    this.auth.acceptLink(this.token, body).subscribe({
      next: async () => {
        await this.auth.refreshSession();
        this.busy.set(false);
        this.router.navigateByUrl('/');
      },
      error: (err: HttpErrorResponse) => {
        this.busy.set(false);
        this.error.set(err.error?.error || 'Could not complete this request.');
      },
    });
  }
}
