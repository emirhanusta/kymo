import {
  Component, ChangeDetectionStrategy, inject, signal, computed, effect, ElementRef, DestroyRef,
} from '@angular/core';
import { Title } from '@angular/platform-browser';
import { Router, NavigationEnd, RouterLink, RouterLinkActive } from '@angular/router';
import { toSignal } from '@angular/core/rxjs-interop';
import { filter, map } from 'rxjs/operators';

import { RefreshService } from '../../core/services/refresh.service';
import { SourceStateService } from '../../core/services/source-state.service';
import { sourceDotClass, sourceStatusLabel } from '../../shared/utils/source';
import { SourceStatus } from '../../core/models/container.model';
import { PageContextService } from '../../core/services/page-context.service';
import { LogoComponent } from '../../shared/components/logo/logo.component';
import { CommandPaletteComponent } from '../command-palette/command-palette.component';

export interface NavTab {
  path: string;
  label: string;
}

/** Fixed main navigation (digit hotkeys 1–4 in this order). The source selector only filters data. */
export const NAV_TABS: NavTab[] = [
  { path: '/dashboard',  label: 'Overview' },
  { path: '/workloads',  label: 'Workloads' },
  { path: '/events',     label: 'Events' },
  { path: '/sources',    label: 'Sources' },
];

/** Command palette "pages": the tabs plus deep links into sub-views. */
export const PALETTE_PAGES: NavTab[] = [
  ...NAV_TABS,
  { path: '/workloads?kind=Deployment',     label: 'Deployments' },
  { path: '/workloads?kind=CronJob',        label: 'CronJobs' },
  { path: '/workloads?kind=Compose',        label: 'Compose projects' },
  { path: '/workloads?kind=Container',      label: 'Standalone containers' },
  { path: '/workloads/archived',            label: 'Archived & ignored' },
  { path: '/sources?add=1',                 label: 'Add source' },
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
  imports: [RouterLink, RouterLinkActive, LogoComponent, CommandPaletteComponent],
  templateUrl: './topbar.component.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: {
    class: 'block sticky top-0 z-40',
    '(document:click)': 'onDocumentClick($event)',
    '(document:keydown)': 'onKey($event)',
  },
})
export class TopbarComponent {
  private router  = inject(Router);
  private host    = inject<ElementRef<HTMLElement>>(ElementRef);
  private pageCtx = inject(PageContextService);
  private title   = inject(Title);
  refreshSvc      = inject(RefreshService);
  sourceState     = inject(SourceStateService);

  showSourceMenu = signal(false);
  paletteOpen    = signal(false);
  spinning       = signal(false);
  private now    = signal(Date.now());

  readonly isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);

  readonly tabs = NAV_TABS;
  readonly palettePages = PALETTE_PAGES;

  updatedLabel = computed(() => {
    const last = this.refreshSvc.lastUpdated();
    if (!last) return 'not updated';
    const s = Math.max(0, Math.floor((this.now() - last.getTime()) / 1000));
    if (s < 5) return 'updated now';
    if (s < 60) return `updated ${s}s ago`;
    const m = Math.floor(s / 60);
    return m < 60 ? `updated ${m}m ago` : `updated ${Math.floor(m / 60)}h ago`;
  });

  selectedSource = computed<SourceStatus | null>(() => this.sourceState.selectedSourceInfo());

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
    const tab = NAV_TABS.find(t => t.path === '/' + (segs[0] ?? 'dashboard'));
    if (segs.length > 1) return this.pageCtx.detailLabel() ?? tab?.label ?? null;
    return tab?.label ?? null;
  });

  dotClass = sourceDotClass;
  statusLabel = sourceStatusLabel;

  constructor() {
    const id = setInterval(() => this.now.set(Date.now()), 1000);
    inject(DestroyRef).onDestroy(() => clearInterval(id));
    this.router.events.pipe(filter(e => e instanceof NavigationEnd)).subscribe(() => this.showSourceMenu.set(false));
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

  toggleSourceMenu(): void {
    this.showSourceMenu.update(v => !v);
  }

  /** Changing the source is picked up by each page via `reloadOnSourceAndRefresh`; no extra refresh needed. */
  selectSource(name: string): void {
    this.sourceState.select(name);
    this.showSourceMenu.set(false);
  }

  onDocumentClick(ev: MouseEvent): void {
    if (!this.showSourceMenu()) return;
    const menu = this.host.nativeElement.querySelector('[data-source-menu]');
    if (menu && !menu.contains(ev.target as Node)) this.showSourceMenu.set(false);
  }

  onKey(ev: KeyboardEvent): void {
    // ⌘K / Ctrl+K: command palette (works from anywhere, including inputs).
    if ((ev.metaKey || ev.ctrlKey) && !ev.altKey && !ev.shiftKey && ev.key.toLowerCase() === 'k') {
      ev.preventDefault();
      if (!this.paletteOpen() && !document.querySelector('[aria-modal="true"]')) this.openPalette();
      return;
    }

    if (ev.key === 'Escape' && this.showSourceMenu()) {
      this.showSourceMenu.set(false);
      return;
    }

    // 1–4: switch tabs.
    if (ev.ctrlKey || ev.metaKey || ev.altKey || !/^[1-4]$/.test(ev.key)) return;
    if (isTypingOrModal(ev)) return;
    const tab = this.tabs[+ev.key - 1];
    if (!tab) return;
    ev.preventDefault();
    this.router.navigateByUrl(tab.path);
  }

  openPalette(): void {
    this.showSourceMenu.set(false);
    this.paletteOpen.set(true);
  }
}
