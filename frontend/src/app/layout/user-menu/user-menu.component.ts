import { Component, ChangeDetectionStrategy, ElementRef, computed, inject, signal } from '@angular/core';
import { Router, RouterLink } from '@angular/router';

import { AuthService } from '../../core/services/auth.service';
import { ChangePasswordDialogComponent } from './change-password-dialog.component';
import { ProfileDialogComponent } from './profile-dialog.component';
import { RoleChipComponent } from '../../shared/components/role-chip/role-chip.component';

/** Far-right of the top bar: user menu, or an "auth disabled" chip when authentication is off. */
@Component({
  selector: 'app-user-menu',
  standalone: true,
  imports: [RouterLink, RoleChipComponent, ChangePasswordDialogComponent, ProfileDialogComponent],
  template: `
    @if (!auth.authEnabled()) {
      <span class="inline-flex items-center h-[1.875rem] px-2 text-[10px] tracking-[0.06em] border border-kymo-amber/60 text-kymo-amber"
            title="Authentication is disabled (auth.enabled: false). Everyone has full access.">auth disabled</span>
    } @else if (auth.user(); as u) {
      <div class="relative">
        <button type="button" class="k-btn gap-2 px-2" (click)="open.update(v => !v)" aria-haspopup="menu" [attr.aria-expanded]="open()" aria-label="User menu">
          <span class="w-5 h-5 inline-flex items-center justify-center text-[10px] font-semibold bg-kymo-amber text-kymo-deep" aria-hidden="true">{{ initials() }}</span>
          <span class="hidden xl:inline max-w-[120px] truncate">{{ u.username }}</span>
          <span class="hidden xl:inline-flex"><app-role-chip [role]="u.role" /></span>
        </button>
        @if (open()) {
          <div class="absolute right-0 top-full mt-1 bg-kymo-deep border border-kymo-rule z-50 min-w-[220px] py-1" role="menu">
            <div class="px-3 py-2 border-b border-kymo-rule">
              <p class="text-xs text-kymo-ink truncate">{{ u.username }}</p>
              @if (u.name && u.name !== u.username) { <p class="text-[11px] text-kymo-muted truncate">{{ u.name }}</p> }
            </div>
            <button type="button" role="menuitem" class="w-full px-3 py-2 text-xs text-left hover:bg-kymo-raised" (click)="open.set(false); profile.set(true)">Profile</button>
            @if (u.has_password) {
              <button type="button" role="menuitem" class="w-full px-3 py-2 text-xs text-left hover:bg-kymo-raised" (click)="open.set(false); changePw.set(true)">Change password</button>
            }
            <a role="menuitem" routerLink="/settings/tokens" class="block px-3 py-2 text-xs hover:bg-kymo-raised" (click)="open.set(false)">API tokens</a>
            <button type="button" role="menuitem" class="w-full px-3 py-2 text-xs text-left hover:bg-kymo-raised text-kymo-danger" (click)="signOut()">Sign out</button>
          </div>
        }
      </div>
    }
    @if (profile()) { <app-profile-dialog (closed)="profile.set(false)" /> }
    @if (changePw()) { <app-change-password-dialog (closed)="changePw.set(false)" /> }
  `,
  host: {
    '(document:click)': 'onDocClick($event)',
    '(document:keydown.escape)': 'open.set(false)',
  },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class UserMenuComponent {
  auth = inject(AuthService);
  private router = inject(Router);
  private host = inject<ElementRef<HTMLElement>>(ElementRef);

  open = signal(false);
  changePw = signal(false);
  profile = signal(false);
  initials = computed(() => {
    const parts = (this.auth.user()?.name || this.auth.user()?.username || '').trim().split(/\s+/).filter(Boolean);
    return ((parts[0]?.[0] ?? '?') + (parts.length > 1 ? parts[parts.length - 1][0] : '')).toUpperCase();
  });

  onDocClick(ev: MouseEvent): void {
    if (this.open() && !this.host.nativeElement.contains(ev.target as Node)) this.open.set(false);
  }

  signOut(): void {
    this.open.set(false);
    this.auth.logout().subscribe({ complete: () => this.router.navigate(['/login']), error: () => this.router.navigate(['/login']) });
  }
}
