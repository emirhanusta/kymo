import {
  Component, ChangeDetectionStrategy, signal, computed, inject,
} from '@angular/core';
import { Router } from '@angular/router';

import { ContainerService } from '../../core/services/container.service';
import { ToastService } from '../../core/services/toast.service';
import { RefreshService } from '../../core/services/refresh.service';
import { SourceStateService } from '../../core/services/source-state.service';
import { reloadOnSourceAndRefresh } from '../../core/utils/page-reload';
import { PageHeaderComponent } from '../../shared/components/page-header/page-header.component';
import { EmptyStateComponent } from '../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../shared/components/error-state/error-state.component';
import { SkeletonComponent } from '../../shared/components/skeleton/skeleton.component';
import { DeploymentInfo, DeploymentDetail, DeploymentPod } from '../../core/models/container.model';
import { PaginationComponent } from '../../shared/components/pagination/pagination.component';
import { NamespaceSelectorComponent } from '../../shared/components/namespace-selector/namespace-selector.component';

type SortField = 'name' | 'namespace' | 'replicas' | 'ready';

const PAGE_SIZE = 15;

import { StatusBadgeComponent } from '../../shared/components/status-badge/status-badge.component';
import { StatusKey, worstStatus } from '../../shared/utils/status';

@Component({
  selector: 'app-deployment-manager',
  standalone: true,
  imports: [StatusBadgeComponent, PaginationComponent, NamespaceSelectorComponent, PageHeaderComponent, EmptyStateComponent, ErrorStateComponent, SkeletonComponent],
  templateUrl: './deployment-manager.component.html',
  styleUrl: './deployment-manager.component.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class DeploymentManagerComponent {
  private svc        = inject(ContainerService);
  private toast      = inject(ToastService);
  private refreshSvc = inject(RefreshService);
  protected sourceSvc  = inject(SourceStateService);
  private router     = inject(Router);

  selectedNamespace  = signal(''); // '' = all namespaces

  loading            = signal(true);
  error              = signal(false);
  deployments        = signal<DeploymentInfo[]>([]);
  selectedDeployment = signal<DeploymentInfo | null>(null);
  deploymentDetail   = signal<DeploymentDetail | null>(null);
  detailLoading      = signal(false);
  busyKey            = signal<string | null>(null);

  page      = signal(1);
  sortField = signal<SortField>('name');
  sortDir   = signal<'asc' | 'desc'>('asc');
  readonly pageSize = PAGE_SIZE;

  scaleValues: Record<string, number> = {};

  // Inline row actions
  scalingKey    = signal<string | null>(null);
  restartingKey = signal<string | null>(null);
  inlineScaleValues: Record<string, number> = {};

  sorted = computed(() => {
    const field = this.sortField();
    const dir   = this.sortDir();
    return [...this.deployments()].sort((a, b) => {
      let av: string | number;
      let bv: string | number;
      switch (field) {
        case 'name':      av = a.name;           bv = b.name;           break;
        case 'namespace': av = a.namespace;       bv = b.namespace;      break;
        case 'replicas':  av = a.replicas;        bv = b.replicas;       break;
        case 'ready':     av = a.ready_replicas;  bv = b.ready_replicas; break;
        default:          av = a.name;            bv = b.name;
      }
      if (av < bv) return dir === 'asc' ? -1 : 1;
      if (av > bv) return dir === 'asc' ? 1 : -1;
      return 0;
    });
  });

  totalPages = computed(() => Math.max(1, Math.ceil(this.deployments().length / PAGE_SIZE)));

  paged = computed(() => {
    const start = (this.page() - 1) * PAGE_SIZE;
    return this.sorted().slice(start, start + PAGE_SIZE);
  });

  constructor() {
    reloadOnSourceAndRefresh({
      onSourceChange: () => {
        this.selectedNamespace.set('');
        this.scaleValues = {};
        this.selectedDeployment.set(null);
        this.deploymentDetail.set(null);
        this.page.set(1);
      },
      load: () => this.loadData(),
    });
  }

  loadData(): void {
    const source = this.sourceSvc.selectedSource() || undefined;
    this.svc.getDeployments(this.selectedNamespace(), source).subscribe({
      next: (deps) => {
        this.deployments.set(deps ?? []);
        (deps ?? []).forEach(d => {
          const key = `${d.namespace}/${d.name}`;
          if (this.scaleValues[key] === undefined) {
            this.scaleValues[key] = d.replicas;
          }
        });
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

  onNamespaceChange(ns: string): void {
    this.selectedNamespace.set(ns);
    this.scaleValues = {};
    this.selectedDeployment.set(null);
    this.deploymentDetail.set(null);
    this.page.set(1);
    this.loading.set(true);
    this.loadData();
  }

  setSort(field: SortField): void {
    if (this.sortField() === field) {
      this.sortDir.update(d => d === 'asc' ? 'desc' : 'asc');
    } else {
      this.sortField.set(field);
      this.sortDir.set('asc');
    }
    this.page.set(1);
  }

  sortIndicator(field: SortField): string {
    if (this.sortField() !== field) return '↕';
    return this.sortDir() === 'asc' ? '↑' : '↓';
  }

  onPageChange(p: number): void { this.page.set(p); }

  selectDeployment(dep: DeploymentInfo): void {
    const cur = this.selectedDeployment();
    if (cur?.name === dep.name && cur?.namespace === dep.namespace) {
      this.selectedDeployment.set(null);
      this.deploymentDetail.set(null);
      return;
    }
    this.selectedDeployment.set(dep);
    this.detailLoading.set(true);
    const source = this.sourceSvc.selectedSource() || undefined;
    this.svc.getDeploymentDetail(dep.namespace, dep.name, source).subscribe({
      next: detail => {
        this.deploymentDetail.set(detail);
        this.detailLoading.set(false);
      },
      error: () => this.detailLoading.set(false),
    });
  }

  navigateToPod(pod: DeploymentPod): void {
    const first = pod.containers[0];
    if (!first) return;
    this.router.navigate(['/pods', first.id]);
  }

  replicaStatus(dep: DeploymentInfo): 'ok' | 'partial' | 'down' {
    if (dep.replicas === 0) return dep.ready_replicas === 0 ? 'ok' : 'partial';
    if (dep.ready_replicas === dep.replicas) return 'ok';
    if (dep.ready_replicas === 0) return 'down';
    return 'partial';
  }

  /** A pod reads as its worst container status (one crashing container ⇒ crashing). */
  podStatus(pod: DeploymentPod): StatusKey {
    return worstStatus(pod.containers.map(c => c.current_status));
  }

  decrement(dep: DeploymentInfo): void {
    const key = `${dep.namespace}/${dep.name}`;
    const cur = this.scaleValues[key] ?? dep.replicas;
    if (cur > 0) this.scaleValues[key] = cur - 1;
  }

  increment(dep: DeploymentInfo): void {
    const key = `${dep.namespace}/${dep.name}`;
    const cur = this.scaleValues[key] ?? dep.replicas;
    this.scaleValues[key] = cur + 1;
  }

  getScaleValue(dep: DeploymentInfo): number {
    return this.scaleValues[`${dep.namespace}/${dep.name}`] ?? dep.replicas;
  }

  scale(dep: DeploymentInfo): void {
    const key = `${dep.namespace}/${dep.name}`;
    const replicas = this.scaleValues[key] ?? dep.replicas;
    if (this.busyKey()) return;
    this.busyKey.set(key);
    const source = this.sourceSvc.selectedSource() || undefined;
    this.svc.scaleDeployment(dep.namespace, dep.name, replicas, source).subscribe({
      next: (res) => {
        this.busyKey.set(null);
        if (res.success) {
          this.toast.success(res.message || `${dep.name} scaled to ${replicas}`);
          this.loadData();
        } else {
          this.toast.error(res.message || 'Scale failed');
        }
      },
      error: (err) => {
        this.busyKey.set(null);
        this.toast.error(err?.error?.message || 'Scale failed');
      },
    });
  }

  restart(dep: DeploymentInfo): void {
    const key = `${dep.namespace}/${dep.name}`;
    if (this.busyKey()) return;
    this.busyKey.set(key);
    const source = this.sourceSvc.selectedSource() || undefined;
    this.svc.restartDeployment(dep.namespace, dep.name, source).subscribe({
      next: (res) => {
        this.busyKey.set(null);
        if (res.success) {
          this.toast.success(res.message || `${dep.name} rolling restart triggered`);
        } else {
          this.toast.error(res.message || 'Restart failed');
        }
      },
      error: (err) => {
        this.busyKey.set(null);
        this.toast.error(err?.error?.message || 'Restart failed');
      },
    });
  }

  isBusy(dep: DeploymentInfo): boolean {
    return this.busyKey() === `${dep.namespace}/${dep.name}`;
  }

  // ── inline row actions ─────────────────────────────────────────────────────

  depKey(dep: DeploymentInfo): string { return `${dep.namespace}/${dep.name}`; }

  openInlineScale(dep: DeploymentInfo): void {
    const key = this.depKey(dep);
    this.inlineScaleValues[key] = dep.replicas;
    this.scalingKey.set(key);
  }

  closeInlineScale(): void { this.scalingKey.set(null); }

  getInlineScaleValue(dep: DeploymentInfo): number {
    return this.inlineScaleValues[this.depKey(dep)] ?? dep.replicas;
  }

  setInlineScaleValue(dep: DeploymentInfo, val: number): void {
    this.inlineScaleValues[this.depKey(dep)] = Math.max(0, Math.min(50, val));
  }

  confirmInlineScale(dep: DeploymentInfo): void {
    const key    = this.depKey(dep);
    const replicas = this.inlineScaleValues[key] ?? dep.replicas;
    this.scalingKey.set(null);
    if (this.busyKey()) return;
    this.busyKey.set(key);
    const source = this.sourceSvc.selectedSource() || undefined;
    this.svc.scaleDeployment(dep.namespace, dep.name, replicas, source).subscribe({
      next: res => {
        this.busyKey.set(null);
        if (res.success) {
          this.toast.success(res.message || `${dep.name} scaled to ${replicas}`);
          this.loadData();
        } else {
          this.toast.error(res.message || 'Scale failed');
        }
      },
      error: err => {
        this.busyKey.set(null);
        this.toast.error(err?.error?.message || 'Scale failed');
      },
    });
  }

  restartDeploymentInline(dep: DeploymentInfo): void {
    const key = this.depKey(dep);
    if (this.restartingKey()) return;
    this.restartingKey.set(key);
    const source = this.sourceSvc.selectedSource() || undefined;
    this.svc.restartDeployment(dep.namespace, dep.name, source).subscribe({
      next: res => {
        this.restartingKey.set(null);
        if (res.success) {
          this.toast.success(res.message || `${dep.name} restarted`);
        } else {
          this.toast.error(res.message || 'Restart failed');
        }
      },
      error: err => {
        this.restartingKey.set(null);
        this.toast.error(err?.error?.message || 'Restart failed');
      },
    });
  }

  isRowRestarting(dep: DeploymentInfo): boolean {
    return this.restartingKey() === this.depKey(dep);
  }

  isSelected(dep: DeploymentInfo): boolean {
    const cur = this.selectedDeployment();
    return cur?.name === dep.name && cur?.namespace === dep.namespace;
  }
}
