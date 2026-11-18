import { Component, ChangeDetectionStrategy, signal, computed, inject, effect, untracked, DestroyRef } from '@angular/core';
import { ActivatedRoute, Router, RouterLink } from '@angular/router';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';
import { DatePipe } from '@angular/common';
import { Subject, Subscription } from 'rxjs';
import { debounceTime, distinctUntilChanged } from 'rxjs/operators';

import { ContainerService } from '../../core/services/container.service';
import { RefreshService } from '../../core/services/refresh.service';
import { SourceStateService } from '../../core/services/source-state.service';
import { LoadReason, reloadOnSourceAndRefresh } from '../../core/utils/page-reload';
import { K8sEvent, K8sEventType, K8sReasonCount } from '../../core/models/container.model';
import { EmptyStateComponent } from '../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../shared/components/error-state/error-state.component';
import { SkeletonComponent } from '../../shared/components/skeleton/skeleton.component';
import { SearchHotkeyDirective } from '../../shared/directives/search-hotkey.directive';
import { TimeagoPipe } from '../../shared/pipes/timeago.pipe';
import { DayGroup, groupByDay } from '../../shared/utils/days';
import { byLastSeen, isWarning, k8sObject, occurrences } from '../../shared/utils/k8s-events';

const PAGE_SIZE = 100;
const TOP_REASONS = 12;

type TypeParam = '' | 'warnings' | 'normal';
const TYPE_SEGMENTS: { value: TypeParam; label: string }[] = [
  { value: '',         label: 'All' },
  { value: 'warnings', label: 'Warnings' },
  { value: 'normal',   label: 'Normal' },
];
const TYPE_ALIASES: Record<string, TypeParam> = { warnings: 'warnings', warning: 'warnings', normal: 'normal' };
const TO_API: Record<TypeParam, K8sEventType | ''> = { '': '', warnings: 'Warning', normal: 'Normal' };

/**
 * Kubernetes view of the Events page: persisted cluster events, newest `last_seen` first, grouped by day, cursor paged.
 * Filters live in the query string: ?view=k8s &q= &type=warnings|normal &reason=
 */
@Component({
  selector: 'app-k8s-events',
  standalone: true,
  imports: [RouterLink, DatePipe, TimeagoPipe, EmptyStateComponent, ErrorStateComponent, SkeletonComponent, SearchHotkeyDirective],
  template: `
    <div class="flex items-center gap-2 flex-wrap">
      <div class="relative flex-1 min-w-[12rem] max-w-md">
        <input appSearchHotkey type="search" placeholder="Search object, reason or message…" aria-label="Search Kubernetes events"
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

      <select aria-label="Filter by reason" class="k-input cursor-pointer max-w-[14rem]" [value]="reason()" (change)="setReason($any($event.target).value)">
        <option value="" [selected]="!reason()">All reasons</option>
        @for (r of reasonOptions(); track r.reason) {
          <option [value]="r.reason" [selected]="r.reason === reason()">{{ r.reason }}@if (r.count) { ({{ r.count }}) }</option>
        }
      </select>

      @if (hasFilters()) {
        <button type="button" class="text-xs text-kymo-muted hover:text-kymo-amber hover:underline underline-offset-4" (click)="clearFilters()">Clear filters</button>
      }
    </div>

    @if (error()) {
      <app-error-state message="Failed to load Kubernetes events" (retry)="reset()" />
    }

    @if (loading()) {
      <app-skeleton [rows]="10" />
    } @else if (rowCount() === 0 && !error()) {
      <div class="k-panel">
        @if (hasFilters()) {
          <app-empty-state icon="search" message="No Kubernetes events match your filters" hint="Try another search term, type or reason.">
            <button type="button" class="k-btn k-btn-primary k-btn-sm mt-4" (click)="clearFilters()">Clear filters</button>
          </app-empty-state>
        } @else {
          <app-empty-state icon="events" message="No Kubernetes events recorded yet"
            hint="Warnings and rollout activity (scheduling, image pulls, probes, scaling) show up here as the collectors see them." />
        }
      </div>
    } @else if (rowCount() > 0) {
      <div class="k-panel">
        <div [class]="grid + ' py-1.5 border-b border-kymo-rule max-sm:hidden'" aria-hidden="true">
          <span class="k-label">Time</span>
          <span class="k-label">Type</span>
          <span class="k-label">Reason</span>
          <span class="k-label">Object</span>
          <span class="k-label hidden md:block">Message</span>
          <span class="k-label hidden md:block text-right">Count</span>
        </div>
        @for (g of groups(); track g.key) {
          <section [attr.aria-label]="g.label">
            <h3 class="sticky top-12 z-10 px-4 py-1.5 bg-kymo-raised border-y border-kymo-rule first:border-t-0 flex items-center gap-2">
              <span class="k-label !text-kymo-muted">{{ g.label }}</span>
              <span class="text-[11px] tabular-nums text-kymo-faint">{{ g.rows.length }}</span>
            </h3>
            <ol class="divide-y divide-kymo-rule">
              @for (e of g.rows; track e.id) {
                @let warn = isWarn(e);
                @let n = times(e);
                <li [class]="grid + ' py-2 hover:bg-kymo-raised transition-colors ' + (warn ? 'shadow-[inset_2px_0_0_color-mix(in_srgb,var(--color-kymo-amber)_55%,transparent)]' : '')"
                    [class.k-fresh]="fresh().has(e.id)">
                  <span class="text-xs leading-5 tabular-nums text-kymo-muted whitespace-nowrap"
                        [title]="(e.last_seen | date:'medium') + ' · ' + (e.last_seen | timeago) + (n > 1 ? ' · first seen ' + (e.first_seen | date:'MMM d, HH:mm:ss') : '')">{{ e.last_seen | date:'HH:mm:ss' }}</span>

                  <span class="flex items-center gap-2 min-w-0 leading-5 sm:hidden">
                    <span class="k-led" [class]="warn ? 'bg-kymo-amber' : 'bg-kymo-stopped'" aria-hidden="true"></span>
                    <span class="text-xs truncate" [class]="warn ? 'text-kymo-amber' : 'text-kymo-ink'">{{ e.reason }}</span>
                    @if (n > 1) { <span class="text-[11px] tabular-nums text-kymo-muted">×{{ n }}</span> }
                    <span class="sr-only">{{ e.type }}</span>
                  </span>

                  <span class="hidden sm:flex items-center gap-2 leading-5">
                    <span class="k-led" [class]="warn ? 'bg-kymo-amber' : 'bg-kymo-stopped'" aria-hidden="true"></span>
                    <span class="text-xs" [class]="warn ? 'text-kymo-amber' : 'text-kymo-muted'">{{ e.type }}</span>
                  </span>

                  <span class="hidden sm:flex items-baseline gap-1.5 min-w-0 text-xs leading-5 text-kymo-ink">
                    <span class="truncate" [title]="e.reason">{{ e.reason }}</span>
                    @if (n > 1) { <span class="md:hidden text-[11px] tabular-nums text-kymo-muted flex-none">×{{ n }}</span> }
                  </span>

                  <span class="min-w-0 max-sm:col-start-2">
                    @if (e.workload_id) {
                      <a [routerLink]="['/workloads', e.workload_id]" [queryParams]="{ tab: 'activity' }"
                         class="block text-xs leading-5 text-kymo-ink truncate hover:text-kymo-amber hover:underline underline-offset-4" [title]="object(e)">{{ object(e) }}</a>
                    } @else {
                      <span class="block text-xs leading-5 text-kymo-ink truncate" [title]="object(e)">{{ object(e) }}</span>
                    }
                    <span class="block text-[11px] text-kymo-faint truncate">{{ e.namespace || '—' }}@if (multiCluster() && e.source_name) { · {{ e.source_name }} }</span>
                  </span>

                  <span class="hidden md:block text-xs leading-5 text-kymo-muted truncate" [title]="e.message">{{ e.message }}</span>
                  <span class="hidden md:block text-xs leading-5 text-right tabular-nums" [class]="n > 1 ? 'text-kymo-ink' : 'text-kymo-faint'"
                        [title]="n + (n === 1 ? ' occurrence' : ' occurrences')">×{{ n }}</span>
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
          <span class="text-xs text-kymo-faint">— start of recorded Kubernetes events —</span>
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
export class K8sEventsComponent {
  private svc        = inject(ContainerService);
  private refreshSvc = inject(RefreshService);
  private sourceSvc  = inject(SourceStateService);
  private route      = inject(ActivatedRoute);
  private router     = inject(Router);

  readonly segments = TYPE_SEGMENTS;
  readonly isWarn = isWarning;
  readonly object = k8sObject;
  readonly times = occurrences;
  /** Time | Type | Reason | Object | Message | Count; below md Message/Count drop out, below sm rows are two lines. */
  readonly grid = 'grid items-start gap-x-4 px-4 grid-cols-[3.75rem_minmax(0,1fr)] sm:grid-cols-[4.5rem_6rem_minmax(0,1fr)_minmax(0,1.2fr)] '
    + 'md:grid-cols-[4.5rem_6rem_minmax(0,0.9fr)_minmax(0,1.1fr)_minmax(0,2fr)_3rem]';

  type       = signal<TypeParam>('');
  reason     = signal('');
  q          = signal('');
  searchText = signal('');
  private search$ = new Subject<string>();

  loading     = signal(true);
  error       = signal(false);
  loadingMore = signal(false);
  moreError   = signal(false);
  hasMore     = signal(true);
  private loaded = signal<K8sEvent[]>([]);
  reasons     = signal<K8sReasonCount[]>([]);
  fresh       = signal<ReadonlySet<number>>(new Set());

  /** Without a cluster selected, rows of removed clusters stay out (same rule as every other page). */
  private rows = computed(() => this.sourceSvc.inScope(this.loaded(), e => e.source_name));
  rowCount = computed(() => this.rows().length);
  groups = computed<DayGroup<K8sEvent>[]>(() => groupByDay(this.rows(), e => e.last_seen));
  multiCluster = computed(() => this.sourceSvc.sources().length > 1 && !this.sourceSvc.selectedSource());
  hasFilters = computed(() => !!(this.type() || this.reason() || this.q().trim()));

  /** Reasons for the active type, most frequent first: the top ones plus the selected one. */
  reasonOptions = computed(() => {
    const t = TO_API[this.type()];
    const merged = new Map<string, number>();
    for (const r of this.reasons()) if (!t || r.type === t) merged.set(r.reason, (merged.get(r.reason) ?? 0) + r.count);
    const all = [...merged].map(([reason, count]) => ({ reason, count })).sort((a, b) => b.count - a.count || a.reason.localeCompare(b.reason));
    const top = all.slice(0, TOP_REASONS);
    const sel = this.reason();
    if (sel && !top.some(r => r.reason === sel)) top.push(all.find(r => r.reason === sel) ?? { reason: sel, count: 0 });
    return top;
  });

  private sub?: Subscription;
  private moreSub?: Subscription;
  private metaSub?: Subscription;
  private freshTimer?: ReturnType<typeof setTimeout>;
  private loadedOnce = false;

  constructor() {
    this.route.queryParamMap.pipe(takeUntilDestroyed()).subscribe(qp => {
      const type = TYPE_ALIASES[(qp.get('type') ?? '').toLowerCase()] ?? '';
      const reason = qp.get('reason') ?? '';
      const q = qp.get('q') ?? '';
      const changed = type !== this.type() || reason !== this.reason() || q !== this.q();
      this.type.set(type);
      this.reason.set(reason);
      this.q.set(q);
      if (q !== this.searchText().trim()) this.searchText.set(q);
      if (changed && this.loadedOnce) this.reset();
    });
    this.search$.pipe(debounceTime(300), distinctUntilChanged(), takeUntilDestroyed())
      .subscribe(v => this.setParams({ q: v.trim() || null }));
    reloadOnSourceAndRefresh({ load: reason => this.load(reason) });
    // Reason options follow the cluster scope.
    effect(() => {
      const src = this.sourceSvc.selectedSource();
      untracked(() => {
        this.metaSub?.unsubscribe();
        this.metaSub = this.svc.getK8sEventReasons(src || undefined).subscribe({
          next: r => this.reasons.set(r ?? []),
          error: () => this.reasons.set([]),
        });
      });
    });
    inject(DestroyRef).onDestroy(() => { this.sub?.unsubscribe(); this.moreSub?.unsubscribe(); this.metaSub?.unsubscribe(); clearTimeout(this.freshTimer); });
  }

  private setParams(params: Record<string, string | null>): void {
    this.router.navigate([], { relativeTo: this.route, queryParams: params, queryParamsHandling: 'merge', replaceUrl: true });
  }

  setType(v: TypeParam): void  { this.setParams({ type: v || null, reason: null }); }
  setReason(v: string): void   { this.setParams({ reason: v || null }); }
  onSearch(v: string): void    { this.searchText.set(v); this.search$.next(v.trim()); }
  clearFilters(): void {
    this.searchText.set('');
    this.setParams({ q: null, type: null, reason: null });
  }

  reset(): void {
    this.loading.set(true);
    this.loaded.set([]);
    this.load('init');
  }

  private query(before?: string) {
    return {
      limit: PAGE_SIZE,
      source: this.sourceSvc.selectedSource() || undefined,
      type: TO_API[this.type()],
      reason: this.reason() || undefined,
      q: this.q().trim() || undefined,
      before,
    };
  }

  /** Initial / filter loads replace the list; refresh ticks merge the newest page so "Load older" pages survive. */
  load(reason: LoadReason): void {
    const merge = (reason === 'auto' || reason === 'manual') && this.loaded().length > 0;
    this.sub?.unsubscribe();
    if (!merge) { this.moreSub?.unsubscribe(); this.loadingMore.set(false); }
    this.sub = this.svc.queryK8sEvents(this.query()).subscribe({
      next: page => {
        if (merge) {
          const byId = new Map(this.loaded().map(e => [e.id, e]));
          const added: number[] = [];
          for (const e of page) { if (!byId.has(e.id)) added.push(e.id); byId.set(e.id, e); }   // aggregated rows update in place
          this.loaded.set([...byId.values()].sort(byLastSeen));
          if (added.length) this.flash(added);
        } else {
          this.loaded.set([...page].sort(byLastSeen));
          this.hasMore.set(page.length >= PAGE_SIZE);
          this.moreError.set(false);
        }
        this.loadedOnce = true;
        this.error.set(false);
        this.loading.set(false);
        this.refreshSvc.markUpdated();
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

  loadOlder(): void {
    const list = this.loaded();
    if (!list.length || this.loadingMore()) return;
    const oldest = list[list.length - 1];
    this.loadingMore.set(true);
    this.moreError.set(false);
    this.moreSub = this.svc.queryK8sEvents(this.query(oldest.last_seen)).subscribe({
      next: page => {
        const seen = new Set(this.loaded().map(e => e.id));
        this.loaded.set([...this.loaded(), ...page.filter(e => !seen.has(e.id))].sort(byLastSeen));
        this.hasMore.set(page.length >= PAGE_SIZE);
        this.loadingMore.set(false);
      },
      error: () => { this.loadingMore.set(false); this.moreError.set(true); },
    });
  }
}
