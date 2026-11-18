import { Component, ChangeDetectionStrategy, signal, computed, inject, DestroyRef } from '@angular/core';
import { ActivatedRoute, Router } from '@angular/router';
import { toSignal } from '@angular/core/rxjs-interop';
import { Subscription, forkJoin } from 'rxjs';
import { catchError, map } from 'rxjs/operators';
import { of } from 'rxjs';

import { ContainerService } from '../../core/services/container.service';
import { SourceStateService } from '../../core/services/source-state.service';
import { reloadOnSourceAndRefresh } from '../../core/utils/page-reload';
import { LifecycleEvent, K8sEvent } from '../../core/models/container.model';
import { LifecycleEventsComponent } from './lifecycle-events.component';
import { K8sEventsComponent } from './k8s-events.component';

export type EventsView = 'lifecycle' | 'k8s';

const DAY = 86_400_000;
const SUMMARY_LIMIT = 200;
const K8S_SUMMARY_LIMIT = 500;

/** Count of one problem kind in the last 24h; `more` when the fetched page was full and still inside the window. */
interface Tally { n: number; more: boolean; }
const EMPTY: Tally = { n: 0, more: false };

/**
 * Events page shell: title, a summary of the last 24h whose problem chips jump to the filtered lists, and the
 * Lifecycle | Kubernetes switch. Query params: `?view=k8s` (anything else = Lifecycle); the lists own the rest.
 */
@Component({
  selector: 'app-events',
  standalone: true,
  imports: [LifecycleEventsComponent, K8sEventsComponent],
  template: `
    <div class="flex flex-col gap-4">
      <header class="border-b border-kymo-rule pb-3 flex items-end justify-between gap-4 flex-wrap">
        <div class="min-w-0">
          <h1 class="text-lg font-semibold text-kymo-ink tracking-tight">Events</h1>
          <p class="mt-1.5 text-xs text-kymo-muted flex items-center gap-x-2 flex-wrap min-h-4" role="status">
            @if (!summaryLoaded()) {
              Loading…
            } @else {
              <span>Last 24h</span>
              <span class="text-kymo-faint" aria-hidden="true">·</span>
              <button type="button" class="tabular-nums hover:underline underline-offset-4 disabled:no-underline disabled:cursor-default"
                      [class]="crashes().n > 0 ? 'text-kymo-danger' : 'text-kymo-muted'" [disabled]="crashes().n === 0"
                      (click)="showProblems()">{{ count(crashes()) }} {{ crashes().n === 1 && !crashes().more ? 'crash' : 'crashes' }}</button>
              <span class="text-kymo-faint" aria-hidden="true">·</span>
              <button type="button" class="tabular-nums hover:underline underline-offset-4 disabled:no-underline disabled:cursor-default"
                      [class]="ooms().n > 0 ? 'text-kymo-amber' : 'text-kymo-muted'" [disabled]="ooms().n === 0"
                      (click)="showProblems()">{{ count(ooms()) }} OOM {{ ooms().n === 1 && !ooms().more ? 'kill' : 'kills' }}</button>
              <span class="text-kymo-faint" aria-hidden="true">·</span>
              <button type="button" class="tabular-nums hover:underline underline-offset-4 disabled:no-underline disabled:cursor-default"
                      [class]="warnings().n > 0 ? 'text-kymo-amber' : 'text-kymo-muted'" [disabled]="warnings().n === 0"
                      (click)="showWarnings()">{{ count(warnings()) }} {{ warnings().n === 1 && !warnings().more ? 'warning' : 'warnings' }}</button>
            }
          </p>
        </div>

        <div class="flex border border-kymo-rule h-[1.875rem]" role="group" aria-label="Event source">
          @for (v of views; track v.key) {
            <button type="button"
              class="px-3 text-xs whitespace-nowrap transition-colors border-r border-kymo-rule last:border-r-0"
              [class]="view() === v.key ? 'bg-kymo-raised text-kymo-ink' : 'text-kymo-muted hover:text-kymo-ink hover:bg-kymo-raised'"
              [attr.aria-pressed]="view() === v.key" (click)="setView(v.key)">{{ v.label }}</button>
          }
        </div>
      </header>

      @if (view() === 'k8s') {
        <app-k8s-events />
      } @else {
        <app-lifecycle-events />
      }
    </div>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class EventsComponent {
  private svc      = inject(ContainerService);
  private sources  = inject(SourceStateService);
  private route    = inject(ActivatedRoute);
  private router   = inject(Router);

  /** `?view=k8s` selects the Kubernetes events view; anything else is the lifecycle log. */
  readonly view = toSignal(
    this.route.queryParamMap.pipe(map((p): EventsView => (p.get('view') === 'k8s' ? 'k8s' : 'lifecycle'))),
    { initialValue: 'lifecycle' as EventsView },
  );
  readonly views: { key: EventsView; label: string }[] = [
    { key: 'lifecycle', label: 'Lifecycle' },
    { key: 'k8s', label: 'Kubernetes' },
  ];

  summaryLoaded = signal(false);
  crashes  = signal<Tally>(EMPTY);
  ooms     = signal<Tally>(EMPTY);
  warnings = signal<Tally>(EMPTY);

  private sub?: Subscription;

  constructor() {
    reloadOnSourceAndRefresh({ load: () => this.loadSummary() });
    inject(DestroyRef).onDestroy(() => this.sub?.unsubscribe());
  }

  count(t: Tally): string { return t.n + (t.more ? '+' : ''); }

  /** Crashes, OOM kills and Kubernetes warnings of the last 24h (own light queries, independent of list paging/filters). */
  private loadSummary(): void {
    const since = Date.now() - DAY;
    const source = this.sources.selectedSource() || undefined;
    this.sub?.unsubscribe();
    this.sub = forkJoin({
      crash: this.svc.queryRecentEvents({ limit: SUMMARY_LIMIT, source, type: 'crash' }).pipe(catchError(() => of(null))),
      oom:   this.svc.queryRecentEvents({ limit: SUMMARY_LIMIT, source, type: 'oom' }).pipe(catchError(() => of(null))),
      warn:  this.svc.queryK8sEvents({ limit: K8S_SUMMARY_LIMIT, source, type: 'Warning', since: new Date(since).toISOString() }).pipe(catchError(() => of(null))),
    }).subscribe(({ crash, oom, warn }) => {
      const life = (list: LifecycleEvent[] | null): Tally => {
        const rows = this.sources.inScope(list ?? [], e => e.source_name).filter(e => new Date(e.occurred_at).getTime() >= since);
        const raw = list ?? [];
        return { n: rows.length, more: raw.length >= SUMMARY_LIMIT && new Date(raw[raw.length - 1].occurred_at).getTime() >= since };
      };
      const k8s = (list: K8sEvent[] | null): Tally => {
        const raw = list ?? [];
        return { n: this.sources.inScope(raw, e => e.source_name).length, more: raw.length >= K8S_SUMMARY_LIMIT };
      };
      this.crashes.set(life(crash));
      this.ooms.set(life(oom));
      this.warnings.set(k8s(warn));
      this.summaryLoaded.set(true);
    });
  }

  private go(queryParams: Record<string, string | null>): void {
    this.router.navigate([], { relativeTo: this.route, queryParams, queryParamsHandling: 'merge', replaceUrl: true });
  }

  setView(v: EventsView): void {
    if (v === this.view()) return;
    // Each view has its own filters: leaving one drops them.
    this.go({ view: v === 'k8s' ? 'k8s' : null, q: null, type: null, reason: null, jobs: null });
  }

  showProblems(): void {
    this.go({ view: null, q: null, type: 'problems', reason: null, jobs: null });
  }

  showWarnings(): void {
    this.go({ view: 'k8s', q: null, type: 'warnings', reason: null, jobs: null });
  }
}
