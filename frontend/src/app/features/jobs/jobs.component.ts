import { Component, ChangeDetectionStrategy, signal, computed, inject } from '@angular/core';
import { ActivatedRoute, Router, RouterLink } from '@angular/router';
import { DatePipe } from '@angular/common';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';
import { Subject } from 'rxjs';
import { debounceTime, distinctUntilChanged } from 'rxjs/operators';

import { WorkloadStateService } from '../../core/services/workload-state.service';
import { PeekController } from '../../shared/utils/peek-controller';
import { WorkloadPeekComponent } from '../../shared/components/workload-peek/workload-peek.component';
import { WorkloadBadgeComponent } from '../../shared/components/workload-badge/workload-badge.component';
import { PageHeaderComponent } from '../../shared/components/page-header/page-header.component';
import { EmptyStateComponent } from '../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../shared/components/error-state/error-state.component';
import { SkeletonComponent } from '../../shared/components/skeleton/skeleton.component';
import { SearchHotkeyDirective } from '../../shared/directives/search-hotkey.directive';
import { TimeagoPipe } from '../../shared/pipes/timeago.pipe';
import { formatDuration } from '../../shared/utils/format';
import {
  HEALTH_DOT, JobRow, RUN_CELL_CLASS, RunCell, healthRank,
} from '../../shared/utils/jobs';

const KIND_CHIPS = [
  { value: '', label: 'All' },
  { value: 'CronJob', label: 'CronJobs' },
  { value: 'Job', label: 'Jobs' },
];

/**
 * CronJobs and standalone Jobs. A job is judged by its last run and its recent success rate — never "crashing".
 * Failing jobs sort first. Data comes from the shared WorkloadStateService (CronJob runs: last 20 executions).
 */
@Component({
  selector: 'app-jobs',
  standalone: true,
  imports: [
    RouterLink, DatePipe, PageHeaderComponent, WorkloadBadgeComponent, EmptyStateComponent, ErrorStateComponent,
    SkeletonComponent, SearchHotkeyDirective, TimeagoPipe, WorkloadPeekComponent,
  ],
  templateUrl: './jobs.component.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class JobsComponent {
  private route   = inject(ActivatedRoute);
  private router  = inject(Router);
  protected state = inject(WorkloadStateService);

  readonly kindChips = KIND_CHIPS;
  readonly healthDot = HEALTH_DOT;
  readonly cellClass = RUN_CELL_CLASS;
  readonly duration  = formatDuration;

  kind       = signal('');
  query      = signal('');
  system     = signal(false);
  searchText = signal('');
  private search$ = new Subject<string>();

  private base = computed(() =>
    this.state.scopedJobRows().filter(r => this.system() || !r.w.is_system));

  kindCounts = computed(() => {
    const q = this.query().trim().toLowerCase();
    const counts = new Map<string, number>([['', 0], ['CronJob', 0], ['Job', 0]]);
    for (const r of this.base()) {
      if (q && !this.matches(r, q)) continue;
      counts.set(r.w.kind, (counts.get(r.w.kind) ?? 0) + 1);
      counts.set('', (counts.get('') ?? 0) + 1);
    }
    return counts;
  });

  rows = computed(() => {
    const q = this.query().trim().toLowerCase();
    const k = this.kind();
    return this.base()
      .filter(r => (!k || r.w.kind === k) && (!q || this.matches(r, q)))
      .sort((a, b) =>
        healthRank(a.health) - healthRank(b.health) ||
        (b.last?.time ?? 0) - (a.last?.time ?? 0) ||
        a.w.name.localeCompare(b.w.name));
  });

  readonly peek = new PeekController(() => this.rows().map(r => r.w.id));

  failingCount = computed(() => this.rows().filter(r => r.health === 'failing').length);
  hiddenSystem = computed(() => this.system() ? 0 : this.state.scopedJobRows().filter(r => r.w.is_system).length);

  constructor() {
    this.route.queryParamMap.pipe(takeUntilDestroyed()).subscribe(qp => {
      const kind = qp.get('kind') ?? '';
      const q = qp.get('q') ?? '';
      this.kind.set(KIND_CHIPS.some(c => c.value === kind) ? kind : '');
      this.query.set(q);
      this.system.set(qp.get('system') === 'true');
      if (q !== this.searchText().trim()) this.searchText.set(q);
    });
    this.search$.pipe(debounceTime(250), distinctUntilChanged(), takeUntilDestroyed())
      .subscribe(v => this.setParams({ q: v.trim() || null }));
  }

  private matches(r: JobRow, q: string): boolean {
    return [r.w.name, r.w.namespace ?? '', r.w.source_name].some(s => s.toLowerCase().includes(q));
  }

  private setParams(params: Record<string, string | null>): void {
    this.router.navigate([], { relativeTo: this.route, queryParams: params, queryParamsHandling: 'merge', replaceUrl: true });
  }

  setKind(v: string): void     { this.setParams({ kind: v || null }); }
  setSystem(on: boolean): void { this.setParams({ system: on ? 'true' : null }); }
  onSearch(v: string): void    { this.searchText.set(v); this.search$.next(v); }
  clearFilters(): void         { this.searchText.set(''); this.setParams({ kind: null, q: null, system: null }); }

  hasFilters(): boolean { return !!(this.kind() || this.query() || this.system()); }

  /** "Jun 3, 14:02:11 · 6.2s · exit 1" */
  cellTitle(c: RunCell): string {
    const when = c.time ? new Date(c.time).toLocaleString() : 'unknown time';
    const state = c.state === 'running' ? 'running' : c.state === 'ok' ? 'succeeded' : 'failed';
    const parts = [when, state];
    if (c.durationSeconds != null) parts.push(formatDuration(c.durationSeconds));
    if (c.exitCode != null) parts.push(`exit ${c.exitCode}`);
    return parts.join(' · ');
  }

  rateLabel(r: JobRow): string {
    return r.finished ? `${r.ok}/${r.finished}` : '—';
  }
  rateClass(r: JobRow): string {
    if (r.rate === null) return 'text-kymo-faint';
    return r.rate >= 0.95 ? 'text-kymo-running' : r.rate >= 0.8 ? 'text-kymo-ink' : r.rate >= 0.5 ? 'text-kymo-amber' : 'text-kymo-danger';
  }
  healthLabel(r: JobRow): string {
    return { failing: 'Failing', flaky: 'Flaky', ok: 'Healthy', none: 'No runs' }[r.health];
  }
}
