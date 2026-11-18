import { Component, ChangeDetectionStrategy, inject, signal, computed } from '@angular/core';
import { FormsModule } from '@angular/forms';
import { forkJoin } from 'rxjs';

import { AuthService } from '../../../core/services/auth.service';
import { ToastService } from '../../../core/services/toast.service';
import { AddUserResult, Invite, Role, User, userLabel } from '../../../core/models/auth.model';
import { PageHeaderComponent } from '../../../shared/components/page-header/page-header.component';
import { ErrorStateComponent } from '../../../shared/components/error-state/error-state.component';
import { SkeletonComponent } from '../../../shared/components/skeleton/skeleton.component';
import { ConfirmDialogComponent } from '../../../shared/components/confirm-dialog/confirm-dialog.component';
import { CopyDialogComponent } from '../../../shared/components/copy-dialog/copy-dialog.component';
import { RoleChipComponent } from '../../../shared/components/role-chip/role-chip.component';
import { TimeagoPipe } from '../../../shared/pipes/timeago.pipe';
import { AddUserDialogComponent } from './add-user-dialog.component';
import { ActionsMenuComponent } from '../actions-menu.component';

interface LinkShown { title: string; message: string; value: string; expires: string; warning: string; note: string; }
const LINK_WARNING = 'Anyone with this link can use it. It is shown only once.';

@Component({
  selector: 'app-users',
  standalone: true,
  imports: [
    FormsModule, TimeagoPipe, PageHeaderComponent, ErrorStateComponent, SkeletonComponent,
    ConfirmDialogComponent, CopyDialogComponent, AddUserDialogComponent, RoleChipComponent, ActionsMenuComponent,
  ],
  template: `
<div class="space-y-4">
  <app-page-header title="Users">
    <ng-container subtitle>
      @if (!loaded()) { Loading… }
      @else { Who can sign in and what they can do · {{ users().length }} user{{ users().length === 1 ? '' : 's' }} }
    </ng-container>
    <button type="button" class="k-btn k-btn-primary" (click)="adding.set(true)">
      <svg class="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2.5" aria-hidden="true"><path stroke-linecap="square" d="M12 5v14M5 12h14" /></svg>
      Add user
    </button>
  </app-page-header>

  @if (error()) { <app-error-state message="Failed to load users" (retry)="load()" /> }

  @if (!loaded()) {
    <app-skeleton [rows]="4" />
  } @else {
    <div class="k-panel overflow-x-auto scrollbar-thin">
      <table class="k-table">
        <thead>
          <tr>
            <th>User</th><th class="hidden md:table-cell">Email</th><th>Role</th>
            <th class="hidden lg:table-cell">Login</th><th class="hidden lg:table-cell">Last login</th>
            <th class="w-10"><span class="sr-only">Actions</span></th>
          </tr>
        </thead>
        <tbody>
          @for (u of users(); track u.id) {
            <tr [class.opacity-60]="busyId() === u.id || u.status === 'disabled'">
              <td class="px-4 py-2.5 text-xs max-w-[18rem]">
                <div class="flex items-center gap-2 min-w-0">
                  <span class="k-led" [class]="u.status === 'active' ? 'bg-kymo-running' : 'bg-kymo-unknown'" [title]="u.status" aria-hidden="true"></span>
                  <span class="text-kymo-ink truncate" [title]="u.name ? u.username + ' · ' + u.name : u.username">{{ u.username }}</span>
                  @if (u.name) { <span class="text-kymo-muted truncate hidden sm:inline">{{ u.name }}</span> }
                  @if (u.id === me()?.id) { <span class="text-kymo-faint flex-none">you</span> }
                  @if (u.status === 'disabled') { <span class="inline-flex items-center h-4 px-1 text-[10px] border border-kymo-rule text-kymo-muted flex-none">disabled</span> }
                  @if (u.must_change_password) { <span class="inline-flex items-center h-4 px-1 text-[10px] border border-kymo-amber/50 text-kymo-amber flex-none" title="Must change the password at next sign-in">temp password</span> }
                </div>
                @if (editEmailId() === u.id) {
                  <form class="flex items-center gap-1 mt-1.5" (ngSubmit)="saveEmail(u)" aria-label="Edit email">
                    <input class="k-input !h-7 w-44 min-w-0" type="email" name="email" autocomplete="off" data-autofocus [attr.aria-label]="'Email of ' + u.username" [ngModel]="emailDraft()" (ngModelChange)="emailDraft.set($event)" />
                    <button type="submit" class="k-btn k-btn-sm k-btn-primary" [disabled]="busyId() === u.id">Save</button>
                    <button type="button" class="k-btn k-btn-sm" (click)="editEmailId.set(null)">Cancel</button>
                  </form>
                }
              </td>
              <td class="px-4 py-2.5 text-xs text-kymo-muted hidden md:table-cell">
                <span class="block truncate max-w-[16rem]" [title]="u.email">{{ u.email || '—' }}</span>
              </td>
              <td class="px-4 py-2.5"><app-role-chip [role]="u.role" /></td>
              <td class="px-4 py-2.5 text-xs text-kymo-muted hidden lg:table-cell whitespace-nowrap">{{ method(u) }}</td>
              <td class="px-4 py-2.5 text-xs text-kymo-muted whitespace-nowrap hidden lg:table-cell">{{ u.last_login_at ? (u.last_login_at | timeago) : 'never' }}</td>
              <td class="px-2 py-1.5 text-right">
                @if (hasActions(u)) {
                  <app-actions-menu [label]="'Actions for ' + u.username" [disabled]="busyId() === u.id">
                    @for (r of roleOptions(u); track r) {
                      <button type="button" role="menuitem" [class]="menuItem" (click)="changeRole(u, r)">Change role to {{ r }}</button>
                    }
                    @if (manageable(u)) {
                      <button type="button" role="menuitem" [class]="menuItem" (click)="startEmail(u)">Edit email</button>
                    }
                    @if (canResetLink(u)) {
                      <button type="button" role="menuitem" [class]="menuItem" (click)="resetLink(u)">Reset password link</button>
                    }
                    @if (canToggle(u)) {
                      <button type="button" role="menuitem" [class]="menuItem" (click)="toggleStatus(u)">{{ u.status === 'active' ? 'Disable' : 'Enable' }}</button>
                    }
                    @if (canTransfer(u)) {
                      <button type="button" role="menuitem" [class]="menuItem + ' text-kymo-danger'" (click)="transferTarget.set(u)">Make owner…</button>
                    }
                  </app-actions-menu>
                }
              </td>
            </tr>
          }
        </tbody>
      </table>
    </div>

    @if (invites().length) {
      <section class="k-panel" aria-labelledby="pending-title">
        <h2 id="pending-title" class="k-label !text-kymo-ink px-4 py-2 border-b border-kymo-rule">Pending setup links</h2>
        <ul class="divide-y divide-kymo-rule">
          @for (i of invites(); track i.id) {
            <li class="flex items-center gap-3 px-4 py-1.5 text-xs min-w-0">
              <span class="text-kymo-ink truncate" [title]="i.email">{{ i.username }}</span>
              <app-role-chip [role]="i.role" />
              <span class="text-kymo-muted ml-auto whitespace-nowrap" [title]="i.expires_at">expires {{ i.expires_at | timeago }}</span>
              <button type="button" class="k-btn k-btn-sm flex-none" (click)="revokeTarget.set(i)" [attr.aria-label]="'Revoke setup link for ' + i.username">Revoke</button>
            </li>
          }
        </ul>
      </section>
    }
  }
</div>

@if (adding()) {
  <app-add-user-dialog [assignable]="assignable()" (created)="onCreated($event)" (closed)="adding.set(false)" />
}
@if (shown(); as l) {
  <app-copy-dialog [title]="l.title" [message]="l.message" [value]="l.value" [expiresAt]="l.expires" [note]="l.note"
    [warning]="l.warning" (closed)="shown.set(null)" />
}
@if (transferTarget(); as t) {
  <app-confirm-dialog title="Transfer ownership?"
    [message]="'Make ' + label(t) + ' the owner of this KYMO installation. You will become an admin. Only the new owner can undo this.'"
    confirmLabel="Transfer ownership" (confirmed)="confirmTransfer()" (cancelled)="transferTarget.set(null)" />
}
@if (revokeTarget(); as i) {
  <app-confirm-dialog title="Revoke setup link?" [message]="'The setup link for ' + i.username + ' will stop working.'"
    confirmLabel="Revoke link" (confirmed)="confirmRevoke()" (cancelled)="revokeTarget.set(null)" />
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
  adding = signal(false);
  editEmailId = signal<number | null>(null);
  emailDraft = signal('');
  label = userLabel;
  readonly menuItem = 'w-full px-3 py-2 text-xs text-left hover:bg-kymo-raised disabled:opacity-40 disabled:cursor-not-allowed';
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
  hasActions(u: User): boolean { return this.manageable(u) || this.canResetLink(u) || this.canTransfer(u); }
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
        this.shown.set({ title: 'Password reset link', message: `Share this link with ${userLabel(u)}. It is valid for 1 hour.`, value: this.abs(r.url), expires: r.expires_at, warning: LINK_WARNING, note: '' });
      },
      error: err => { this.busyId.set(null); this.toast.apiError(err, 'Could not create reset link'); },
    });
  }

  onCreated(e: { result: AddUserResult; username: string }): void {
    this.adding.set(false);
    const r = e.result;
    const where = `${location.origin}/login with username "${e.username}"`;
    if (r.method === 'link') {
      this.shown.set({
        title: 'Setup link', message: `Share this link with ${e.username}. It is valid for 7 days.`, value: this.abs(r.url), expires: r.expires_at, warning: LINK_WARNING,
        note: `Open the link above and choose your KYMO password. After that, sign in at ${where}.`,
      });
    } else if (r.password) {
      this.shown.set({
        title: 'Temporary password', message: `Share this password with ${e.username}. They must change it at first sign-in.`, value: r.password, expires: '',
        warning: 'This password is shown only once. KYMO cannot display it again.',
        note: `Sign in at ${where} and the temporary password above. You will be asked to choose a new password.`,
      });
    } else {
      this.toast.success(`${e.username} added. They must change the password you set at first sign-in.`);
    }
    this.load();
  }

  confirmRevoke(): void {
    const i = this.revokeTarget();
    if (!i) return;
    this.revokeTarget.set(null);
    this.auth.revokeInvite(i.id).subscribe({
      next: () => { this.toast.success('Setup link revoked'); this.load(); },
      error: err => this.toast.apiError(err, 'Could not revoke link'),
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
