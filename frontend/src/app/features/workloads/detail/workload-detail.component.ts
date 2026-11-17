import {
  Component, ChangeDetectionStrategy, signal, computed, inject, DestroyRef,
} from '@angular/core';
import { ActivatedRoute, RouterLink } from '@angular/router';
import { DecimalPipe, DatePipe, NgTemplateOutlet } from '@angular/common';
import { HttpErrorResponse } from '@angular/common/http';
import { Subscription, forkJoin } from 'rxjs';
import { distinctUntilChanged, map } from 'rxjs/operators';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';

import { ContainerService } from '../../../core/services/container.service';
import { ToastService } from '../../../core/services/toast.service';
import { RefreshService } from '../../../core/services/refresh.service';
import { PageContextService } from '../../../core/services/page-context.service';
import { SourceStateService } from '../../../core/services/source-state.service';
import {
  Container, CronJobExecution, DeploymentDetail, LifecycleEvent, ResourceSample,
  WorkloadDetail, WorkloadScore,
} from '../../../core/models/container.model';
import { PageHeaderComponent } from '../../../shared/components/page-header/page-header.component';
import { StatusBadgeComponent } from '../../../shared/components/status-badge/status-badge.component';
import { WorkloadBadgeComponent } from '../../../shared/components/workload-badge/workload-badge.component';
import { StabilityGaugeComponent } from '../../../shared/components/stability-gauge/stability-gauge.component';
import { ResourceChartsComponent } from '../../../shared/components/resource-charts/resource-charts.component';
import { ReadOnlyTagComponent } from '../../../shared/components/read-only-tag/read-only-tag.component';
import { EmptyStateComponent } from '../../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../../shared/components/error-state/error-state.component';
import { TimeagoPipe } from '../../../shared/pipes/timeago.pipe';
import { ScoreClassPipe } from '../../../shared/pipes/score-class.pipe';
import { readStorage, writeStorage } from '../../../shared/utils/storage';
import { containerRoute } from '../../../shared/utils/format';
import { podNameFromId } from '../../../shared/utils/source';
import { scoreHex } from '../../../shared/utils/score';
import { STATUS_DOT_CLASS, StatusKey, normalizeStatus, worstStatus } from '../../../shared/utils/status';
import { eventDotClass, eventLabel, eventReason, eventTextClass, showsExitCode } from '../../../shared/utils/events';

const HOUR = 3_600_000;
const RANGE_KEY = 'kymo.resourceRange';
const EVENTS_PAGE = 30;

type RangeKey = '1h' | '6h' | '24h' | '7d';
const RANGES: { key: RangeKey; ms: number }[] = [
  { key: '1h',  ms: HOUR },
  { key: '6h',  ms: 6 * HOUR },
  { key: '24h', ms: 24 * HOUR },
  { key: '7d',  ms: 7 * 24 * HOUR },
];

/** All containers of one pod (k8s) or one container (compose). */
interface InstanceGroup {
  key: string;
  name: string;
  node: string;
  containers: Container[];
  status: StatusKey;
  current: boolean;
  restarts: number;
  crashes: number;
  firstSeen: string;
  lastSeen: string;
}

const time = (s: string) => new Date(s).getTime();

@Component({
  selector: 'app-workload-detail',
  standalone: true,
  imports: [
    RouterLink, DecimalPipe, DatePipe, NgTemplateOutlet, PageHeaderComponent, StatusBadgeComponent, WorkloadBadgeComponent,
    StabilityGaugeComponent, ResourceChartsComponent, ReadOnlyTagComponent, EmptyStateComponent,
    ErrorStateComponent, TimeagoPipe, ScoreClassPipe,
  ],
  templateUrl: './workload-detail.component.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class WorkloadDetailComponent {
  private route      = inject(ActivatedRoute);
  private svc        = inject(ContainerService);
  private toast      = inject(ToastService);
  private refreshSvc = inject(RefreshService);
  private pageCtx    = inject(PageContextService);
  private sources    = inject(SourceStateService);

  readonly ranges = RANGES;
  readonly eventLabel  = eventLabel;
  readonly eventReason = eventReason;
  readonly eventDot    = eventDotClass;
  readonly eventText   = eventTextClass;
  readonly showsExit   = showsExitCode;
  readonly dotClass    = (s: string) => STATUS_DOT_CLASS[normalizeStatus(s)];
  readonly containerRoute = containerRoute;
  readonly podOf = (id: string) => podNameFromId(id);

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

  events        = signal<LifecycleEvent[]>([]);
  hasMoreEvents = signal(false);
  loadingMore   = signal(false);

  resources        = signal<ResourceSample[]>([]);
  resourcesLoading = signal(false);
  range  = signal<RangeKey>(readStorage<RangeKey>(RANGE_KEY, '24h'));
  window = signal<{ from: number; to: number } | null>(null);

  showPrevious = signal(false);

  // Deployment panel
  deployment     = signal<DeploymentDetail | null>(null);
  scaleValue     = signal<number | null>(null);
  busy           = signal<'scale' | 'restart' | null>(null);
  // CronJob runs
  executions     = signal<CronJobExecution[]>([]);

  private mainSub?: Subscription;
  private resSub?: Subscription;
  private kindSub?: Subscription;
  private moreSub?: Subscription;

  // ── derived ───────────────────────────────────────────────────────────────

  workload = computed(() => this.detail()?.workload ?? null);
  score    = computed(() => this.detail()?.score ?? null);
  isK8s    = computed(() => this.workload()?.runtime === 'kubernetes');
  readOnly = computed(() => this.sources.isReadOnly(this.workload()?.source_name));
  isDeployment = computed(() => this.workload()?.kind === 'Deployment');
  isCronJob    = computed(() => this.workload()?.kind === 'CronJob');
  isJob        = computed(() => this.workload()?.kind === 'Job');

  /** Container name used to link a CronJob run to its container detail page. */
  runContainer = computed(() => this.workload()?.containers?.[0] ?? '');

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

  groups = computed<InstanceGroup[]>(() => {
    const w = this.workload();
    const k8s = w?.runtime === 'kubernetes';
    const map = new Map<string, Container[]>();
    for (const c of this.detail()?.instances ?? []) {
      const key = k8s ? (podNameFromId(c.id) || c.id) : c.id;
      (map.get(key) ?? map.set(key, []).get(key)!).push(c);
    }
    const out: InstanceGroup[] = [];
    for (const [key, cs] of map) {
      const statuses = cs.map(c => c.current_status);
      out.push({
        key,
        name: k8s ? key : cs[0].name,
        node: cs.find(c => c.node_name)?.node_name ?? '',
        containers: cs,
        status: worstStatus(statuses),
        current: statuses.some(s => normalizeStatus(s) !== 'stopped'),
        restarts: cs.reduce((n, c) => n + Math.max(0, c.start_count - 1), 0),
        crashes: cs.reduce((n, c) => n + c.crash_count, 0),
        firstSeen: cs.reduce((a, c) => (time(c.first_seen_at) < time(a) ? c.first_seen_at : a), cs[0].first_seen_at),
        lastSeen: cs.reduce((a, c) => (time(c.last_seen_at) > time(a) ? c.last_seen_at : a), cs[0].last_seen_at),
      });
    }
    return out.sort((a, b) => time(b.lastSeen) - time(a.lastSeen));
  });
  currentGroups  = computed(() => this.groups().filter(g => g.current));
  previousGroups = computed(() => this.groups().filter(g => !g.current));
  /** Section vocabulary: a Job's pods are its run attempts. */
  instanceLabel  = computed(() => (this.isJob() ? 'Run' : this.isK8s() ? 'Pod' : 'Container'));

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

    // Shared refresh bus (manual + auto): silent reloads that keep what is on screen.
    this.refreshSvc.refresh$.pipe(takeUntilDestroyed()).subscribe(() => this.loadAll(true));

    inject(DestroyRef).onDestroy(() => {
      this.mainSub?.unsubscribe();
      this.resSub?.unsubscribe();
      this.kindSub?.unsubscribe();
      this.moreSub?.unsubscribe();
      this.pageCtx.detailLabel.set(null);
    });
  }

  private resetState(): void {
    this.mainSub?.unsubscribe();
    this.resSub?.unsubscribe();
    this.kindSub?.unsubscribe();
    this.moreSub?.unsubscribe();
    this.loading.set(true);
    this.loadError.set(null);
    this.detail.set(null);
    this.history.set([]);
    this.events.set([]);
    this.hasMoreEvents.set(false);
    this.loadingMore.set(false);
    this.resources.set([]);
    this.deployment.set(null);
    this.scaleValue.set(null);
    this.executions.set([]);
    this.showPrevious.set(false);
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
      events:  this.svc.getWorkloadEvents(id, { limit: EVENTS_PAGE }),
    }).subscribe({
      next: ({ detail, history, events }) => {
        const w = detail.workload;
        this.detail.set({ ...detail, instances: detail.instances ?? [], workload: { ...w, images: w.images ?? [], containers: w.containers ?? [] } });
        this.history.set(history ?? []);
        this.mergeEvents(events ?? [], silent);
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

  /** First load replaces; silent refreshes merge so already-loaded older pages stay. */
  private mergeEvents(fresh: LifecycleEvent[], silent: boolean): void {
    if (!silent || !this.events().length) {
      this.events.set(fresh);
      this.hasMoreEvents.set(fresh.length >= EVENTS_PAGE);
      return;
    }
    const byId = new Map(this.events().map(e => [e.id, e]));
    for (const e of fresh) byId.set(e.id, e);
    this.events.set([...byId.values()].sort((a, b) => time(b.occurred_at) - time(a.occurred_at)));
  }

  loadMoreEvents(): void {
    const list = this.events();
    const last = list[list.length - 1];
    if (!last || this.loadingMore()) return;
    this.loadingMore.set(true);
    this.moreSub?.unsubscribe();
    this.moreSub = this.svc.getWorkloadEvents(this.id(), { limit: EVENTS_PAGE, before: last.occurred_at }).subscribe({
      next: older => {
        const have = new Set(this.events().map(e => e.id));
        this.events.update(l => [...l, ...(older ?? []).filter(e => !have.has(e.id))]);
        this.hasMoreEvents.set((older ?? []).length >= EVENTS_PAGE);
        this.loadingMore.set(false);
      },
      error: err => {
        this.loadingMore.set(false);
        this.toast.apiError(err, 'Failed to load older events');
      },
    });
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
      this.kindSub = this.svc.getCronJobExecutions(w.namespace, w.name, 50, w.source_name)
        .subscribe(list => this.executions.set(list ?? []));
    }
  }

  // ── deployment actions ────────────────────────────────────────────────────

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
