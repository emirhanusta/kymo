import { Component, ChangeDetectionStrategy, inject, signal, computed } from '@angular/core';
import { FormsModule } from '@angular/forms';

import { AuthService } from '../../../core/services/auth.service';
import { ToastService } from '../../../core/services/toast.service';
import { ApiToken, ROLES, Role } from '../../../core/models/auth.model';
import { PageHeaderComponent } from '../../../shared/components/page-header/page-header.component';
import { ErrorStateComponent } from '../../../shared/components/error-state/error-state.component';
import { SkeletonComponent } from '../../../shared/components/skeleton/skeleton.component';
import { EmptyStateComponent } from '../../../shared/components/empty-state/empty-state.component';
import { ConfirmDialogComponent } from '../../../shared/components/confirm-dialog/confirm-dialog.component';
import { CopyDialogComponent } from '../../../shared/components/copy-dialog/copy-dialog.component';
import { RoleChipComponent } from '../../../shared/components/role-chip/role-chip.component';
import { TimeagoPipe } from '../../../shared/pipes/timeago.pipe';
import { SettingsNavComponent } from '../settings-nav.component';

@Component({
  selector: 'app-tokens',
  standalone: true,
  imports: [
    FormsModule, TimeagoPipe, PageHeaderComponent, ErrorStateComponent, SkeletonComponent, EmptyStateComponent,
    ConfirmDialogComponent, CopyDialogComponent, RoleChipComponent, SettingsNavComponent,
  ],
  template: `
<div class="space-y-4">
  <app-settings-nav />
  <app-page-header title="API tokens">
    <ng-container subtitle>Bearer tokens for scripts and CI: <span class="font-mono">Authorization: Bearer kymo_…</span></ng-container>
    @if (isAdmin()) {
      <label class="inline-flex items-center gap-2 text-xs text-kymo-muted cursor-pointer">
        <input type="checkbox" [ngModel]="all()" (ngModelChange)="setAll($event)" /> All users
      </label>
    }
  </app-page-header>

  <form class="k-panel p-4 flex items-end gap-3 flex-wrap" (ngSubmit)="create()" aria-label="Create API token">
    <label class="flex flex-col gap-1">
      <span class="k-label">Name</span>
      <input class="k-input w-56" name="name" required autocomplete="off" placeholder="e.g. ci-deploy" [ngModel]="name()" (ngModelChange)="name.set($event)" />
    </label>
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
    <button type="submit" class="k-btn k-btn-primary" [disabled]="!name().trim() || creating()">{{ creating() ? 'Creating…' : 'Create token' }}</button>
  </form>

  @if (error()) { <app-error-state message="Failed to load tokens" (retry)="load()" /> }

  @if (!loaded()) {
    <app-skeleton [rows]="3" />
  } @else if (tokens().length === 0 && !error()) {
    <div class="k-panel"><app-empty-state icon="events" [compact]="true" message="No API tokens" hint="Create a token above to call the KYMO API from scripts." /></div>
  } @else if (tokens().length) {
    <div class="k-panel overflow-x-auto scrollbar-thin">
      <table class="k-table">
        <thead>
          <tr>
            <th>Name</th><th>Prefix</th><th>Role</th>
            @if (all()) { <th>Owner</th> }
            <th class="hidden md:table-cell">Created</th><th class="hidden md:table-cell">Expires</th>
            <th class="hidden lg:table-cell">Last used</th><th>Status</th><th><span class="sr-only">Actions</span></th>
          </tr>
        </thead>
        <tbody>
          @for (t of tokens(); track t.id) {
            <tr [class.opacity-60]="!!t.revoked_at">
              <td class="px-4 py-2.5 text-xs text-kymo-ink">{{ t.name }}</td>
              <td class="px-4 py-2.5 text-xs font-mono text-kymo-muted">{{ t.prefix }}</td>
              <td class="px-4 py-2.5"><app-role-chip [role]="t.role" /></td>
              @if (all()) { <td class="px-4 py-2.5 text-xs text-kymo-muted">{{ t.user_username || t.user_email || '—' }}</td> }
              <td class="px-4 py-2.5 text-xs text-kymo-muted whitespace-nowrap hidden md:table-cell">{{ t.created_at | timeago }}</td>
              <td class="px-4 py-2.5 text-xs text-kymo-muted whitespace-nowrap hidden md:table-cell">{{ t.expires_at ? (t.expires_at | timeago) : 'never' }}</td>
              <td class="px-4 py-2.5 text-xs text-kymo-muted whitespace-nowrap hidden lg:table-cell">{{ t.last_used_at ? (t.last_used_at | timeago) : 'never' }}</td>
              <td class="px-4 py-2.5 text-xs" [class]="t.revoked_at ? 'text-kymo-danger' : 'text-kymo-running'">{{ t.revoked_at ? 'revoked' : 'active' }}</td>
              <td class="px-4 py-2.5">
                <div class="flex justify-end">
                  @if (!t.revoked_at) { <button type="button" class="k-btn k-btn-sm" (click)="revokeTarget.set(t)">Revoke</button> }
                </div>
              </td>
            </tr>
          }
        </tbody>
      </table>
    </div>
  }
</div>

@if (secret(); as s) {
  <app-copy-dialog title="Token created" message="Use this as a Bearer token in the Authorization header." [value]="s"
    warning="Copy it now. This secret is shown only once and cannot be retrieved later." (closed)="secret.set(null)" />
}
@if (revokeTarget(); as t) {
  <app-confirm-dialog title="Revoke token?" [message]="'Anything using “' + t.name + '” (' + t.prefix + ') will lose access immediately.'"
    confirmLabel="Revoke token" (confirmed)="confirmRevoke()" (cancelled)="revokeTarget.set(null)" />
}
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class TokensComponent {
  private auth = inject(AuthService);
  private toast = inject(ToastService);

  isAdmin = computed(() => this.auth.can('tokens:manage_all'));
  tokens = signal<ApiToken[]>([]);
  loaded = signal(false);
  error = signal(false);
  all = signal(false);
  name = signal('');
  role = signal<Role>('viewer');
  expiry = signal('90');
  creating = signal(false);
  secret = signal<string | null>(null);
  revokeTarget = signal<ApiToken | null>(null);

  /** Roles up to the user's own. */
  roles = computed(() => {
    const me = this.auth.role();
    return me ? ROLES.slice(0, ROLES.indexOf(me) + 1) : [];
  });

  constructor() { this.load(); }

  setAll(v: boolean): void { this.all.set(v); this.load(); }

  load(): void {
    this.auth.tokens(this.all()).subscribe({
      next: t => { this.tokens.set(t); this.loaded.set(true); this.error.set(false); },
      error: () => { this.error.set(true); this.loaded.set(true); },
    });
  }

  create(): void {
    const name = this.name().trim();
    if (!name || this.creating()) return;
    this.creating.set(true);
    const days = this.expiry() === 'never' ? null : +this.expiry();
    this.auth.createToken(name, this.role(), days).subscribe({
      next: r => { this.creating.set(false); this.name.set(''); this.secret.set(r.secret); this.load(); },
      error: err => { this.creating.set(false); this.toast.apiError(err, 'Could not create token'); },
    });
  }

  confirmRevoke(): void {
    const t = this.revokeTarget();
    if (!t) return;
    this.revokeTarget.set(null);
    this.auth.revokeToken(t.id).subscribe({
      next: () => { this.toast.success('Token revoked'); this.load(); },
      error: err => this.toast.apiError(err, 'Could not revoke token'),
    });
  }
}
