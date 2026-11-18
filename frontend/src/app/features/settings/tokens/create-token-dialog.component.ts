import { Component, ChangeDetectionStrategy, computed, inject, output, signal } from '@angular/core';
import { FormsModule } from '@angular/forms';

import { AuthService } from '../../../core/services/auth.service';
import { ToastService } from '../../../core/services/toast.service';
import { ROLES, Role } from '../../../core/models/auth.model';
import { DialogDirective } from '../../../shared/directives/dialog.directive';

/** "Create token" dialog: name, role (up to the creator's own) and expiry. Emits the one-time secret. */
@Component({
  selector: 'app-create-token-dialog',
  standalone: true,
  imports: [FormsModule, DialogDirective],
  template: `
<div class="fixed inset-0 bg-kymo-deep/85 z-50 flex items-center justify-center p-4 overflow-y-auto" (click)="closed.emit()">
  <form appDialog (dismiss)="closed.emit()" aria-labelledby="create-token-title" (ngSubmit)="submit()"
        class="bg-kymo-panel border border-kymo-rule border-t-kymo-amber p-5 max-w-md w-full space-y-4" (click)="$event.stopPropagation()">
    <div>
      <h3 id="create-token-title" class="text-sm font-semibold uppercase tracking-[0.08em] text-kymo-ink">Create API token</h3>
      <p class="text-xs text-kymo-muted mt-1.5">Bearer token for scripts and CI: <span class="font-mono">Authorization: Bearer kymo_…</span></p>
    </div>
    <label class="flex flex-col gap-1">
      <span class="k-label">Name</span>
      <input class="k-input" name="name" required data-autofocus autocomplete="off" placeholder="e.g. ci-deploy" [ngModel]="name()" (ngModelChange)="name.set($event)" />
    </label>
    <div class="grid grid-cols-2 gap-3">
      <label class="flex flex-col gap-1">
        <span class="k-label">Role</span>
        <select class="k-input" name="role" [ngModel]="role()" (ngModelChange)="role.set($event)">
          @for (r of roles(); track r) { <option [value]="r">{{ r }}</option> }
        </select>
      </label>
      <label class="flex flex-col gap-1">
        <span class="k-label">Expires</span>
        <select class="k-input" name="exp" [ngModel]="expiry()" (ngModelChange)="expiry.set($event)">
          <option value="30">in 30 days</option><option value="90">in 90 days</option>
          <option value="365">in 365 days</option><option value="never">never</option>
        </select>
      </label>
    </div>
    <div class="flex justify-end gap-2 pt-1">
      <button type="button" class="k-btn" (click)="closed.emit()">Cancel</button>
      <button type="submit" class="k-btn k-btn-primary" [disabled]="!name().trim() || creating()">{{ creating() ? 'Creating…' : 'Create token' }}</button>
    </div>
  </form>
</div>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class CreateTokenDialogComponent {
  private auth = inject(AuthService);
  private toast = inject(ToastService);

  created = output<string>();
  closed = output<void>();

  name = signal('');
  role = signal<Role>('viewer');
  expiry = signal('90');
  creating = signal(false);

  /** Roles up to the user's own. */
  roles = computed(() => {
    const me = this.auth.role();
    return me ? ROLES.slice(0, ROLES.indexOf(me) + 1) : [];
  });

  submit(): void {
    const name = this.name().trim();
    if (!name || this.creating()) return;
    this.creating.set(true);
    const days = this.expiry() === 'never' ? null : +this.expiry();
    this.auth.createToken(name, this.role(), days).subscribe({
      next: r => { this.creating.set(false); this.created.emit(r.secret); },
      error: err => { this.creating.set(false); this.toast.apiError(err, 'Could not create token'); },
    });
  }
}
