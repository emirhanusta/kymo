import { Component, ChangeDetectionStrategy, signal, computed, inject } from '@angular/core';
import { RouterLink } from '@angular/router';
import { catchError } from 'rxjs/operators';
import { of } from 'rxjs';

import { RefreshService } from '../../core/services/refresh.service';
import { SourceStateService } from '../../core/services/source-state.service';
import { WorkloadStateService } from '../../core/services/workload-state.service';
import { IssuesService } from '../../core/services/issues.service';
import { ErrorLogService } from '../../core/services/error-log.service';
import { OverviewService } from '../../core/services/overview.service';
import { reloadOnSourceAndRefresh } from '../../core/utils/page-reload';
import { Activity, ActivityBucket, OverviewChange } from '../../core/models/overview.model';
import { ErrorSignature } from '../../core/models/error-log.model';
import { SourceStatus, WorkloadSummary } from '../../core/models/container.model';
import { TimeagoPipe } from '../../shared/pipes/timeago.pipe';
import { WorkloadBadgeComponent } from '../../shared/components/workload-badge/workload-badge.component';
import { EmptyStateComponent } from '../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../shared/components/error-state/error-state.component';
import { ErrorSparkComponent } from '../../shared/components/error-spark/error-spark.component';
import { ErrorTemplateComponent } from '../../shared/components/error-spark/error-template.component';
import { isLongRunning } from '../../shared/utils/workload';
import { sourceDotClass, sourceStatusLabel } from '../../shared/utils/source';
import { issueWorkloadQuery, severityLedClass, severityTextClass } from '../../shared/utils/issues';
import {
  bucketCrashes, changeLabel, deltaLabel, niceMax, severityBreakdown, sortIssues,
} from '../../shared/utils/overview';

const MAX_ISSUES = 6;
const MAX_CHANGES = 6;
const MAX_ERRORS = 5;
const ACTIVITY_HOURS = 24;

/** Chart geometry (SVG user units; the SVG scales to its container width). */
const CHART_W = 960;
const CHART_H = 160;

interface ClusterRow {
  name: string;
  source: SourceStatus | null;
  workloads: number;
  ready: number;
  desired: number;
  issues: number;
}

interface Bar {
  index: number;
  x: number;
  w: number;
  segments: { y: number; h: number; cls: string }[];
}

/** Overview: is everything OK, what needs attention, what changed. Respects the cluster scope and history mode. */
@Component({
  selector: 'app-dashboard',
  standalone: true,
  imports: [
    RouterLink, TimeagoPipe, WorkloadBadgeComponent, EmptyStateComponent, ErrorStateComponent,
    ErrorSparkComponent, ErrorTemplateComponent,
  ],
  templateUrl: './dashboard.component.html',
  styleUrl: './dashboard.component.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class DashboardComponent {
  private refreshSvc = inject(RefreshService);
  private sourceSvc  = inject(SourceStateService);
  private issueSvc   = inject(IssuesService);
  private errorSvc   = inject(ErrorLogService);
  private overviewSvc = inject(OverviewService);
  protected state    = inject(WorkloadStateService);

  readonly issueQuery = issueWorkloadQuery;
  readonly issueLed   = severityLedClass;
  readonly issueText  = severityTextClass;
  readonly dotClass   = sourceDotClass;
  readonly statusLabel = sourceStatusLabel;
  readonly changeLabel = changeLabel;
  readonly chartW = CHART_W;
  readonly chartH = CHART_H;

  /** A removed cluster is selected: recorded history only (issues are frozen and not tracked). */
  historyMode = this.sourceSvc.historyMode;
  issuesLoaded = this.issueSvc.loaded;
  sourceCount = computed(() => this.sourceSvc.sources().length);
  private selected = this.sourceSvc.selectedSource;

  // --- data ---
  /** `null` = not loaded yet for this scope. */
  activity = signal<Activity | null>(null);
  activityError = signal(false);
  changes = signal<OverviewChange[] | null>(null);
  changesError = signal(false);
  private loadedErrors  = signal<ErrorSignature[]>([]);

  // --- scope ---
  // Stopped workloads with no pods left (finished one-off pods, scaled-away apps) are not "live" and would
  // otherwise make "14 / 15 healthy" disagree with "All workloads healthy".
  private workloads = computed(() =>
    this.sourceSvc.inScope(this.state.workloads(), w => w.source_name)
      .filter(w => !w.is_system && isLongRunning(w) && !(w.status === 'stopped' && w.active_instances === 0)));
  openIssues = computed(() => this.historyMode() ? [] : sortIssues(this.issueSvc.scopedOpen()));
  topIssues = computed(() => this.openIssues().slice(0, MAX_ISSUES));
  criticalCount = computed(() => this.openIssues().filter(i => i.severity === 'critical').length);
  warningCount  = computed(() => this.openIssues().length - this.criticalCount());
  breakdown = computed(() => severityBreakdown(this.criticalCount(), this.warningCount()));

  /** Workloads that are crashing or have an open issue. */
  private troubled = computed(() => {
    const ids = new Set(this.openIssues().map(i => i.workload_id));
    return this.workloads().filter(w => w.status === 'crashing' || ids.has(w.id));
  });
  workloadCount = computed(() => this.workloads().length);
  unhealthyCount = computed(() => this.troubled().length);
  healthyCount = computed(() => {
    const bad = new Set(this.troubled().map(w => w.id));
    return this.workloads().filter(w => w.status === 'running' && !bad.has(w.id)).length;
  });
  desiredPods = computed(() => this.workloads().reduce((n, w) => n + w.active_instances, 0));
  readyPods   = computed(() => this.workloads().reduce((n, w) => n + w.running_instances, 0));
  notReadyPods = computed(() => this.desiredPods() - this.readyPods());
  readyPct = computed(() => this.desiredPods() ? Math.round(this.readyPods() / this.desiredPods() * 100) : 0);

  // --- health statement ---
  health = computed<{ tone: 'ok' | 'warn' | 'bad' | 'history'; text: string; detail: string }>(() => {
    if (this.historyMode()) return { tone: 'history', text: 'Recorded history', detail: 'Live status is not tracked for removed clusters' };
    const n = this.openIssues().length;
    if (n > 0) {
      return {
        tone: this.criticalCount() > 0 ? 'bad' : 'warn',
        text: `${n} issue${n === 1 ? '' : 's'} need${n === 1 ? 's' : ''} attention`,
        detail: this.breakdown(),
      };
    }
    const crashing = this.workloads().filter(w => w.status === 'crashing').length;
    if (crashing > 0) return { tone: 'bad', text: `${crashing} workload${crashing === 1 ? ' is' : 's are'} crashing`, detail: '' };
    return { tone: 'ok', text: 'All workloads healthy', detail: '' };
  });
  scopeText = computed(() => {
    const sel = this.selected();
    if (this.historyMode()) return `history of ${sel}`;
    if (sel) return `in ${sel}`;
    const n = this.sourceSvc.sources().length;
    return n ? `across ${n} cluster${n === 1 ? '' : 's'}` : '';
  });

  // --- crashes KPI ---
  crashes24h = computed(() => (this.activity()?.buckets ?? []).reduce((n, b) => n + bucketCrashes(b), 0));
  crashDelta = computed(() => deltaLabel(this.crashes24h(), this.activity()?.previous_crashes ?? 0));
  sparkBars = computed(() => {
    const b = this.activity()?.buckets ?? [];
    const max = Math.max(1, ...b.map(bucketCrashes));
    return b.map((x, i) => ({ x: i * 4, h: bucketCrashes(x) ? Math.max(2, Math.round(bucketCrashes(x) / max * 20)) : 1, hot: bucketCrashes(x) > 0 }));
  });

  // --- activity chart ---
  private chartMax = computed(() => niceMax(Math.max(0, ...(this.activity()?.buckets ?? []).map(b => b.crashes + b.ooms + b.starts))));
  yTicks = computed(() => {
    const m = this.chartMax();
    return [m, m / 2].map(v => ({ label: Number.isInteger(v) ? String(v) : v.toFixed(1), y: CHART_H - (v / m) * CHART_H }));
  });
  bars = computed<Bar[]>(() => {
    const buckets = this.activity()?.buckets ?? [];
    const n = buckets.length;
    if (!n) return [];
    const m = this.chartMax();
    const slot = CHART_W / n, w = slot * 0.62;
    return buckets.map((b, i) => {
      let y = CHART_H;
      const segments: Bar['segments'] = [];
      for (const [v, cls] of [[b.crashes, 'fill-kymo-danger'], [b.ooms, 'fill-kymo-amber'], [b.starts, 'fill-kymo-faint']] as [number, string][]) {
        if (v <= 0) continue;
        const h = v / m * CHART_H;
        y -= h;
        segments.push({ y, h, cls });
      }
      return { index: i, x: i * slot + (slot - w) / 2, w, segments };
    });
  });
  /** Hour labels every 6h, positioned in percent of the chart width. */
  xLabels = computed(() => {
    const buckets = this.activity()?.buckets ?? [];
    const n = buckets.length;
    return buckets.map((b, i) => ({ b, i })).filter(({ b }) => new Date(b.start).getHours() % 6 === 0)
      .map(({ b, i }) => ({ left: (i + 0.5) / n * 100, text: String(new Date(b.start).getHours()).padStart(2, '0') + ':00' }));
  });
  hovered = signal<number | null>(null);
  hoveredBucket = computed<ActivityBucket | null>(() => {
    const i = this.hovered();
    return i === null ? null : this.activity()?.buckets[i] ?? null;
  });
  hoveredRange = computed(() => {
    const b = this.hoveredBucket();
    if (!b) return '';
    const start = new Date(b.start), end = new Date(start.getTime() + 3600_000);
    const t = (d: Date) => d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hour12: false });
    return `${start.toLocaleDateString([], { weekday: 'short' })} ${t(start)}–${t(end)}`;
  });
  tooltipLeft = computed(() => {
    const i = this.hovered(), n = this.activity()?.buckets.length ?? 0;
    return i === null || !n ? 0 : Math.min(Math.max((i + 0.5) / n * 100, 12), 88);
  });
  activityTotals = computed(() => {
    const b = this.activity()?.buckets ?? [];
    return { starts: b.reduce((n, x) => n + x.starts, 0), crashes: b.reduce((n, x) => n + x.crashes, 0), ooms: b.reduce((n, x) => n + x.ooms, 0) };
  });
  activitySummary = computed(() => {
    const t = this.activityTotals();
    return `Activity over the last 24 hours: ${t.starts} starts, ${t.crashes} crashes, ${t.ooms} OOM kills`;
  });

  // --- top errors ---
  topErrors = computed(() =>
    [...this.sourceSvc.inScope(this.loadedErrors(), e => e.source_name)]
      .sort((a, b) => b.count - a.count || b.last_seen.localeCompare(a.last_seen)).slice(0, MAX_ERRORS));
  /** Only when log capture produced signatures. */
  showErrors = computed(() => this.topErrors().length > 0);

  // --- clusters ---
  showClusters = computed(() => !this.selected() && !this.historyMode() && this.sourceSvc.sources().length >= 2);
  clusterRows = computed<ClusterRow[]>(() => {
    const issueCount = new Map<string, number>();
    for (const i of this.issueSvc.open()) issueCount.set(i.source_name, (issueCount.get(i.source_name) ?? 0) + 1);
    const byCluster = new Map<string, WorkloadSummary[]>();
    for (const w of this.workloads()) (byCluster.get(w.source_name) ?? byCluster.set(w.source_name, []).get(w.source_name)!).push(w);
    return this.sourceSvc.sources().map(s => {
      const ws = byCluster.get(s.name) ?? [];
      return {
        name: s.name, source: s, workloads: ws.length,
        ready: ws.reduce((n, w) => n + w.running_instances, 0),
        desired: ws.reduce((n, w) => n + w.active_instances, 0),
        issues: issueCount.get(s.name) ?? 0,
      };
    });
  });

  constructor() {
    reloadOnSourceAndRefresh({
      load: reason => this.load(reason === 'source' || reason === 'init'),
    });
  }

  selectCluster(name: string): void {
    this.sourceSvc.select(name);
  }

  retryAll(): void {
    this.state.load();
    this.issueSvc.load();
    this.load(true);
  }

  load(reset = false): void {
    const source = this.selected();
    if (reset) {
      this.activity.set(null);
      this.changes.set(null);
      this.loadedErrors.set([]);
    }
    this.overviewSvc.activity(source, ACTIVITY_HOURS).pipe(catchError(() => of(null))).subscribe(a => {
      this.activityError.set(a === null);
      if (a) this.activity.set(a);
      this.refreshSvc.markUpdated();
    });
    this.overviewSvc.changes(source, MAX_CHANGES).pipe(catchError(() => of(null))).subscribe(c => {
      this.changesError.set(c === null);
      if (c) this.changes.set(c);
    });
    // The API orders by recency; fetch a wider page so the most frequent ones can be picked client-side.
    this.errorSvc.list({ since: '24h', limit: 50, source: source || undefined }).pipe(catchError(() => of(null)))
      .subscribe(list => { if (list) this.loadedErrors.set(list); });
  }
}
