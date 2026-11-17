import {
  Component, ChangeDetectionStrategy, signal, computed, effect,
  inject, DestroyRef,
} from '@angular/core';
import { RouterLink, ActivatedRoute, Router } from '@angular/router';
import { DecimalPipe } from '@angular/common';
import { HttpErrorResponse } from '@angular/common/http';
import { Observable, Subscription, forkJoin } from 'rxjs';
import { distinctUntilChanged, filter, map } from 'rxjs/operators';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';

import { ContainerService } from '../../../core/services/container.service';
import { ToastService } from '../../../core/services/toast.service';
import { RefreshService } from '../../../core/services/refresh.service';
import { PageContextService } from '../../../core/services/page-context.service';
import { SourceStateService } from '../../../core/services/source-state.service';
import { ReadOnlyTagComponent } from '../../../shared/components/read-only-tag/read-only-tag.component';
import {
  ActionResult, Container, StabilityScore, LifecycleEvent, ResourceSample, PodDetail,
} from '../../../core/models/container.model';
import { MetricCardComponent } from '../../../shared/components/metric-card/metric-card.component';
import { StatusBadgeComponent } from '../../../shared/components/status-badge/status-badge.component';
import { WorkloadBadgeComponent } from '../../../shared/components/workload-badge/workload-badge.component';
import { StabilityGaugeComponent } from '../../../shared/components/stability-gauge/stability-gauge.component';
import { TimeagoPipe } from '../../../shared/pipes/timeago.pipe';
import { CrashLogInlineComponent } from '../../../shared/components/crash-log-inline/crash-log-inline.component';
import { READ_ONLY_HINT, REMOVED_HINT } from '../../../shared/utils/source';
import { normalizeStatus } from '../../../shared/utils/status';
import { LogViewerComponent } from '../../../shared/components/log-viewer/log-viewer.component';
import { DeleteModalComponent } from '../../../shared/components/delete-modal/delete-modal.component';
import { ResourceChartsComponent } from '../../../shared/components/resource-charts/resource-charts.component';
import { EmptyStateComponent } from '../../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../../shared/components/error-state/error-state.component';
import { ScoreClassPipe } from '../../../shared/pipes/score-class.pipe';
import { PageHeaderComponent } from '../../../shared/components/page-header/page-header.component';
import { formatBytes } from '../../../shared/utils/format';
import { readStorage, writeStorage } from '../../../shared/utils/storage';
import { eventDotClass, eventLabel, eventReason, eventTextClass, hasCrashLogs, showsExitCode } from '../../../shared/utils/events';

const HOUR = 3_600_000;
const DETAIL_REFRESH_MS = 15_000;
const RANGE_KEY = 'kymo.resourceRange';

export type RangeKey = '1h' | '6h' | '24h' | '7d';
const RANGES: { key: RangeKey; ms: number }[] = [
  { key: '1h',  ms: HOUR },
  { key: '6h',  ms: 6 * HOUR },
  { key: '24h', ms: 24 * HOUR },
  { key: '7d',  ms: 7 * 24 * HOUR },
];

@Component({
  selector: 'app-container-detail',
  standalone: true,
  imports: [
    RouterLink, DecimalPipe,
    MetricCardComponent, StatusBadgeComponent, WorkloadBadgeComponent, StabilityGaugeComponent,
    TimeagoPipe, LogViewerComponent, CrashLogInlineComponent, DeleteModalComponent,
    ResourceChartsComponent, EmptyStateComponent, ErrorStateComponent, PageHeaderComponent, ScoreClassPipe,
    ReadOnlyTagComponent,
  ],
  templateUrl: './container-detail.component.html',
  styleUrl: './container-detail.component.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ContainerDetailComponent {
  private route      = inject(ActivatedRoute);
  private router     = inject(Router);
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
  readonly crashLogs   = hasCrashLogs;

  id        = signal('');
  loading   = signal(true);
  loadError = signal<'not-found' | 'error' | null>(null);
  container = signal<Container | null>(null);
  score     = signal<StabilityScore | null>(null);
  events    = signal<LifecycleEvent[]>([]);
  resources = signal<ResourceSample[]>([]);
  resourcesLoading = signal(false);
  podDetail = signal<PodDetail | null>(null);

  range  = signal<RangeKey>(readStorage<RangeKey>(RANGE_KEY, '24h'));
  window = signal<{ from: number; to: number } | null>(null);

  actionBusy      = signal(false);
  showLogViewer   = signal(false);
  showDeleteModal = signal(false);
  private modalOpen = computed(() => this.showLogViewer() || this.showDeleteModal());

  private mainSub?: Subscription;
  private resSub?: Subscription;
  private podSub?: Subscription;

  // ── computed ──────────────────────────────────────────────────────────────

  /** Newest first, regardless of backend ordering. */
  sortedEvents = computed(() =>
    [...this.events()].sort((a, b) => new Date(b.occurred_at).getTime() - new Date(a.occurred_at).getTime())
  );

  cpuStats = computed(() => {
    const rs = this.resources();
    if (!rs.length) return null;
    const vals = rs.map(r => r.cpu_percent);
    return { avg: vals.reduce((a, b) => a + b, 0) / vals.length, peak: Math.max(...vals) };
  });

  memStats = computed(() => {
    const rs = this.resources();
    if (!rs.length) return null;
    const latest = rs.reduce((a, b) => (new Date(b.sampled_at) > new Date(a.sampled_at) ? b : a));
    const avg = rs.reduce((s, r) => s + r.mem_bytes, 0) / rs.length;
    return { latest: latest.mem_bytes, latestPct: latest.mem_percent, avg };
  });

  cpuDisplay = computed(() => this.cpuStats()?.avg.toFixed(1) ?? '—');
  cpuSub     = computed(() => {
    const s = this.cpuStats();
    return s ? `peak ${s.peak.toFixed(1)}% · ${this.range()}` : `no samples · ${this.range()}`;
  });
  memDisplay = computed(() => (this.memStats() ? formatBytes(this.memStats()!.latest) : '—'));
  memSub     = computed(() => {
    const m = this.memStats();
    return m ? `avg ${formatBytes(m.avg)} · ${this.range()}` : `no samples · ${this.range()}`;
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

  /** Read-only clusters: hide restart pod; logs and archive/ignore stay available. */
  readOnly = computed(() => this.sources.isReadOnly(this.container()?.source_name));
  /** Cluster no longer registered: no live logs, crash snapshots only. */
  removed = computed(() => this.sources.isRemoved(this.container()?.source_name));
  readOnlyHint = computed(() => this.removed() ? REMOVED_HINT : READ_ONLY_HINT);

  podSiblings = computed(() => {
    const detail = this.podDetail();
    const self   = this.container();
    if (!detail || !self) return [];
    return detail.containers.filter(c => c.id !== self.id);
  });

  /** Containers of this pod for the log viewer's switcher. */
  podContainers = computed(() => (this.podDetail()?.containers ?? []).map(c => ({ id: c.id, name: c.name })));

  /** CronJob / Job pods: only Logs makes sense. */
  isCronJobOrJob = computed(() =>
    ['CronJob', 'Job'].includes(this.container()?.workload_kind ?? '')
  );

  /** Breadcrumb goes back to the owning workload when known, else the workload list. */
  backPath  = computed<string | unknown[]>(() => {
    const id = this.container()?.workload_id;
    return id ? ['/workloads', id] : this.isJobKind() ? '/jobs' : '/workloads';
  });
  private isJobKind = computed(() => this.isCronJobOrJob());
  backLabel = computed(() => {
    const c = this.container();
    return c?.workload_id && c.workload_name ? c.workload_name : this.isJobKind() ? 'Jobs' : 'Workloads';
  });

  // ── lifecycle ─────────────────────────────────────────────────────────────

  constructor() {
    // The component is reused when navigating between sibling containers → react to every id change.
    this.route.paramMap.pipe(
      map(p => p.get('id') ?? ''),
      distinctUntilChanged(),
      takeUntilDestroyed(),
    ).subscribe(id => {
      this.id.set(id);
      this.resetState();
      this.loadAll(false);
    });

    // Manual refresh from the navbar; auto ticks are handled by the faster local timer below.
    this.refreshSvc.refresh$.pipe(
      filter(r => r === 'manual'),
      takeUntilDestroyed(),
    ).subscribe(() => this.loadAll(true));

    // Light auto-refresh: silent, every 15s, paused while a modal is open or auto-refresh is off.
    effect(onCleanup => {
      if (!this.refreshSvc.autoRefresh() || this.modalOpen()) return;
      const t = setInterval(() => {
        if (document.visibilityState === 'visible') this.loadAll(true);
      }, DETAIL_REFRESH_MS);
      onCleanup(() => clearInterval(t));
    });

    effect(() => this.pageCtx.detailLabel.set(this.container()?.name ?? null));

    inject(DestroyRef).onDestroy(() => {
      this.mainSub?.unsubscribe();
      this.resSub?.unsubscribe();
      this.podSub?.unsubscribe();
      this.pageCtx.detailLabel.set(null);
    });
  }

  // ── data loading ──────────────────────────────────────────────────────────

  private resetState(): void {
    this.loading.set(true);
    this.loadError.set(null);
    this.container.set(null);
    this.score.set(null);
    this.events.set([]);
    this.resources.set([]);
    this.podDetail.set(null);
    this.showLogViewer.set(false);
    this.showDeleteModal.set(false);
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
      container: this.svc.getContainer(id),
      score:     this.svc.getStabilityScore(id),
      events:    this.svc.getLifecycleEvents(id),
    }).subscribe({
      next: ({ container, score, events }) => {
        this.container.set(container);
        this.score.set(score);
        this.events.set(events ?? []);
        this.loadError.set(null);
        this.loading.set(false);
        this.refreshSvc.markUpdated();
        this.loadPod(container);
      },
      error: (err: HttpErrorResponse) => {
        this.loading.set(false);
        // Silent refresh failures keep the last good data on screen.
        if (silent && this.container()) return;
        this.loadError.set(err?.status === 404 ? 'not-found' : 'error');
      },
    });

    this.loadResources(silent);
  }

  private loadPod(container: Container): void {
    const parts = container.id.split('/');
    if (parts.length < 2) return;
    this.podSub?.unsubscribe();
    this.podSub = this.svc.getPodDetail(parts[0], parts[1], container.source_name).subscribe({
      next: detail => this.podDetail.set(detail),
    });
  }

  private loadResources(silent: boolean): void {
    const id = this.id();
    const span = RANGES.find(r => r.key === this.range())?.ms ?? 24 * HOUR;
    const to = Date.now();
    const from = to - span;
    this.resSub?.unsubscribe();
    if (!silent) this.resourcesLoading.set(true);
    this.resSub = this.svc.getResourceSamples(id, new Date(from).toISOString(), new Date(to).toISOString()).subscribe({
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

  // ── actions ───────────────────────────────────────────────────────────────

  restartContainer(): void {
    this.runAction(() => this.svc.restartContainer(this.id()), 'Pod deleted — Kubernetes will recreate it', 'Failed to restart pod');
  }

  /** Stopped containers: the pod is (almost always) gone, so open the logs viewer in snapshot mode. */
  isStopped = computed(() => normalizeStatus(this.container()?.current_status ?? '') === 'stopped');

  openLogs(): void { this.showLogViewer.set(true); }

  openDeleteModal(): void { this.showDeleteModal.set(true); }

  onDeleteAction(action: 'soft' | 'ignore' | 'cancel'): void {
    this.showDeleteModal.set(false);
    if (action === 'cancel') return;

    const id = this.id();
    const back = this.backPath();
    const obs = action === 'soft'
      ? this.svc.softDeleteContainer(id)
      : this.svc.ignoreContainer(id);
    const successMsg = action === 'soft' ? 'Container archived' : 'Container ignored';

    obs.subscribe({
      next: res => {
        this.toast.success(res?.message || successMsg);
        this.router.navigateByUrl(this.router.createUrlTree(Array.isArray(back) ? back : [back]));
      },
      error: err => this.toast.apiError(err, 'Operation failed'),
    });
  }

  private runAction(action: () => Observable<ActionResult>, successMsg: string, errorMsg: string): void {
    if (this.actionBusy()) return;
    this.actionBusy.set(true);
    action().subscribe({
      next: res => {
        this.actionBusy.set(false);
        if (res?.success !== false) {
          this.toast.success(res?.message || successMsg);
        } else {
          this.toast.error(res.message || errorMsg);
        }
        // Give the runtime a moment, then pick up the new status silently.
        setTimeout(() => this.loadAll(true), 1000);
      },
      error: err => {
        this.actionBusy.set(false);
        this.toast.apiError(err, errorMsg);
      },
    });
  }
}
