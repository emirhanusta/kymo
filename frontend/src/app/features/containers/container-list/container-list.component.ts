import {
  Component, ChangeDetectionStrategy, signal, computed, inject, untracked,
} from '@angular/core';
import { ActivatedRoute, Router, RouterLink } from '@angular/router';
import { Subject, of } from 'rxjs';
import { catchError, debounceTime, distinctUntilChanged, map, switchMap } from 'rxjs/operators';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';

import { ContainerService } from '../../../core/services/container.service';
import { SourceStateService } from '../../../core/services/source-state.service';
import { RefreshService } from '../../../core/services/refresh.service';
import { ToastService } from '../../../core/services/toast.service';
import { reloadOnSourceAndRefresh } from '../../../core/utils/page-reload';
import {
  Container, ContainerSortField, ContainerStatusFilter, Page,
} from '../../../core/models/container.model';
import { StatusBadgeComponent } from '../../../shared/components/status-badge/status-badge.component';
import { TimeagoPipe } from '../../../shared/pipes/timeago.pipe';
import { ScoreClassPipe } from '../../../shared/pipes/score-class.pipe';
import { PaginationComponent } from '../../../shared/components/pagination/pagination.component';
import { DeleteModalComponent } from '../../../shared/components/delete-modal/delete-modal.component';
import { PageHeaderComponent } from '../../../shared/components/page-header/page-header.component';
import { EmptyStateComponent } from '../../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../../shared/components/error-state/error-state.component';
import { SkeletonComponent } from '../../../shared/components/skeleton/skeleton.component';
import { SearchHotkeyDirective } from '../../../shared/directives/search-hotkey.directive';
import { formatScore } from '../../../shared/utils/score';
import { podNameFromId } from '../../../shared/utils/source';

const PAGE_SIZE = 15;

export const STATUS_OPTIONS: { value: ContainerStatusFilter; label: string }[] = [
  { value: '',         label: 'All statuses' },
  { value: 'running',  label: 'Running' },
  { value: 'stopped',  label: 'Stopped' },
  { value: 'crashing', label: 'Crashing' },
  { value: 'paused',   label: 'Paused' },
  { value: 'unknown',  label: 'Unknown' },
];

export type RuntimeFilter = '' | 'docker' | 'kubernetes';

export const RUNTIME_OPTIONS: { value: RuntimeFilter; label: string }[] = [
  { value: '',           label: 'All' },
  { value: 'docker',     label: 'Docker' },
  { value: 'kubernetes', label: 'Kubernetes' },
];

const asRuntime = (v: string | null): RuntimeFilter =>
  v === 'docker' || v === 'kubernetes' ? v : '';

type LoadResult = { ok: true; page: Page<Container> } | { ok: false; err: unknown };

@Component({
  selector: 'app-container-list',
  standalone: true,
  imports: [
    RouterLink, StatusBadgeComponent, TimeagoPipe, ScoreClassPipe, PaginationComponent, DeleteModalComponent,
    PageHeaderComponent, EmptyStateComponent, ErrorStateComponent, SkeletonComponent, SearchHotkeyDirective,
  ],
  templateUrl: './container-list.component.html',
  styleUrl: './container-list.component.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ContainerListComponent {
  private svc        = inject(ContainerService);
  private sourceSvc  = inject(SourceStateService);
  private refreshSvc = inject(RefreshService);
  private toast      = inject(ToastService);
  private route      = inject(ActivatedRoute);
  private router     = inject(Router);

  readonly pageSize = PAGE_SIZE;
  readonly statusOptions = STATUS_OPTIONS;
  readonly runtimeOptions = RUNTIME_OPTIONS;
  readonly formatScore = formatScore;

  /** First load (skeleton) vs. subsequent loads (dimmed table). */
  loading        = signal(true);
  busy           = signal(false);
  error          = signal(false);
  containers     = signal<Container[]>([]);
  search         = signal('');
  query          = signal('');
  status         = signal<ContainerStatusFilter>('');
  page           = signal(1);
  total          = signal(0);
  totalPages     = signal(0);
  sortField      = signal<ContainerSortField>('last_seen_at');
  sortDir        = signal<'asc' | 'desc'>('desc');
  deleteTargetId = signal<string | null>(null);

  /** URL-backed filters: ?runtime=docker|kubernetes&workload_kind=…&workload=… */
  runtime        = signal<RuntimeFilter>('');
  workloadKind   = signal('');
  workload       = signal('');

  hasFilters     = computed(() => !!this.query() || !!this.status() || !!this.runtime() || !!this.workloadKind() || !!this.workload());
  sourceSelected = computed(() => !!this.sourceSvc.selectedSource());
  /** Namespace/Pod columns appear whenever Kubernetes rows can be on screen. */
  showK8sColumns = computed(() =>
    this.runtime() === 'kubernetes' ||
    (this.runtime() === '' && this.containers().some(c => c.runtime === 'kubernetes'))
  );
  workloadLabel  = computed(() => {
    const kind = this.workloadKind();
    const name = this.workload();
    if (kind && name) return `${kind} ${name}`;
    return kind || name;
  });

  private search$  = new Subject<string>();
  private request$ = new Subject<void>();

  constructor() {
    // Filters live in the query string so links like /containers?workload_kind=Compose&workload=shop work.
    let firstParams = true;
    this.route.queryParamMap.pipe(takeUntilDestroyed()).subscribe(qp => {
      const runtime = asRuntime(qp.get('runtime'));
      const kind    = qp.get('workload_kind') ?? '';
      const name    = qp.get('workload') ?? '';
      const changed = runtime !== this.runtime() || kind !== this.workloadKind() || name !== this.workload();
      this.runtime.set(runtime);
      this.workloadKind.set(kind);
      this.workload.set(name);
      if (firstParams) { firstParams = false; return; } // initial load comes from reloadOnSourceAndRefresh
      if (changed) {
        this.page.set(1);
        this.fetch();
      }
    });

    this.search$.pipe(
      map(v => v.trim()),
      debounceTime(300),
      distinctUntilChanged(),
      takeUntilDestroyed(),
    ).subscribe(q => {
      this.query.set(q);
      this.page.set(1);
      this.fetch();
    });

    // switchMap: a newer request (typing, paging, refresh) cancels the in-flight one.
    this.request$.pipe(
      switchMap(() => this.svc.getContainersPage(untracked(() => ({
        page:    this.page(),
        limit:   PAGE_SIZE,
        sort:    this.sortField(),
        order:   this.sortDir(),
        runtime: this.runtime() || undefined,
        source:  this.sourceSvc.selectedSource() || undefined,
        q:       this.query() || undefined,
        status:  this.status() || undefined,
        workload_kind: this.workloadKind() || undefined,
        workload:      this.workload() || undefined,
      }))).pipe(
        map((page): LoadResult => ({ ok: true, page })),
        catchError(err => of<LoadResult>({ ok: false, err })),
      )),
      takeUntilDestroyed(),
    ).subscribe(res => this.onResult(res));

    reloadOnSourceAndRefresh({
      onSourceChange: () => this.page.set(1),
      load: () => this.fetch(),
    });
  }

  onSearchInput(value: string): void {
    this.search.set(value);
    this.search$.next(value);
  }

  clearFilters(): void {
    this.search.set('');
    this.search$.next('');
    this.query.set('');
    this.status.set('');
    this.page.set(1);
    if (this.runtime() || this.workloadKind() || this.workload()) {
      this.setQueryParams({ runtime: null, workload_kind: null, workload: null }); // triggers fetch
    } else {
      this.fetch();
    }
  }

  setRuntime(value: RuntimeFilter): void {
    this.setQueryParams({ runtime: value || null });
  }

  clearWorkload(): void {
    this.setQueryParams({ workload_kind: null, workload: null });
  }

  private setQueryParams(params: Record<string, string | null>): void {
    this.router.navigate([], {
      relativeTo: this.route,
      queryParams: params,
      queryParamsHandling: 'merge',
      replaceUrl: true,
    });
  }

  /** Secondary line under the name: source (when all sources) and runtime (when both runtimes are listed). */
  subLine(c: Container): string {
    const parts: string[] = [];
    if (!this.sourceSelected()) parts.push(c.source_name);
    if (!this.runtime()) parts.push(c.runtime === 'kubernetes' ? 'k8s' : 'docker');
    return parts.filter(Boolean).join(' · ');
  }

  podName(c: Container): string {
    return c.runtime === 'kubernetes' ? podNameFromId(c.id) : '';
  }

  setStatus(value: ContainerStatusFilter): void {
    this.status.set(value);
    this.page.set(1);
    this.fetch();
  }

  setSort(field: ContainerSortField): void {
    if (this.sortField() === field) {
      this.sortDir.update(d => d === 'asc' ? 'desc' : 'asc');
    } else {
      this.sortField.set(field);
      this.sortDir.set(field === 'name' ? 'asc' : 'desc');
    }
    this.page.set(1);
    this.fetch();
  }

  sortIndicator(field: ContainerSortField): string {
    if (this.sortField() !== field) return '↕';
    return this.sortDir() === 'asc' ? '↑' : '↓';
  }

  ariaSort(field: ContainerSortField): string {
    if (this.sortField() !== field) return 'none';
    return this.sortDir() === 'asc' ? 'ascending' : 'descending';
  }

  onPageChange(p: number): void {
    this.page.set(p);
    this.fetch();
  }

  getContainerName(id: string): string {
    return this.containers().find(c => c.id === id)?.name ?? id;
  }

  onDeleteAction(action: 'soft' | 'ignore' | 'cancel', containerId: string): void {
    this.deleteTargetId.set(null);
    if (action === 'cancel') return;

    const obs = action === 'soft'
      ? this.svc.softDeleteContainer(containerId)
      : this.svc.ignoreContainer(containerId);

    obs.subscribe({
      next: res => {
        this.toast.success(res?.message || (action === 'soft' ? 'Container archived' : 'Container ignored'));
        this.fetch();
      },
      error: err => this.toast.apiError(err, 'Operation failed'),
    });
  }

  fetch(): void {
    if (!this.loading()) this.busy.set(true);
    this.request$.next();
  }

  private onResult(res: LoadResult): void {
    this.loading.set(false);
    this.busy.set(false);
    if (!res.ok) {
      this.error.set(true);
      return;
    }
    const p = res.page;
    // Page fell off the end (e.g. after removing the last row on the last page) → step back.
    if (p.total_pages > 0 && p.page > p.total_pages) {
      this.page.set(p.total_pages);
      this.fetch();
      return;
    }
    this.error.set(false);
    this.containers.set(p.data ?? []);
    this.total.set(p.total);
    this.totalPages.set(p.total_pages);
    this.page.set(p.page || 1);
    this.refreshSvc.markUpdated();
  }
}
