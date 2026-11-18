import { Component, ChangeDetectionStrategy, inject, signal, computed } from '@angular/core';
import { FormsModule } from '@angular/forms';
import { forkJoin } from 'rxjs';

import { AuthService } from '../../../core/services/auth.service';
import { ToastService } from '../../../core/services/toast.service';
import { Invite, Role, USERNAME_RE, User, userLabel } from '../../../core/models/auth.model';
import { PageHeaderComponent } from '../../../shared/components/page-header/page-header.component';
import { ErrorStateComponent } from '../../../shared/components/error-state/error-state.component';
import { SkeletonComponent } from '../../../shared/components/skeleton/skeleton.component';
import { ConfirmDialogComponent } from '../../../shared/components/confirm-dialog/confirm-dialog.component';
import { CopyDialogComponent } from '../../../shared/components/copy-dialog/copy-dialog.component';
import { RoleChipComponent } from '../../../shared/components/role-chip/role-chip.component';
import { TimeagoPipe } from '../../../shared/pipes/timeago.pipe';
import { SettingsNavComponent } from '../settings-nav.component';

interface LinkShown { title: string; message: string; url: string; expires: string; }

@Component({
  selector: 'app-users',
  standalone: true,
  imports: [
    FormsModule, TimeagoPipe, PageHeaderComponent, ErrorStateComponent, SkeletonComponent,
    ConfirmDialogComponent, CopyDialogComponent, RoleChipComponent, SettingsNavComponent,
  ],
  template: `
<div class="space-y-4">
  <app-settings-nav />
  <app-page-header title="Users">
    <ng-container subtitle>{{ loaded() ? users().length + ' user' + (users().length === 1 ? '' : 's') : 'Loading…' }}</ng-container>
  </app-page-header>

  @if (error()) { <app-error-state message="Failed to load users" (retry)="load()" /> }

  @if (!loaded()) {
    <app-skeleton [rows]="4" />
  } @else {
    <div class="k-panel overflow-x-auto scrollbar-thin">
      <table class="k-table">
        <thead>
          <tr>
            <th>Username</th><th>Name</th><th>Email</th><th>Role</th><th>Status</th>
            <th class="hidden md:table-cell">Login</th><th class="hidden lg:table-cell">Last login</th>
            <th><span class="sr-only">Actions</span></th>
          </tr>
        </thead>
        <tbody>
          @for (u of users(); track u.id) {
            <tr [class.opacity-60]="busyId() === u.id || u.status === 'disabled'">
              <td class="px-4 py-2.5 text-xs text-kymo-ink">{{ u.username }}@if (u.id === me()?.id) { <span class="text-kymo-faint"> (you)</span> }</td>
              <td class="px-4 py-2.5 text-xs text-kymo-muted">{{ u.name || '—' }}</td>
              <td class="px-4 py-2.5 text-xs text-kymo-muted">
                @if (editEmailId() === u.id) {
                  <form class="flex items-center gap-1" (ngSubmit)="saveEmail(u)" aria-label="Edit email">
                    <input class="k-input !h-7 w-48" type="email" name="email" autocomplete="off" [attr.aria-label]="'Email of ' + u.username" [ngModel]="emailDraft()" (ngModelChange)="emailDraft.set($event)" />
                    <button type="submit" class="k-btn k-btn-sm k-btn-primary" [disabled]="busyId() === u.id">Save</button>
                    <button type="button" class="k-btn k-btn-sm" (click)="editEmailId.set(null)">Cancel</button>
                  </form>
                } @else {
                  <span>{{ u.email || '—' }}</span>
                  @if (manageable(u)) {
                    <button type="button" class="k-btn k-btn-sm ml-2" [attr.aria-label]="'Edit email of ' + u.username" (click)="startEmail(u)">Edit</button>
                  }
                }
              </td>
              <td class="px-4 py-2.5 text-xs">
                @if (roleOptions(u).length) {
                  <select class="k-input !h-7" [attr.aria-label]="'Role of ' + u.username" [ngModel]="u.role" (ngModelChange)="changeRole(u, $event)" [disabled]="busyId() === u.id">
                    <option [value]="u.role" disabled>{{ u.role }}</option>
                    @for (r of roleOptions(u); track r) { <option [value]="r">{{ r }}</option> }
                  </select>
                } @else { <app-role-chip [role]="u.role" /> }
              </td>
              <td class="px-4 py-2.5 text-xs" [class]="u.status === 'active' ? 'text-kymo-running' : 'text-kymo-muted'">{{ u.status }}</td>
              <td class="px-4 py-2.5 text-xs text-kymo-muted hidden md:table-cell">{{ method(u) }}</td>
              <td class="px-4 py-2.5 text-xs text-kymo-muted whitespace-nowrap hidden lg:table-cell">{{ u.last_login_at ? (u.last_login_at | timeago) : 'never' }}</td>
              <td class="px-4 py-2.5">
                <div class="flex justify-end gap-1.5 flex-wrap">
                  @if (canResetLink(u)) {
                    <button type="button" class="k-btn k-btn-sm" [disabled]="busyId() === u.id" (click)="resetLink(u)">Reset link</button>
                  }
                  @if (canToggle(u)) {
                    <button type="button" class="k-btn k-btn-sm" [disabled]="busyId() === u.id" (click)="toggleStatus(u)">{{ u.status === 'active' ? 'Disable' : 'Enable' }}</button>
                  }
                  @if (canTransfer(u)) {
                    <button type="button" class="k-btn k-btn-sm k-btn-danger" [disabled]="busyId() === u.id" (click)="transferTarget.set(u)">Make owner</button>
                  }
                </div>
              </td>
            </tr>
          }
        </tbody>
      </table>
    </div>

    <section class="space-y-2 pt-2" aria-labelledby="invite-title">
      <h2 id="invite-title" class="k-label !text-kymo-ink">Invite a user</h2>
      <form class="k-panel p-4 flex items-end gap-3 flex-wrap" (ngSubmit)="invite()" aria-label="Invite user">
        <label class="flex flex-col gap-1">
          <span class="k-label">Username</span>
          <input class="k-input w-48" name="username" required autocomplete="off" autocapitalize="none" spellcheck="false" [ngModel]="inviteUsername()" (ngModelChange)="inviteUsername.set($event)" />
          @if (inviteUsername().trim() && !usernameValid()) { <span class="text-[11px] text-kymo-danger">2–64 chars: a-z, 0-9, . _ -</span> }
        </label>
        <label class="flex flex-col gap-1">
          <span class="k-label">Email (optional)</span>
          <input class="k-input w-64" type="email" name="email" autocomplete="off" [ngModel]="inviteEmail()" (ngModelChange)="inviteEmail.set($event)" />
        </label>
        <label class="flex flex-col gap-1">
          <span class="k-label">Role</span>
          <select class="k-input" name="role" [ngModel]="inviteRole()" (ngModelChange)="inviteRole.set($event)">
            @for (r of assignable(); track r) { <option [value]="r">{{ r }}</option> }
          </select>
        </label>
        <button type="submit" class="k-btn k-btn-primary" [disabled]="!usernameValid() || inviting()">{{ inviting() ? 'Creating…' : 'Create invite' }}</button>
      </form>
      <p class="text-xs text-kymo-muted">Invites are valid for 7 days. KYMO does not send email; share the link yourself.</p>
    </section>

    @if (invites().length) {
      <section class="space-y-2" aria-labelledby="pending-title">
        <h2 id="pending-title" class="k-label !text-kymo-ink">Pending invites</h2>
        <div class="k-panel overflow-x-auto scrollbar-thin">
          <table class="k-table">
            <thead><tr><th>Username</th><th>Role</th><th>Expires</th><th><span class="sr-only">Actions</span></th></tr></thead>
            <tbody>
              @for (i of invites(); track i.id) {
                <tr>
                  <td class="px-4 py-2.5 text-xs text-kymo-ink">{{ i.username }}@if (i.email) { <span class="text-kymo-muted"> ({{ i.email }})</span> }</td>
                  <td class="px-4 py-2.5"><app-role-chip [role]="i.role" /></td>
                  <td class="px-4 py-2.5 text-xs text-kymo-muted" [title]="i.expires_at">{{ i.expires_at | timeago }}</td>
                  <td class="px-4 py-2.5"><div class="flex justify-end"><button type="button" class="k-btn k-btn-sm" (click)="revokeTarget.set(i)">Revoke</button></div></td>
                </tr>
              }
            </tbody>
          </table>
        </div>
      </section>
    }
  }
</div>

@if (shown(); as l) {
  <app-copy-dialog [title]="l.title" [message]="l.message" [value]="l.url" [expiresAt]="l.expires"
    warning="Anyone with this link can use it. It is shown only once." (closed)="shown.set(null)" />
}
@if (transferTarget(); as t) {
  <app-confirm-dialog title="Transfer ownership?"
    [message]="'Make ' + label(t) + ' the owner of this KYMO installation. You will become an admin. Only the new owner can undo this.'"
    confirmLabel="Transfer ownership" (confirmed)="confirmTransfer()" (cancelled)="transferTarget.set(null)" />
}
@if (revokeTarget(); as i) {
  <app-confirm-dialog title="Revoke invite?" [message]="'The invite link for ' + i.username + ' will stop working.'"
    confirmLabel="Revoke invite" (confirmed)="confirmRevoke()" (cancelled)="revokeTarget.set(null)" />
}
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class UsersComponent {
  private auth = inject(AuthService);
  private toast = inject(ToastService);

  me = this.auth.user;
  users = signal<User[]>([]);
  invites = signal<Invite[]>([]);
  loaded = signal(false);
  error = signal(false);
  busyId = signal<number | null>(null);
  inviting = signal(false);
  inviteUsername = signal('');
  inviteEmail = signal('');
  editEmailId = signal<number | null>(null);
  emailDraft = signal('');
  label = userLabel;
  usernameValid = computed(() => USERNAME_RE.test(this.inviteUsername().trim().toLowerCase()));
  inviteRole = signal<Role>('viewer');
  shown = signal<LinkShown | null>(null);
  transferTarget = signal<User | null>(null);
  revokeTarget = signal<Invite | null>(null);

  private isOwner = computed(() => this.auth.role() === 'owner');
  /** Roles the current user may hand out: only the owner can grant admin. */
  assignable = computed<Role[]>(() => (this.isOwner() ? ['viewer', 'operator', 'admin'] : ['viewer', 'operator']));

  constructor() { this.load(); }

  load(): void {
    forkJoin({ users: this.auth.users(), invites: this.auth.invites() }).subscribe({
      next: ({ users, invites }) => { this.users.set(users); this.invites.set(invites); this.loaded.set(true); this.error.set(false); },
      error: () => { this.error.set(true); this.loaded.set(true); },
    });
  }

  method(u: User): string {
    return u.has_password && u.sso ? 'Password + SSO' : u.has_password ? 'Password' : u.sso ? 'SSO' : '—';
  }

  /** Mirrors the backend: nobody changes the owner or themselves; only the owner touches admins. */
  manageable(u: User): boolean {
    if (u.id === this.me()?.id || u.role === 'owner') return false;
    return this.isOwner() || u.role !== 'admin';
  }
  roleOptions(u: User): Role[] { return this.manageable(u) ? this.assignable().filter(r => r !== u.role) : []; }
  canToggle(u: User): boolean { return this.manageable(u); }
  canResetLink(u: User): boolean { return u.has_password && u.status === 'active' && (u.id === this.me()?.id || this.manageable(u)); }
  canTransfer(u: User): boolean { return this.isOwner() && u.role === 'admin' && u.status === 'active'; }

  private patch(u: User, body: { role?: Role; status?: 'active' | 'disabled'; email?: string }, ok: string): void {
    this.busyId.set(u.id);
    this.auth.updateUser(u.id, body).subscribe({
      next: () => { this.busyId.set(null); this.toast.success(ok); this.load(); },
      error: err => { this.busyId.set(null); this.toast.apiError(err, 'Update failed'); this.load(); },
    });
  }
  changeRole(u: User, role: Role): void { if (role !== u.role) this.patch(u, { role }, `${userLabel(u)} is now ${role}`); }
  toggleStatus(u: User): void {
    const status = u.status === 'active' ? 'disabled' : 'active';
    this.patch(u, { status }, `${userLabel(u)} ${status === 'active' ? 'enabled' : 'disabled'}`);
  }

  startEmail(u: User): void { this.emailDraft.set(u.email); this.editEmailId.set(u.id); }
  saveEmail(u: User): void {
    const email = this.emailDraft().trim();
    if (email === u.email) { this.editEmailId.set(null); return; }
    this.busyId.set(u.id);
    this.auth.updateUser(u.id, { email }).subscribe({
      next: () => { this.busyId.set(null); this.editEmailId.set(null); this.toast.success('Email updated'); this.load(); },
      error: err => { this.busyId.set(null); this.toast.apiError(err, 'Could not update email'); },
    });
  }

  resetLink(u: User): void {
    this.busyId.set(u.id);
    this.auth.resetLink(u.id).subscribe({
      next: r => {
        this.busyId.set(null);
        this.shown.set({ title: 'Password reset link', message: `Share this link with ${userLabel(u)}. It is valid for 1 hour.`, url: this.abs(r.url), expires: r.expires_at });
      },
      error: err => { this.busyId.set(null); this.toast.apiError(err, 'Could not create reset link'); },
    });
  }

  invite(): void {
    const username = this.inviteUsername().trim().toLowerCase();
    if (!USERNAME_RE.test(username) || this.inviting()) return;
    const email = this.inviteEmail().trim();
    this.inviting.set(true);
    this.auth.createInvite({ username, role: this.inviteRole(), ...(email ? { email } : {}) }).subscribe({
      next: r => {
        this.inviting.set(false);
        this.inviteUsername.set('');
        this.inviteEmail.set('');
        this.shown.set({ title: 'Invite link', message: `Share this link with ${username}. It is valid for 7 days.`, url: this.abs(r.url), expires: r.expires_at });
        this.load();
      },
      error: err => { this.inviting.set(false); this.toast.apiError(err, 'Could not create invite'); },
    });
  }

  confirmRevoke(): void {
    const i = this.revokeTarget();
    if (!i) return;
    this.revokeTarget.set(null);
    this.auth.revokeInvite(i.id).subscribe({
      next: () => { this.toast.success('Invite revoked'); this.load(); },
      error: err => this.toast.apiError(err, 'Could not revoke invite'),
    });
  }

  confirmTransfer(): void {
    const t = this.transferTarget();
    if (!t) return;
    this.transferTarget.set(null);
    this.busyId.set(t.id);
    this.auth.transferOwnership(t.id).subscribe({
      next: () => {
        this.busyId.set(null);
        this.toast.success(`${userLabel(t)} is now the owner`);
        this.auth.loadMe().subscribe(() => this.load());
      },
      error: err => { this.busyId.set(null); this.toast.apiError(err, 'Transfer failed'); },
    });
  }

  /** The API may return a path-only URL; make it absolute for sharing. */
  private abs(url: string): string { return url.startsWith('/') ? location.origin + url : url; }
}
