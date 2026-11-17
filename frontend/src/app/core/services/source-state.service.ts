import { Injectable, inject, signal, computed } from '@angular/core';
import { forkJoin, of } from 'rxjs';
import { catchError } from 'rxjs/operators';

import { ContainerService } from './container.service';
import { SourceService } from './source.service';
import { ToastService } from './toast.service';
import { RefreshService } from './refresh.service';
import { RemovedSource, SourceStatus } from '../models/container.model';

/**
 * Clusters (API: sources) and the app-wide cluster scope.
 *
 * Scoping lives here so every page agrees:
 *  - "All clusters" (nothing selected) = currently registered clusters only; rows of clusters that were removed
 *    (history kept) are dropped, their statuses are frozen and must not look like live problems.
 *  - A registered cluster selected = just that cluster.
 *  - A removed cluster selected = "history mode": just that cluster's recorded history, read-only.
 * Pages call `inScope` (honours the selection) or `connectedOnly` (ignores the selection, e.g. the Overview).
 */
@Injectable({ providedIn: 'root' })
export class SourceStateService {
  private svc = inject(ContainerService);
  private api = inject(SourceService);
  private toast = inject(ToastService);
  private refresh = inject(RefreshService);

  readonly sources = signal<SourceStatus[]>([]);
  /** Clusters with recorded history that are not registered any more, newest first. */
  readonly removed = signal<RemovedSource[]>([]);
  readonly sourcesError = signal(false);
  /** True once the first /sources request has finished (successfully or not). */
  readonly loaded = signal(false);
  /** Empty string means "all clusters". */
  readonly selectedSource = signal<string>('');

  readonly selectedSourceInfo = computed(() => {
    const name = this.selectedSource();
    return name ? this.sources().find(s => s.name === name) ?? null : null;
  });

  private byName = computed(() => new Map(this.sources().map(s => [s.name, s])));
  /** Names of the currently registered clusters. */
  readonly registeredNames = computed<ReadonlySet<string>>(() => new Set(this.sources().map(s => s.name)));
  /** Without a trustworthy registry (not loaded / failed) nothing can be told apart, so nothing is dropped. */
  private registryKnown = computed(() => this.loaded() && !this.sourcesError());

  /** The removed cluster picked in the top bar (null = none → normal mode). */
  readonly selectedRemoved = computed<RemovedSource | null>(() => {
    const name = this.selectedSource();
    if (!name || this.byName().has(name)) return null;
    return this.removed().find(r => r.name === name) ?? null;
  });
  /** A removed cluster is selected: recorded history only, everything read-only. */
  readonly historyMode = computed(() => this.selectedRemoved() !== null);

  /** Load (or refresh) the registered and removed cluster lists. */
  loadSources(): void {
    forkJoin({
      sources: this.svc.getSources(),
      removed: this.api.removed().pipe(catchError(() => of(null))),
    }).subscribe({
      next: ({ sources, removed }) => {
        const list = sources ?? [];
        this.sources.set(list);
        this.sourcesError.set(false);
        if (removed) this.removed.set(removed);
        this.loaded.set(true);
        // The selected cluster is gone for good (deleted / history purged) → fall back to "all clusters".
        const sel = this.selectedSource();
        if (sel && !list.some(s => s.name === sel) && !this.removed().some(r => r.name === sel)) {
          this.selectedSource.set('');
        }
      },
      error: () => {
        this.sourcesError.set(true);
        this.loaded.set(true);
      },
    });
  }

  /** Permanently deletes a removed cluster's history (callers confirm first), then refreshes everything. */
  purgeHistory(name: string, done?: () => void): void {
    this.api.purgeRemoved(name).subscribe({
      next: res => {
        const n = res?.deleted_containers ?? 0;
        this.toast.success(res?.message || `Deleted the recorded history of ${name} (${n} container${n === 1 ? '' : 's'})`);
        if (this.selectedSource() === name) this.selectedSource.set('');
        this.refresh.trigger();
        done?.();
      },
      error: err => {
        const status = (err as { status?: number } | null)?.status;
        this.toast.apiError(err, status === 409
          ? `${name} is a registered cluster — delete it from the Clusters page instead`
          : status === 404 ? `${name} has no recorded history (already deleted?)` : `Failed to delete the history of ${name}`);
        this.refresh.trigger();
        done?.();
      },
    });
  }

  select(name: string): void {
    this.selectedSource.set(name);
  }

  find(name: string | null | undefined): SourceStatus | null {
    return name ? this.byName().get(name) ?? null : null;
  }

  /** Recorded history of a cluster that is no longer registered. */
  isRemoved(name: string | null | undefined): boolean {
    return !!name && this.registryKnown() && !this.byName().has(name);
  }

  /** Read-only clusters (and removed ones, whose data is frozen) refuse management actions. */
  isReadOnly(name: string | null | undefined): boolean {
    return !!this.find(name)?.read_only || this.isRemoved(name);
  }

  /** Drops rows of clusters that are not registered (rows without a cluster name are kept). */
  connectedOnly<T>(list: T[], source: (x: T) => string | null | undefined): T[] {
    if (!this.registryKnown()) return list;
    const names = this.registeredNames();
    return list.filter(x => { const s = source(x); return !s || names.has(s); });
  }

  /** Rows for the selected cluster, or for all connected clusters when none is selected. */
  inScope<T>(list: T[], source: (x: T) => string | null | undefined): T[] {
    const sel = this.selectedSource();
    return sel ? list.filter(x => source(x) === sel) : this.connectedOnly(list, source);
  }

  /** Like `inScope`, but a registered cluster selection is ignored (Overview, palette): all connected clusters, or the selected removed one. */
  globalScope<T>(list: T[], source: (x: T) => string | null | undefined): T[] {
    return this.historyMode() ? this.inScope(list, source) : this.connectedOnly(list, source);
  }
}
