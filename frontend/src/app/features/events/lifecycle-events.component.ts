import { Component, ChangeDetectionStrategy, signal, computed, inject, DestroyRef } from '@angular/core';
import { ActivatedRoute, Router, RouterLink } from '@angular/router';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';
import { DatePipe } from '@angular/common';
import { Subject, Subscription } from 'rxjs';
import { debounceTime, distinctUntilChanged } from 'rxjs/operators';

import { ContainerService } from '../../core/services/container.service';
import { RefreshService } from '../../core/services/refresh.service';
import { SourceStateService } from '../../core/services/source-state.service';
import { WorkloadStateService } from '../../core/services/workload-state.service';
import { reloadOnSourceAndRefresh } from '../../core/utils/page-reload';
import { LifecycleEvent, LifecycleEventType } from '../../core/models/container.model';
import { EmptyStateComponent } from '../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../shared/components/error-state/error-state.component';
import { SkeletonComponent } from '../../shared/components/skeleton/skeleton.component';
import { WorkloadBadgeComponent } from '../../shared/components/workload-badge/workload-badge.component';
import { ExitTagComponent } from '../../shared/components/exit-tag/exit-tag.component';
import { CrashLogButtonComponent } from '../../shared/components/crash-log-button/crash-log-button.component';
import { SearchHotkeyDirective } from '../../shared/directives/search-hotkey.directive';
import { TimeagoPipe } from '../../shared/pipes/timeago.pipe';
import { DayGroup, groupByDay } from '../../shared/utils/days';
import { eventContainerName, eventDotClass, eventLabel, eventReason, eventTextClass, hasCrashLogs, isProblemEvent, showsExitCode } from '../../shared/utils/events';
import { containerRoute } from '../../shared/utils/format';
import { podNameFromId } from '../../shared/utils/source';
import { isJobKind, workloadResolver } from '../../shared/utils/workload';

const PAGE_SIZE = 100;
/** When filters leave fewer rows than this, older pages are fetched automatically (a few times at most). */
const MIN_VISIBLE = 20;
const MAX_AUTO_PAGES = 5;

export type LifecycleFilter = '' | 'problems' | 'starts' | 'stops';
const TYPE_SEGMENTS: { value: LifecycleFilter; label: string }[] = [
  { value: '',         label: 'All' },
  { value: 'problems', label: 'Problems' },
  { value: 'starts',   label: 'Starts' },
  { value: 'stops',    label: 'Stops' },
];
/** Old / alternative `?type=` spellings. */
const TYPE_ALIASES: Record<string, LifecycleFilter> = {
  problems: 'problems', starts: 'starts', stops: 'stops',
  start: 'starts', stop: 'stops', die: 'problems', crash: 'problems', oom: 'problems',
};

/** A failed termination: crash, OOM kill, or an exit with a non-zero code. */
export const isFailure = (ev: LifecycleEvent): boolean =>
  isProblemEvent(ev) || (ev.event_type.toLowerCase() === 'die' && !!ev.exit_code);
const isStart = (ev: LifecycleEvent) => ev.event_type.toLowerCase() === 'start';
const isCleanExit = (ev: LifecycleEvent) => ev.event_type.toLowerCase() === 'die' && !ev.exit_code;

const byNewest = (a: LifecycleEvent, b: LifecycleEvent) =>
  new Date(b.occurred_at).getTime() - new Date(a.occurred_at).getTime() || b.id - a.id;

/** Display model of one event row, computed once per data change. */
interface Row {
  ev: LifecycleEvent;
  at: string;
  label: string;
  dot: string;
  text: string;
  failure: boolean;
  exit: number | null;
  name: string;
  kind: string;
  namespace: string;
  cluster: string;
  link: string[];
  query: Record<string, string> | null;
  pod: string;
  container: string;
  details: string;
  jobRun: boolean;
  logs: boolean;
  search: string;
}

/**
 * Lifecycle view of the Events page: recorded start / stop / exit / crash / OOM events, newest first, grouped by day.
 * Filters live in the query string: ?q= &type=problems|starts|stops &jobs=0 (hide routine Job/CronJob runs).
 */
@Component({
  selector: 'app-lifecycle-events',
  standalone: true,
  imports: [
    RouterLink, DatePipe, TimeagoPipe, EmptyStateComponent, ErrorStateComponent, SkeletonComponent,
    WorkloadBadgeComponent, ExitTagComponent, CrashLogButtonComponent, SearchHotkeyDirective,
  ],
  template: `
    <div class="flex items-center gap-2 flex-wrap">
      <div class="relative flex-1 min-w-[12rem] max-w-md">
        <input appSearchHotkey type="search" placeholder="Search workload, pod or reason…" aria-label="Search events"
               class="k-input w-full pr-8" [value]="searchText()"
               (input)="onSearch($any($event.target).value)" (keydown.escape)="onSearch('')" />
        <kbd class="k-kbd hidden sm:inline-flex absolute right-2 top-1/2 -translate-y-1/2 pointer-events-none">/</kbd>
      </div>

      <div class="flex border border-kymo-rule h-[1.875rem]" role="group" aria-label="Filter by type">
        @for (s of segments; track s.value) {
          <button type="button"
            class="px-3 text-xs whitespace-nowrap transition-colors border-r border-kymo-rule last:border-r-0"
            [class]="type() === s.value ? 'bg-kymo-raised text-kymo-ink' : 'text-kymo-muted hover:text-kymo-ink hover:bg-kymo-raised'"
            [attr.aria-pressed]="type() === s.value" (click)="setType(s.value)">{{ s.label }}</button>
        }
      </div>

      <label class="inline-flex items-center gap-2 h-[1.875rem] text-xs text-kymo-muted cursor-pointer select-none hover:text-kymo-ink"
             title="Show the start and clean exit of every Job / CronJob run. Off hides that routine noise; failed runs always stay.">
        <input type="checkbox" [checked]="jobRuns()" (change)="setJobRuns($any($event.target).checked)" />
        Job runs
      </label>

      @if (hasFilters()) {
        <button type="button" class="ml-3 text-xs text-kymo-muted hover:text-kymo-amber hover:underline underline-offset-4" (click)="clearFilters()">Clear filters</button>
      }
    </div>

    @if (error()) {
      <app-error-state message="Failed to load events" (retry)="reset()" />
    }

    @if (loading()) {
      <app-skeleton [rows]="10" />
    } @else if (shown() === 0 && !error()) {
      <div class="k-panel">
        @if (hasFilters() && loadedCount() > 0) {
          <app-empty-state icon="search" message="No events match your filters"
            [hint]="hasMore() ? 'Only the loaded events were searched — load older ones or relax the filters.' : 'Try another search term or type.'">
            <div class="flex items-center gap-2 mt-4">
              <button type="button" class="k-btn k-btn-primary k-btn-sm" (click)="clearFilters()">Clear filters</button>
              @if (hasMore()) {
                <button type="button" class="k-btn k-btn-sm" [disabled]="loadingMore()" (click)="loadOlder()">{{ loadingMore() ? 'Loading…' : 'Load older' }}</button>
              }
            </div>
          </app-empty-state>
        } @else if (hasFilters()) {
          <app-empty-state icon="search" message="No events match your filters" hint="Try another type.">
            <button type="button" class="k-btn k-btn-primary k-btn-sm mt-4" (click)="clearFilters()">Clear filters</button>
          </app-empty-state>
        } @else {
          <app-empty-state icon="events" message="No events recorded yet" hint="Starts, stops, crashes and OOM kills appear here as the collectors record them." />
        }
      </div>
    } @else if (shown() > 0) {
      <div class="k-panel">
        <div [class]="grid + ' py-1.5 border-b border-kymo-rule max-sm:hidden'" aria-hidden="true">
          <span class="k-label">Time</span>
          <span class="k-label">Event</span>
          <span class="k-label">Workload</span>
          <span class="k-label hidden md:block">Pod</span>
          <span class="k-label hidden md:block">Details</span>
        </div>
        @for (g of groups(); track g.key) {
          <section [attr.aria-label]="g.label">
            <h3 class="sticky top-12 z-10 px-4 py-1.5 bg-kymo-raised border-y border-kymo-rule first:border-t-0 flex items-center gap-2">
              <span class="k-label !text-kymo-muted">{{ g.label }}</span>
              <span class="text-[11px] tabular-nums text-kymo-faint">{{ g.rows.length }}</span>
            </h3>
            <ol class="divide-y divide-kymo-rule">
              @for (r of g.rows; track r.ev.id) {
                <li [class]="grid + ' py-2 hover:bg-kymo-raised transition-colors ' + edge(r)" [class.k-fresh]="fresh().has(r.ev.id)">
                  <span class="text-xs leading-5 tabular-nums text-kymo-muted whitespace-nowrap" [title]="(r.at | date:'medium') + ' · ' + (r.at | timeago)">{{ r.at | date:'HH:mm:ss' }}</span>

                  <span class="flex items-center gap-2 min-w-0 leading-5">
                    <span class="k-led" [class]="r.dot" aria-hidden="true"></span>
                    <span class="text-xs truncate" [class]="r.failure ? r.text : 'text-kymo-ink'">{{ r.label }}</span>
                    @if (r.exit !== null) { <app-exit-tag class="flex-none" [code]="r.exit" /> }
                    @if (r.logs) { <app-crash-log-button class="flex-none -my-1" [containerId]="r.ev.container_id" [occurredAt]="r.ev.occurred_at" /> }
                  </span>

                  <span class="min-w-0 max-sm:col-start-2">
                    <span class="flex items-center gap-2 min-w-0">
                      <a [routerLink]="r.link" [queryParams]="r.query"
                         class="text-xs leading-5 text-kymo-ink truncate hover:text-kymo-amber hover:underline underline-offset-4" [title]="r.name">{{ r.name }}</a>
                      @if (r.kind) { <app-workload-badge class="flex-none" [kind]="r.kind" /> }
                    </span>
                    <span class="block text-[11px] text-kymo-faint truncate">{{ r.namespace || '—' }}@if (r.cluster) { · {{ r.cluster }} }</span>
                  </span>

                  <span class="hidden md:block text-xs leading-5 text-kymo-muted truncate" [title]="r.pod + (r.container ? ' · ' + r.container : '')">
                    {{ r.pod || '—' }}@if (r.container) { <span class="text-kymo-faint"> · {{ r.container }}</span> }
                  </span>

                  <span class="hidden md:block text-xs leading-5 text-kymo-muted truncate" [title]="r.details">{{ r.details }}</span>
                </li>
              }
            </ol>
          </section>
        }
      </div>

      <div class="flex items-center justify-center gap-3 py-2">
        @if (hasMore()) {
          <button type="button" class="k-btn" [disabled]="loadingMore()" (click)="loadOlder()">
            @if (loadingMore()) {
              <span class="w-3 h-3 border border-kymo-rule border-t-kymo-amber rounded-full animate-spin" aria-hidden="true"></span>
              Loading…
            } @else {
              Load older
            }
          </button>
        } @else {
          <span class="text-xs text-kymo-faint">— start of recorded history —</span>
        }
        @if (moreError()) {
          <span class="text-xs text-kymo-danger" role="alert">failed to load older events</span>
        }
      </div>
    }
  `,
  host: { class: 'flex flex-col gap-4' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class LifecycleEventsComponent {
  private svc        = inject(ContainerService);
  private refreshSvc = inject(RefreshService);
  private sources    = inject(SourceStateService);
  private wlState    = inject(WorkloadStateService);
  private route      = inject(ActivatedRoute);
  private router     = inject(Router);

  readonly segments = TYPE_SEGMENTS;
  /** Column layout: compact two-line rows below sm, + workload below md, all five columns from md. */
  readonly grid = 'grid items-start gap-x-4 px-4 grid-cols-[3.75rem_minmax(0,1fr)] sm:grid-cols-[4.5rem_minmax(0,14rem)_minmax(0,1fr)] '
    + 'md:grid-cols-[4.5rem_minmax(0,14rem)_minmax(0,1.2fr)_minmax(0,1fr)_minmax(0,1.4fr)]';

  // Filters (mirrors of the query string)
  type       = signal<LifecycleFilter>('');
  query      = signal('');
  jobRuns    = signal(true);
  searchText = signal('');
  private search$ = new Subject<string>();

  loading     = signal(true);
  error       = signal(false);
  loadingMore = signal(false);
  moreError   = signal(false);
  /** False once a page came back shorter than PAGE_SIZE. */
  hasMore     = signal(true);
  private loaded = signal<LifecycleEvent[]>([]);
  /** Ids that arrived through a refresh: highlighted briefly. */
  fresh = signal<ReadonlySet<number>>(new Set());

  hasFilters  = computed(() => !!(this.type() || this.query() || !this.jobRuns()));
  loadedCount = computed(() => this.loaded().length);
  private multiCluster = computed(() => this.sources.sources().length > 1 && !this.sources.selectedSource());

  private resolve = computed(() => workloadResolver(this.wlState.workloads()));

  private rows = computed<Row[]>(() => {
    const resolve = this.resolve();
    const multi = this.multiCluster();
    // Without a cluster selected, events of removed clusters stay out of the list (global scope).
    return this.sources.inScope(this.loaded(), e => e.source_name).map(ev => {
      const ns = ev.container_id.split('/')[0] ?? '';
      const pod = podNameFromId(ev.container_id);
      const hasNs = ev.container_id.split('/').length >= 3;
      const w = hasNs ? resolve(ev.source_name, ns, pod) : null;
      const container = eventContainerName(ev);
      const name = w?.name ?? container;
      const reason = eventReason(ev);
      const exit = showsExitCode(ev) ? ev.exit_code! : null;
      const failure = isFailure(ev);
      return {
        ev, at: ev.occurred_at, label: eventLabel(ev), dot: eventDotClass(ev), text: eventTextClass(ev), failure, exit,
        name, kind: w?.kind ?? '', namespace: hasNs ? ns : '', cluster: multi ? (ev.source_name ?? '') : '',
        link: w ? ['/workloads', w.id] : containerRoute(ev.container_id, ev.runtime),
        query: w ? { tab: 'activity' } : null,
        pod, container: container !== name ? container : '', details: reason,
        jobRun: !!w && isJobKind(w.kind) && (isStart(ev) || isCleanExit(ev)),
        logs: hasCrashLogs(ev),
        search: `${name} ${pod} ${container} ${ns} ${reason}`.toLowerCase(),
      } satisfies Row;
    });
  });

  private visible = computed<Row[]>(() => {
    const type = this.type(), q = this.query().trim().toLowerCase(), jobs = this.jobRuns();
    return this.rows().filter(r => {
      if (!jobs && r.jobRun) return false;
      if (q && !r.search.includes(q)) return false;
      switch (type) {
        case 'problems': return r.failure;
        case 'starts':   return isStart(r.ev);
        case 'stops':    return !r.failure && !isStart(r.ev) && ['stop', 'die'].includes(r.ev.event_type.toLowerCase());
        default:         return true;
      }
    });
  });
  shown  = computed(() => this.visible().length);
  groups = computed<DayGroup<Row>[]>(() => groupByDay(this.visible(), r => r.at));

  private sub?: Subscription;
  private moreSub?: Subscription;
  private freshTimer?: ReturnType<typeof setTimeout>;
  private autoPages = 0;
  /** The server-side type of the current list ('start' for Starts; everything else is filtered here). */
  private serverType: LifecycleEventType | '' = '';

  constructor() {
    this.route.queryParamMap.pipe(takeUntilDestroyed()).subscribe(qp => {
      const type = TYPE_ALIASES[qp.get('type') ?? ''] ?? '';
      const q = qp.get('q') ?? '';
      this.type.set(type);
      this.query.set(q);
      this.jobRuns.set(qp.get('jobs') !== '0');
      if (q !== this.searchText().trim()) this.searchText.set(q);
      const server: LifecycleEventType | '' = type === 'starts' ? 'start' : '';
      if (server !== this.serverType) {
        const first = this.loadedOnce;
        this.serverType = server;
        if (first) this.reset();
      }
      this.autoPages = 0;
      this.autoFill();
    });
    this.search$.pipe(debounceTime(250), distinctUntilChanged(), takeUntilDestroyed())
      .subscribe(v => this.setParams({ q: v.trim() || null }));
    reloadOnSourceAndRefresh({ load: reason => this.load(reason === 'auto' || reason === 'manual' ? 'merge' : 'replace') });
    inject(DestroyRef).onDestroy(() => { this.sub?.unsubscribe(); this.moreSub?.unsubscribe(); clearTimeout(this.freshTimer); });
  }

  private loadedOnce = false;

  private setParams(params: Record<string, string | null>): void {
    this.router.navigate([], { relativeTo: this.route, queryParams: params, queryParamsHandling: 'merge', replaceUrl: true });
  }

  setType(v: LifecycleFilter): void { this.setParams({ type: v || null }); }
  setJobRuns(on: boolean): void     { this.setParams({ jobs: on ? null : '0' }); }
  onSearch(v: string): void         { this.searchText.set(v); this.search$.next(v); }
  clearFilters(): void {
    this.searchText.set('');
    this.setParams({ q: null, type: null, jobs: null });
  }

  /** Subtle left border on problem rows: amber for OOM kills, danger for crashes and failed exits. */
  edge(r: Row): string {
    if (!r.failure) return '';
    return r.ev.event_type.toLowerCase().startsWith('oom')
      ? 'shadow-[inset_2px_0_0_color-mix(in_srgb,var(--color-kymo-amber)_55%,transparent)]'
      : 'shadow-[inset_2px_0_0_var(--color-kymo-danger)]';
  }

  reset(): void {
    this.loading.set(true);
    this.loaded.set([]);
    this.autoPages = 0;
    this.load('replace');
  }

  /**
   * Initial / source / filter loads replace the list. Refresh ticks fetch the newest page and merge it in,
   * so pages loaded with "Load older" survive auto-refresh; merged-in events are highlighted briefly.
   */
  load(mode: 'replace' | 'merge'): void {
    const merge = mode === 'merge' && this.loaded().length > 0;
    this.sub?.unsubscribe();
    if (!merge) { this.moreSub?.unsubscribe(); this.loadingMore.set(false); }
    this.sub = this.svc.queryRecentEvents({
      limit: PAGE_SIZE,
      source: this.sources.selectedSource() || undefined,
      type: this.serverType,
    }).subscribe({
      next: list => {
        const page = list ?? [];
        if (merge) {
          const seen = new Set(this.loaded().map(e => e.id));
          const added = page.filter(e => !seen.has(e.id));
          if (added.length) {
            this.loaded.set([...added, ...this.loaded()].sort(byNewest));
            this.flash(added.map(e => e.id));
          }
        } else {
          this.loaded.set([...page].sort(byNewest));
          this.hasMore.set(page.length >= PAGE_SIZE);
          this.moreError.set(false);
          this.autoPages = 0;
        }
        this.loadedOnce = true;
        this.error.set(false);
        this.loading.set(false);
        this.refreshSvc.markUpdated();
        this.autoFill();
      },
      error: () => {
        this.loading.set(false);
        if (!merge) this.error.set(true);
      },
    });
  }

  private flash(ids: number[]): void {
    this.fresh.set(new Set(ids));
    clearTimeout(this.freshTimer);
    this.freshTimer = setTimeout(() => this.fresh.set(new Set()), 4000);
  }

  /** Filters can leave a page almost empty: keep pulling older pages (a few) until something shows. */
  private autoFill(): void {
    if (this.loading() || this.loadingMore() || !this.hasMore() || this.autoPages >= MAX_AUTO_PAGES) return;
    if (!this.hasFilters() || this.shown() >= MIN_VISIBLE || !this.loaded().length) return;
    this.autoPages++;
    this.loadOlder();
  }

  loadOlder(): void {
    const list = this.loaded();
    if (!list.length || this.loadingMore()) return;
    const oldest = list[list.length - 1];
    this.loadingMore.set(true);
    this.moreError.set(false);
    this.moreSub = this.svc.queryRecentEvents({
      limit: PAGE_SIZE,
      source: this.sources.selectedSource() || undefined,
      type: this.serverType,
      before: oldest.occurred_at,
    }).subscribe({
      next: older => {
        const page = older ?? [];
        const seen = new Set(this.loaded().map(e => e.id));
        this.loaded.set([...this.loaded(), ...page.filter(e => !seen.has(e.id))].sort(byNewest));
        this.hasMore.set(page.length >= PAGE_SIZE);
        this.loadingMore.set(false);
        this.autoFill();
      },
      error: () => {
        this.loadingMore.set(false);
        this.moreError.set(true);
      },
    });
  }
}
