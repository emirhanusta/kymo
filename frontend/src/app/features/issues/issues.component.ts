import { Component, ChangeDetectionStrategy, signal, computed, inject } from '@angular/core';
import { RouterLink } from '@angular/router';

import { WorkloadStateService } from '../../core/services/workload-state.service';
import { IssuesService } from '../../core/services/issues.service';
import { WorkloadBadgeComponent } from '../../shared/components/workload-badge/workload-badge.component';
import { PageHeaderComponent } from '../../shared/components/page-header/page-header.component';
import { EmptyStateComponent } from '../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../shared/components/error-state/error-state.component';
import { SkeletonComponent } from '../../shared/components/skeleton/skeleton.component';
import { TimeagoPipe } from '../../shared/pipes/timeago.pipe';
import { Issue, IssueSeverity } from '../../shared/utils/issues';

/** "What is broken right now": one row per affected workload, critical first. Derivation lives in IssuesService. */
@Component({
  selector: 'app-issues',
  standalone: true,
  imports: [
    RouterLink, PageHeaderComponent, WorkloadBadgeComponent, EmptyStateComponent, ErrorStateComponent,
    SkeletonComponent, TimeagoPipe,
  ],
  templateUrl: './issues.component.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class IssuesComponent {
  protected state  = inject(WorkloadStateService);
  private issueSvc = inject(IssuesService);

  system = signal(false);

  issues = computed(() => this.issueSvc.forCluster(this.state.selectedCluster(), this.system()));
  hiddenSystem = computed(() =>
    this.system() ? 0 : this.issueSvc.forCluster(this.state.selectedCluster(), true).length - this.issues().length);

  groups = computed(() => {
    const all = this.issues();
    return ([
      { severity: 'critical' as IssueSeverity, label: 'Critical', items: all.filter(i => i.severity === 'critical') },
      { severity: 'warning' as IssueSeverity, label: 'Warning', items: all.filter(i => i.severity === 'warning') },
    ]).filter(g => g.items.length > 0);
  });

  ledClass(i: Issue): string {
    return i.severity === 'critical' ? 'bg-kymo-danger' : 'bg-kymo-amber';
  }
  reasonClass(i: Issue): string {
    return i.severity === 'critical' ? 'text-kymo-danger' : 'text-kymo-amber';
  }
}
