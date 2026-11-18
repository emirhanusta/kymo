import { Component, ChangeDetectionStrategy, signal, computed, inject, effect, untracked } from '@angular/core';
import { ActivatedRoute, Router } from '@angular/router';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';

import { SourceService, sourceToInput } from '../../core/services/source.service';
import { SourceStateService } from '../../core/services/source-state.service';
import { ToastService } from '../../core/services/toast.service';
import { RefreshService } from '../../core/services/refresh.service';
import { RemovedSource, ServerMeta, SourceStatus, SyncResult } from '../../core/models/container.model';
import { PageHeaderComponent } from '../../shared/components/page-header/page-header.component';
import { EmptyStateComponent } from '../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../shared/components/error-state/error-state.component';
import { SkeletonComponent } from '../../shared/components/skeleton/skeleton.component';
import { ConfirmDialogComponent } from '../../shared/components/confirm-dialog/confirm-dialog.component';
import { purgeMessage } from '../../shared/utils/removed';
import { TimeagoPipe } from '../../shared/pipes/timeago.pipe';
import { WorkloadStateService } from '../../core/services/workload-state.service';
import { isLongRunning } from '../../shared/utils/workload';
import {
  CONFIG_ORIGIN_HINT, sourceConnectionSummary, sourceConnectionTitle, sourceDotClass,
} from '../../shared/utils/source';
import { ClusterWizardComponent } from './cluster-wizard/cluster-wizard.component';
import { AuthService } from '../../core/services/auth.service';

/** Clusters: list with status + management of user-created clusters (config.yaml ones are locked). */
@Component({
  selector: 'app-clusters',
  standalone: true,
  imports: [
    TimeagoPipe, PageHeaderComponent, EmptyStateComponent, ErrorStateComponent, SkeletonComponent,
    ConfirmDialogComponent, ClusterWizardComponent,
  ],
  templateUrl: './clusters.component.html',
  host: { '(document:keydown.escape)': 'closeMenu()' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ClustersComponent {
  readonly auth = inject(AuthService);
  private api        = inject(SourceService);
  private toast      = inject(ToastService);
  private route      = inject(ActivatedRoute);
  private router     = inject(Router);
  protected state    = inject(SourceStateService);
  private refreshSvc = inject(RefreshService);

  private workloadState = inject(WorkloadStateService);

  readonly dotClass    = sourceDotClass;
  readonly connection  = sourceConnectionSummary;
  readonly connectionTitle = sourceConnectionTitle;
  readonly configHint  = CONFIG_ORIGIN_HINT;

  /** null = closed; 'new' = create; SourceStatus = edit. */
  wizard       = signal<'new' | SourceStatus | null>(null);
  deleteTarget = signal<SourceStatus | null>(null);
  /** Name prefilled (and locked) in the wizard when re-adding a removed cluster. */
  readdName    = signal('');
  purgeTarget  = signal<RemovedSource | null>(null);
  purgingName  = signal<string | null>(null);
  readonly purgeText = purgeMessage;
  purge        = signal(false);
  busyName     = signal<string | null>(null);
  meta         = signal<ServerMeta | null>(null);
  reloading    = signal(false);

  sources = computed(() =>
    [...this.state.sources()].sort((a, b) =>
      (a.origin === b.origin ? 0 : a.origin === 'config' ? -1 : 1) || a.name.localeCompare(b.name)));

  editSource = computed(() => {
    const w = this.wizard();
    return w && w !== 'new' ? w : null;
  });

  /** Live workloads / pods per cluster: non-system, long-running, not stopped-with-no-pods (same as the Overview). */
  liveStats = computed(() => {
    const m = new Map<string, { workloads: number; running: number; desired: number }>();
    for (const w of this.workloadState.workloads()) {
      if (w.is_system || !isLongRunning(w) || (w.status === 'stopped' && w.active_instances === 0)) continue;
      const e = m.get(w.source_name) ?? { workloads: 0, running: 0, desired: 0 };
      e.workloads++;
      e.running += w.running_instances;
      e.desired += w.active_instances;
      m.set(w.source_name, e);
    }
    return m;
  });
  statsFor(name: string): { workloads: number; running: number; desired: number } | null {
    return this.workloadState.loaded() ? this.liveStats().get(name) ?? { workloads: 0, running: 0, desired: 0 } : null;
  }

  hasConfigSources = computed(() => this.state.sources().some(s => s.origin === 'config'));
  /** Shown when a config file is in use or any config-file cluster exists. */
  showReload = computed(() => !!this.meta()?.config_file || this.hasConfigSources());

  removedOpen = signal(false);

  /** Row actions menu: fixed-position so the table's overflow does not clip it. */
  menu = signal<{ src: SourceStatus; x: number; y: number } | null>(null);
  testingName = signal<string | null>(null);

  openMenu(ev: MouseEvent, src: SourceStatus): void {
    ev.stopPropagation();
    if (this.menu()?.src.name === src.name) { this.menu.set(null); return; }
    const r = (ev.currentTarget as HTMLElement).getBoundingClientRect();
    this.menu.set({ src, x: Math.max(8, r.right - 208), y: r.bottom + 4 });
  }
  closeMenu(): void { this.menu.set(null); }

  /** Row click: scope the whole app to this cluster and open its dashboard. */
  open(src: SourceStatus): void {
    this.state.select(src.name);
    this.router.navigate(['/dashboard']);
  }

  /** "30m" from a timestamp ("for 30m"). */
  since(iso: string | undefined): string {
    return iso ? this.timeago.transform(iso).replace(/ ago$/, '') : '';
  }
  private timeago = new TimeagoPipe();

  testConnection(src: SourceStatus): void {
    if (this.testingName()) return;
    this.testingName.set(src.name);
    this.api.test(sourceToInput(src)).subscribe({
      next: res => {
        this.testingName.set(null);
        const ver = res.checks.find(c => c.name === 'API server' && c.ok)?.detail;
        if (res.ok) this.toast.success(`${src.name}: connected${ver ? ` · ${ver}` : ''}`);
        else this.toast.error(`${src.name}: ${res.checks.find(c => !c.ok)?.detail || 'connection test failed'}`);
      },
      error: err => {
        this.testingName.set(null);
        this.toast.apiError(err, `Could not test ${src.name}`);
      },
    });
  }

  counts = computed(() => {
    const list = this.state.sources();
    return {
      total: list.length,
      connected: list.filter(s => s.status === 'connected').length,
      error: list.filter(s => s.status === 'error').length,
    };
  });

  constructor() {
    this.state.loadSources();
    this.loadMeta();

    // The cluster list is this page's data: feed the top bar's "updated" label.
    effect(() => {
      this.state.sources();
      if (this.state.loaded() && !this.state.sourcesError()) {
        untracked(() => this.refreshSvc.markUpdated());
      }
    });

    // /clusters?add=1 (command palette, empty states) opens the wizard directly;
    // /clusters?readd=<name> (history banner) opens it for a removed cluster.
    this.route.queryParamMap.pipe(takeUntilDestroyed()).subscribe(qp => {
      const readd = qp.get('readd');
      if ((qp.get('add') || readd) && this.auth.can('sources:manage')) {
        this.readdName.set(readd ?? '');
        this.wizard.set('new');
        this.router.navigate([], { relativeTo: this.route, queryParams: { add: null, readd: null }, queryParamsHandling: 'merge', replaceUrl: true });
      }
    });
  }

  private loadMeta(): void {
    this.api.getMeta().subscribe({ next: m => this.meta.set(m), error: () => {} });
  }

  reloadConfig(): void {
    if (this.reloading()) return;
    this.reloading.set(true);
    this.api.reloadConfig().subscribe({
      next: res => {
        this.reloading.set(false);
        if (res.conflicts.length > 0) {
          this.toast.error(`Name already used by a cluster added in the UI: ${res.conflicts.join(', ')} (config entry ignored)`);
        } else {
          this.toast.success(this.syncSummary(res));
        }
        this.state.loadSources();
        this.loadMeta();
      },
      error: err => {
        this.reloading.set(false);
        this.toast.apiError(err, 'Failed to reload config.yaml');
        this.loadMeta();
      },
    });
  }

  private syncSummary(r: SyncResult): string {
    const parts = [
      r.added.length ? `${r.added.length} added` : '',
      r.updated.length ? `${r.updated.length} updated` : '',
      r.removed.length ? `${r.removed.length} removed` : '',
    ].filter(Boolean);
    return parts.length ? parts.join(' · ') : 'No changes';
  }

  openAdd(): void {
    this.readdName.set('');
    this.wizard.set('new');
  }

  readd(r: RemovedSource): void {
    this.readdName.set(r.name);
    this.wizard.set('new');
  }

  closeWizard(): void {
    this.wizard.set(null);
    this.readdName.set('');
  }

  viewHistory(r: RemovedSource): void {
    this.state.select(r.name);
    this.router.navigate(['/workloads']);
  }

  confirmPurge(): void {
    const r = this.purgeTarget();
    if (!r) return;
    this.purgeTarget.set(null);
    this.purgingName.set(r.name);
    this.state.purgeHistory(r.name, () => this.purgingName.set(null));
  }

  openEdit(src: SourceStatus): void {
    if (src.origin === 'config') return;
    this.wizard.set(src);
  }

  onSaved(): void { this.closeWizard(); }

  toggleEnabled(src: SourceStatus): void {
    if (src.origin === 'config' || this.busyName()) return;
    const enabled = !src.enabled;
    this.busyName.set(src.name);
    this.api.update(src.name, sourceToInput(src, { enabled })).subscribe({
      next: () => {
        this.busyName.set(null);
        this.toast.success(enabled ? `Cluster ${src.name} enabled — connecting…` : `Cluster ${src.name} disabled`);
        this.state.loadSources();
      },
      error: err => {
        this.busyName.set(null);
        this.toast.apiError(err, `Failed to ${enabled ? 'enable' : 'disable'} ${src.name}`);
      },
    });
  }

  askDelete(src: SourceStatus): void {
    if (src.origin === 'config') return;
    this.purge.set(false);
    this.deleteTarget.set(src);
  }

  confirmDelete(): void {
    const src = this.deleteTarget();
    if (!src) return;
    const purge = this.purge();
    this.deleteTarget.set(null);
    this.busyName.set(src.name);
    this.api.delete(src.name, purge).subscribe({
      next: res => {
        this.busyName.set(null);
        this.toast.success(res?.message || `Cluster ${src.name} deleted${purge ? ' with its history' : ''}`);
        this.state.loadSources();
      },
      error: err => {
        this.busyName.set(null);
        this.toast.apiError(err, `Failed to delete ${src.name}`);
      },
    });
  }

  deleteMessage(src: SourceStatus): string {
    return `Stop recording ${src.name} and remove it from KYMO. ` +
      'Recorded containers, events and metrics are kept unless you also delete the history below.';
  }
}
