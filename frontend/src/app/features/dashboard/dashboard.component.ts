import {
  Component, ChangeDetectionStrategy, signal, computed, inject,
} from '@angular/core';
import { Router, RouterLink } from '@angular/router';
import { catchError } from 'rxjs/operators';
import { of } from 'rxjs';

import { ContainerService } from '../../core/services/container.service';
import { RefreshService } from '../../core/services/refresh.service';
import { SourceStateService } from '../../core/services/source-state.service';
import { WorkloadStateService } from '../../core/services/workload-state.service';
import { IssuesService } from '../../core/services/issues.service';
import { reloadOnSourceAndRefresh } from '../../core/utils/page-reload';
import { LifecycleEvent, SourceStatus, WorkloadSummary } from '../../core/models/container.model';
import { TimeagoPipe } from '../../shared/pipes/timeago.pipe';
import { ScoreClassPipe } from '../../shared/pipes/score-class.pipe';
import { WorkloadBadgeComponent } from '../../shared/components/workload-badge/workload-badge.component';
import { PageHeaderComponent } from '../../shared/components/page-header/page-header.component';
import { EmptyStateComponent } from '../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../shared/components/error-state/error-state.component';
import { SkeletonComponent } from '../../shared/components/skeleton/skeleton.component';
import { averageScore, formatScore } from '../../shared/utils/score';
import { containerRoute, shortId } from '../../shared/utils/format';
import { sourceDotClass, sourceStatusLabel } from '../../shared/utils/source';
import { isLongRunning } from '../../shared/utils/workload';
import { Issue } from '../../shared/utils/issues';
import { eventChipClass, eventTextClass, eventIcon, eventLabel, eventReason, showsExitCode } from '../../shared/utils/events';

const TOP_ISSUES = 5;

interface ClusterCard {
  name: string;
  source: SourceStatus | null;
  workloads: number;
  /** Ready / active pods across the cluster's workloads. */
  running: number;
  total: number;
  issues: number;
  avgScore: number | null;
}

@Component({
  selector: 'app-dashboard',
  standalone: true,
  imports: [
    RouterLink, TimeagoPipe, ScoreClassPipe, WorkloadBadgeComponent,
    PageHeaderComponent, EmptyStateComponent, ErrorStateComponent, SkeletonComponent,
  ],
  templateUrl: './dashboard.component.html',
  styleUrl: './dashboard.component.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class DashboardComponent {
  private svc        = inject(ContainerService);
  private refreshSvc = inject(RefreshService);
  private sourceSvc  = inject(SourceStateService);
  private router     = inject(Router);
  protected state    = inject(WorkloadStateService);
  private issueSvc   = inject(IssuesService);

  eventsError  = signal(false);
  private loadedEvents = signal<LifecycleEvent[]>([]);
  recentEvents = computed(() => this.sourceSvc.globalScope(this.loadedEvents(), e => e.source_name));

  clusters = this.sourceSvc.sources;

  readonly formatScore = formatScore;
  readonly eventLabel  = eventLabel;
  readonly eventReason = eventReason;
  readonly eventIcon   = eventIcon;
  readonly eventChip   = eventChipClass;
  readonly eventText   = eventTextClass;
  readonly showsExit   = showsExitCode;
  readonly dotClass    = sourceDotClass;
  readonly statusLabel = sourceStatusLabel;

  /** The Overview spans every connected cluster (or the selected removed one); system workloads are left out. */
  private workloads = computed(() =>
    this.sourceSvc.globalScope(this.state.workloads(), w => w.source_name).filter(w => !w.is_system && isLongRunning(w)));

  /** Open issues across all clusters, system workloads excluded. */
  issues    = computed(() => this.issueSvc.forCluster('', false));
  /** Issues are derived from live status, which is frozen for removed clusters. */
  historyMode = this.sourceSvc.historyMode;
  topIssues = computed(() => this.issues().slice(0, TOP_ISSUES));

  totalPods   = computed(() => this.workloads().reduce((n, w) => n + w.active_instances, 0));
  runningPods = computed(() => this.workloads().reduce((n, w) => n + w.running_instances, 0));
  overallAvgScore = computed(() => averageScore(this.workloads().map(w => w.score)));
  workloadCount = computed(() => this.workloads().length);

  clusterCards = computed<ClusterCard[]>(() => {
    const groups = new Map<string, WorkloadSummary[]>();
    for (const w of this.workloads()) {
      const key = w.source_name || 'default';
      (groups.get(key) ?? groups.set(key, []).get(key)!).push(w);
    }
    const issueCount = new Map<string, number>();
    for (const i of this.issues()) issueCount.set(i.source, (issueCount.get(i.source) ?? 0) + 1);

    // Prefer the backend's cluster list (has connection status); fall back to what workloads report.
    // A removed cluster in history mode is not in that list: show its own card.
    const known = this.historyMode() ? [] : this.clusters();
    const entries: { name: string; source: SourceStatus | null }[] = known.length
      ? known.map(s => ({ name: s.name, source: s }))
      : [...groups.keys()].map(name => ({ name, source: null }));

    return entries.map(({ name, source }) => {
      const ws = groups.get(name) ?? [];
      return {
        name, source,
        workloads: ws.length,
        running: ws.reduce((n, w) => n + w.running_instances, 0),
        total: ws.reduce((n, w) => n + w.active_instances, 0),
        issues: issueCount.get(name) ?? 0,
        avgScore: averageScore(ws.map(w => w.score)),
      };
    });
  });

  constructor() {
    // Workloads/issues come from the shared state; only the recent-events feed is loaded here.
    reloadOnSourceAndRefresh({ load: () => this.loadEvents() });
  }

  /** Two-digit channel number for the cluster card header. */
  channelNo(i: number): string {
    return String(i + 1).padStart(2, '0');
  }

  eventName(ev: LifecycleEvent): string {
    return ev.container_name || shortId(ev.container_id);
  }

  eventRoute(ev: LifecycleEvent): string[] {
    return containerRoute(ev.container_id, ev.runtime);
  }

  issueReasonClass(i: Issue): string {
    return i.severity === 'critical' ? 'text-kymo-danger' : 'text-kymo-amber';
  }

  /** Select the cluster in the global selector and show its workloads. */
  selectCluster(card: ClusterCard): void {
    this.sourceSvc.select(card.name);
    this.router.navigate(['/workloads']);
  }

  loadEvents(): void {
    // A removed cluster is requested explicitly; otherwise the newest events are filtered to connected clusters.
    const removed = this.sourceSvc.selectedRemoved()?.name;
    this.svc.getRecentEvents(removed ? 20 : 40, removed).pipe(catchError(() => of(null))).subscribe(events => {
      this.eventsError.set(events === null);
      if (events) this.loadedEvents.set(events);
      this.refreshSvc.markUpdated();
    });
  }
}
