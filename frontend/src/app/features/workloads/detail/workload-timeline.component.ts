import { Component, ChangeDetectionStrategy, input, signal, computed, inject, effect, untracked, DestroyRef } from '@angular/core';
import { RouterLink } from '@angular/router';
import { DatePipe } from '@angular/common';
import { Subscription } from 'rxjs';

import { ContainerService } from '../../../core/services/container.service';
import { RefreshService } from '../../../core/services/refresh.service';
import { ToastService } from '../../../core/services/toast.service';
import { K8sEvent, LifecycleEvent, Revision } from '../../../core/models/container.model';
import { CrashLogInlineComponent } from '../../../shared/components/crash-log-inline/crash-log-inline.component';
import { EmptyStateComponent } from '../../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../../shared/components/error-state/error-state.component';
import { TimeagoPipe } from '../../../shared/pipes/timeago.pipe';
import {
  eventContainerName, eventDotClass, eventLabel, eventReason, eventTextClass, hasCrashLogs, isPodReplaced, showsExitCode,
} from '../../../shared/utils/events';
import { isWarning, k8sObject, occurrences, spansTime } from '../../../shared/utils/k8s-events';
import { podNameFromId } from '../../../shared/utils/source';
import { TimelineFilter, TimelineRow, buildTimeline, shortRev } from '../../../shared/utils/timeline';

const PAGE = 30;
const FILTERS: { key: TimelineFilter; label: string }[] = [
  { key: 'all', label: 'All' },
  { key: 'problems', label: 'Problems' },
  { key: 'rollouts', label: 'Rollouts' },
  { key: 'k8s', label: 'K8s events' },
];
/** Cap for the Kubernetes events fetched alongside the loaded lifecycle pages. */
const K8S_LIMIT = 500;

/** Timeline tab: workload events (paged) merged with rollout markers from the revisions, newest first. */
@Component({
  selector: 'app-workload-timeline',
  standalone: true,
  imports: [RouterLink, DatePipe, CrashLogInlineComponent, EmptyStateComponent, ErrorStateComponent, TimeagoPipe],
  template: `
    <div class="flex items-center justify-between gap-3 flex-wrap mb-3">
      <div class="flex border border-kymo-rule" role="group" aria-label="Filter timeline">
        @for (f of filters; track f.key) {
          <button type="button"
            class="px-3 h-7 text-xs transition-colors border-r border-kymo-rule last:border-r-0"
            [class]="filter() === f.key ? 'bg-kymo-amber text-kymo-deep font-medium' : 'text-kymo-muted hover:text-kymo-ink hover:bg-kymo-raised'"
            [attr.aria-pressed]="filter() === f.key" (click)="filter.set(f.key)">{{ f.label }}</button>
        }
      </div>
      <span class="text-[11px] text-kymo-faint">newest first</span>
    </div>

    @if (error()) {
      <app-error-state message="Failed to load the timeline" (retry)="load(false)" />
    } @else if (loading()) {
      <div class="h-48 bg-kymo-panel border border-kymo-rule animate-pulse" aria-busy="true" aria-label="Loading timeline"></div>
    } @else if (rows().length === 0) {
      <div class="k-panel">
        @if (events().length || revisions().length || k8s().length) {
          <app-empty-state icon="events" [compact]="true" message="Nothing matches this filter"
            [hint]="hasMore() ? 'Older events are not loaded yet — load more below.' : 'Try another filter.'" />
        } @else {
          <app-empty-state icon="events" message="No events recorded" hint="Start, stop and crash events of every pod and container will appear here." />
        }
      </div>
    } @else {
      <ol class="k-panel divide-y divide-kymo-rule">
        @for (r of rows(); track r.key) {
          @if (r.kind === 'rollout') {
            <li class="flex items-start gap-3 px-4 py-2.5 bg-kymo-raised/40">
              <span class="w-2 h-2 mt-1.5 flex-none rotate-45 bg-kymo-paused" aria-hidden="true"></span>
              <div class="flex-1 min-w-0">
                <div class="flex items-center gap-2 flex-wrap">
                  <span class="text-xs font-medium uppercase tracking-wide text-kymo-paused">Rollout</span>
                  <span class="text-xs text-kymo-ink">→ rev {{ short(r.rollout.revision.revision) }}</span>
                  <span class="text-[11px] text-kymo-faint ml-auto flex-none tabular-nums" [title]="r.at">{{ r.at | timeago }}</span>
                </div>
                <p class="text-xs text-kymo-muted mt-0.5 break-words">
                  @if (r.rollout.imageChange) { image {{ r.rollout.imageChange }}, }{{ r.rollout.revision.pods }} pod{{ r.rollout.revision.pods === 1 ? '' : 's' }}
                </p>
              </div>
            </li>
          } @else if (r.kind === 'k8s') {
            @let k = r.k8s;
            @let warn = isWarn(k);
            <li class="flex items-start gap-3 px-4 py-2.5" [class.opacity-70]="!warn">
              <span class="w-3 mt-px flex-none text-center text-xs leading-4" [class]="warn ? 'text-kymo-amber' : 'text-kymo-faint'" aria-hidden="true">{{ warn ? '⚠' : '·' }}</span>
              <div class="flex-1 min-w-0">
                <div class="flex items-center gap-2 flex-wrap">
                  <span class="sr-only">{{ k.type }} Kubernetes event:</span>
                  <span class="text-xs font-medium" [class]="warn ? 'text-kymo-amber' : 'text-kymo-muted'">{{ k.reason }}</span>
                  @if (times(k) > 1) { <span class="text-xs tabular-nums" [class]="warn ? 'text-kymo-amber' : 'text-kymo-muted'">×{{ times(k) }}</span> }
                  <span class="text-[10px] uppercase tracking-wide text-kymo-faint border border-kymo-rule px-1">k8s</span>
                  <span class="text-[11px] text-kymo-faint ml-auto flex-none tabular-nums" [title]="k.last_seen">{{ r.at | timeago }}</span>
                </div>
                <p class="text-[11px] text-kymo-muted truncate" [title]="object(k)">
                  {{ object(k) }}@if (spans(k)) { · <span class="tabular-nums">{{ k.first_seen | date:'HH:mm:ss' }} → {{ k.last_seen | date:'HH:mm:ss' }}</span> }
                </p>
                @if (k.message) {
                  <p class="text-xs text-kymo-muted mt-0.5 break-words line-clamp-3" [title]="k.message">{{ k.message }}</p>
                }
              </div>
            </li>
          } @else {
            @let ev = r.ev;
            @let replaced = podReplaced(ev);
            <li class="flex items-start gap-3 px-4 py-2.5" [class.opacity-60]="replaced">
              <span class="k-led mt-1.5 flex-none" [class]="replaced ? 'bg-kymo-stopped' : dot(ev)" aria-hidden="true"></span>
              <div class="flex-1 min-w-0">
                <div class="flex items-center gap-2 flex-wrap">
                  <span class="text-xs font-medium uppercase tracking-wide" [class]="replaced ? 'text-kymo-muted' : text(ev)">
                    {{ replaced ? 'Stopped (pod replaced)' : label(ev) }}@if (r.count > 1) { <span class="tabular-nums"> ×{{ r.count }}</span> }
                  </span>
                  @if (showsExit(ev)) {
                    <span class="text-[11px] text-kymo-danger border border-kymo-danger/40 px-1.5 tabular-nums">exit {{ ev.exit_code }}</span>
                  }
                  <span class="text-[11px] text-kymo-faint ml-auto flex-none tabular-nums" [title]="r.count > 1 ? r.firstAt + ' → ' + r.at : r.at">{{ r.at | timeago }}</span>
                </div>
                <p class="text-[11px] text-kymo-muted truncate">
                  @if (pod(ev); as p) { {{ p }} · }<a [routerLink]="['/containers', ev.container_id]" class="hover:text-kymo-amber hover:underline underline-offset-4">{{ container(ev) }}</a>
                </p>
                @if (!replaced && reason(ev); as rs) {
                  <p class="text-xs text-kymo-muted mt-0.5 break-words">{{ rs }}</p>
                }
                @if (crashLogs(ev)) {
                  <app-crash-log-inline class="mt-1" [containerId]="ev.container_id" [occurredAt]="ev.occurred_at" />
                }
              </div>
            </li>
          }
        }
      </ol>
      @if (hasMore()) {
        <div class="mt-3 text-center">
          <button type="button" class="k-btn k-btn-sm" [disabled]="loadingMore()" (click)="loadMore()">
            {{ loadingMore() ? 'Loading…' : 'Load older' }}
          </button>
        </div>
      }
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class WorkloadTimelineComponent {
  workloadId = input.required<string>();
  revisions  = input<Revision[]>([]);

  private svc     = inject(ContainerService);
  private refresh = inject(RefreshService);
  private toast   = inject(ToastService);

  readonly filters = FILTERS;
  readonly label = eventLabel;
  readonly reason = eventReason;
  readonly dot = eventDotClass;
  readonly text = eventTextClass;
  readonly showsExit = showsExitCode;
  readonly podReplaced = isPodReplaced;
  readonly crashLogs = hasCrashLogs;
  readonly isWarn = isWarning;
  readonly object = k8sObject;
  readonly times = occurrences;
  readonly spans = spansTime;
  readonly short = shortRev;
  readonly pod = (ev: LifecycleEvent) => podNameFromId(ev.container_id);
  readonly container = eventContainerName;

  filter      = signal<TimelineFilter>('all');
  events      = signal<LifecycleEvent[]>([]);
  hasMore     = signal(false);
  /** Kubernetes events for the time window covered by the loaded lifecycle pages. */
  k8s         = signal<K8sEvent[]>([]);
  loading     = signal(true);
  loadingMore = signal(false);
  error       = signal(false);

  rows = computed<TimelineRow[]>(() => buildTimeline(this.events(), this.revisions(), this.filter(), this.hasMore(), this.k8s()));

  private sub?: Subscription;
  private moreSub?: Subscription;
  private k8sSub?: Subscription;

  constructor() {
    effect(() => {
      this.workloadId();
      untracked(() => { this.events.set([]); this.k8s.set([]); this.hasMore.set(false); this.loading.set(true); this.load(false); });
    });
    const rs = this.refresh.refresh$.subscribe(() => this.load(true));
    inject(DestroyRef).onDestroy(() => { rs.unsubscribe(); this.sub?.unsubscribe(); this.moreSub?.unsubscribe(); this.k8sSub?.unsubscribe(); });
  }

  /** First load replaces; silent refreshes merge so already-loaded older pages stay. */
  load(silent: boolean): void {
    this.sub?.unsubscribe();
    if (!silent) { this.error.set(false); this.loading.set(true); }
    this.sub = this.svc.getWorkloadEvents(this.workloadId(), { limit: PAGE }).subscribe({
      next: fresh => {
        fresh = fresh ?? [];
        if (!silent || !this.events().length) {
          this.events.set(fresh);
          this.hasMore.set(fresh.length >= PAGE);
        } else {
          const byId = new Map(this.events().map(e => [e.id, e]));
          for (const e of fresh) byId.set(e.id, e);
          this.events.set([...byId.values()]);
        }
        this.error.set(false);
        this.loading.set(false);
        this.loadK8s();
      },
      error: () => {
        this.loading.set(false);
        if (!silent || !this.events().length) this.error.set(true);
      },
    });
  }

  loadMore(): void {
    const list = this.events();
    const oldest = list.reduce<LifecycleEvent | null>((a, e) => (!a || e.occurred_at < a.occurred_at ? e : a), null);
    if (!oldest || this.loadingMore()) return;
    this.loadingMore.set(true);
    this.moreSub?.unsubscribe();
    this.moreSub = this.svc.getWorkloadEvents(this.workloadId(), { limit: PAGE, before: oldest.occurred_at }).subscribe({
      next: older => {
        const have = new Set(this.events().map(e => e.id));
        this.events.update(l => [...l, ...(older ?? []).filter(e => !have.has(e.id))]);
        this.hasMore.set((older ?? []).length >= PAGE);
        this.loadingMore.set(false);
        this.loadK8s();
      },
      error: err => { this.loadingMore.set(false); this.toast.apiError(err, 'Failed to load older events'); },
    });
  }

  /**
   * Kubernetes events for the same window as the loaded lifecycle pages: from the oldest loaded event up to now,
   * or everything once the lifecycle history is exhausted. Failures keep what is shown (the feature is additive).
   */
  private loadK8s(): void {
    const evs = this.events();
    const oldest = evs.reduce<string | null>((a, e) => (!a || e.occurred_at < a ? e.occurred_at : a), null);
    const since = this.hasMore() && oldest ? oldest : undefined;
    this.k8sSub?.unsubscribe();
    this.k8sSub = this.svc.getWorkloadK8sEvents(this.workloadId(), { since, limit: K8S_LIMIT }).subscribe({
      next: list => this.k8s.set(list),
      error: () => {},
    });
  }
}
