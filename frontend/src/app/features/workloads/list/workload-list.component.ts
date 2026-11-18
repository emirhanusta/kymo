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
import { Container, WorkloadSummary } from '../../../core/models/container.model';
import { PeekController } from '../../../shared/utils/peek-controller';
import { WorkloadPeekComponent } from '../../../shared/components/workload-peek/workload-peek.component';
import { WorkloadBadgeComponent } from '../../../shared/components/workload-badge/workload-badge.component';
import { EmptyStateComponent } from '../../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../../shared/components/error-state/error-state.component';
import { SkeletonComponent } from '../../../shared/components/skeleton/skeleton.component';
import { SearchHotkeyDirective } from '../../../shared/directives/search-hotkey.directive';
import { TimeagoPipe } from '../../../shared/pipes/timeago.pipe';
import { readStorage, writeStorage } from '../../../shared/utils/storage';
import { LONG_RUNNING_KINDS, isLongRunning } from '../../../shared/utils/workload';

type SortField = 'status' | 'name' | 'ready' | 'restarts' | 'updated';
type Tone = 'bad' | 'warn' | 'ok' | 'muted';

const COLLAPSED_KEY = 'kymo.workloads.collapsed';
const GROUP_KEY     = 'kymo.workloads.groupByNamespace';
const KINDS = [...(LONG_RUNNING_KINDS as readonly string[]), 'Other'];
/** Values accepted by ?status= ('unknown' is the pre-redesign spelling of 'pending'). */
const STATUS_VALUES = ['problems', 'healthy', 'crashing', 'degraded', 'running', 'stopped', 'pending'];
const STATUS_SEGMENTS = [
  { value: '', label: 'All' },
  { value: 'problems', label: 'Problems' },
  { value: 'healthy', label: 'Healthy' },
];

const EDGE: Record<Tone, string> = {
  bad: 'shadow-[inset_2px_0_0_var(--color-kymo-danger)]',
  warn: 'shadow-[inset_2px_0_0_var(--color-kymo-amber)]',
  ok: '', muted: '',
};
export const PEEK_EDGE = 'shadow-[inset_2px_0_0_var(--color-kymo-amber)]';

/** Display model of one workload row, computed once per data change (never in the template). */
interface Row {
  id: string;
  w: WorkloadSummary;
  label: 'Running' | 'Crashing' | 'Degraded' | 'Pending' | 'Stopped';
  tone: Tone;
  problem: boolean;
  crashing: boolean;
  /** Problem that is not a crash: pods not all ready, or an open issue. */
  degraded: boolean;
  issues: number;
  rank: number;           // 0 crashing, 1 other problems, 2 the rest
  readyRatio: number;     // 0..1, 1 for 0/0
  noPods: boolean;
  image: string;
  imageTitle: string;
  moreImages: number;
  updated: number;
  nameKey: string;
  cluster: string;
}

interface NamespaceGroup {
  key: string;
  namespace: string;
  cluster: string;
  rows: Row[];
  problems: number;
}

interface DisplayItem { key: string; r?: Row; g?: NamespaceGroup }

/** "registry.io/team/app:1.2" → "app:1.2"; digests are shortened. */
function shortImage(ref: string): string {
  if (!ref) return '';
  const at = ref.indexOf('@');
  const base = at >= 0 ? ref.slice(0, at) : ref;
  const name = base.slice(base.lastIndexOf('/') + 1);
  return at >= 0 ? `${name}@${ref.slice(at + 1, at + 1 + 14)}` : name;
}

/**
 * The Workloads page: long-running workloads (Deployment, StatefulSet, DaemonSet, plus ReplicaSet/Pod as "Other").
 * CronJobs and Jobs live on /jobs. Data comes from WorkloadStateService / IssuesService and is filtered client-side;
 * filters live in the query string (?q=&ns=&kind=&status=&system=). The cluster scope is the global scope bar's.
 */
@Component({
  selector: 'app-workload-list',
  standalone: true,
  imports: [
    RouterLink, WorkloadBadgeComponent, EmptyStateComponent, ErrorStateComponent, SkeletonComponent,
    SearchHotkeyDirective, TimeagoPipe, WorkloadPeekComponent,
  ],
  templateUrl: './workload-list.component.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class WorkloadListComponent {
  private svc        = inject(ContainerService);
  private route      = inject(ActivatedRoute);
  private router     = inject(Router);
  private issueSvc   = inject(IssuesService);
  protected state    = inject(WorkloadStateService);
  protected sourceSvc = inject(SourceStateService);

  readonly kinds    = KINDS;
  readonly segments = STATUS_SEGMENTS;
  readonly peekEdge = PEEK_EDGE;
  readonly historyMode = this.sourceSvc.historyMode;

  /** Archived + ignored containers (null until known / when unavailable). */
  private archivedAll = signal<Container[] | null>(null);
  private ignoredAll  = signal<Container[] | null>(null);
  archivedCount = computed<number | null>(() => {
    const a = this.archivedAll(), i = this.ignoredAll();
    if (a === null && i === null) return null;
    const inScope = (l: Container[] | null) => this.sourceSvc.inScope(l ?? [], c => c.source_name).length;
    return inScope(a) + inScope(i);
  });

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
  groupByNs = signal<boolean>(readStorage<boolean>(GROUP_KEY, false) === true);
  collapsed = signal<ReadonlySet<string>>(new Set(readStorage<string[]>(COLLAPSED_KEY, [])));

  private search$ = new Subject<string>();

  private multiCluster = computed(() => this.sourceSvc.sources().length > 1 && !this.sourceSvc.selectedSource());

  private openIssueCounts = computed(() => {
    const m = new Map<string, number>();
    if (this.historyMode()) return m;
    for (const i of this.issueSvc.scopedOpen()) m.set(i.workload_id, (m.get(i.workload_id) ?? 0) + 1);
    return m;
  });

  /** Every long-running workload in scope (system ones only when the System toggle is on), as display rows. */
  private rows = computed<Row[]>(() => {
    const counts = this.openIssueCounts();
    const sys = this.system();
    const multi = this.multiCluster();
    const out: Row[] = [];
    for (const w of this.state.scopedWorkloads()) {
      if (!isLongRunning(w) || (!sys && w.is_system)) continue;
      const notReady = w.running_instances < w.active_instances;
      const issues = counts.get(w.id) ?? 0;
      const crashing = w.status === 'crashing';
      const degraded = !crashing && (notReady || issues > 0);
      let label: Row['label'] = 'Running';
      if (crashing) label = 'Crashing';
      else if (w.status === 'stopped') label = 'Stopped';
      else if (w.status === 'unknown') label = 'Pending';
      else if (notReady) label = 'Degraded';
      const tone: Tone = crashing ? 'bad' : degraded ? 'warn' : label === 'Running' ? 'ok' : 'muted';
      const images = w.images ?? [];
      out.push({
        id: w.id, w, label, tone, problem: crashing || degraded, crashing, degraded, issues,
        rank: crashing ? 0 : degraded ? 1 : 2,
        readyRatio: w.active_instances > 0 ? w.running_instances / w.active_instances : 1,
        noPods: w.active_instances === 0,
        image: shortImage(images[0] ?? ''),
        imageTitle: images.join('\n'),
        moreImages: Math.max(0, images.length - 1),
        updated: new Date(w.last_seen_at).getTime() || 0,
        nameKey: w.name.toLowerCase(),
        cluster: multi ? w.source_name : '',
      });
    }
    return out;
  });

  // Header summary (all rows in scope, before search/namespace/kind/status filters).
  totalCount    = computed(() => this.rows().length);
  crashingCount = computed(() => this.rows().filter(r => r.crashing).length);
  degradedCount = computed(() => this.rows().filter(r => r.degraded).length);

  readonly hasFilters = computed(() =>
    !!(this.kind() || this.namespace() || this.status() || this.query() || this.system()));

  namespaces = computed(() => {
    const set = new Set<string>();
    for (const r of this.rows()) if (r.w.namespace) set.add(r.w.namespace);
    const cur = this.namespace();
    if (cur) set.add(cur);
    return [...set].sort();
  });

  kindCounts = computed(() => {
    const counts = new Map<string, number>();
    for (const r of this.rows()) {
      if (!this.matchesNs(r) || !this.matchesStatus(r) || !this.matchesQuery(r)) continue;
      const key = this.kindKey(r);
      counts.set(key, (counts.get(key) ?? 0) + 1);
    }
    return counts;
  });

  filtered = computed(() => {
    const out = this.rows().filter(r => this.matchesKind(r) && this.matchesNs(r) && this.matchesStatus(r) && this.matchesQuery(r));
    const dir = this.sortDir() === 'asc' ? 1 : -1;
    const f = this.sortField();
    const num = (r: Row): number => {
      switch (f) {
        case 'status':   return r.rank;
        case 'ready':    return r.readyRatio;
        case 'restarts': return r.w.crash_count + r.w.oom_count;
        case 'updated':  return r.updated;
        default:         return 0;
      }
    };
    return out.sort((a, b) => {
      const d = f === 'name' ? a.nameKey.localeCompare(b.nameKey) : num(a) - num(b);
      return d * dir || a.rank - b.rank || a.nameKey.localeCompare(b.nameKey) || a.id.localeCompare(b.id);
    });
  });

  groups = computed<NamespaceGroup[]>(() => {
    if (!this.groupByNs()) return [];
    const map = new Map<string, NamespaceGroup>();
    for (const r of this.filtered()) {
      const ns = r.w.namespace || '—';
      const key = `${r.w.source_name}\u0000${ns}`;
      let g = map.get(key);
      if (!g) map.set(key, g = { key, namespace: ns, cluster: r.cluster, rows: [], problems: 0 });
      g.rows.push(r);
      if (r.problem) g.problems++;
    }
    return [...map.values()].sort((a, b) => a.namespace.localeCompare(b.namespace) || a.cluster.localeCompare(b.cluster));
  });

  /** Flat list of group headers and rows in display order, so the template has a single row definition. */
  display = computed<DisplayItem[]>(() => {
    if (!this.groupByNs()) return this.filtered().map(r => ({ key: r.id, r }));
    const out: DisplayItem[] = [];
    const collapsed = this.collapsed();
    for (const g of this.groups()) {
      out.push({ key: 'g:' + g.key, g });
      if (!collapsed.has(g.key)) for (const r of g.rows) out.push({ key: r.id, r });
    }
    return out;
  });

  /** Row order as displayed (collapsed namespaces skipped): j/k walk through it while the peek drawer is open. */
  private orderedIds = computed(() => this.groupByNs()
    ? this.groups().filter(g => !this.collapsed().has(g.key)).flatMap(g => g.rows.map(r => r.id))
    : this.filtered().map(r => r.id));
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
      let status = (qp.get('status') ?? '').toLowerCase();
      if (status === 'unknown') status = 'pending';
      this.kind.set(KINDS.includes(kind) ? kind : '');
      this.namespace.set(qp.get('ns') ?? '');
      this.status.set(STATUS_VALUES.includes(status) ? status : '');
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
    ]).subscribe(([a, i]) => { this.archivedAll.set(a); this.ignoredAll.set(i); });
  }

  private kindKey(r: Row): string { return KINDS.includes(r.w.kind) ? r.w.kind : 'Other'; }
  private matchesKind(r: Row): boolean { const k = this.kind(); return !k || this.kindKey(r) === k; }
  private matchesNs(r: Row): boolean { return !this.namespace() || r.w.namespace === this.namespace(); }
  private matchesStatus(r: Row): boolean {
    switch (this.status()) {
      case '':         return true;
      case 'problems': return r.problem;
      case 'healthy':  return !r.problem;
      case 'crashing': return r.crashing;
      case 'degraded': return r.degraded;
      default:         return r.label.toLowerCase() === this.status();
    }
  }
  private matchesQuery(r: Row): boolean {
    const q = this.query().trim().toLowerCase();
    if (!q) return true;
    return r.nameKey.includes(q) || (r.w.namespace ?? '').toLowerCase().includes(q)
      || r.w.source_name.toLowerCase().includes(q) || r.w.images.some(i => i.toLowerCase().includes(q));
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
  /** Header chips toggle their filter. */
  toggleStatus(v: string): void { this.setStatus(this.status() === v ? '' : v); }

  onSearch(v: string): void {
    this.searchText.set(v);
    this.search$.next(v);
  }

  clearFilters(): void {
    this.searchText.set('');
    this.setParams({ kind: null, ns: null, status: null, q: null, system: null });
  }

  setGroupByNs(on: boolean): void {
    this.groupByNs.set(on);
    writeStorage(GROUP_KEY, on);
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
      this.sortDir.set(f === 'name' || f === 'status' || f === 'ready' ? 'asc' : 'desc');
    }
  }

  sortIndicator(f: SortField): string {
    if (this.sortField() !== f) return '';
    return this.sortDir() === 'asc' ? '↑' : '↓';
  }

  ariaSort(f: SortField): 'ascending' | 'descending' | 'none' {
    return this.sortField() !== f ? 'none' : this.sortDir() === 'asc' ? 'ascending' : 'descending';
  }

  edge(r: Row): string { return this.peek.id() === r.id ? PEEK_EDGE : EDGE[r.tone]; }

  /** Whole-row click / Enter opens the detail page; links and buttons inside keep their own behaviour. */
  openRow(ev: Event, r: Row): void {
    if (ev.type === 'keydown') {
      if (ev.target !== ev.currentTarget) return;
      ev.preventDefault();
    } else if ((ev.target as HTMLElement).closest('a, button, input, select, textarea, label')) {
      return;
    }
    this.router.navigate(['/workloads', r.id]);
  }

  peekRow(ev: Event, r: Row): void {
    ev.stopPropagation();
    this.peek.open(r.id);
  }
}
