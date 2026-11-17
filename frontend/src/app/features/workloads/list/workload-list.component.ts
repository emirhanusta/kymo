import { Component, ChangeDetectionStrategy, signal, computed, inject } from '@angular/core';
import { ActivatedRoute, Router, RouterLink } from '@angular/router';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';
import { Subject, forkJoin, of } from 'rxjs';
import { debounceTime, distinctUntilChanged, catchError } from 'rxjs/operators';

import { ContainerService } from '../../../core/services/container.service';
import { WorkloadStateService } from '../../../core/services/workload-state.service';
import { IssuesService } from '../../../core/services/issues.service';
import { SourceStateService } from '../../../core/services/source-state.service';
import { reloadOnSourceAndRefresh } from '../../../core/utils/page-reload';
import { WorkloadSummary } from '../../../core/models/container.model';
import { PeekController } from '../../../shared/utils/peek-controller';
import { WorkloadPeekComponent } from '../../../shared/components/workload-peek/workload-peek.component';
import { WorkloadBadgeComponent } from '../../../shared/components/workload-badge/workload-badge.component';
import { StatusBadgeComponent } from '../../../shared/components/status-badge/status-badge.component';
import { PageHeaderComponent } from '../../../shared/components/page-header/page-header.component';
import { EmptyStateComponent } from '../../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../../shared/components/error-state/error-state.component';
import { SkeletonComponent } from '../../../shared/components/skeleton/skeleton.component';
import { SearchHotkeyDirective } from '../../../shared/directives/search-hotkey.directive';
import { TimeagoPipe } from '../../../shared/pipes/timeago.pipe';
import { ScoreClassPipe } from '../../../shared/pipes/score-class.pipe';
import { formatScore } from '../../../shared/utils/score';
import { readStorage, writeStorage } from '../../../shared/utils/storage';
import { LONG_RUNNING_KINDS, isLongRunning } from '../../../shared/utils/workload';

type SortField = 'status' | 'name' | 'kind' | 'instances' | 'starts' | 'crashes' | 'ooms' | 'score' | 'last_seen';

const COLLAPSED_KEY = 'kymo.workloads.collapsed';
const KIND_CHIPS = [{ value: '', label: 'All' }, ...LONG_RUNNING_KINDS.map(k => ({ value: k as string, label: k as string })), { value: 'Other', label: 'Other' }];
const STATUS_OPTIONS = [
  { value: '', label: 'All statuses' },
  { value: 'running', label: 'Running' },
  { value: 'crashing', label: 'Crashing' },
  { value: 'stopped', label: 'Stopped' },
  { value: 'unknown', label: 'Unknown' },
];
const STATUS_RANK: Record<string, number> = { crashing: 0, unknown: 1, stopped: 2, running: 3 };

interface NamespaceGroup {
  key: string;            // cluster + namespace, unique across clusters
  namespace: string;
  source: string;
  rows: WorkloadSummary[];
  crashing: number;
}

/**
 * The Workloads page: long-running workloads (Deployment, StatefulSet, DaemonSet, plus ReplicaSet/Pod as "Other"),
 * grouped by namespace. CronJobs and Jobs live on /jobs. Data comes from the shared WorkloadStateService and is
 * filtered client-side; filters live in the query string (?kind=&ns=&status=&q=&system=).
 */
@Component({
  selector: 'app-workload-list',
  standalone: true,
  imports: [
    RouterLink, PageHeaderComponent, WorkloadBadgeComponent, StatusBadgeComponent, EmptyStateComponent,
    ErrorStateComponent, SkeletonComponent, SearchHotkeyDirective, TimeagoPipe, ScoreClassPipe, WorkloadPeekComponent,
  ],
  templateUrl: './workload-list.component.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class WorkloadListComponent {
  private svc        = inject(ContainerService);
  private route      = inject(ActivatedRoute);
  private router     = inject(Router);
  protected state    = inject(WorkloadStateService);
  protected issues   = inject(IssuesService);
  protected sourceSvc = inject(SourceStateService);

  readonly kindChips     = KIND_CHIPS;
  readonly statusOptions = STATUS_OPTIONS;
  readonly formatScore   = formatScore;

  /** Archived + ignored containers (null until known / when unavailable). */
  archivedCount = signal<number | null>(null);

  // URL-backed filters
  kind      = signal('');
  namespace = signal('');
  status    = signal('');
  query     = signal('');
  system    = signal(false);
  /** Immediate input value (the URL follows after the debounce). */
  searchText = signal('');

  sortField = signal<SortField>('status');
  sortDir   = signal<'asc' | 'desc'>('asc');
  collapsed = signal<ReadonlySet<string>>(new Set(readStorage<string[]>(COLLAPSED_KEY, [])));

  private search$ = new Subject<string>();

  private base = computed(() =>
    this.state.scopedWorkloads().filter(w => isLongRunning(w) && (this.system() || !w.is_system)));

  readonly hasFilters = computed(() =>
    !!(this.kind() || this.namespace() || this.status() || this.query() || this.system()));

  namespaces = computed(() => {
    const set = new Set<string>();
    for (const w of this.base()) if (w.namespace) set.add(w.namespace);
    const cur = this.namespace();
    if (cur) set.add(cur);
    return [...set].sort();
  });

  kindCounts = computed(() => {
    const counts = new Map<string, number>();
    for (const w of this.base()) {
      if (!this.matchesNs(w) || !this.matchesStatus(w) || !this.matchesQuery(w)) continue;
      const key = (LONG_RUNNING_KINDS as readonly string[]).includes(w.kind) ? w.kind : 'Other';
      counts.set(key, (counts.get(key) ?? 0) + 1);
      counts.set('', (counts.get('') ?? 0) + 1);
    }
    return counts;
  });

  filtered = computed(() =>
    this.base().filter(w => this.matchesKind(w) && this.matchesNs(w) && this.matchesStatus(w) && this.matchesQuery(w)));

  hiddenSystem = computed(() =>
    this.system() ? 0 : this.state.scopedWorkloads().filter(w => isLongRunning(w) && w.is_system).length);

  groups = computed<NamespaceGroup[]>(() => {
    const field = this.sortField();
    const dir = this.sortDir() === 'asc' ? 1 : -1;
    const val = (w: WorkloadSummary): string | number | null => {
      switch (field) {
        case 'status':    return STATUS_RANK[w.status] ?? 1;
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
    const cmp = (a: WorkloadSummary, b: WorkloadSummary) => {
      const av = val(a), bv = val(b);
      // Missing values always sink to the bottom.
      if (av === null && bv === null) return a.name.localeCompare(b.name);
      if (av === null) return 1;
      if (bv === null) return -1;
      if (av < bv) return -dir;
      if (av > bv) return dir;
      // Within equal status, worst score first.
      return (a.score ?? 101) - (b.score ?? 101) || a.name.localeCompare(b.name);
    };
    const multiCluster = this.sourceSvc.sources().length > 1 && !this.sourceSvc.selectedSource();
    const map = new Map<string, NamespaceGroup>();
    for (const w of this.filtered()) {
      const ns = w.namespace || '—';
      const key = `${w.source_name}\u0000${ns}`;
      const g = map.get(key) ?? map.set(key, { key, namespace: ns, source: multiCluster ? w.source_name : '', rows: [], crashing: 0 }).get(key)!;
      g.rows.push(w);
      if (w.status === 'crashing') g.crashing++;
    }
    const out = [...map.values()];
    for (const g of out) g.rows.sort(cmp);
    return out.sort((a, b) => a.namespace.localeCompare(b.namespace) || a.source.localeCompare(b.source));
  });

  /** Row order as displayed (collapsed namespaces skipped): j/k walk through it. */
  private orderedIds = computed(() => this.groups().filter(g => !this.collapsed().has(g.key)).flatMap(g => g.rows.map(w => w.id)));
  readonly peek = new PeekController(() => this.orderedIds());

  allCollapsed = computed(() => this.groups().length > 0 && this.groups().every(g => this.collapsed().has(g.key)));

  constructor() {
    this.route.queryParamMap.pipe(takeUntilDestroyed()).subscribe(qp => {
      // Old links: ?source=x → global cluster selector; ?view=deployments → kind filter.
      const legacyKind = qp.get('view') === 'deployments' ? 'Deployment' : '';
      const src = qp.get('source');
      if (legacyKind || src !== null || qp.has('view')) {
        if (src) this.sourceSvc.select(src);
        this.setParams({ view: null, source: null, ...(legacyKind && !qp.get('kind') ? { kind: legacyKind } : {}) });
      }
      const kind = qp.get('kind') ?? '';
      const q = qp.get('q') ?? '';
      this.kind.set(KIND_CHIPS.some(k => k.value === kind) ? kind : '');
      this.namespace.set(qp.get('ns') ?? '');
      this.status.set(qp.get('status') ?? '');
      this.query.set(q);
      this.system.set(qp.get('system') === 'true');
      if (q !== this.searchText().trim()) this.searchText.set(q);
    });

    this.search$.pipe(debounceTime(250), distinctUntilChanged(), takeUntilDestroyed())
      .subscribe(v => this.setParams({ q: v.trim() || null }));

    // Workload data is shared (WorkloadStateService); only the archived count is page-local.
    reloadOnSourceAndRefresh({ load: () => this.loadArchivedCount() });
  }

  private loadArchivedCount(): void {
    const source = this.sourceSvc.selectedSource() || undefined;
    forkJoin([
      this.svc.getArchivedContainers(source).pipe(catchError(() => of(null))),
      this.svc.getIgnoredContainers(source).pipe(catchError(() => of(null))),
    ]).subscribe(([a, i]) => this.archivedCount.set(a === null && i === null ? null : (a?.length ?? 0) + (i?.length ?? 0)));
  }

  private matchesKind(w: WorkloadSummary): boolean {
    const k = this.kind();
    if (!k) return true;
    if (k === 'Other') return !(LONG_RUNNING_KINDS as readonly string[]).includes(w.kind);
    return w.kind === k;
  }
  private matchesNs(w: WorkloadSummary): boolean { return !this.namespace() || w.namespace === this.namespace(); }
  private matchesStatus(w: WorkloadSummary): boolean { return !this.status() || w.status === this.status(); }
  private matchesQuery(w: WorkloadSummary): boolean {
    const q = this.query().trim().toLowerCase();
    if (!q) return true;
    return [w.name, w.namespace ?? '', w.source_name, w.kind, ...w.images].some(s => s.toLowerCase().includes(q));
  }

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

  toggleGroup(key: string): void {
    const next = new Set(this.collapsed());
    if (!next.delete(key)) next.add(key);
    this.collapsed.set(next);
    writeStorage(COLLAPSED_KEY, [...next]);
  }

  toggleAll(): void {
    const next = this.allCollapsed() ? new Set<string>() : new Set(this.groups().map(g => g.key));
    this.collapsed.set(next);
    writeStorage(COLLAPSED_KEY, [...next]);
  }

  setSort(f: SortField): void {
    if (this.sortField() === f) {
      this.sortDir.update(d => (d === 'asc' ? 'desc' : 'asc'));
    } else {
      this.sortField.set(f);
      this.sortDir.set(f === 'name' || f === 'kind' || f === 'score' || f === 'status' ? 'asc' : 'desc');
    }
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
