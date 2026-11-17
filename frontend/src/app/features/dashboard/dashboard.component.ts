import {
  Component, ChangeDetectionStrategy, signal, computed, inject,
} from '@angular/core';
import { Router, RouterLink } from '@angular/router';
import { forkJoin, of } from 'rxjs';
import { catchError } from 'rxjs/operators';

import { ContainerService } from '../../core/services/container.service';
import { RefreshService } from '../../core/services/refresh.service';
import { SourceStateService } from '../../core/services/source-state.service';
import { reloadOnSourceAndRefresh } from '../../core/utils/page-reload';
import { LifecycleEvent, SourceStatus, WorkloadSummary } from '../../core/models/container.model';
import { TimeagoPipe } from '../../shared/pipes/timeago.pipe';
import { ScoreClassPipe } from '../../shared/pipes/score-class.pipe';
import { WorkloadBadgeComponent } from '../../shared/components/workload-badge/workload-badge.component';
import { StatusBadgeComponent } from '../../shared/components/status-badge/status-badge.component';
import { PageHeaderComponent } from '../../shared/components/page-header/page-header.component';
import { EmptyStateComponent } from '../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../shared/components/error-state/error-state.component';
import { SkeletonComponent } from '../../shared/components/skeleton/skeleton.component';
import { averageScore, formatScore } from '../../shared/utils/score';
import { containerRoute, shortId } from '../../shared/utils/format';
import { sourceDotClass, sourceStatusLabel } from '../../shared/utils/source';
import { eventChipClass, eventTextClass, eventIcon, eventLabel, eventReason, showsExitCode } from '../../shared/utils/events';

/** Workloads scoring below this are listed under "Needs attention". */
const ATTENTION_SCORE = 70;

const needsAttention = (w: WorkloadSummary) =>
  w.status === 'crashing' || (w.score !== null && w.score < ATTENTION_SCORE);

interface SourceCard {
  name: string;
  type: 'docker' | 'kubernetes';
  source: SourceStatus | null;
  workloads: number;
  attention: number;
  /** Running / total (active) pods or containers across the source's workloads. */
  running: number;
  total: number;
  avgScore: number | null;
}

@Component({
  selector: 'app-dashboard',
  standalone: true,
  imports: [
    RouterLink, TimeagoPipe, ScoreClassPipe, StatusBadgeComponent, WorkloadBadgeComponent,
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

  /** Spinner/skeleton only on the very first load; refreshes are silent. */
  loading      = signal(true);
  error        = signal(false);
  eventsError  = signal(false);
  workloads    = signal<WorkloadSummary[]>([]);
  recentEvents = signal<LifecycleEvent[]>([]);

  sources = this.sourceSvc.sources;

  readonly formatScore = formatScore;
  readonly eventLabel  = eventLabel;
  readonly eventReason = eventReason;
  readonly eventIcon   = eventIcon;
  readonly eventChip   = eventChipClass;
  readonly eventText   = eventTextClass;
  readonly showsExit   = showsExitCode;
  readonly dotClass    = sourceDotClass;
  readonly statusLabel = sourceStatusLabel;
  readonly attentionScore = ATTENTION_SCORE;

  /** Crashing workloads, or score below the threshold, worst first. */
  attention = computed(() =>
    this.workloads()
      .filter(needsAttention)
      .sort((a, b) =>
        (Number(b.status === 'crashing') - Number(a.status === 'crashing')) ||
        ((a.score ?? 100) - (b.score ?? 100)) || (b.crash_count - a.crash_count))
  );
  attentionShown = computed(() => this.attention().slice(0, 10));

  totalPods   = computed(() => this.workloads().reduce((n, w) => n + w.active_instances, 0));
  runningPods = computed(() => this.workloads().reduce((n, w) => n + w.running_instances, 0));
  overallAvgScore = computed(() => averageScore(this.workloads().map(w => w.score)));

  sourceCards = computed<SourceCard[]>(() => {
    const groups = new Map<string, WorkloadSummary[]>();
    for (const w of this.workloads()) {
      const key = w.source_name || 'default';
      (groups.get(key) ?? groups.set(key, []).get(key)!).push(w);
    }

    // Prefer the backend's source list (has connection status); fall back to what workloads report.
    const known = this.sources();
    const entries: { name: string; type: 'docker' | 'kubernetes'; source: SourceStatus | null }[] = known.length
      ? known.map(s => ({ name: s.name, type: s.type, source: s }))
      : [...groups.entries()].map(([name, ws]) => ({ name, type: ws[0]?.runtime ?? 'docker', source: null }));

    return entries.map(({ name, type, source }) => {
      const ws = groups.get(name) ?? [];
      return {
        name, type, source,
        workloads: ws.length,
        attention: ws.filter(needsAttention).length,
        running: ws.reduce((n, w) => n + w.running_instances, 0),
        total: ws.reduce((n, w) => n + w.active_instances, 0),
        avgScore: averageScore(ws.map(w => w.score)),
      };
    });
  });

  constructor() {
    reloadOnSourceAndRefresh({ ignoreSource: true, load: () => this.loadData() });
  }

  /** Two-digit channel number for the source card header. */
  channelNo(i: number): string {
    return String(i + 1).padStart(2, '0');
  }

  eventName(ev: LifecycleEvent): string {
    return ev.container_name || shortId(ev.container_id);
  }

  eventRoute(ev: LifecycleEvent): string[] {
    const runtime = ev.runtime ?? (ev.container_id.includes('/') ? 'kubernetes' : 'docker');
    return containerRoute(ev.container_id, runtime);
  }

  /** Select the source in the global selector and show its workloads. */
  selectSource(card: SourceCard): void {
    this.sourceSvc.select(card.name);
    this.router.navigate(['/workloads']);
  }

  loadData(): void {
    forkJoin({
      workloads: this.svc.getWorkloads(),
      events: this.svc.getRecentEvents(20).pipe(catchError(() => of(null))),
    }).subscribe({
      next: ({ workloads, events }) => {
        this.workloads.set(workloads ?? []);
        this.eventsError.set(events === null);
        if (events) this.recentEvents.set(events);
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
}
