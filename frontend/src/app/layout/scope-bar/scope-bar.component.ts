import { Component, ChangeDetectionStrategy, ElementRef, computed, inject, signal } from '@angular/core';
import { NavigationEnd, Router, RouterLink } from '@angular/router';
import { toSignal } from '@angular/core/rxjs-interop';
import { filter, map } from 'rxjs/operators';

import { SourceStateService } from '../../core/services/source-state.service';
import { AuthService } from '../../core/services/auth.service';
import { ConfirmDialogComponent } from '../../shared/components/confirm-dialog/confirm-dialog.component';
import { TimeagoPipe } from '../../shared/pipes/timeago.pipe';
import { purgeMessage } from '../../shared/utils/removed';
import { sourceDotClass, sourceStatusLabel } from '../../shared/utils/source';

type MenuKind = 'more' | 'history' | 'compact';

/** Pills shown inline before the rest moves into the "+N" menu. */
const MAX_INLINE = 4;
const INLINE_WHEN_OVERFLOW = 3;

/**
 * Slim cluster-scope bar under the top bar: "All clusters" + one pill per registered cluster (overflow menu beyond 4),
 * a "History" menu for removed clusters, and — while a removed cluster is selected — the history state with its actions.
 * Hidden on settings pages, which are global by nature.
 */
@Component({
  selector: 'app-scope-bar',
  standalone: true,
  imports: [RouterLink, ConfirmDialogComponent, TimeagoPipe],
  templateUrl: './scope-bar.component.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: {
    '(document:click)': 'onDocClick($event)',
    '(document:keydown.escape)': 'menu.set(null)',
  },
})
export class ScopeBarComponent {
  readonly state = inject(SourceStateService);
  readonly auth = inject(AuthService);
  private router = inject(Router);
  private host = inject<ElementRef<HTMLElement>>(ElementRef);

  readonly dotClass = sourceDotClass;
  readonly statusLabel = sourceStatusLabel;
  readonly message = purgeMessage;

  menu = signal<MenuKind | null>(null);
  filterText = signal('');
  confirming = signal(false);

  private url = toSignal(
    this.router.events.pipe(filter((e): e is NavigationEnd => e instanceof NavigationEnd), map(e => e.urlAfterRedirects)),
    { initialValue: this.router.url },
  );
  /** Settings are global: the cluster scope has no effect there, so the bar is hidden. */
  visible = computed(() => !this.url().startsWith('/settings'));

  clusters = computed(() => this.state.sources());
  selected = computed(() => this.state.selectedSource());

  private overflowing = computed(() => this.clusters().length > MAX_INLINE);
  /** Inline pills; with overflow the selected cluster takes the last inline slot so it never hides in the menu. */
  inline = computed(() => {
    const all = this.clusters();
    if (!this.overflowing()) return all;
    const head = all.slice(0, INLINE_WHEN_OVERFLOW);
    const sel = this.selected();
    const selInfo = sel && !head.some(c => c.name === sel) ? all.find(c => c.name === sel) : undefined;
    return selInfo ? [...head.slice(0, INLINE_WHEN_OVERFLOW - 1), selInfo] : head;
  });
  overflow = computed(() => {
    const shown = new Set(this.inline().map(c => c.name));
    return this.clusters().filter(c => !shown.has(c.name));
  });
  filteredOverflow = computed(() => this.filter(this.overflow()));
  filteredAll = computed(() => this.filter(this.clusters()));
  filteredRemoved = computed(() => {
    const q = this.filterText().trim().toLowerCase();
    return this.state.removed().filter(r => !q || r.name.toLowerCase().includes(q));
  });

  connected = computed(() => this.clusters().filter(c => c.status === 'connected').length);
  failing = computed(() => this.clusters().filter(c => c.status === 'error').length);

  /** Label of the compact (narrow screens) button. */
  currentLabel = computed(() => this.selected() || 'All clusters');
  currentInfo = computed(() => this.state.selectedSourceInfo());

  private filter<T extends { name: string }>(list: T[]): T[] {
    const q = this.filterText().trim().toLowerCase();
    return q ? list.filter(c => c.name.toLowerCase().includes(q)) : list;
  }

  toggle(kind: MenuKind): void {
    this.filterText.set('');
    this.menu.update(m => (m === kind ? null : kind));
  }

  pick(name: string): void {
    this.state.select(name);
    this.menu.set(null);
  }

  onDocClick(ev: MouseEvent): void {
    if (this.menu() && !(ev.target as HTMLElement | null)?.closest('[data-scope-menu]')) this.menu.set(null);
  }

  readd(name: string): void {
    this.router.navigate(['/clusters'], { queryParams: { readd: name } });
  }

  purge(name: string): void {
    this.confirming.set(false);
    this.state.purgeHistory(name);
  }
}
