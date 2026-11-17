import { Component, ChangeDetectionStrategy, signal, computed, inject } from '@angular/core';
import { ActivatedRoute, Router, RouterLink } from '@angular/router';
import { toSignal } from '@angular/core/rxjs-interop';
import { map } from 'rxjs/operators';

import { IssuesService } from '../../core/services/issues.service';
import { SourceStateService } from '../../core/services/source-state.service';
import { Issue, IssueSeverity } from '../../core/models/issue.model';
import { reloadOnSourceAndRefresh } from '../../core/utils/page-reload';
import { PeekController } from '../../shared/utils/peek-controller';
import { WorkloadPeekComponent } from '../../shared/components/workload-peek/workload-peek.component';
import { WorkloadBadgeComponent } from '../../shared/components/workload-badge/workload-badge.component';
import { PageHeaderComponent } from '../../shared/components/page-header/page-header.component';
import { EmptyStateComponent } from '../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../shared/components/error-state/error-state.component';
import { SkeletonComponent } from '../../shared/components/skeleton/skeleton.component';
import { TimeagoPipe } from '../../shared/pipes/timeago.pipe';
import { formatSpan } from '../../shared/utils/format';
import { issueTypeLabel, issueWorkloadQuery, severityLedClass, severityTextClass } from '../../shared/utils/issues';

type Tab = 'open' | 'resolved';
const PAGE = 50;

/** Server-tracked issues: what is broken right now (Open) and what was fixed (Resolved). */
@Component({
  selector: 'app-issues',
  standalone: true,
  imports: [
    RouterLink, PageHeaderComponent, WorkloadBadgeComponent, EmptyStateComponent, ErrorStateComponent,
    SkeletonComponent, TimeagoPipe, WorkloadPeekComponent,
  ],
  templateUrl: './issues.component.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class IssuesComponent {
  protected svc     = inject(IssuesService);
  protected sources = inject(SourceStateService);
  private route     = inject(ActivatedRoute);
  private router    = inject(Router);

  readonly tab = toSignal(
    this.route.queryParamMap.pipe(map((qp): Tab => (qp.get('tab') === 'resolved' ? 'resolved' : 'open'))),
    { initialValue: 'open' as Tab },
  );

  readonly typeLabel = issueTypeLabel;
  readonly wlQuery   = issueWorkloadQuery;
  readonly led       = severityLedClass;
  readonly text      = severityTextClass;

  open = this.svc.scopedOpen;

  groups = computed(() => {
    const all = this.open();
    return ([
      { severity: 'critical' as IssueSeverity, label: 'Critical', items: all.filter(i => i.severity === 'critical') },
      { severity: 'warning' as IssueSeverity, label: 'Warning', items: all.filter(i => i.severity === 'warning') },
    ]).filter(g => g.items.length > 0);
  });

  // ── Resolved tab (loaded on demand, cursor-paged) ─────────────────────────
  resolved        = signal<Issue[]>([]);
  resolvedLoaded  = signal(false);
  resolvedError   = signal(false);
  hasMore         = signal(false);
  loadingMore     = signal(false);
  private resolvedSeq = 0;

  readonly peek = new PeekController(() =>
    (this.tab() === 'open' ? this.open() : this.resolved()).map(i => i.workload_id));

  constructor() {
    reloadOnSourceAndRefresh({ load: reason => this.loadResolved(reason === 'init' || reason === 'source') });
  }

  setTab(t: Tab): void {
    this.router.navigate([], {
      relativeTo: this.route, queryParams: { tab: t === 'open' ? null : t, peek: null },
      queryParamsHandling: 'merge', replaceUrl: true,
    });
  }

  /** Arrow keys / Home / End move between the two tabs. */
  onTabKey(ev: KeyboardEvent): void {
    const keys: Tab[] = ['open', 'resolved'];
    let n = keys.indexOf(this.tab());
    if (ev.key === 'ArrowRight' || ev.key === 'ArrowLeft') n = 1 - n;
    else if (ev.key === 'Home') n = 0;
    else if (ev.key === 'End') n = 1;
    else return;
    ev.preventDefault();
    this.setTab(keys[n]);
    setTimeout(() => document.getElementById('issues-tab-' + keys[n])?.focus(), 0);
  }

  /** (Re)loads the first page of resolved issues; silent refreshes keep what is on screen. */
  loadResolved(reset: boolean): void {
    if (this.sources.historyMode()) return;
    const seq = ++this.resolvedSeq;
    if (reset) { this.resolved.set([]); this.resolvedLoaded.set(false); }
    this.svc.list({ status: 'resolved', source: this.sources.selectedSource(), limit: PAGE }).subscribe({
      next: list => {
        if (seq !== this.resolvedSeq) return;
        this.resolved.set(list);
        this.hasMore.set(list.length >= PAGE);
        this.resolvedError.set(false);
        this.resolvedLoaded.set(true);
      },
      error: () => {
        if (seq !== this.resolvedSeq) return;
        this.resolvedError.set(true);
        this.resolvedLoaded.set(true);
      },
    });
  }

  loadMore(): void {
    const last = this.resolved().at(-1);
    if (!last || this.loadingMore()) return;
    const seq = this.resolvedSeq;
    this.loadingMore.set(true);
    this.svc.list({ status: 'resolved', source: this.sources.selectedSource(), limit: PAGE, before: last.resolved_at ?? last.first_seen }).subscribe({
      next: list => {
        this.loadingMore.set(false);
        if (seq !== this.resolvedSeq) return;
        const seen = new Set(this.resolved().map(i => i.id));
        const fresh = list.filter(i => !seen.has(i.id));
        this.resolved.update(cur => [...cur, ...fresh]);
        this.hasMore.set(list.length >= PAGE && fresh.length > 0);
      },
      error: () => this.loadingMore.set(false),
    });
  }

  /** "12 min" between opening and resolution. */
  duration(i: Issue): string {
    if (!i.resolved_at) return '—';
    return formatSpan(new Date(i.resolved_at).getTime() - new Date(i.first_seen).getTime());
  }
}
