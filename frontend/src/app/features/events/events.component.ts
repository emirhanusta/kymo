import { Component, ChangeDetectionStrategy, signal, computed, inject } from '@angular/core';
import { RouterLink } from '@angular/router';
import { DatePipe } from '@angular/common';
import { Subscription } from 'rxjs';

import { ContainerService } from '../../core/services/container.service';
import { RefreshService } from '../../core/services/refresh.service';
import { SourceStateService } from '../../core/services/source-state.service';
import { LoadReason, reloadOnSourceAndRefresh } from '../../core/utils/page-reload';
import { LifecycleEvent, LifecycleEventType } from '../../core/models/container.model';
import { PageHeaderComponent } from '../../shared/components/page-header/page-header.component';
import { EmptyStateComponent } from '../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../shared/components/error-state/error-state.component';
import { SkeletonComponent } from '../../shared/components/skeleton/skeleton.component';
import { TimeagoPipe } from '../../shared/pipes/timeago.pipe';
import { containerRoute, shortId } from '../../shared/utils/format';
import { eventChipClass, eventIcon, eventLabel, eventReason, showsExitCode } from '../../shared/utils/events';

const PAGE_SIZE = 100;

export const EVENT_TYPE_OPTIONS: { value: LifecycleEventType | ''; label: string }[] = [
  { value: '',       label: 'All events' },
  { value: 'start',  label: 'Start' },
  { value: 'stop',   label: 'Stop' },
  { value: 'die',    label: 'Die / exit' },
  { value: 'crash',  label: 'Crash' },
  { value: 'oom',    label: 'OOM kill' },
  { value: 'pause',  label: 'Pause' },
  { value: 'resume', label: 'Resume' },
];

const byNewest = (a: LifecycleEvent, b: LifecycleEvent) =>
  new Date(b.occurred_at).getTime() - new Date(a.occurred_at).getTime() || b.id - a.id;

/** Full lifecycle event log with type/source filters and "load older" cursor paging. */
@Component({
  selector: 'app-events',
  standalone: true,
  imports: [
    RouterLink, DatePipe, TimeagoPipe, PageHeaderComponent, EmptyStateComponent, ErrorStateComponent, SkeletonComponent,
  ],
  templateUrl: './events.component.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class EventsComponent {
  private svc        = inject(ContainerService);
  private refreshSvc = inject(RefreshService);
  protected sourceSvc = inject(SourceStateService);

  readonly typeOptions = EVENT_TYPE_OPTIONS;
  readonly eventLabel  = eventLabel;
  readonly eventReason = eventReason;
  readonly eventChip   = eventChipClass;
  readonly eventIcon   = eventIcon;
  readonly showsExit   = showsExitCode;
  readonly shortId     = shortId;

  loading     = signal(true);
  error       = signal(false);
  events      = signal<LifecycleEvent[]>([]);
  type        = signal<LifecycleEventType | ''>('');
  /** Page-level source filter; follows the top-bar selector but can be changed here. */
  source      = signal('');
  loadingMore = signal(false);
  moreError   = signal(false);
  /** False once a page came back shorter than PAGE_SIZE. */
  hasMore     = signal(true);

  hasFilters = computed(() => !!this.type() || !!this.source());
  showSourceColumn = computed(() => !this.source());

  private sub?: Subscription;
  private moreSub?: Subscription;

  constructor() {
    reloadOnSourceAndRefresh({
      onSourceChange: src => this.source.set(src),
      load: reason => this.load(reason),
    });
  }

  setType(t: LifecycleEventType | ''): void {
    this.type.set(t);
    this.reset();
  }

  setSource(name: string): void {
    this.source.set(name);
    this.reset();
  }

  clearFilters(): void {
    this.type.set('');
    this.source.set('');
    this.reset();
  }

  reset(): void {
    this.loading.set(true);
    this.events.set([]);
    this.load('init');
  }

  /**
   * Initial / filter loads replace the list. Refresh ticks fetch the newest page and merge it in,
   * so pages loaded with "Load older" survive auto-refresh.
   */
  load(reason: LoadReason): void {
    const merge = (reason === 'auto' || reason === 'manual') && this.events().length > 0;
    this.sub?.unsubscribe();
    if (!merge) this.moreSub?.unsubscribe();
    this.sub = this.svc.queryRecentEvents({
      limit: PAGE_SIZE,
      source: this.source() || undefined,
      type: this.type(),
    }).subscribe({
      next: list => {
        const page = list ?? [];
        if (merge) {
          const seen = new Set(this.events().map(e => e.id));
          const fresh = page.filter(e => !seen.has(e.id));
          if (fresh.length) this.events.set([...fresh, ...this.events()].sort(byNewest));
        } else {
          this.events.set([...page].sort(byNewest));
          this.hasMore.set(page.length >= PAGE_SIZE);
          this.moreError.set(false);
        }
        this.error.set(false);
        this.loading.set(false);
        this.refreshSvc.markUpdated();
      },
      error: () => {
        this.loading.set(false);
        if (!merge) this.error.set(true);
      },
    });
  }

  loadOlder(): void {
    const list = this.events();
    if (!list.length || this.loadingMore()) return;
    const oldest = list[list.length - 1];
    this.loadingMore.set(true);
    this.moreError.set(false);
    this.moreSub = this.svc.queryRecentEvents({
      limit: PAGE_SIZE,
      source: this.source() || undefined,
      type: this.type(),
      before: oldest.occurred_at,
    }).subscribe({
      next: older => {
        const page = older ?? [];
        const seen = new Set(this.events().map(e => e.id));
        this.events.set([...this.events(), ...page.filter(e => !seen.has(e.id))].sort(byNewest));
        this.hasMore.set(page.length >= PAGE_SIZE);
        this.loadingMore.set(false);
      },
      error: () => {
        this.loadingMore.set(false);
        this.moreError.set(true);
      },
    });
  }

  route(ev: LifecycleEvent): string[] {
    return containerRoute(ev.container_id, ev.runtime);
  }

  name(ev: LifecycleEvent): string {
    return ev.container_name || shortId(ev.container_id);
  }
}
