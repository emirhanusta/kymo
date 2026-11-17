import {
  Component, ChangeDetectionStrategy, signal, computed, inject,
} from '@angular/core';
import { RouterLink } from '@angular/router';

import { ContainerService } from '../../core/services/container.service';
import { RefreshService } from '../../core/services/refresh.service';
import { SourceStateService } from '../../core/services/source-state.service';
import { reloadOnSourceAndRefresh } from '../../core/utils/page-reload';
import { ScoreClassPipe } from '../../shared/pipes/score-class.pipe';
import { PageHeaderComponent } from '../../shared/components/page-header/page-header.component';
import { EmptyStateComponent } from '../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../shared/components/error-state/error-state.component';
import { SkeletonComponent } from '../../shared/components/skeleton/skeleton.component';
import { SearchHotkeyDirective } from '../../shared/directives/search-hotkey.directive';
import { formatScore } from '../../shared/utils/score';
import { Container } from '../../core/models/container.model';
import { StatusBadgeComponent } from '../../shared/components/status-badge/status-badge.component';
import { TimeagoPipe } from '../../shared/pipes/timeago.pipe';
import { PaginationComponent } from '../../shared/components/pagination/pagination.component';
import { NamespaceSelectorComponent } from '../../shared/components/namespace-selector/namespace-selector.component';

const EXCLUDED_WORKLOADS = new Set(['CronJob', 'Job', 'DaemonSet', 'StatefulSet']);

type SortField = 'name' | 'namespace' | 'current_status' | 'start_count' | 'last_seen_at' | 'score';

const PAGE_SIZE = 20;

@Component({
  selector: 'app-pods',
  standalone: true,
  imports: [
    RouterLink, StatusBadgeComponent, TimeagoPipe, ScoreClassPipe, PaginationComponent, NamespaceSelectorComponent,
    PageHeaderComponent, EmptyStateComponent, ErrorStateComponent, SkeletonComponent, SearchHotkeyDirective,
  ],
  templateUrl: './pods.component.html',
  styleUrl: './pods.component.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class PodsComponent {
  private svc        = inject(ContainerService);
  private refreshSvc = inject(RefreshService);
  protected sourceSvc  = inject(SourceStateService);

  readonly formatScore = formatScore;

  loading           = signal(true);
  error             = signal(false);
  search            = signal('');
  allPods           = signal<Container[]>([]);
  selectedNamespace = signal('');

  page      = signal(1);
  sortField = signal<SortField>('last_seen_at');
  sortDir   = signal<'asc' | 'desc'>('desc');
  readonly pageSize = PAGE_SIZE;

  pods = computed(() => {
    const ns = this.selectedNamespace().trim();
    const q  = this.search().trim().toLowerCase();
    return this.allPods().filter(c => {
      if (EXCLUDED_WORKLOADS.has(c.workload_kind ?? '')) return false;
      if (ns && c.namespace !== ns) return false;
      if (q && ![c.name, c.image, c.namespace, c.workload_name, c.node_name, c.id]
        .some(v => v?.toLowerCase().includes(q))) return false;
      return true;
    });
  });

  sorted = computed(() => {
    const field = this.sortField();
    const dir   = this.sortDir();
    return [...this.pods()].sort((a, b) => {
      let av: string | number;
      let bv: string | number;
      switch (field) {
        case 'name':           av = a.name;           bv = b.name;           break;
        case 'namespace':      av = a.namespace ?? ''; bv = b.namespace ?? ''; break;
        case 'current_status': av = a.current_status;  bv = b.current_status;  break;
        case 'start_count':    av = a.start_count;     bv = b.start_count;     break;
        case 'score':          av = a.score ?? -1;     bv = b.score ?? -1;     break;
        case 'last_seen_at':
        default:               av = a.last_seen_at;    bv = b.last_seen_at;    break;
      }
      if (av < bv) return dir === 'asc' ? -1 : 1;
      if (av > bv) return dir === 'asc' ? 1 : -1;
      return 0;
    });
  });

  totalPages = computed(() => Math.max(1, Math.ceil(this.pods().length / PAGE_SIZE)));

  paged = computed(() => {
    const start = (this.page() - 1) * PAGE_SIZE;
    return this.sorted().slice(start, start + PAGE_SIZE);
  });

  constructor() {
    reloadOnSourceAndRefresh({
      onSourceChange: () => { this.selectedNamespace.set(''); this.page.set(1); },
      load: () => this.loadData(),
    });
  }

  loadData(): void {
    const src = this.sourceSvc.selectedSource() || undefined;
    this.svc.getContainers('kubernetes', src).subscribe({
      next: pods => {
        this.allPods.set(pods ?? []);
        // Keep the current page on silent refresh unless it no longer exists.
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

  /** Kubernetes container IDs are `namespace/pod/container`. */
  podName(c: Container): string {
    return c.id.split('/')[1] ?? c.workload_name ?? '';
  }

  onSearch(value: string): void {
    this.search.set(value);
    this.page.set(1);
  }

  onNamespaceChange(ns: string): void {
    this.selectedNamespace.set(ns);
    this.page.set(1);
  }

  setSort(field: SortField): void {
    if (this.sortField() === field) {
      this.sortDir.update(d => d === 'asc' ? 'desc' : 'asc');
    } else {
      this.sortField.set(field);
      this.sortDir.set(field === 'last_seen_at' || field === 'score' ? 'desc' : 'asc');
    }
    this.page.set(1);
  }

  sortIndicator(field: SortField): string {
    if (this.sortField() !== field) return '↕';
    return this.sortDir() === 'asc' ? '↑' : '↓';
  }

  onPageChange(p: number): void { this.page.set(p); }
}
