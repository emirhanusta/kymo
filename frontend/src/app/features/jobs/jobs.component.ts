import { Component, ChangeDetectionStrategy, signal, computed, inject } from '@angular/core';
import { ActivatedRoute, Router, RouterLink } from '@angular/router';
import { KeyValuePipe } from '@angular/common';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';
import { Subject } from 'rxjs';
import { debounceTime, distinctUntilChanged } from 'rxjs/operators';

import { WorkloadStateService } from '../../core/services/workload-state.service';
import { SourceStateService } from '../../core/services/source-state.service';
import { CronJobSpec } from '../../core/models/container.model';
import { PeekController } from '../../shared/utils/peek-controller';
import { WorkloadPeekComponent } from '../../shared/components/workload-peek/workload-peek.component';
import { WorkloadBadgeComponent } from '../../shared/components/workload-badge/workload-badge.component';
import { EmptyStateComponent } from '../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../shared/components/error-state/error-state.component';
import { SkeletonComponent } from '../../shared/components/skeleton/skeleton.component';
import { SearchHotkeyDirective } from '../../shared/directives/search-hotkey.directive';
import { TimeagoPipe } from '../../shared/pipes/timeago.pipe';
import { formatDuration } from '../../shared/utils/format';
import { cronKey, describeSchedule, untilLabel } from '../../shared/utils/cron';
import { JobRow, RunCell } from '../../shared/utils/jobs';

type SortField = 'default' | 'job' | 'last' | 'next' | 'success';

const KINDS = ['CronJob', 'Job'];
const STATUS_VALUES = ['failing', 'healthy'];
const STATUS_SEGMENTS = [
  { value: '', label: 'All' },
  { value: 'failing', label: 'Failing' },
  { value: 'healthy', label: 'Healthy' },
];

/** A Job named "<cronjob>-<digits>" was created from that CronJob (the scheduler's naming scheme, also used by `kubectl create job --from`). */
const CRON_CHILD = /^(.+)-\d+$/;

/** Display model of one job row, computed once per data change (never in the template). */
interface Row {
  id: string;
  j: JobRow;
  kind: string;
  failing: boolean;
  suspended: boolean;
  spec: CronJobSpec | null;
  schedule: string;
  hint: string;
  nextIso: string | null;
  nextMs: number;
  lastMs: number;
  rate: number | null;
  nameKey: string;
  cluster: string;
  /** The CronJob a standalone Job was most likely created from (inferred from its name). */
  parent: { id: string; name: string } | null;
  successTitle: string;
}

/**
 * CronJobs and standalone Jobs. A job is judged by its last run and its recent success rate, never "crashing".
 * Data comes from the shared WorkloadStateService (runs: last 20 executions; schedules: live CronJob specs).
 * Filters live in the query string (?q=&kind=&status=&suspended=&system=).
 */
@Component({
  selector: 'app-jobs',
  standalone: true,
  imports: [
    RouterLink, KeyValuePipe, WorkloadBadgeComponent, EmptyStateComponent, ErrorStateComponent,
    SkeletonComponent, SearchHotkeyDirective, TimeagoPipe, WorkloadPeekComponent,
  ],
  templateUrl: './jobs.component.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class JobsComponent {
  private route   = inject(ActivatedRoute);
  private router  = inject(Router);
  protected state = inject(WorkloadStateService);
  protected sourceSvc = inject(SourceStateService);

  readonly kinds    = KINDS;
  readonly segments = STATUS_SEGMENTS;
  readonly duration = formatDuration;
  readonly until    = untilLabel;
  readonly historyMode = this.sourceSvc.historyMode;

  kind       = signal('');
  status     = signal('');
  suspended  = signal(false);
  query      = signal('');
  system     = signal(false);
  searchText = signal('');
  sortField  = signal<SortField>('default');
  sortDir    = signal<'asc' | 'desc'>('asc');
  private search$ = new Subject<string>();

  private multiCluster = computed(() => this.sourceSvc.sources().length > 1 && !this.sourceSvc.selectedSource());

  private specIndex = computed(() => {
    const m = new Map<string, CronJobSpec>();
    for (const s of this.state.cronSpecs()) m.set(cronKey(s.source_name, s.namespace, s.name), s);
    return m;
  });

  private cronIds = computed(() => {
    const m = new Map<string, string>();
    for (const w of this.state.workloads()) if (w.kind === 'CronJob') m.set(cronKey(w.source_name, w.namespace, w.name), w.id);
    return m;
  });

  /** Every job in scope (system ones only when the System toggle is on), as display rows. */
  private rows = computed<Row[]>(() => {
    const specs = this.specIndex(), crons = this.cronIds(), multi = this.multiCluster();
    const sys = this.system();
    const out: Row[] = [];
    for (const j of this.state.scopedJobRows()) {
      const w = j.w;
      if (!sys && w.is_system) continue;
      const isCron = w.kind === 'CronJob';
      const spec = isCron ? specs.get(cronKey(w.source_name, w.namespace, w.name)) ?? null : null;
      let parent: Row['parent'] = null;
      if (!isCron) {
        const base = CRON_CHILD.exec(w.name)?.[1];
        const id = base ? crons.get(cronKey(w.source_name, w.namespace, base)) : undefined;
        if (base && id) parent = { id, name: base };
      }
      const nextIso = spec && !spec.suspend ? spec.next_run : null;
      const avg = j.avgDuration !== null ? ` · avg ${formatDuration(j.avgDuration)}` : '';
      out.push({
        id: w.id, j, kind: w.kind, failing: j.health === 'failing',
        suspended: !!spec?.suspend, spec,
        schedule: spec?.schedule ?? '', hint: spec ? describeSchedule(spec.schedule) : '',
        nextIso, nextMs: nextIso ? new Date(nextIso).getTime() : Infinity,
        lastMs: j.last?.time ?? 0, rate: j.rate,
        nameKey: w.name.toLowerCase(),
        cluster: multi ? w.source_name : '',
        parent,
        successTitle: j.finished ? `${j.ok} of ${j.finished} recent runs succeeded${avg}` : 'No finished runs',
      });
    }
    return out;
  });

  // Header summary (all rows in scope, before the other filters).
  totalCount     = computed(() => this.rows().length);
  failingCount   = computed(() => this.rows().filter(r => r.failing).length);
  suspendedCount = computed(() => this.rows().filter(r => r.suspended).length);
  hiddenSystem   = computed(() => this.system() ? 0 : this.state.scopedJobRows().filter(r => r.w.is_system).length);

  readonly hasFilters = computed(() =>
    !!(this.kind() || this.status() || this.suspended() || this.query() || this.system()));

  kindCounts = computed(() => {
    const counts = new Map<string, number>();
    for (const r of this.rows()) {
      if (!this.matchesStatus(r) || !this.matchesSuspended(r) || !this.matchesQuery(r)) continue;
      counts.set(r.kind, (counts.get(r.kind) ?? 0) + 1);
    }
    return counts;
  });

  filtered = computed(() => {
    const out = this.rows().filter(r =>
      (!this.kind() || r.kind === this.kind()) && this.matchesStatus(r) && this.matchesSuspended(r) && this.matchesQuery(r));
    const f = this.sortField();
    const dir = this.sortDir() === 'asc' ? 1 : -1;
    // Failing first, then the soonest next run, then the newest last run.
    const byDefault = (a: Row, b: Row) =>
      Number(b.failing) - Number(a.failing) ||
      (a.nextMs === b.nextMs ? 0 : a.nextMs < b.nextMs ? -1 : 1) ||
      b.lastMs - a.lastMs ||
      a.nameKey.localeCompare(b.nameKey) || a.id.localeCompare(b.id);
    const cmp = (a: Row, b: Row): number => {
      switch (f) {
        case 'job':     return a.nameKey.localeCompare(b.nameKey);
        case 'last':    return a.lastMs - b.lastMs;
        case 'next':    return a.nextMs === b.nextMs ? 0 : a.nextMs < b.nextMs ? -1 : 1;
        case 'success': return (a.rate ?? -1) - (b.rate ?? -1);
        default:        return 0;
      }
    };
    return out.sort((a, b) => (f === 'default' ? 0 : cmp(a, b) * dir) || byDefault(a, b));
  });

  readonly peek = new PeekController(() => this.filtered().map(r => r.id));

  constructor() {
    this.route.queryParamMap.pipe(takeUntilDestroyed()).subscribe(qp => {
      const kind = qp.get('kind') ?? '';
      const status = (qp.get('status') ?? '').toLowerCase();
      const q = qp.get('q') ?? '';
      this.kind.set(KINDS.includes(kind) ? kind : '');
      this.status.set(STATUS_VALUES.includes(status) ? status : '');
      this.suspended.set(qp.get('suspended') === 'true');
      this.query.set(q);
      this.system.set(qp.get('system') === 'true');
      if (q !== this.searchText().trim()) this.searchText.set(q);
    });
    this.search$.pipe(debounceTime(250), distinctUntilChanged(), takeUntilDestroyed())
      .subscribe(v => this.setParams({ q: v.trim() || null }));
  }

  private matchesStatus(r: Row): boolean {
    const s = this.status();
    return !s || (s === 'failing' ? r.failing : !r.failing);
  }
  private matchesSuspended(r: Row): boolean { return !this.suspended() || r.suspended; }
  private matchesQuery(r: Row): boolean {
    const q = this.query().trim().toLowerCase();
    if (!q) return true;
    const w = r.j.w;
    return r.nameKey.includes(q) || (w.namespace ?? '').toLowerCase().includes(q) || w.source_name.toLowerCase().includes(q);
  }

  private setParams(params: Record<string, string | null>): void {
    this.router.navigate([], { relativeTo: this.route, queryParams: params, queryParamsHandling: 'merge', replaceUrl: true });
  }

  setKind(v: string): void      { this.setParams({ kind: v || null }); }
  setStatus(v: string): void    { this.setParams({ status: v || null }); }
  setSystem(on: boolean): void  { this.setParams({ system: on ? 'true' : null }); }
  /** Header chips toggle their filter. */
  toggleStatus(v: string): void { this.setStatus(this.status() === v ? '' : v); }
  toggleSuspended(): void       { this.setParams({ suspended: this.suspended() ? null : 'true' }); }
  onSearch(v: string): void     { this.searchText.set(v); this.search$.next(v); }
  clearFilters(): void {
    this.searchText.set('');
    this.setParams({ kind: null, status: null, suspended: null, q: null, system: null });
  }

  setSort(f: SortField): void {
    if (this.sortField() === f) {
      this.sortDir.update(d => (d === 'asc' ? 'desc' : 'asc'));
    } else {
      this.sortField.set(f);
      this.sortDir.set(f === 'job' || f === 'next' ? 'asc' : 'desc');
    }
  }
  sortIndicator(f: SortField): string {
    return this.sortField() !== f ? '' : this.sortDir() === 'asc' ? '↑' : '↓';
  }
  ariaSort(f: SortField): 'ascending' | 'descending' | 'none' {
    return this.sortField() !== f ? 'none' : this.sortDir() === 'asc' ? 'ascending' : 'descending';
  }

  edge(r: Row): string {
    return this.peek.id() === r.id ? 'shadow-[inset_2px_0_0_var(--color-kymo-amber)]' : r.failing ? 'shadow-[inset_2px_0_0_var(--color-kymo-danger)]' : '';
  }

  /** Whole-row click / Enter opens the Runs tab of the job; links and buttons inside keep their own behaviour. */
  openRow(ev: Event, r: Row): void {
    if (ev.type === 'keydown') {
      if (ev.target !== ev.currentTarget) return;
      ev.preventDefault();
    } else if ((ev.target as HTMLElement).closest('a, button, input, select, textarea, label')) {
      return;
    }
    this.router.navigate(['/workloads', r.id], { queryParams: { tab: 'runs' } });
  }

  peekRow(ev: Event, r: Row): void {
    ev.stopPropagation();
    this.peek.open(r.id);
  }

  /** "Jun 3, 14:02:11 · succeeded · 6.2s · exit 1" */
  cellTitle(c: RunCell): string {
    const when = c.time ? new Date(c.time).toLocaleString() : 'unknown time';
    const state = c.state === 'running' ? 'running' : c.state === 'ok' ? 'succeeded' : 'failed';
    const parts = [when, state];
    if (c.durationSeconds != null) parts.push(formatDuration(c.durationSeconds));
    if (c.exitCode != null) parts.push(`exit ${c.exitCode}`);
    return parts.join(' · ');
  }

  /** Bar colour of one run in the strip. */
  barClass(c: RunCell): string {
    return c.state === 'ok' ? 'bg-kymo-running/40' : c.state === 'failed' ? 'bg-kymo-danger' : 'bg-kymo-amber animate-pulse';
  }
  ledClass(c: RunCell): string {
    return c.state === 'ok' ? 'bg-kymo-running' : c.state === 'failed' ? 'bg-kymo-danger' : 'bg-kymo-amber';
  }
  rateClass(r: Row): string {
    if (r.rate === null) return 'text-kymo-faint';
    return r.rate < 0.5 ? 'text-kymo-danger' : r.rate < 0.9 ? 'text-kymo-amber' : 'text-kymo-ink';
  }
}
