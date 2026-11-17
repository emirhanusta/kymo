import {
  Component, ChangeDetectionStrategy, signal, computed, inject,
} from '@angular/core';
import { DecimalPipe, DatePipe } from '@angular/common';

import { ContainerService } from '../../core/services/container.service';
import { RefreshService } from '../../core/services/refresh.service';
import { SourceStateService } from '../../core/services/source-state.service';
import { reloadOnSourceAndRefresh } from '../../core/utils/page-reload';
import { PageHeaderComponent } from '../../shared/components/page-header/page-header.component';
import { EmptyStateComponent } from '../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../shared/components/error-state/error-state.component';
import { SkeletonComponent } from '../../shared/components/skeleton/skeleton.component';
import { SearchHotkeyDirective } from '../../shared/directives/search-hotkey.directive';
import { scoreTextClass } from '../../shared/utils/score';
import { CronJobExecution, CronJobSummary } from '../../core/models/container.model';
import { TimeagoPipe } from '../../shared/pipes/timeago.pipe';
import { PaginationComponent } from '../../shared/components/pagination/pagination.component';
import { NamespaceSelectorComponent } from '../../shared/components/namespace-selector/namespace-selector.component';

type SortField = 'cronjob_name' | 'namespace' | 'last_run' | 'status';

const PAGE_SIZE = 15;

@Component({
  selector: 'app-cronjobs',
  standalone: true,
  imports: [
    DecimalPipe, DatePipe, TimeagoPipe, PaginationComponent, NamespaceSelectorComponent,
    PageHeaderComponent, EmptyStateComponent, ErrorStateComponent, SkeletonComponent, SearchHotkeyDirective,
  ],
  templateUrl: './cronjobs.component.html',
  styleUrl: './cronjobs.component.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class CronJobsComponent {
  private svc        = inject(ContainerService);
  private refreshSvc = inject(RefreshService);
  protected sourceSvc  = inject(SourceStateService);

  /** Success rate reuses the score palette (≥80 good, ≥50 warn). */
  readonly rateClass = scoreTextClass;

  loading           = signal(true);
  error             = signal(false);
  search            = signal('');
  selectedNamespace = signal('');
  cronJobs          = signal<CronJobSummary[]>([]);
  selectedCronJob   = signal<CronJobSummary | null>(null);
  executions        = signal<CronJobExecution[]>([]);
  execLoading       = signal(false);

  page      = signal(1);
  sortField = signal<SortField>('last_run');
  sortDir   = signal<'asc' | 'desc'>('desc');
  readonly pageSize = PAGE_SIZE;

  filteredCronJobs = computed(() => {
    const ns = this.selectedNamespace().trim();
    const q  = this.search().trim().toLowerCase();
    return this.cronJobs().filter(j =>
      (!ns || j.namespace === ns) &&
      (!q || j.cronjob_name.toLowerCase().includes(q) || j.namespace.toLowerCase().includes(q))
    );
  });

  sorted = computed(() => {
    const field = this.sortField();
    const dir   = this.sortDir();
    return [...this.filteredCronJobs()].sort((a, b) => {
      let av: string | number;
      let bv: string | number;
      switch (field) {
        case 'cronjob_name': av = a.cronjob_name; bv = b.cronjob_name; break;
        case 'namespace':    av = a.namespace;     bv = b.namespace;    break;
        case 'status':
          av = a.last_execution?.succeeded === true ? 1 : 0;
          bv = b.last_execution?.succeeded === true ? 1 : 0;
          break;
        case 'last_run':
        default:
          av = a.last_execution?.sampled_at ?? '';
          bv = b.last_execution?.sampled_at ?? '';
      }
      if (av < bv) return dir === 'asc' ? -1 : 1;
      if (av > bv) return dir === 'asc' ? 1 : -1;
      return 0;
    });
  });

  totalPages = computed(() => Math.max(1, Math.ceil(this.sorted().length / PAGE_SIZE)));

  paged = computed(() => {
    const start = (this.page() - 1) * PAGE_SIZE;
    return this.sorted().slice(start, start + PAGE_SIZE);
  });

  successRate = computed(() => {
    const execs = this.executions();
    if (!execs.length) return 0;
    return (execs.filter(e => e.succeeded).length / execs.length) * 100;
  });

  avgDuration = computed(() => {
    const execs = this.executions().filter(e => e.duration_seconds != null);
    if (!execs.length) return 0;
    return execs.reduce((sum, e) => sum + (e.duration_seconds ?? 0), 0) / execs.length;
  });

  constructor() {
    reloadOnSourceAndRefresh({
      onSourceChange: () => {
        this.selectedNamespace.set('');
        this.selectedCronJob.set(null);
        this.executions.set([]);
        this.page.set(1);
      },
      load: () => this.loadData(),
    });
  }

  loadData(): void {
    const ns  = this.selectedNamespace().trim();
    const src = this.sourceSvc.selectedSource() || undefined;
    this.svc.getCronJobs(ns, src).subscribe({
      next: execs => {
        const summaries: CronJobSummary[] = (execs ?? []).map(e => ({
          namespace:      e.namespace,
          cronjob_name:   e.cronjob_name,
          source_name:    e.source_name,
          last_execution: e,
        }));
        this.cronJobs.set(summaries);
        this.page.update(p => Math.min(p, this.totalPages()));
        this.error.set(false);
        this.loading.set(false);
        this.refreshSvc.markUpdated();
      },
      error: () => {
        this.error.set(true);
        this.loading.set(false);
      },
    });
  }

  onSearch(value: string): void {
    this.search.set(value);
    this.page.set(1);
  }

  onNamespaceChange(ns: string): void {
    this.selectedNamespace.set(ns);
    this.page.set(1);
    this.selectedCronJob.set(null);
    this.executions.set([]);
    this.loading.set(true);
    this.loadData();
  }

  setSort(field: SortField): void {
    if (this.sortField() === field) {
      this.sortDir.update(d => d === 'asc' ? 'desc' : 'asc');
    } else {
      this.sortField.set(field);
      this.sortDir.set(field === 'last_run' ? 'desc' : 'asc');
    }
    this.page.set(1);
  }

  sortIndicator(field: SortField): string {
    if (this.sortField() !== field) return '↕';
    return this.sortDir() === 'asc' ? '↑' : '↓';
  }

  onPageChange(p: number): void { this.page.set(p); }

  selectCronJob(job: CronJobSummary): void {
    if (this.selectedCronJob()?.cronjob_name === job.cronjob_name &&
        this.selectedCronJob()?.namespace === job.namespace) {
      this.selectedCronJob.set(null);
      this.executions.set([]);
      return;
    }
    this.selectedCronJob.set(job);
    this.execLoading.set(true);
    const src = this.sourceSvc.selectedSource() || undefined;
    this.svc.getCronJobExecutions(job.namespace, job.cronjob_name, 50, src).subscribe({
      next: execs => {
        this.executions.set(execs);
        this.execLoading.set(false);
      },
      error: () => this.execLoading.set(false),
    });
  }
}
