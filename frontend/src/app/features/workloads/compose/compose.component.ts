import { Component, ChangeDetectionStrategy, signal, computed, inject } from '@angular/core';
import { RouterLink } from '@angular/router';

import { ContainerService } from '../../../core/services/container.service';
import { RefreshService } from '../../../core/services/refresh.service';
import { SourceStateService } from '../../../core/services/source-state.service';
import { reloadOnSourceAndRefresh } from '../../../core/utils/page-reload';
import { Container } from '../../../core/models/container.model';
import { StatusBadgeComponent } from '../../../shared/components/status-badge/status-badge.component';
import { EmptyStateComponent } from '../../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../../shared/components/error-state/error-state.component';
import { SkeletonComponent } from '../../../shared/components/skeleton/skeleton.component';
import { SearchHotkeyDirective } from '../../../shared/directives/search-hotkey.directive';
import { TimeagoPipe } from '../../../shared/pipes/timeago.pipe';
import { ScoreClassPipe } from '../../../shared/pipes/score-class.pipe';
import { StatusKey, normalizeStatus, worstStatus } from '../../../shared/utils/status';
import { averageScore, formatScore } from '../../../shared/utils/score';
import { containerRoute } from '../../../shared/utils/format';

interface ComposeProject {
  /** source + project, since two hosts may run a project with the same name. */
  key: string;
  name: string;
  source: string;
  containers: Container[];
  running: number;
  worst: StatusKey;
  avgScore: number | null;
  lastSeen: string;
}

/** Docker Compose projects: containers with workload_kind=Compose grouped by workload_name. */
@Component({
  selector: 'app-compose',
  standalone: true,
  imports: [
    RouterLink, StatusBadgeComponent, EmptyStateComponent, ErrorStateComponent, SkeletonComponent,
    SearchHotkeyDirective, TimeagoPipe, ScoreClassPipe,
  ],
  templateUrl: './compose.component.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ComposeComponent {
  private svc        = inject(ContainerService);
  private refreshSvc = inject(RefreshService);
  protected sourceSvc = inject(SourceStateService);

  readonly formatScore = formatScore;

  loading    = signal(true);
  error      = signal(false);
  containers = signal<Container[]>([]);
  search     = signal('');
  expanded   = signal<ReadonlySet<string>>(new Set());

  projects = computed<ComposeProject[]>(() => {
    const groups = new Map<string, Container[]>();
    for (const c of this.containers()) {
      const name = c.workload_name || '(unnamed)';
      const key = `${c.source_name}/${name}`;
      (groups.get(key) ?? groups.set(key, []).get(key)!).push(c);
    }
    return [...groups.entries()]
      .map(([key, cs]) => ({
        key,
        name: cs[0].workload_name || '(unnamed)',
        source: cs[0].source_name,
        containers: [...cs].sort((a, b) => a.name.localeCompare(b.name)),
        running: cs.filter(c => normalizeStatus(c.current_status) === 'running').length,
        worst: worstStatus(cs.map(c => c.current_status)),
        avgScore: averageScore(cs.map(c => c.score)),
        lastSeen: cs.reduce((m, c) => (c.last_seen_at > m ? c.last_seen_at : m), ''),
      }))
      .sort((a, b) => a.name.localeCompare(b.name) || a.source.localeCompare(b.source));
  });

  filtered = computed(() => {
    const q = this.search().trim().toLowerCase();
    if (!q) return this.projects();
    return this.projects().filter(p =>
      p.name.toLowerCase().includes(q) || p.containers.some(c => c.name.toLowerCase().includes(q)));
  });

  showSource = computed(() => !this.sourceSvc.selectedSource());

  constructor() {
    reloadOnSourceAndRefresh({
      onSourceChange: () => this.expanded.set(new Set()),
      load: () => this.loadData(),
    });
  }

  loadData(): void {
    const src = this.sourceSvc.selectedSource() || undefined;
    this.svc.getContainers('docker', src, { workload_kind: 'Compose' }).subscribe({
      next: list => {
        this.containers.set(list ?? []);
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

  isExpanded(p: ComposeProject): boolean { return this.expanded().has(p.key); }

  toggle(p: ComposeProject): void {
    this.expanded.update(set => {
      const next = new Set(set);
      if (next.has(p.key)) next.delete(p.key); else next.add(p.key);
      return next;
    });
  }

  route(c: Container): string[] { return containerRoute(c.id, c.runtime); }

  panelId(p: ComposeProject): string {
    return 'compose-' + p.key.replace(/[^a-zA-Z0-9_-]/g, '_');
  }
}
