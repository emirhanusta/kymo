import { Component, ChangeDetectionStrategy, inject, output, signal, computed } from '@angular/core';
import { FormsModule } from '@angular/forms';
import { HttpErrorResponse } from '@angular/common/http';

import { AuthService } from '../../core/services/auth.service';
import { ToastService } from '../../core/services/toast.service';
import { DialogDirective } from '../../shared/directives/dialog.directive';

const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

@Component({
  selector: 'app-profile-dialog',
  standalone: true,
  imports: [FormsModule, DialogDirective],
  template: `
    <div class="fixed inset-0 bg-kymo-deep/85 z-50 flex items-center justify-center p-4" (click)="closed.emit()">
      <form appDialog (dismiss)="closed.emit()" (ngSubmit)="submit()" aria-labelledby="pf-title"
            class="bg-kymo-panel border border-kymo-rule border-t-kymo-amber p-5 max-w-sm w-full space-y-3" (click)="$event.stopPropagation()">
        <h3 id="pf-title" class="text-sm font-semibold uppercase tracking-[0.08em] text-kymo-ink">Profile</h3>
        <p class="text-xs text-kymo-muted">Username: <span class="text-kymo-ink">{{ username }}</span></p>
        @if (error()) { <p class="text-xs text-kymo-danger border border-kymo-danger/50 px-3 py-2" role="alert">{{ error() }}</p> }
        <label class="flex flex-col gap-1">
          <span class="k-label">Name</span>
          <input data-autofocus class="k-input w-full" name="name" autocomplete="name" [ngModel]="name()" (ngModelChange)="name.set($event)" />
        </label>
        <label class="flex flex-col gap-1">
          <span class="k-label">Email (optional)</span>
          <input class="k-input w-full" type="email" name="email" autocomplete="email" [ngModel]="email()" (ngModelChange)="email.set($event)" />
          @if (emailInvalid()) { <span class="text-[11px] text-kymo-danger">Enter a valid email address</span> }
        </label>
        <div class="flex justify-end gap-2 pt-1">
          <button type="button" class="k-btn" (click)="closed.emit()">Cancel</button>
          <button type="submit" class="k-btn k-btn-primary" [disabled]="!valid() || busy()">{{ busy() ? 'Saving…' : 'Save' }}</button>
        </div>
      </form>
    </div>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ProfileDialogComponent {
  private auth = inject(AuthService);
  private toast = inject(ToastService);
  closed = output<void>();

  username = this.auth.user()?.username ?? '';
  private initial = { name: this.auth.user()?.name ?? '', email: this.auth.user()?.email ?? '' };
  name = signal(this.initial.name);
  email = signal(this.initial.email);
  busy = signal(false);
  error = signal('');
  emailInvalid = computed(() => { const e = this.email().trim(); return !!e && !EMAIL_RE.test(e); });
  valid = computed(() => !!this.name().trim() && !this.emailInvalid()
    && (this.name().trim() !== this.initial.name || this.email().trim() !== this.initial.email));

  submit(): void {
    if (!this.valid() || this.busy()) return;
    this.busy.set(true);
    this.error.set('');
    const patch: { name?: string; email?: string } = {};
    if (this.name().trim() !== this.initial.name) patch.name = this.name().trim();
    if (this.email().trim() !== this.initial.email) patch.email = this.email().trim();
    this.auth.updateProfile(patch).subscribe({
      next: () => {
        this.busy.set(false);
        this.toast.success('Profile updated');
        this.auth.loadMe().subscribe();
        this.closed.emit();
      },
      error: (err: HttpErrorResponse) => {
        this.busy.set(false);
        const code = err.error?.code;
        this.error.set(code === 'email_taken' ? 'That email address is already in use.'
          : code === 'invalid_email' ? 'That email address is not valid.'
          : err.error?.error || 'Could not update profile.');
      },
    });
  }
}
