import { Component, ChangeDetectionStrategy, inject, output, signal, computed } from '@angular/core';
import { FormsModule } from '@angular/forms';
import { HttpErrorResponse } from '@angular/common/http';

import { AuthService } from '../../core/services/auth.service';
import { ToastService } from '../../core/services/toast.service';
import { DialogDirective } from '../../shared/directives/dialog.directive';
import { MIN_PASSWORD, hintClass, passwordHint } from '../../features/auth/password-utils';

@Component({
  selector: 'app-change-password-dialog',
  standalone: true,
  imports: [FormsModule, DialogDirective],
  template: `
    <div class="fixed inset-0 bg-kymo-deep/85 z-50 flex items-center justify-center p-4" (click)="closed.emit()">
      <form appDialog (dismiss)="closed.emit()" (ngSubmit)="submit()" aria-labelledby="cp-title"
            class="bg-kymo-panel border border-kymo-rule border-t-kymo-amber p-5 max-w-sm w-full space-y-3" (click)="$event.stopPropagation()">
        <h3 id="cp-title" class="text-sm font-semibold uppercase tracking-[0.08em] text-kymo-ink">Change password</h3>
        <p class="text-xs text-kymo-muted">Your other sessions will be signed out.</p>
        @if (error()) { <p class="text-xs text-kymo-danger border border-kymo-danger/50 px-3 py-2" role="alert">{{ error() }}</p> }
        <label class="flex flex-col gap-1">
          <span class="k-label">Current password</span>
          <input data-autofocus class="k-input w-full" type="password" name="cur" autocomplete="current-password" required [ngModel]="current()" (ngModelChange)="current.set($event)" />
        </label>
        <label class="flex flex-col gap-1">
          <span class="k-label">New password</span>
          <input class="k-input w-full" type="password" name="new" autocomplete="new-password" required [ngModel]="next()" (ngModelChange)="next.set($event)" />
          @if (hint(); as h) { <span class="text-[11px]" [class]="cls(h.tone)">{{ h.label }}</span> }
        </label>
        <label class="flex flex-col gap-1">
          <span class="k-label">Confirm new password</span>
          <input class="k-input w-full" type="password" name="conf" autocomplete="new-password" required [ngModel]="confirm()" (ngModelChange)="confirm.set($event)" />
          @if (confirm() && confirm() !== next()) { <span class="text-[11px] text-kymo-danger">Passwords do not match</span> }
        </label>
        <div class="flex justify-end gap-2 pt-1">
          <button type="button" class="k-btn" (click)="closed.emit()">Cancel</button>
          <button type="submit" class="k-btn k-btn-primary" [disabled]="!valid() || busy()">{{ busy() ? 'Saving…' : 'Change password' }}</button>
        </div>
      </form>
    </div>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ChangePasswordDialogComponent {
  private auth = inject(AuthService);
  private toast = inject(ToastService);
  closed = output<void>();

  current = signal(''); next = signal(''); confirm = signal('');
  busy = signal(false);
  error = signal('');
  cls = hintClass;
  hint = computed(() => passwordHint(this.next()));
  valid = computed(() => !!this.current() && this.next().length >= MIN_PASSWORD && this.next() === this.confirm());

  submit(): void {
    if (!this.valid() || this.busy()) return;
    this.busy.set(true);
    this.error.set('');
    this.auth.changePassword(this.current(), this.next()).subscribe({
      next: () => { this.busy.set(false); this.toast.success('Password changed'); this.closed.emit(); },
      error: (err: HttpErrorResponse) => {
        this.busy.set(false);
        this.error.set(err.status === 401 || err.error?.code === 'invalid_credentials' ? 'Current password is incorrect.' : err.error?.error || 'Could not change password.');
      },
    });
  }
}
