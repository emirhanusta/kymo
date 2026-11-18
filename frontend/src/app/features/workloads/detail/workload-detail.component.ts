import {
  Component, ChangeDetectionStrategy, signal, computed, inject, DestroyRef,
} from '@angular/core';
import { ActivatedRoute, Router, RouterLink } from '@angular/router';
import { DecimalPipe, DatePipe } from '@angular/common';
import { HttpErrorResponse } from '@angular/common/http';
import { Subscription, forkJoin, of } from 'rxjs';
import { catchError, distinctUntilChanged, map } from 'rxjs/operators';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';

import { ContainerService, CrashContext } from '../../../core/services/container.service';
import { ToastService } from '../../../core/services/toast.service';
import { RefreshService } from '../../../core/services/refresh.service';
import { PageContextService } from '../../../core/services/page-context.service';
import { IssuesService } from '../../../core/services/issues.service';
import { Issue } from '../../../core/models/issue.model';
import { ErrorSignature } from '../../../core/models/error-log.model';
import { ErrorLogService } from '../../../core/services/error-log.service';
import { WorkloadErrorsComponent } from './workload-errors.component';
import { statusReason, severityLedClass, severityTextClass } from '../../../shared/utils/issues';
import { imageNameTag } from '../../../shared/utils/format';
import { buildJobRow } from '../../../shared/utils/jobs';
import { describeSchedule, untilLabel } from '../../../shared/utils/cron';
import { SourceStateService } from '../../../core/services/source-state.service';
import {
  CronJobExecution, CronJobSpec, DeploymentDetail, K8sEvent, ResourceSample, WorkloadDetail, WorkloadScore,
} from '../../../core/models/container.model';
import { ConfirmDialogComponent } from '../../../shared/components/confirm-dialog/confirm-dialog.component';
import { StatusBadgeComponent } from '../../../shared/components/status-badge/status-badge.component';
import { LastCrashCardComponent } from '../../../shared/components/last-crash-card/last-crash-card.component';
import { WorkloadTimelineComponent } from './workload-timeline.component';
import { WorkloadPodsComponent } from './workload-pods.component';
import { WorkloadBadgeComponent } from '../../../shared/components/workload-badge/workload-badge.component';
import { ResourceChartsComponent } from '../../../shared/components/resource-charts/resource-charts.component';
import { READ_ONLY_HINT, REMOVED_HINT } from '../../../shared/utils/source';
import { ReadOnlyTagComponent } from '../../../shared/components/read-only-tag/read-only-tag.component';
import { EmptyStateComponent } from '../../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../../shared/components/error-state/error-state.component';
import { TimeagoPipe } from '../../../shared/pipes/timeago.pipe';
import { ScoreClassPipe } from '../../../shared/pipes/score-class.pipe';
import { hourAgoIso, warningsLastHour } from '../../../shared/utils/k8s-events';
import { readStorage, writeStorage } from '../../../shared/utils/storage';
import { scoreHex } from '../../../shared/utils/score';
import { AuthService } from '../../../core/services/auth.service';

const HOUR = 3_600_000;
const RANGE_KEY = 'kymo.resourceRange';

type RangeKey = '1h' | '6h' | '24h' | '7d';
const RANGES: { key: RangeKey; ms: number }[] = [
  { key: '1h',  ms: HOUR },
  { key: '6h',  ms: 6 * HOUR },
  { key: '24h', ms: 24 * HOUR },
  { key: '7d',  ms: 7 * 24 * HOUR },
];

/** `timeline` is the old key of `activity`; `?tab=timeline` keeps working as an alias. */
type TabKey = 'activity' | 'pods' | 'resources' | 'runs' | 'errors';
const TAB_ALIASES: Record<string, TabKey> = { timeline: 'activity' };

@Component({
  selector: 'app-workload-detail',
  standalone: true,
  imports: [
    RouterLink, DecimalPipe, DatePipe, StatusBadgeComponent, WorkloadBadgeComponent,
    ResourceChartsComponent, ReadOnlyTagComponent, EmptyStateComponent, ConfirmDialogComponent,
    ErrorStateComponent, TimeagoPipe, ScoreClassPipe, LastCrashCardComponent, WorkloadTimelineComponent,
    WorkloadPodsComponent, WorkloadErrorsComponent,
  ],
  templateUrl: './workload-detail.component.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class WorkloadDetailComponent {
  readonly auth = inject(AuthService);
  private route      = inject(ActivatedRoute);
  private router     = inject(Router);
  private svc        = inject(ContainerService);
  private toast      = inject(ToastService);
  private refreshSvc = inject(RefreshService);
  private pageCtx    = inject(PageContextService);
  private sources    = inject(SourceStateService);
  private issueSvc   = inject(IssuesService);
  private errorSvc   = inject(ErrorLogService);
  readonly statusReason = statusReason;
  readonly ledClass = severityLedClass;
  readonly textClass = severityTextClass;

  readonly ranges = RANGES;
  readonly until = untilLabel;

  /** `namespace/pod/container` id of a CronJob run's container, or null when the pod is unknown. */
  runRoute(ex: CronJobExecution): string[] | null {
    const c = this.runContainer();
    if (!ex.pod_name || !c) return null;
    return ['/containers', `${ex.namespace}/${ex.pod_name}/${c}`];
  }

  formatDuration(s: number | null | undefined): string {
    if (s == null || isNaN(s)) return '—';
    if (s < 60) return `${Math.round(s * 10) / 10}s`;
    const m = Math.floor(s / 60);
    return m < 60 ? `${m}m ${Math.round(s % 60)}s` : `${Math.floor(m / 60)}h ${m % 60}m`;
  }

  id        = signal('');
  loading   = signal(true);
  loadError = signal<'not-found' | 'error' | null>(null);
  detail    = signal<WorkloadDetail | null>(null);
  history   = signal<WorkloadScore[]>([]);
  /** Kubernetes Warning events (incl. repeats) of the last hour. */
  warnings1h = signal(0);
  /** The workload's open issues (server-detected). */
  openIssues = signal<Issue[]>([]);
  /** Error signatures of the last 24h (Errors tab badge). */
  errors24h  = signal<ErrorSignature[]>([]);
  hasNewError = computed(() => this.errors24h().some(e => e.is_new));
  /** Recorded history of a removed cluster: capture settings are read-only. */
  isRemovedCluster = computed(() => this.sources.isRemoved(this.workload()?.source_name));

  /** Newest crash/OOM events, crash-log snapshots and revisions (Last crash card, timeline markers, pods). */
  ctx = signal<CrashContext>({ crashEvents: [], logs: [], revisions: [] });
  private requestedTab = signal<string | null>(null);
  /** The `?tab=` value (aliases resolved) if that tab exists for this workload, else Activity. */
  tab = computed<TabKey>(() => {
    const raw = this.requestedTab();
    const want = raw ? (TAB_ALIASES[raw] ?? raw) : null;
    return this.tabs().find(t => t.key === want)?.key ?? 'activity';
  });

  resources        = signal<ResourceSample[]>([]);
  resourcesLoading = signal(false);
  range  = signal<RangeKey>(readStorage<RangeKey>(RANGE_KEY, '24h'));
  window = signal<{ from: number; to: number } | null>(null);

  // Deployment panel
  deployment     = signal<DeploymentDetail | null>(null);
  scaleValue     = signal<number | null>(null);
  busy           = signal<'scale' | 'restart' | null>(null);
  scaleOpen      = signal(false);
  menuOpen       = signal(false);
  restartConfirm = signal(false);
  scoreOpen      = signal(false);
  /** Error-log capture on (null = not known yet). */
  captureOn      = signal<boolean | null>(null);
  // CronJob runs
  executions     = signal<CronJobExecution[]>([]);
  /** Live schedule of a CronJob (null when unavailable). */
  cronSpec       = signal<CronJobSpec | null>(null);

  private mainSub?: Subscription;
  private resSub?: Subscription;
  private kindSub?: Subscription;

  // ── derived ───────────────────────────────────────────────────────────────

  workload = computed(() => this.detail()?.workload ?? null);
  score    = computed(() => this.detail()?.score ?? null);
  readOnly = computed(() => this.sources.isReadOnly(this.workload()?.source_name));
  readOnlyHint = computed(() => this.sources.isRemoved(this.workload()?.source_name) ? REMOVED_HINT : READ_ONLY_HINT);
  isDeployment = computed(() => this.workload()?.kind === 'Deployment');
  isCronJob    = computed(() => this.workload()?.kind === 'CronJob');
  isJob        = computed(() => this.workload()?.kind === 'Job');
  isJobKind    = computed(() => this.isCronJob() || this.isJob());

  /** Container name used to link a CronJob run to its container detail page. */
  runContainer = computed(() => this.workload()?.containers?.[0] ?? '');

  /** Run history of a CronJob / Job (last 20 runs) for the summary tiles. */
  jobRow = computed(() => buildJobRow({
    workload: this.workload()!,
    executions: this.isCronJob() ? this.executions() : [],
    instances: this.isJob() ? this.detail()?.instances ?? [] : [],
  }));
  scheduleHint = computed(() => describeSchedule(this.cronSpec()?.schedule ?? ''));

  /** "8 of 10 succeeded, avg 6.2s" over the loaded runs. */
  runSummary = computed(() => {
    const list = this.executions();
    if (!list.length) return null;
    const ok = list.filter(e => e.succeeded).length;
    const durs = list.map(e => e.duration_seconds).filter((d): d is number => typeof d === 'number');
    const avg = durs.length ? durs.reduce((a, b) => a + b, 0) / durs.length : null;
    return { ok, total: list.length, failed: list.length - ok, avg };
  });

  penalties = computed(() => {
    const s = this.score();
    if (!s) return [];
    return [
      { name: 'Restart',      value: s.restart_penalty },
      { name: 'Crash',        value: s.crash_penalty },
      { name: 'OOM',          value: s.oom_penalty },
      { name: 'Volatility',   value: s.volatility_penalty },
      { name: 'Availability', value: s.availability_penalty },
    ];
  });

  /** Oldest → newest, scaled into a 200×44 viewBox. */
  spark = computed(() => {
    const pts = [...this.history()].reverse();
    if (!pts.length) return null;
    const W = 200, H = 44, PAD = 4;
    const x = (i: number) => (pts.length === 1 ? W / 2 : PAD + (i / (pts.length - 1)) * (W - 2 * PAD));
    const y = (v: number) => PAD + (1 - Math.max(0, Math.min(100, v)) / 100) * (H - 2 * PAD);
    const last = pts[pts.length - 1];
    return {
      points: pts.map((p, i) => `${x(i).toFixed(1)},${y(p.score).toFixed(1)}`).join(' '),
      lastX: x(pts.length - 1), lastY: y(last.score),
      color: scoreHex(last.score), count: pts.length,
      min: Math.min(...pts.map(p => p.score)), max: Math.max(...pts.map(p => p.score)),
      single: pts.length === 1,
    };
  });

  /** Section vocabulary: a Job's pods are its run attempts. */
  instanceLabel  = computed(() => (this.isJob() ? 'Run' : 'Pod'));

  /** Cron/Job runs live in the Runs tab; a Job's pods are its runs, so it has no separate Pods tab. */
  tabs = computed<{ key: TabKey; label: string; badge?: number; dot?: boolean; muted?: boolean }[]>(() => {
    const t: { key: TabKey; label: string; badge?: number; dot?: boolean; muted?: boolean }[] = [{ key: 'activity', label: 'Activity' }];
    const w = this.workload();
    if (!this.isJob()) t.push({ key: 'pods', label: 'Pods', badge: w?.active_instances || undefined });
    if (this.isJobKind()) t.push({ key: 'runs', label: 'Runs' });
    if (w?.runtime !== 'docker') {
      const n = this.errors24h().length;
      t.push({ key: 'errors', label: 'Errors', badge: n || undefined, dot: this.hasNewError(), muted: this.captureOn() === false && n === 0 });
    }
    t.push({ key: 'resources', label: 'Resources' });
    return t;
  });

  // ── header / summary ──────────────────────────────────────────────────────

  /** Cluster is only worth showing when there is more than one to tell apart. */
  showCluster = computed(() => this.sources.sources().length > 1 || this.isRemovedCluster());
  /** "argocd:v2.11.3" (+N for more images); the full references go in the tooltip. */
  imageLine = computed(() => {
    const imgs = this.workload()?.images ?? [];
    if (!imgs.length) return '';
    return imageNameTag(imgs[0]) + (imgs.length > 1 ? ` +${imgs.length - 1}` : '');
  });
  imageTitle = computed(() => (this.workload()?.images ?? []).join('\n'));

  /** Scale / rollout restart: Deployments only, with live state, on a writable cluster. */
  canAct = computed(() => this.isDeployment() && !this.readOnly() && !!this.deployment()?.deployment);

  readyCount   = computed(() => this.deployment()?.deployment?.ready_replicas ?? this.workload()?.running_instances ?? 0);
  desiredCount = computed(() => this.deployment()?.deployment?.replicas ?? this.workload()?.active_instances ?? 0);
  readyPct     = computed(() => (this.desiredCount() > 0 ? Math.min(100, Math.round((this.readyCount() / this.desiredCount()) * 100)) : 0));
  notReady     = computed(() => Math.max(0, this.desiredCount() - this.readyCount()));

  /** Aggregated network counters are not available: hide the chart when everything is zero. */
  noNetwork = computed(() =>
    this.resources().every(r => !r.net_rx_bytes && !r.net_tx_bytes));

  replicaState = computed<'ok' | 'partial' | 'down'>(() => {
    const d = this.deployment()?.deployment;
    if (!d) return 'ok';
    if (d.replicas === 0) return d.ready_replicas === 0 ? 'ok' : 'partial';
    if (d.ready_replicas === d.replicas) return 'ok';
    return d.ready_replicas === 0 ? 'down' : 'partial';
  });

  // ── lifecycle ─────────────────────────────────────────────────────────────

  constructor() {
    this.route.paramMap.pipe(
      map(p => p.get('id') ?? ''),
      distinctUntilChanged(),
      takeUntilDestroyed(),
    ).subscribe(id => {
      this.id.set(id);
      this.resetState();
      this.loadAll(false);
    });

    this.route.queryParamMap.pipe(map(p => p.get('tab')), distinctUntilChanged(), takeUntilDestroyed())
      .subscribe(t => this.requestedTab.set(t));

    // Shared refresh bus (manual + auto): silent reloads that keep what is on screen.
    this.refreshSvc.refresh$.pipe(takeUntilDestroyed()).subscribe(() => this.loadAll(true));

    inject(DestroyRef).onDestroy(() => {
      this.mainSub?.unsubscribe();
      this.resSub?.unsubscribe();
      this.kindSub?.unsubscribe();
      this.pageCtx.detailLabel.set(null);
    });
  }

  private resetState(): void {
    this.mainSub?.unsubscribe();
    this.resSub?.unsubscribe();
    this.kindSub?.unsubscribe();
    this.loading.set(true);
    this.loadError.set(null);
    this.detail.set(null);
    this.history.set([]);
    this.ctx.set({ crashEvents: [], logs: [], revisions: [] });
    this.warnings1h.set(0);
    this.openIssues.set([]);
    this.errors24h.set([]);
    this.captureOn.set(null);
    this.scaleOpen.set(false); this.menuOpen.set(false); this.restartConfirm.set(false); this.scoreOpen.set(false);
    this.resources.set([]);
    this.deployment.set(null);
    this.scaleValue.set(null);
    this.executions.set([]);
    this.cronSpec.set(null);
    this.pageCtx.detailLabel.set(null);
  }

  retry(): void {
    this.resetState();
    this.loadAll(false);
  }

  loadAll(silent: boolean): void {
    const id = this.id();
    if (!id) return;
    this.mainSub?.unsubscribe();
    this.mainSub = forkJoin({
      detail:  this.svc.getWorkload(id),
      history: this.svc.getWorkloadScoreHistory(id, 60),
      ctx:     this.svc.getCrashContext(id),
      k8s:     this.svc.getWorkloadK8sEvents(id, { since: hourAgoIso(), limit: 500 }).pipe(catchError(() => of([] as K8sEvent[]))),
      issues:  this.issueSvc.forWorkload(id, 'open').pipe(catchError(() => of([] as Issue[]))),
      errors:  this.errorSvc.forWorkload(id, '24h').pipe(catchError(() => of([] as ErrorSignature[]))),
      capture: this.errorSvc.captureState(id).pipe(map((c): boolean | null => !!(c.enabled && c.global_enabled)), catchError(() => of(null))),
    }).subscribe({
      next: ({ detail, history, ctx, k8s, issues, errors, capture }) => {
        this.captureOn.set(capture);
        this.errors24h.set(errors);
        this.warnings1h.set(warningsLastHour(k8s));
        this.openIssues.set(issues);
        const w = detail.workload;
        this.detail.set({ ...detail, instances: detail.instances ?? [], workload: { ...w, images: w.images ?? [], containers: w.containers ?? [] } });
        this.history.set(history ?? []);
        this.ctx.set(ctx);
        this.loadError.set(null);
        this.loading.set(false);
        this.pageCtx.detailLabel.set(w.name);
        this.refreshSvc.markUpdated();
        this.loadKindPanel(silent);
      },
      error: (err: HttpErrorResponse) => {
        this.loading.set(false);
        if (silent && this.detail()) return; // keep the last good data on a failed background refresh
        this.loadError.set(err?.status === 404 ? 'not-found' : 'error');
      },
    });
    this.loadResources(silent);
  }

  // ── tabs ──────────────────────────────────────────────────────────────────

  setTab(t: TabKey): void {
    this.router.navigate([], {
      relativeTo: this.route, queryParams: { tab: t === 'activity' ? null : t },
      queryParamsHandling: 'merge', replaceUrl: true,
    });
  }

  /** Arrow keys / Home / End move between tabs (automatic activation). */
  onTabKey(ev: KeyboardEvent): void {
    const keys = this.tabs().map(t => t.key);
    const i = keys.indexOf(this.tab());
    let n = i;
    if (ev.key === 'ArrowRight') n = (i + 1) % keys.length;
    else if (ev.key === 'ArrowLeft') n = (i - 1 + keys.length) % keys.length;
    else if (ev.key === 'Home') n = 0;
    else if (ev.key === 'End') n = keys.length - 1;
    else return;
    ev.preventDefault();
    this.setTab(keys[n]);
    setTimeout(() => document.getElementById('wl-tab-' + keys[n])?.focus(), 0);
  }

  private loadResources(silent: boolean): void {
    const span = RANGES.find(r => r.key === this.range())?.ms ?? 24 * HOUR;
    const to = Date.now();
    const from = to - span;
    this.resSub?.unsubscribe();
    if (!silent) this.resourcesLoading.set(true);
    this.resSub = this.svc.getWorkloadResources(this.id(), new Date(from).toISOString(), new Date(to).toISOString()).subscribe({
      next: samples => {
        this.window.set({ from, to });
        this.resources.set(samples ?? []);
        this.resourcesLoading.set(false);
      },
      error: () => this.resourcesLoading.set(false),
    });
  }

  setRange(r: RangeKey): void {
    if (r === this.range()) return;
    this.range.set(r);
    writeStorage(RANGE_KEY, r);
    this.loadResources(false);
  }

  private loadKindPanel(silent: boolean): void {
    const w = this.workload();
    if (!w || !w.namespace) return;
    this.kindSub?.unsubscribe();
    if (w.kind === 'Deployment') {
      this.kindSub = this.svc.getDeploymentDetail(w.namespace, w.name, w.source_name).subscribe(d => {
        this.deployment.set(d);
        const replicas = d?.deployment?.replicas;
        // Don't clobber a value the user is editing on a silent refresh.
        if (replicas !== undefined && (!silent || this.scaleValue() === null)) this.scaleValue.set(replicas);
      });
    } else if (w.kind === 'CronJob') {
      this.kindSub = forkJoin({
        runs:  this.svc.getCronJobExecutions(w.namespace, w.name, 50, w.source_name),
        specs: this.svc.getCronJobSpecs(w.source_name),
      }).subscribe(({ runs, specs }) => {
        this.executions.set(runs ?? []);
        this.cronSpec.set(specs.items.find(s => s.namespace === w.namespace && s.name === w.name) ?? null);
      });
    }
  }

  // ── deployment actions ────────────────────────────────────────────────────

  openScale(): void {
    const d = this.deployment()?.deployment;
    if (d) this.scaleValue.set(d.replicas);
    this.menuOpen.set(false);
    this.scaleOpen.set(true);
  }
  closePopovers(): void { this.scaleOpen.set(false); this.menuOpen.set(false); this.scoreOpen.set(false); }
  askRestart(): void { this.menuOpen.set(false); this.scaleOpen.set(false); this.restartConfirm.set(true); }
  confirmRestart(): void { this.restartConfirm.set(false); this.restart(); }

  decrement(): void { this.scaleValue.update(v => Math.max(0, (v ?? 0) - 1)); }
  increment(): void { this.scaleValue.update(v => Math.min(50, (v ?? 0) + 1)); }

  scale(): void {
    const w = this.workload();
    const replicas = this.scaleValue();
    if (!w?.namespace || replicas === null || this.busy()) return;
    this.busy.set('scale');
    this.svc.scaleDeployment(w.namespace, w.name, replicas, w.source_name).subscribe({
      next: res => {
        this.busy.set(null);
        if (res.success) {
          this.scaleOpen.set(false);
          this.toast.success(res.message || `${w.name} scaled to ${replicas}`);
          this.loadKindPanel(false);
        } else {
          this.toast.error(res.message || 'Scale failed');
        }
      },
      error: err => {
        this.busy.set(null);
        this.toast.apiError(err, 'Scale failed');
      },
    });
  }

  restart(): void {
    const w = this.workload();
    if (!w?.namespace || this.busy()) return;
    this.busy.set('restart');
    this.svc.restartDeployment(w.namespace, w.name, w.source_name).subscribe({
      next: res => {
        this.busy.set(null);
        if (res.success) this.toast.success(res.message || `${w.name} rolling restart triggered`);
        else this.toast.error(res.message || 'Restart failed');
      },
      error: err => {
        this.busy.set(null);
        this.toast.apiError(err, 'Restart failed');
      },
    });
  }
}
