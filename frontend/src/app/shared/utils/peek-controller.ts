import { DestroyRef, inject, signal } from '@angular/core';
import { ActivatedRoute, Router } from '@angular/router';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';

/**
 * Master–detail selection for list pages: the open row lives in the `?peek=<workload id>` query param (so it
 * survives refresh), and `j`/`k` or ↑/↓ move it through `ids()` while the drawer is open.
 * Create in an injection context (component field initialiser).
 */
export class PeekController {
  private route  = inject(ActivatedRoute);
  private router = inject(Router);

  readonly id = signal<string | null>(null);

  constructor(private ids: () => string[]) {
    this.route.queryParamMap.pipe(takeUntilDestroyed()).subscribe(qp => this.id.set(qp.get('peek') || null));

    const onKey = (e: KeyboardEvent) => {
      if (!this.id() || e.metaKey || e.ctrlKey || e.altKey) return;
      const el = e.target as HTMLElement | null;
      if (el && (/^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName) || el.isContentEditable)) return;
      // Inside the drawer / dialogs arrows keep their normal meaning (scrolling the log, tablists).
      if (el?.closest('[role="dialog"][aria-modal="true"], [role="tablist"], pre')) return;
      if (e.key === 'j' || e.key === 'ArrowDown') { e.preventDefault(); this.move(1); }
      else if (e.key === 'k' || e.key === 'ArrowUp') { e.preventDefault(); this.move(-1); }
    };
    document.addEventListener('keydown', onKey);
    inject(DestroyRef).onDestroy(() => document.removeEventListener('keydown', onKey));
  }

  open(id: string): void { this.set(id); }
  close(): void { this.set(null); }

  /** Row click handler: ignores clicks on links, buttons and form controls inside the row. */
  rowClick(ev: Event, id: string): void {
    if ((ev.target as HTMLElement).closest('a, button, input, select, textarea, label')) return;
    this.open(id);
  }

  /** Enter/Space on a focused row itself. */
  rowKey(ev: Event, id: string): void {
    if (ev.target !== ev.currentTarget) return;
    ev.preventDefault();
    this.open(id);
  }

  move(delta: number): void {
    const ids = this.ids();
    if (!ids.length) return;
    const i = ids.indexOf(this.id() ?? '');
    const next = i < 0 ? (delta > 0 ? 0 : ids.length - 1) : Math.min(ids.length - 1, Math.max(0, i + delta));
    if (ids[next] === this.id()) return;
    this.set(ids[next]);
    setTimeout(() => document.querySelector(`[data-peek-id="${CSS.escape(ids[next])}"]`)?.scrollIntoView({ block: 'nearest' }), 0);
  }

  private set(id: string | null): void {
    this.router.navigate([], {
      relativeTo: this.route, queryParams: { peek: id }, queryParamsHandling: 'merge', replaceUrl: true,
    });
  }
}
