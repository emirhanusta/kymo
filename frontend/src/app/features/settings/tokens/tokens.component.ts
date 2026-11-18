import { Component, ChangeDetectionStrategy, inject, signal, computed } from '@angular/core';
import { FormsModule } from '@angular/forms';

import { AuthService } from '../../../core/services/auth.service';
import { ToastService } from '../../../core/services/toast.service';
import { ApiToken } from '../../../core/models/auth.model';
import { PageHeaderComponent } from '../../../shared/components/page-header/page-header.component';
import { ErrorStateComponent } from '../../../shared/components/error-state/error-state.component';
import { SkeletonComponent } from '../../../shared/components/skeleton/skeleton.component';
import { EmptyStateComponent } from '../../../shared/components/empty-state/empty-state.component';
import { ConfirmDialogComponent } from '../../../shared/components/confirm-dialog/confirm-dialog.component';
import { CopyDialogComponent } from '../../../shared/components/copy-dialog/copy-dialog.component';
import { RoleChipComponent } from '../../../shared/components/role-chip/role-chip.component';
import { TimeagoPipe } from '../../../shared/pipes/timeago.pipe';
import { CreateTokenDialogComponent } from './create-token-dialog.component';

@Component({
  selector: 'app-tokens',
  standalone: true,
  imports: [
    FormsModule, TimeagoPipe, PageHeaderComponent, ErrorStateComponent, SkeletonComponent, EmptyStateComponent,
    ConfirmDialogComponent, CopyDialogComponent, RoleChipComponent, CreateTokenDialogComponent,
  ],
  template: `
<div class="space-y-4">
  <app-page-header title="API tokens">
    <ng-container subtitle>
      @if (!loaded()) { Loading… }
      @else { Bearer tokens for scripts and CI{{ tokens().length ? ' · ' + activeCount() + ' active' : '' }} }
    </ng-container>
    @if (isAdmin()) {
      <label class="inline-flex items-center gap-2 text-xs text-kymo-muted cursor-pointer">
        <input type="checkbox" [ngModel]="all()" (ngModelChange)="setAll($event)" /> All users
      </label>
    }
    <button type="button" class="k-btn k-btn-primary" (click)="creating.set(true)">
      <svg class="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2.5" aria-hidden="true"><path stroke-linecap="square" d="M12 5v14M5 12h14" /></svg>
      Create token
    </button>
  </app-page-header>

  @if (error()) { <app-error-state message="Failed to load tokens" (retry)="load()" /> }

  @if (!loaded()) {
    <app-skeleton [rows]="3" />
  } @else if (tokens().length === 0 && !error()) {
    <div class="k-panel">
      <app-empty-state icon="events" message="No API tokens" hint="Create a token to call the KYMO API from scripts and CI.">
        <button type="button" class="k-btn k-btn-primary k-btn-sm mt-4" (click)="creating.set(true)">Create token</button>
      </app-empty-state>
    </div>
  } @else if (tokens().length) {
    <div class="k-panel overflow-x-auto scrollbar-thin">
      <table class="k-table">
        <thead>
          <tr>
            <th>Name</th><th class="hidden sm:table-cell">Prefix</th><th>Role</th>
            @if (all()) { <th>Owner</th> }
            <th class="hidden md:table-cell">Created</th><th class="hidden md:table-cell">Expires</th>
            <th class="hidden lg:table-cell">Last used</th><th class="w-10"><span class="sr-only">Actions</span></th>
          </tr>
        </thead>
        <tbody>
          @for (t of tokens(); track t.id) {
            <tr [class.opacity-60]="!!t.revoked_at">
              <td class="px-4 py-2.5 text-xs max-w-[16rem]">
                <span class="inline-flex items-center gap-2 min-w-0 max-w-full">
                  <span class="text-kymo-ink truncate" [class.line-through]="!!t.revoked_at" [title]="t.name">{{ t.name }}</span>
                  @if (t.revoked_at) { <span class="inline-flex items-center h-4 px-1 text-[10px] border border-kymo-danger/50 text-kymo-danger flex-none" [title]="t.revoked_at">revoked</span> }
                </span>
              </td>
              <td class="px-4 py-2.5 text-xs font-mono text-kymo-muted hidden sm:table-cell whitespace-nowrap">{{ t.prefix }}…</td>
              <td class="px-4 py-2.5"><app-role-chip [role]="t.role" /></td>
              @if (all()) { <td class="px-4 py-2.5 text-xs text-kymo-muted">{{ t.user_username || t.user_email || '—' }}</td> }
              <td class="px-4 py-2.5 text-xs text-kymo-muted whitespace-nowrap hidden md:table-cell" [title]="t.created_at">{{ t.created_at | timeago }}</td>
              <td class="px-4 py-2.5 text-xs text-kymo-muted whitespace-nowrap hidden md:table-cell" [title]="t.expires_at || ''">{{ t.expires_at ? (t.expires_at | timeago) : 'never' }}</td>
              <td class="px-4 py-2.5 text-xs text-kymo-muted whitespace-nowrap hidden lg:table-cell">{{ t.last_used_at ? (t.last_used_at | timeago) : 'never' }}</td>
              <td class="px-2 py-1.5 text-right">
                @if (!t.revoked_at) { <button type="button" class="k-btn k-btn-sm" (click)="revokeTarget.set(t)" [attr.aria-label]="'Revoke ' + t.name">Revoke</button> }
              </td>
            </tr>
          }
        </tbody>
      </table>
    </div>
  }
</div>

@if (creating()) {
  <app-create-token-dialog (closed)="creating.set(false)" (created)="onCreated($event)" />
}
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
  creating = signal(false);
  secret = signal<string | null>(null);
  revokeTarget = signal<ApiToken | null>(null);

  activeCount = computed(() => this.tokens().filter(t => !t.revoked_at).length);

  constructor() { this.load(); }

  setAll(v: boolean): void { this.all.set(v); this.load(); }

  load(): void {
    this.auth.tokens(this.all()).subscribe({
      next: t => { this.tokens.set(t); this.loaded.set(true); this.error.set(false); },
      error: () => { this.error.set(true); this.loaded.set(true); },
    });
  }

  onCreated(secret: string): void {
    this.creating.set(false);
    this.secret.set(secret);
    this.load();
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
