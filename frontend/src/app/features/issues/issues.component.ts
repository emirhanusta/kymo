import { Component, ChangeDetectionStrategy, signal, computed, inject } from '@angular/core';
import { ActivatedRoute, Router, RouterLink } from '@angular/router';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';
import { Subject } from 'rxjs';
import { debounceTime, distinctUntilChanged } from 'rxjs/operators';

import { IssuesService } from '../../core/services/issues.service';
import { SourceStateService } from '../../core/services/source-state.service';
import { Issue, IssueSeverity, IssueType } from '../../core/models/issue.model';
import { reloadOnSourceAndRefresh } from '../../core/utils/page-reload';
import { WorkloadBadgeComponent } from '../../shared/components/workload-badge/workload-badge.component';
import { EmptyStateComponent } from '../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../shared/components/error-state/error-state.component';
import { SkeletonComponent } from '../../shared/components/skeleton/skeleton.component';
import { SearchHotkeyDirective } from '../../shared/directives/search-hotkey.directive';
import { TimeagoPipe } from '../../shared/pipes/timeago.pipe';
import { formatSpan } from '../../shared/utils/format';
import { ISSUE_TYPES, issueTypeLabel, issueWorkloadQuery, severityLedClass } from '../../shared/utils/issues';
import { IssueDrawerComponent } from './issue-drawer.component';

type Tab = 'open' | 'resolved';
const PAGE = 50;
const TYPE_VALUES = ISSUE_TYPES.map(t => t.type) as string[];
const SEVERITY_SEGMENTS = [
  { value: '', label: 'All' },
  { value: 'critical', label: 'Critical' },
  { value: 'warning', label: 'Warning' },
];

/** Display model of one issue row, computed once per data change (never in the template). */
interface Row {
  i: Issue;
  typeLabel: string;
  /** Cluster name, shown muted only when it helps (scope = all clusters, 2+ clusters). */
  cluster: string;
  removed: boolean;
  forTxt: string;
  duration: string;
}
type Entry = { day: string; label: string } | { row: Row };

/**
 * Server-tracked issues: what is broken right now (Open) and what was fixed (Resolved).
 * State lives in the query string: ?tab=resolved &q= &type= &severity= &issue=<id> (details drawer).
 */
@Component({
  selector: 'app-issues',
  standalone: true,
  imports: [
    RouterLink, WorkloadBadgeComponent, EmptyStateComponent, ErrorStateComponent, SkeletonComponent,
    SearchHotkeyDirective, TimeagoPipe, IssueDrawerComponent,
  ],
  templateUrl: './issues.component.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class IssuesComponent {
  protected svc     = inject(IssuesService);
  protected sources = inject(SourceStateService);
  private route     = inject(ActivatedRoute);
  private router    = inject(Router);

  readonly types    = ISSUE_TYPES;
  readonly segments = SEVERITY_SEGMENTS;
  readonly led      = severityLedClass;
  readonly wlQuery  = issueWorkloadQuery;

  tab        = signal<Tab>('open');
  query      = signal('');
  type       = signal('');
  severity   = signal('');
  issueId    = signal<number | null>(null);
  searchText = signal('');
  private search$ = new Subject<string>();

  private openAll = this.svc.scopedOpen;

  // ── Resolved tab (loaded on demand, cursor-paged; type/severity/source are server-side) ──
  resolved        = signal<Issue[]>([]);
  resolvedLoaded  = signal(false);
  resolvedError   = signal(false);
  hasMore         = signal(false);
  loadingMore     = signal(false);
  private resolvedSeq = 0;
  /** An issue opened by deep link that is in neither loaded list. */
  private fetched = signal<Issue | null>(null);

  private multiCluster = computed(() => this.sources.sources().length > 1 && !this.sources.selectedSource());

  readonly hasFilters = computed(() => !!(this.query() || this.type() || this.severity()));

  // Header summary (all open issues in scope, before the filters).
  openCount     = computed(() => this.openAll().length);
  criticalCount = computed(() => this.openAll().filter(i => i.severity === 'critical').length);
  warningCount  = computed(() => this.openCount() - this.criticalCount());

  private matchesQuery(i: Issue): boolean {
    const q = this.query().trim().toLowerCase();
    return !q || i.title.toLowerCase().includes(q) || i.name.toLowerCase().includes(q) || (i.namespace ?? '').toLowerCase().includes(q);
  }

  /** Per-type counts of the open issues that match the other filters (feeds the Type select). */
  typeCounts = computed(() => {
    const counts = new Map<string, number>();
    const sev = this.severity();
    for (const i of this.openAll()) {
      if ((sev && i.severity !== sev) || !this.matchesQuery(i)) continue;
      counts.set(i.type, (counts.get(i.type) ?? 0) + 1);
    }
    return counts;
  });

  /** Types offered by the select: on Open only those that exist (plus the selected one), on Resolved all. */
  typeOptions = computed(() => {
    if (this.tab() === 'resolved') return this.types;
    const c = this.typeCounts();
    return this.types.filter(t => (c.get(t.type) ?? 0) > 0 || t.type === this.type());
  });

  private toRow(i: Issue, multi: boolean): Row {
    const end = i.resolved_at ? new Date(i.resolved_at).getTime() : Date.now();
    const span = formatSpan(end - new Date(i.first_seen).getTime());
    return {
      // The type chip is dropped when the title already starts with it ("Degraded (0/1 ready)").
      i, typeLabel: i.title.toLowerCase().startsWith(issueTypeLabel(i.type).toLowerCase()) ? '' : issueTypeLabel(i.type),
      cluster: multi ? i.source_name : '',
      removed: this.sources.isRemoved(i.source_name),
      forTxt: i.resolved_at ? span : `for ${span}`,
      duration: span,
    };
  }

  openRows = computed<Row[]>(() => {
    const multi = this.multiCluster(), t = this.type(), s = this.severity();
    return this.openAll()
      .filter(i => (!t || i.type === t) && (!s || i.severity === s) && this.matchesQuery(i))
      .sort((a, b) =>
        Number(b.severity === 'critical') - Number(a.severity === 'critical') ||
        new Date(b.first_seen).getTime() - new Date(a.first_seen).getTime() || b.id - a.id)
      .map(i => this.toRow(i, multi));
  });

  /** Resolved issues grouped by the (local) day they were resolved, as a flat list with day headers. */
  resolvedEntries = computed<Entry[]>(() => {
    const out: Entry[] = [];
    let day = '';
    for (const i of this.resolved()) {
      if (!this.matchesQuery(i)) continue;
      const d = new Date(i.resolved_at ?? i.last_seen);
      const key = `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`;
      if (key !== day) { day = key; out.push({ day: key, label: this.dayLabel(d) }); }
      out.push({ row: this.toRow(i, true) });
    }
    return out;
  });
  resolvedShown = computed(() => this.resolvedEntries().filter(e => 'row' in e).length);

  /** Row selected by the open drawer (list data first, a direct fetch for deep links). */
  drawerIssue = computed<Issue | null>(() => {
    const id = this.issueId();
    if (id === null) return null;
    return this.openAll().find(i => i.id === id) ?? this.resolved().find(i => i.id === id)
      ?? (this.fetched()?.id === id ? this.fetched() : null);
  });

  constructor() {
    this.route.queryParamMap.pipe(takeUntilDestroyed()).subscribe(qp => {
      const type = qp.get('type') ?? '';
      const sev = qp.get('severity') ?? '';
      const q = qp.get('q') ?? '';
      const id = Number(qp.get('issue'));
      const nextType = TYPE_VALUES.includes(type) ? type : '';
      const nextSev = sev === 'critical' || sev === 'warning' ? sev : '';
      const serverChanged = nextType !== this.type() || nextSev !== this.severity();
      this.tab.set(qp.get('tab') === 'resolved' ? 'resolved' : 'open');
      this.type.set(nextType);
      this.severity.set(nextSev);
      this.query.set(q);
      if (q !== this.searchText().trim()) this.searchText.set(q);
      this.issueId.set(Number.isInteger(id) && id > 0 ? id : null);
      if (serverChanged && this.resolvedSeq > 0) this.loadResolved(true);
      this.ensureIssue();
    });
    this.search$.pipe(debounceTime(250), distinctUntilChanged(), takeUntilDestroyed())
      .subscribe(v => this.setParams({ q: v.trim() || null }));
    reloadOnSourceAndRefresh({ load: reason => this.loadResolved(reason === 'init' || reason === 'source') });
  }

  /** Deep link to an issue that neither list holds (e.g. an older resolved one): fetch it. */
  private ensureIssue(): void {
    const id = this.issueId();
    if (id === null || this.fetched()?.id === id) return;
    this.svc.get(id).subscribe(i => {
      if (this.issueId() !== id) return;
      if (i) this.fetched.set(i); else this.setParams({ issue: null });
    });
  }

  private setParams(params: Record<string, string | null>): void {
    this.router.navigate([], { relativeTo: this.route, queryParams: params, queryParamsHandling: 'merge', replaceUrl: true });
  }

  setTab(t: Tab): void       { this.setParams({ tab: t === 'open' ? null : t, issue: null }); }
  setType(v: string): void   { this.setParams({ type: v || null }); }
  setSeverity(v: string): void { this.setParams({ severity: v || null }); }
  toggleSeverity(v: IssueSeverity): void { this.setSeverity(this.severity() === v ? '' : v); }
  onSearch(v: string): void  { this.searchText.set(v); this.search$.next(v); }
  clearFilters(): void {
    this.searchText.set('');
    this.setParams({ q: null, type: null, severity: null });
  }
  openIssue(id: number): void { this.setParams({ issue: String(id) }); }
  closeIssue(): void          { this.setParams({ issue: null }); }

  /** Whole-row click / Enter opens the drawer; links inside keep their own behaviour. */
  openRow(ev: Event, r: Row): void {
    if (ev.type === 'keydown') {
      if (ev.target !== ev.currentTarget) return;
      ev.preventDefault();
    } else if ((ev.target as HTMLElement).closest('a, button, input, select, textarea, label')) {
      return;
    }
    this.openIssue(r.i.id);
  }

  edge(i: Issue): string {
    if (this.issueId() === i.id) return 'shadow-[inset_2px_0_0_var(--color-kymo-amber)]';
    return i.severity === 'critical' ? 'shadow-[inset_2px_0_0_var(--color-kymo-danger)]' : 'shadow-[inset_2px_0_0_color-mix(in_srgb,var(--color-kymo-amber)_55%,transparent)]';
  }

  /** Arrow keys / Home / End move between the two tabs. */
  onTabKey(ev: KeyboardEvent): void {
    const keys: Tab[] = ['open', 'resolved'];
    let n = keys.indexOf(this.tab());
    if (ev.key === 'ArrowRight' || ev.key === 'ArrowLeft') n = 1 - n;
    else if (ev.key === 'Home') n = 0;
    else if (ev.key === 'End') n = 1;
    else return;
    ev.preventDefault();
    this.setTab(keys[n]);
    setTimeout(() => document.getElementById('issues-tab-' + keys[n])?.focus(), 0);
  }

  private dayLabel(d: Date): string {
    const startOf = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
    const diff = Math.round((startOf(new Date()) - startOf(d)) / 86400000);
    const full = d.toLocaleDateString(undefined, { weekday: 'short', month: 'short', day: 'numeric' });
    return diff === 0 ? `Today · ${full}` : diff === 1 ? `Yesterday · ${full}` : full;
  }

  private resolvedQuery() {
    return {
      status: 'resolved' as const,
      source: this.sources.selectedSource(),
      type: (this.type() || undefined) as IssueType | undefined,
      severity: (this.severity() || undefined) as IssueSeverity | undefined,
      limit: PAGE,
    };
  }

  /** (Re)loads the first page of resolved issues; silent refreshes keep what is on screen. */
  loadResolved(reset: boolean): void {
    if (this.sources.historyMode()) return;
    const seq = ++this.resolvedSeq;
    if (reset) { this.resolved.set([]); this.resolvedLoaded.set(false); }
    this.svc.list(this.resolvedQuery()).subscribe({
      next: list => {
        if (seq !== this.resolvedSeq) return;
        this.resolved.set(list);
        this.hasMore.set(list.length >= PAGE);
        this.resolvedError.set(false);
        this.resolvedLoaded.set(true);
      },
      error: () => {
        if (seq !== this.resolvedSeq) return;
        this.resolvedError.set(true);
        this.resolvedLoaded.set(true);
      },
    });
  }

  loadMore(): void {
    const last = this.resolved().at(-1);
    if (!last || this.loadingMore()) return;
    const seq = this.resolvedSeq;
    this.loadingMore.set(true);
    this.svc.list({ ...this.resolvedQuery(), before: last.resolved_at ?? last.first_seen }).subscribe({
      next: list => {
        this.loadingMore.set(false);
        if (seq !== this.resolvedSeq) return;
        const seen = new Set(this.resolved().map(i => i.id));
        const fresh = list.filter(i => !seen.has(i.id));
        this.resolved.update(cur => [...cur, ...fresh]);
        this.hasMore.set(list.length >= PAGE && fresh.length > 0);
      },
      error: () => this.loadingMore.set(false),
    });
  }
}
