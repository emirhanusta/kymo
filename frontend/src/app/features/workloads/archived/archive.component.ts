import {
  Component, ChangeDetectionStrategy, signal, computed, inject,
} from '@angular/core';
import { DatePipe } from '@angular/common';
import { forkJoin, Observable } from 'rxjs';

import { ContainerService } from '../../../core/services/container.service';
import { ToastService } from '../../../core/services/toast.service';
import { RefreshService } from '../../../core/services/refresh.service';
import { SourceStateService } from '../../../core/services/source-state.service';
import { reloadOnSourceAndRefresh } from '../../../core/utils/page-reload';
import { ActionResult, Container } from '../../../core/models/container.model';
import { TimeagoPipe } from '../../../shared/pipes/timeago.pipe';
import { StatusBadgeComponent } from '../../../shared/components/status-badge/status-badge.component';
import { PageHeaderComponent } from '../../../shared/components/page-header/page-header.component';
import { EmptyStateComponent } from '../../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../../shared/components/error-state/error-state.component';
import { SkeletonComponent } from '../../../shared/components/skeleton/skeleton.component';
import { ConfirmDialogComponent } from '../../../shared/components/confirm-dialog/confirm-dialog.component';

interface PendingConfirm {
  title: string;
  message: string;
  label: string;
  run: () => void;
}

@Component({
  selector: 'app-archive',
  standalone: true,
  imports: [
    DatePipe, TimeagoPipe, StatusBadgeComponent, PageHeaderComponent, EmptyStateComponent,
    ErrorStateComponent, SkeletonComponent, ConfirmDialogComponent,
  ],
  templateUrl: './archive.component.html',
  styleUrl: './archive.component.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ArchiveComponent {
  private svc        = inject(ContainerService);
  private toast      = inject(ToastService);
  private refreshSvc = inject(RefreshService);
  private sourceSvc  = inject(SourceStateService);

  tab      = signal<'archived' | 'ignored'>('archived');
  private archivedAll = signal<Container[]>([]);
  private ignoredAll  = signal<Container[]>([]);
  archived = computed(() => this.sourceSvc.inScope(this.archivedAll(), c => c.source_name));
  ignored  = computed(() => this.sourceSvc.inScope(this.ignoredAll(), c => c.source_name));
  loading  = signal(true);
  error    = signal(false);
  pending  = signal<PendingConfirm | null>(null);

  constructor() {
    reloadOnSourceAndRefresh({ load: () => this.loadData() });
  }

  loadData(): void {
    const src = this.sourceSvc.selectedSource() || undefined;
    forkJoin({
      archived: this.svc.getArchivedContainers(src),
      ignored:  this.svc.getIgnoredContainers(src),
    }).subscribe({
      next: ({ archived, ignored }) => {
        this.archivedAll.set(archived ?? []);
        this.ignoredAll.set(ignored ?? []);
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

  restore(c: Container): void {
    this.exec(this.svc.restoreContainer(c.id), 'Container restored', 'Restore failed');
  }

  hardDelete(c: Container): void {
    this.pending.set({
      title: `Permanently delete “${c.name}”?`,
      message: 'All events, resource samples and stability scores for this container will be removed. This cannot be undone.',
      label: 'Delete forever',
      run: () => this.exec(this.svc.hardDeleteContainer(c.id), 'Permanently deleted', 'Delete failed'),
    });
  }

  deleteAllArchived(): void {
    const count = this.archived().length;
    if (count === 0) return;
    const src = this.sourceSvc.selectedSource() || undefined;
    this.pending.set({
      title: `Delete all ${count} archived container${count === 1 ? '' : 's'}?`,
      message: 'All events, resource samples and stability scores will be removed. This cannot be undone.',
      label: 'Delete all',
      run: () => this.exec(this.svc.purgeArchive(src), 'All archived containers deleted', 'Purge failed'),
    });
  }

  deleteAllIgnored(): void {
    const count = this.ignored().length;
    if (count === 0) return;
    const src = this.sourceSvc.selectedSource() || undefined;
    this.pending.set({
      title: `Delete all ${count} ignored container${count === 1 ? '' : 's'}?`,
      message: 'All events, resource samples and stability scores will be removed. This cannot be undone.',
      label: 'Delete all',
      run: () => this.exec(this.svc.purgeIgnored(src), 'All ignored containers deleted', 'Purge failed'),
    });
  }

  confirmPending(): void {
    const p = this.pending();
    this.pending.set(null);
    p?.run();
  }

  private exec(obs: Observable<ActionResult>, ok: string, fail: string): void {
    obs.subscribe({
      next: res => {
        this.toast.success(res?.message || ok);
        this.loadData();
      },
      error: err => this.toast.apiError(err, fail),
    });
  }
}
