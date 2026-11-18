import {
  Component, ChangeDetectionStrategy, inject, signal, computed, effect, DestroyRef,
} from '@angular/core';
import { Title } from '@angular/platform-browser';
import { Router, NavigationEnd, RouterLink, RouterLinkActive } from '@angular/router';
import { toSignal } from '@angular/core/rxjs-interop';
import { filter, map } from 'rxjs/operators';

import { AuthService } from '../../core/services/auth.service';
import { RefreshService } from '../../core/services/refresh.service';
import { ThemeService } from '../../core/services/theme.service';
import { IssuesService } from '../../core/services/issues.service';
import { PageContextService } from '../../core/services/page-context.service';
import { LogoComponent } from '../../shared/components/logo/logo.component';
import { UserMenuComponent } from '../user-menu/user-menu.component';
import { CommandPaletteComponent } from '../command-palette/command-palette.component';

export interface NavTab {
  path: string;
  label: string;
}

/** Fixed main navigation (digit hotkeys 1–6 in this order). The cluster scope lives in the scope bar. */
export const NAV_TABS: NavTab[] = [
  { path: '/dashboard',  label: 'Overview' },
  { path: '/issues',     label: 'Issues' },
  { path: '/workloads',  label: 'Workloads' },
  { path: '/jobs',       label: 'Jobs' },
  { path: '/events',     label: 'Events' },
  { path: '/clusters',   label: 'Clusters' },
];

/** Command palette "pages": the tabs plus deep links into sub-views. */
export const PALETTE_PAGES: NavTab[] = [
  ...NAV_TABS,
  { path: '/workloads?kind=Deployment',     label: 'Deployments' },
  { path: '/workloads?kind=StatefulSet',    label: 'StatefulSets' },
  { path: '/workloads?kind=DaemonSet',      label: 'DaemonSets' },
  { path: '/workloads/archived',            label: 'Archived & ignored' },
  { path: '/clusters?add=1',                label: 'Add cluster' },
  { path: '/settings/notifications',        label: 'Notification channels' },
  { path: '/settings/log-capture',          label: 'Error log capture' },
];

/** True when a keystroke should be left alone (typing in a field, or a modal is open). */
function isTypingOrModal(ev: KeyboardEvent): boolean {
  const t = ev.target as HTMLElement | null;
  if (t && (t.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(t.tagName))) return true;
  return !!document.querySelector('[aria-modal="true"]');
}

@Component({
  selector: 'app-topbar',
  standalone: true,
  imports: [RouterLink, RouterLinkActive, LogoComponent, CommandPaletteComponent, UserMenuComponent],
  templateUrl: './topbar.component.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: {
    class: 'block sticky top-0 z-40',
    '(document:keydown)': 'onKey($event)',
  },
})
export class TopbarComponent {
  private router  = inject(Router);
  private pageCtx = inject(PageContextService);
  private title   = inject(Title);
  refreshSvc      = inject(RefreshService);
  issues          = inject(IssuesService);
  themeSvc        = inject(ThemeService);

  paletteOpen    = signal(false);
  spinning       = signal(false);
  private now    = signal(Date.now());

  readonly isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);

  readonly tabs = NAV_TABS;
  private auth = inject(AuthService);
  readonly palettePages = computed<NavTab[]>(() => [
    ...PALETTE_PAGES,
    ...(this.auth.can('users:manage') ? [{ path: '/settings/users', label: 'Users' }] : []),
    { path: '/settings/tokens', label: 'API tokens' },
    ...(this.auth.can('audit:read') ? [{ path: '/settings/audit', label: 'Audit log' }] : []),
    ...(this.auth.authEnabled() ? [{ path: '#signout', label: 'Sign out' }] : []),
  ]);

  liveTitle = computed(() => this.refreshSvc.autoRefresh()
    ? `Auto-refresh on · ${this.updatedLabel()} — click to pause`
    : `Auto-refresh paused · ${this.updatedLabel()} — click to resume`);

  updatedLabel = computed(() => {
    const last = this.refreshSvc.lastUpdated();
    if (!last) return 'not updated';
    const s = Math.max(0, Math.floor((this.now() - last.getTime()) / 1000));
    if (s < 5) return 'updated now';
    if (s < 60) return `updated ${s}s ago`;
    const m = Math.floor(s / 60);
    return m < 60 ? `updated ${m}m ago` : `updated ${Math.floor(m / 60)}h ago`;
  });

  private url = toSignal(
    this.router.events.pipe(
      filter((e): e is NavigationEnd => e instanceof NavigationEnd),
      map(e => e.urlAfterRedirects),
    ),
    { initialValue: this.router.url },
  );

  /** Document title: detail pages show the container name (from PageContextService), others the tab name. */
  pageTitle = computed(() => {
    const segs = this.url().split(/[?#]/)[0].split('/').filter(Boolean);
    if (segs[0] === 'settings') {
      return ({ 'log-capture': 'Log capture', users: 'Users', tokens: 'API tokens', audit: 'Audit log' } as Record<string, string>)[segs[1]] ?? 'Notifications';
    }
    const tab = NAV_TABS.find(t => t.path === '/' + (segs[0] ?? 'dashboard'));
    if (segs.length > 1) return this.pageCtx.detailLabel() ?? tab?.label ?? null;
    return tab?.label ?? null;
  });

  constructor() {
    const id = setInterval(() => this.now.set(Date.now()), 1000);
    inject(DestroyRef).onDestroy(() => clearInterval(id));
    effect(() => {
      const t = this.pageTitle();
      this.title.setTitle(t ? `${t} · KYMO` : 'KYMO');
    });
  }

  refresh(): void {
    this.refreshSvc.trigger();
    this.spinning.set(true);
    setTimeout(() => this.spinning.set(false), 600);
  }

  onKey(ev: KeyboardEvent): void {
    // ⌘K / Ctrl+K: command palette (works from anywhere, including inputs).
    if ((ev.metaKey || ev.ctrlKey) && !ev.altKey && !ev.shiftKey && ev.key.toLowerCase() === 'k') {
      ev.preventDefault();
      if (!this.paletteOpen() && !document.querySelector('[aria-modal="true"]')) this.openPalette();
      return;
    }

    // 1–6: switch tabs.
    if (ev.ctrlKey || ev.metaKey || ev.altKey || !/^[1-6]$/.test(ev.key)) return;
    if (isTypingOrModal(ev)) return;
    const tab = this.tabs[+ev.key - 1];
    if (!tab) return;
    ev.preventDefault();
    this.router.navigateByUrl(tab.path);
  }

  openPalette(): void {
    this.paletteOpen.set(true);
  }
}
