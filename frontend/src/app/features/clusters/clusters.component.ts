import { Component, ChangeDetectionStrategy, signal, computed, inject, effect, untracked } from '@angular/core';
import { ActivatedRoute, Router, RouterLink } from '@angular/router';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';

import { SourceService, sourceToInput } from '../../core/services/source.service';
import { SourceStateService } from '../../core/services/source-state.service';
import { ToastService } from '../../core/services/toast.service';
import { RefreshService } from '../../core/services/refresh.service';
import { ServerMeta, SourceStatus, SyncResult } from '../../core/models/container.model';
import { PageHeaderComponent } from '../../shared/components/page-header/page-header.component';
import { EmptyStateComponent } from '../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../shared/components/error-state/error-state.component';
import { SkeletonComponent } from '../../shared/components/skeleton/skeleton.component';
import { ConfirmDialogComponent } from '../../shared/components/confirm-dialog/confirm-dialog.component';
import { ReadOnlyTagComponent } from '../../shared/components/read-only-tag/read-only-tag.component';
import { TimeagoPipe } from '../../shared/pipes/timeago.pipe';
import {
  CONFIG_ORIGIN_HINT, sourceConnectionSummary, sourceDotClass, sourceStatusLabel,
} from '../../shared/utils/source';
import { ClusterWizardComponent } from './cluster-wizard/cluster-wizard.component';

/** Clusters: list with status + management of user-created clusters (config.yaml ones are locked). */
@Component({
  selector: 'app-clusters',
  standalone: true,
  imports: [
    RouterLink, TimeagoPipe, PageHeaderComponent, EmptyStateComponent, ErrorStateComponent, SkeletonComponent,
    ConfirmDialogComponent, ReadOnlyTagComponent, ClusterWizardComponent,
  ],
  templateUrl: './clusters.component.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ClustersComponent {
  private api        = inject(SourceService);
  private toast      = inject(ToastService);
  private route      = inject(ActivatedRoute);
  private router     = inject(Router);
  protected state    = inject(SourceStateService);
  private refreshSvc = inject(RefreshService);

  readonly dotClass    = sourceDotClass;
  readonly statusLabel = sourceStatusLabel;
  readonly connection  = sourceConnectionSummary;
  readonly configHint  = CONFIG_ORIGIN_HINT;

  /** null = closed; 'new' = create; SourceStatus = edit. */
  wizard       = signal<'new' | SourceStatus | null>(null);
  deleteTarget = signal<SourceStatus | null>(null);
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

    // /clusters?add=1 (command palette, empty states) opens the wizard directly.
    this.route.queryParamMap.pipe(takeUntilDestroyed()).subscribe(qp => {
      if (qp.get('add')) {
        this.wizard.set('new');
        this.router.navigate([], { relativeTo: this.route, queryParams: { add: null }, queryParamsHandling: 'merge', replaceUrl: true });
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

  openAdd(): void { this.wizard.set('new'); }

  openEdit(src: SourceStatus): void {
    if (src.origin === 'config') return;
    this.wizard.set(src);
  }

  onSaved(): void { this.wizard.set(null); }

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
