import { Component, ChangeDetectionStrategy, signal, computed, inject } from '@angular/core';
import { ActivatedRoute, Router, RouterLink } from '@angular/router';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';
import { Subject } from 'rxjs';
import { debounceTime, distinctUntilChanged, catchError } from 'rxjs/operators';
import { of, forkJoin } from 'rxjs';

import { ContainerService } from '../../../core/services/container.service';
import { RefreshService } from '../../../core/services/refresh.service';
import { SourceStateService } from '../../../core/services/source-state.service';
import { reloadOnSourceAndRefresh } from '../../../core/utils/page-reload';
import { WorkloadSummary } from '../../../core/models/container.model';
import { WorkloadBadgeComponent } from '../../../shared/components/workload-badge/workload-badge.component';
import { PageHeaderComponent } from '../../../shared/components/page-header/page-header.component';
import { EmptyStateComponent } from '../../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../../shared/components/error-state/error-state.component';
import { SkeletonComponent } from '../../../shared/components/skeleton/skeleton.component';
import { PaginationComponent } from '../../../shared/components/pagination/pagination.component';
import { SearchHotkeyDirective } from '../../../shared/directives/search-hotkey.directive';
import { TimeagoPipe } from '../../../shared/pipes/timeago.pipe';
import { ScoreClassPipe } from '../../../shared/pipes/score-class.pipe';
import { formatScore } from '../../../shared/utils/score';
import { STATUS_DOT_CLASS, normalizeStatus } from '../../../shared/utils/status';

type SortField = 'name' | 'kind' | 'instances' | 'starts' | 'crashes' | 'ooms' | 'score' | 'last_seen';

const PAGE_SIZE = 25;
const MAIN_KINDS = ['Deployment', 'StatefulSet', 'DaemonSet', 'CronJob', 'Job', 'Compose', 'Container'];
/** Legacy `?view=` values (old Workloads sub-tabs) now map onto the kind filter. */
const LEGACY_VIEW_KIND: Record<string, string> = { deployments: 'Deployment', compose: 'Compose', cronjobs: 'CronJob' };
const KIND_CHIPS = [{ value: '', label: 'All' }, ...MAIN_KINDS.map(k => ({ value: k, label: k })), { value: 'Other', label: 'Other' }];
const STATUS_OPTIONS = [
  { value: '', label: 'All statuses' },
  { value: 'running', label: 'Running' },
  { value: 'crashing', label: 'Crashing' },
  { value: 'stopped', label: 'Stopped' },
  { value: 'unknown', label: 'Unknown' },
];

/** The Workloads page: one table of every recorded workload. Filters live in the query string (?kind=&ns=&status=&q=&system=). */
@Component({
  selector: 'app-workload-list',
  standalone: true,
  imports: [
    RouterLink, PageHeaderComponent, WorkloadBadgeComponent, EmptyStateComponent, ErrorStateComponent,
    SkeletonComponent, PaginationComponent, SearchHotkeyDirective, TimeagoPipe, ScoreClassPipe,
  ],
  templateUrl: './workload-list.component.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class WorkloadListComponent {
  private svc        = inject(ContainerService);
  private refreshSvc = inject(RefreshService);
  private route      = inject(ActivatedRoute);
  private router     = inject(Router);
  protected sourceSvc = inject(SourceStateService);

  readonly kindChips     = KIND_CHIPS;
  readonly statusOptions = STATUS_OPTIONS;
  readonly pageSize      = PAGE_SIZE;
  readonly formatScore   = formatScore;
  readonly dotClass      = (s: string) => STATUS_DOT_CLASS[normalizeStatus(s)];

  loading   = signal(true);
  error     = signal(false);
  workloads = signal<WorkloadSummary[]>([]);
  /** Archived + ignored containers (null until known / when unavailable). */
  archivedCount = signal<number | null>(null);

  // URL-backed filters
  kind      = signal('');
  namespace = signal('');
  status    = signal('');
  query     = signal('');
  system    = signal(false);
  /** Immediate input value (the URL / server query follows after the debounce). */
  searchText = signal('');

  page      = signal(1);
  sortField = signal<SortField>('score');
  sortDir   = signal<'asc' | 'desc'>('asc');

  private search$ = new Subject<string>();

  readonly hasFilters = computed(() =>
    !!(this.kind() || this.namespace() || this.status() || this.query() || this.system()));

  namespaces = computed(() => {
    const set = new Set<string>();
    for (const w of this.workloads()) if (w.namespace) set.add(w.namespace);
    const cur = this.namespace();
    if (cur) set.add(cur);
    return [...set].sort();
  });

  kindCounts = computed(() => {
    const counts = new Map<string, number>();
    for (const w of this.workloads()) {
      if (!this.matchesNs(w) || !this.matchesStatus(w)) continue;
      const key = MAIN_KINDS.includes(w.kind) ? w.kind : 'Other';
      counts.set(key, (counts.get(key) ?? 0) + 1);
      counts.set('', (counts.get('') ?? 0) + 1);
    }
    return counts;
  });

  filtered = computed(() => this.workloads().filter(w => this.matchesKind(w) && this.matchesNs(w) && this.matchesStatus(w)));

  sorted = computed(() => {
    const field = this.sortField();
    const dir = this.sortDir() === 'asc' ? 1 : -1;
    const val = (w: WorkloadSummary): string | number | null => {
      switch (field) {
        case 'name':      return w.name.toLowerCase();
        case 'kind':      return w.kind;
        case 'instances': return w.active_instances;
        case 'starts':    return w.restart_count;
        case 'crashes':   return w.crash_count;
        case 'ooms':      return w.oom_count;
        case 'score':     return w.score;
        case 'last_seen': return new Date(w.last_seen_at).getTime();
      }
    };
    return [...this.filtered()].sort((a, b) => {
      const av = val(a), bv = val(b);
      // Missing values always sink to the bottom.
      if (av === null && bv === null) return a.name.localeCompare(b.name);
      if (av === null) return 1;
      if (bv === null) return -1;
      if (av < bv) return -dir;
      if (av > bv) return dir;
      return a.name.localeCompare(b.name);
    });
  });

  totalPages = computed(() => Math.max(1, Math.ceil(this.sorted().length / PAGE_SIZE)));
  paged = computed(() => {
    const start = (Math.min(this.page(), this.totalPages()) - 1) * PAGE_SIZE;
    return this.sorted().slice(start, start + PAGE_SIZE);
  });

  constructor() {
    let first = true;
    this.route.queryParamMap.pipe(takeUntilDestroyed()).subscribe(qp => {
      // Old links: ?view=cronjobs|deployments|compose → kind filter; ?source=x → global source selector.
      const legacyKind = LEGACY_VIEW_KIND[qp.get('view') ?? ''];
      const src = qp.get('source');
      if (legacyKind || src !== null) {
        if (src) this.sourceSvc.select(src);
        this.setParams({ view: null, source: null, ...(legacyKind && !qp.get('kind') ? { kind: legacyKind } : {}) });
      }
      const q = qp.get('q') ?? '';
      const system = qp.get('system') === 'true';
      const refetch = !first && (q !== this.query() || system !== this.system());
      this.kind.set(qp.get('kind') ?? '');
      this.namespace.set(qp.get('ns') ?? '');
      this.status.set(qp.get('status') ?? '');
      this.query.set(q);
      this.system.set(system);
      if (q !== this.searchText().trim()) this.searchText.set(q);
      this.page.set(1);
      first = false;
      if (refetch) { this.loading.set(true); this.load(); }
    });

    this.search$.pipe(debounceTime(250), distinctUntilChanged(), takeUntilDestroyed())
      .subscribe(v => this.setParams({ q: v.trim() || null }));

    reloadOnSourceAndRefresh({
      onSourceChange: () => { this.page.set(1); },
      load: reason => {
        if (reason === 'source') this.loading.set(true);
        this.load();
      },
    });
  }

  load(): void {
    const source = this.sourceSvc.selectedSource() || undefined;
    forkJoin([
      this.svc.getArchivedContainers(source).pipe(catchError(() => of(null))),
      this.svc.getIgnoredContainers(source).pipe(catchError(() => of(null))),
    ]).subscribe(([a, i]) => this.archivedCount.set(a === null && i === null ? null : (a?.length ?? 0) + (i?.length ?? 0)));

    this.svc.getWorkloads({
      source,
      q: this.query(),
      system: this.system(),
    }).subscribe({
      next: list => {
        this.workloads.set((list ?? []).map(w => ({ ...w, images: w.images ?? [], containers: w.containers ?? [] })));
        this.error.set(false);
        this.loading.set(false);
        this.refreshSvc.markUpdated();
      },
      error: () => {
        // Silent refresh failures keep the last good rows.
        if (!this.workloads().length) this.error.set(true);
        this.loading.set(false);
      },
    });
  }

  private matchesKind(w: WorkloadSummary): boolean {
    const k = this.kind();
    if (!k) return true;
    if (k === 'Other') return !MAIN_KINDS.includes(w.kind);
    return w.kind === k;
  }
  private matchesNs(w: WorkloadSummary): boolean { return !this.namespace() || w.namespace === this.namespace(); }
  private matchesStatus(w: WorkloadSummary): boolean { return !this.status() || w.status === this.status(); }

  private setParams(params: Record<string, string | null>): void {
    this.router.navigate([], {
      relativeTo: this.route,
      queryParams: params,
      queryParamsHandling: 'merge',
      replaceUrl: true,
    });
  }

  setKind(v: string): void      { this.setParams({ kind: v || null }); }
  setNamespace(v: string): void { this.setParams({ ns: v || null }); }
  setStatus(v: string): void    { this.setParams({ status: v || null }); }
  setSystem(on: boolean): void  { this.setParams({ system: on ? 'true' : null }); }

  onSearch(v: string): void {
    this.searchText.set(v);
    this.search$.next(v);
  }

  clearFilters(): void {
    this.searchText.set('');
    this.setParams({ kind: null, ns: null, status: null, q: null, system: null });
  }

  setSort(f: SortField): void {
    if (this.sortField() === f) {
      this.sortDir.update(d => (d === 'asc' ? 'desc' : 'asc'));
    } else {
      this.sortField.set(f);
      this.sortDir.set(f === 'name' || f === 'kind' || f === 'score' ? 'asc' : 'desc');
    }
    this.page.set(1);
  }

  sortIndicator(f: SortField): string {
    if (this.sortField() !== f) return '↕';
    return this.sortDir() === 'asc' ? '↑' : '↓';
  }

  ariaSort(f: SortField): 'ascending' | 'descending' | 'none' {
    return this.sortField() !== f ? 'none' : this.sortDir() === 'asc' ? 'ascending' : 'descending';
  }

  firstImage(w: WorkloadSummary): string { return w.images?.[0] ?? ''; }
}
