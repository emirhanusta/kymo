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
import { Container, LifecycleEvent, SourceStatus } from '../../core/models/container.model';
import { TimeagoPipe } from '../../shared/pipes/timeago.pipe';
import { ScoreClassPipe } from '../../shared/pipes/score-class.pipe';
import { StatusBadgeComponent } from '../../shared/components/status-badge/status-badge.component';
import { PageHeaderComponent } from '../../shared/components/page-header/page-header.component';
import { EmptyStateComponent } from '../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../shared/components/error-state/error-state.component';
import { SkeletonComponent } from '../../shared/components/skeleton/skeleton.component';
import { averageScore, formatScore } from '../../shared/utils/score';
import { containerRoute, shortId } from '../../shared/utils/format';
import { sourceDotClass, sourceStatusLabel } from '../../shared/utils/source';
import { normalizeStatus } from '../../shared/utils/status';
import { eventChipClass, eventTextClass, eventIcon, eventLabel, eventReason, showsExitCode } from '../../shared/utils/events';

const CIRCUMFERENCE = 2 * Math.PI * 30; // donut r=30

const isCritical = (c: Container) => normalizeStatus(c.current_status) === 'crashing';

interface SourceCard {
  name: string;
  type: 'docker' | 'kubernetes';
  source: SourceStatus | null;
  total: number;
  running: number;
  stopped: number;
  critical: number;
  avgScore: number | null;
  /** stroke-dasharray / offset pairs for the donut. */
  seg: { running: string; stopped: string; stoppedOffset: number; critical: string; criticalOffset: number };
}

@Component({
  selector: 'app-dashboard',
  standalone: true,
  imports: [
    RouterLink, TimeagoPipe, ScoreClassPipe, StatusBadgeComponent,
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
  containers   = signal<Container[]>([]);
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

  totalContainers = computed(() => this.containers().length);
  totalRunning    = computed(() => this.containers().filter(c => c.current_status === 'running').length);
  totalCritical   = computed(() => this.containers().filter(isCritical).length);
  overallAvgScore = computed(() => averageScore(this.containers().map(c => c.score)));

  sourceCards = computed<SourceCard[]>(() => {
    const groups = new Map<string, Container[]>();
    for (const c of this.containers()) {
      const key = c.source_name || 'default';
      (groups.get(key) ?? groups.set(key, []).get(key)!).push(c);
    }

    // Prefer the backend's source list (has connection status); fall back to what containers report.
    const known = this.sources();
    const entries: { name: string; type: 'docker' | 'kubernetes'; source: SourceStatus | null }[] = known.length
      ? known.map(s => ({ name: s.name, type: s.type, source: s }))
      : [...groups.entries()].map(([name, cs]) => ({ name, type: cs[0]?.runtime ?? 'docker', source: null }));

    return entries.map(({ name, type, source }) => {
      const cs = groups.get(name) ?? [];
      const total    = cs.length;
      const running  = cs.filter(c => c.current_status === 'running').length;
      const critical = cs.filter(isCritical).length;
      const stopped  = total - running - critical;
      const len = (n: number) => (total ? (n / total) * CIRCUMFERENCE : 0);
      return {
        name, type, source, total, running, stopped, critical,
        avgScore: averageScore(cs.map(c => c.score)),
        seg: {
          running:        `${len(running)} ${CIRCUMFERENCE}`,
          stopped:        `${len(stopped)} ${CIRCUMFERENCE}`,
          stoppedOffset:  -len(running),
          critical:       `${len(critical)} ${CIRCUMFERENCE}`,
          criticalOffset: -(len(running) + len(stopped)),
        },
      };
    });
  });

  criticalContainers = computed(() =>
    this.containers()
      .filter(c => isCritical(c) || c.crash_count > 5 || c.oom_count > 0 || (c.score !== null && c.score < 50))
      .sort((a, b) => (b.crash_count - a.crash_count) || ((a.score ?? 100) - (b.score ?? 100)))
      .slice(0, 10)
  );

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

  containerRoute(c: Container): string[] {
    return containerRoute(c.id, c.runtime);
  }

  selectSource(card: SourceCard): void {
    this.sourceSvc.select(card.name);
    this.router.navigate(['/containers']);
  }

  loadData(): void {
    forkJoin({
      containers: this.svc.getContainers(),
      events: this.svc.getRecentEvents(20).pipe(catchError(() => of(null))),
    }).subscribe({
      next: ({ containers, events }) => {
        this.containers.set(containers ?? []);
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
