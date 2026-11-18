import { Component, ChangeDetectionStrategy, input, signal, computed, inject, effect, untracked, DestroyRef } from '@angular/core';
import { RouterLink } from '@angular/router';
import { DatePipe } from '@angular/common';
import { Subscription } from 'rxjs';

import { ContainerService } from '../../../core/services/container.service';
import { RefreshService } from '../../../core/services/refresh.service';
import { ToastService } from '../../../core/services/toast.service';
import { K8sEvent, LifecycleEvent, Revision } from '../../../core/models/container.model';
import { CrashLogInlineComponent } from '../../../shared/components/crash-log-inline/crash-log-inline.component';
import { ExitTagComponent } from '../../../shared/components/exit-tag/exit-tag.component';
import { EmptyStateComponent } from '../../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../../shared/components/error-state/error-state.component';
import {
  eventContainerName, eventDotClass, eventLabel, eventReason, eventTextClass, hasCrashLogs, isPodReplaced, showsExitCode,
} from '../../../shared/utils/events';
import { isWarning, k8sObject, occurrences, spansTime } from '../../../shared/utils/k8s-events';
import { podNameFromId } from '../../../shared/utils/source';
import { Rollout, TimelineFilter, TimelineRow, buildTimeline, shortRev } from '../../../shared/utils/timeline';
import { imageNameTag, imageTagOf } from '../../../shared/utils/format';

const PAGE = 30;
const FILTERS: { key: TimelineFilter; label: string }[] = [
  { key: 'all', label: 'All' },
  { key: 'problems', label: 'Problems' },
  { key: 'rollouts', label: 'Rollouts' },
  { key: 'k8s', label: 'K8s events' },
];
/** Cap for the Kubernetes events fetched alongside the loaded lifecycle pages. */
const K8S_LIMIT = 500;

/** Activity tab: workload events (paged) merged with rollout markers from the revisions, newest first. */
@Component({
  selector: 'app-workload-timeline',
  standalone: true,
  imports: [RouterLink, DatePipe, ExitTagComponent, CrashLogInlineComponent, EmptyStateComponent, ErrorStateComponent],
  template: `
    <div class="flex items-center justify-between gap-3 flex-wrap mb-3">
      <div class="flex border border-kymo-rule h-[1.875rem]" role="group" aria-label="Filter activity">
        @for (f of filters(); track f.key) {
          <button type="button"
            class="px-3 text-xs whitespace-nowrap transition-colors border-r border-kymo-rule last:border-r-0 inline-flex items-center gap-1.5"
            [class]="filter() === f.key ? 'bg-kymo-raised text-kymo-ink' : 'text-kymo-muted hover:text-kymo-ink hover:bg-kymo-raised'"
            [attr.aria-pressed]="filter() === f.key" (click)="filter.set(f.key)">
            {{ f.label }}
            @if (!loading()) { <span class="tabular-nums text-kymo-faint">{{ counts()[f.key] }}</span> }
          </button>
        }
      </div>
      <span class="text-[11px] text-kymo-faint">newest first</span>
    </div>

    @if (error()) {
      <app-error-state message="Failed to load activity" (retry)="load(false)" />
    } @else if (loading()) {
      <div class="k-panel divide-y divide-kymo-rule animate-pulse" aria-busy="true" aria-label="Loading activity">
        @for (i of [1, 2, 3, 4]; track i) {
          <div class="px-4 py-3 flex items-center gap-3"><div class="h-2.5 w-10 bg-kymo-raised"></div><div class="h-2.5 w-2/5 bg-kymo-raised"></div></div>
        }
      </div>
    } @else if (rows().length === 0) {
      <div class="k-panel">
        @if (counts().all === 0) {
          <app-empty-state icon="events" message="No activity recorded yet"
            hint="Lifecycle events, rollouts and Kubernetes events will appear here." />
        } @else {
          <app-empty-state icon="events" [compact]="true" [message]="'No ' + filterNoun() + ' in this period'"
            [hint]="hasMore() ? 'Older events are not loaded yet.' : ''">
            <button type="button" class="k-btn k-btn-sm mt-3" (click)="filter.set('all')">Show all</button>
          </app-empty-state>
        }
      </div>
    } @else {
      <div class="k-panel">
        @for (g of groups(); track g.key) {
          <section [attr.aria-label]="g.label">
            <h3 class="sticky top-12 z-10 px-4 py-1.5 bg-kymo-raised border-y border-kymo-rule first:border-t-0 flex items-center gap-2">
              <span class="k-label !text-kymo-muted">{{ g.label }}</span>
              <span class="text-[11px] tabular-nums text-kymo-faint">{{ g.rows.length }}</span>
            </h3>
            <ol class="divide-y divide-kymo-rule">
              @for (r of g.rows; track r.key) {
                @if (r.kind === 'rollout') {
                  <li class="flex items-start gap-3 px-4 py-2">
                    <span class="w-11 flex-none text-[11px] leading-5 tabular-nums text-kymo-faint" [title]="r.at">{{ r.at | date:'HH:mm' }}</span>
                    <span class="w-2 h-2 mt-1.5 flex-none rotate-45 bg-kymo-paused" aria-hidden="true"></span>
                    <div class="flex-1 min-w-0">
                      <p class="text-xs leading-5 text-kymo-ink">Rollout <span class="text-kymo-muted">to rev {{ short(r.rollout.revision.revision) }}</span></p>
                      <p class="text-[11px] text-kymo-muted break-words">
                        @if (change(r.rollout); as c) { <span class="tabular-nums">{{ c }}</span> · }{{ r.rollout.revision.pods }} pod{{ r.rollout.revision.pods === 1 ? '' : 's' }}
                      </p>
                    </div>
                  </li>
                } @else if (r.kind === 'k8s') {
                  @let k = r.k8s;
                  @let warn = isWarn(k);
                  <li class="flex items-start gap-3 px-4 py-2">
                    <span class="w-11 flex-none text-[11px] leading-5 tabular-nums text-kymo-faint" [title]="k.last_seen">{{ r.at | date:'HH:mm' }}</span>
                    <span class="w-2 mt-px flex-none text-center text-xs leading-5" [class]="warn ? 'text-kymo-amber' : 'text-kymo-faint'" aria-hidden="true">{{ warn ? '⚠' : '·' }}</span>
                    <div class="flex-1 min-w-0">
                      <p class="text-xs leading-5">
                        <span class="sr-only">{{ k.type }} Kubernetes event:</span>
                        <span [class]="warn ? 'text-kymo-amber' : 'text-kymo-ink'">{{ k.reason }}</span>
                        @if (times(k) > 1) { <span class="tabular-nums text-kymo-muted"> ×{{ times(k) }}</span> }
                        <span class="text-kymo-faint"> · k8s</span>
                      </p>
                      <p class="text-[11px] text-kymo-muted truncate" [title]="object(k)">
                        {{ object(k) }}@if (spans(k)) { · <span class="tabular-nums">{{ k.first_seen | date:'HH:mm:ss' }} → {{ k.last_seen | date:'HH:mm:ss' }}</span> }
                      </p>
                      @if (k.message) {
                        <p class="text-[11px] text-kymo-muted break-words line-clamp-2" [title]="k.message">{{ k.message }}</p>
                      }
                    </div>
                  </li>
                } @else {
                  @let ev = r.ev;
                  @let replaced = podReplaced(ev);
                  <li class="flex items-start gap-3 px-4 py-2" [class.opacity-60]="replaced">
                    <span class="w-11 flex-none text-[11px] leading-5 tabular-nums text-kymo-faint" [title]="r.count > 1 ? r.firstAt + ' → ' + r.at : r.at">{{ r.at | date:'HH:mm' }}</span>
                    <span class="w-2 flex-none flex items-center h-5"><span class="k-led" [class]="replaced ? 'bg-kymo-stopped' : dot(ev)" aria-hidden="true"></span></span>
                    <div class="flex-1 min-w-0">
                      <p class="text-xs leading-5 flex items-center gap-2 flex-wrap">
                        <span [class]="replaced ? 'text-kymo-muted' : text(ev)">
                          {{ replaced ? 'Stopped (pod replaced)' : label(ev) }}@if (r.count > 1) { <span class="tabular-nums"> ×{{ r.count }}</span> }
                        </span>
                        @if (showsExit(ev)) {
                          <app-exit-tag [code]="ev.exit_code!" />
                        }
                      </p>
                      <p class="text-[11px] text-kymo-muted truncate">
                        @if (pod(ev); as p) { {{ p }} · }<a [routerLink]="['/containers', ev.container_id]" class="hover:text-kymo-amber hover:underline underline-offset-4">{{ container(ev) }}</a>
                      </p>
                      @if (!replaced && reason(ev); as rs) {
                        <p class="text-[11px] text-kymo-muted break-words">{{ rs }}</p>
                      }
                      @if (crashLogs(ev)) {
                        <app-crash-log-inline class="mt-1" [containerId]="ev.container_id" [occurredAt]="ev.occurred_at" />
                      }
                    </div>
                  </li>
                }
              }
            </ol>
          </section>
        }
      </div>
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

  /** Job runs are not rollouts: without revisions the Rollouts chip is hidden. */
  readonly filters = computed(() => this.revisions().length ? FILTERS : FILTERS.filter(f => f.key !== 'rollouts'));
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

  /** Row count per filter (chips). */
  counts = computed<Record<TimelineFilter, number>>(() => {
    const out = {} as Record<TimelineFilter, number>;
    for (const f of FILTERS) out[f.key] = buildTimeline(this.events(), this.revisions(), f.key, this.hasMore(), this.k8s()).length;
    return out;
  });

  filterNoun = computed(() => ({ all: 'activity', problems: 'problems', rollouts: 'rollouts', k8s: 'K8s events' }[this.filter()]));

  /** Rows grouped by local calendar day, newest first. */
  groups = computed(() => {
    const out: { key: string; label: string; rows: TimelineRow[] }[] = [];
    const today = new Date(); today.setHours(0, 0, 0, 0);
    for (const r of this.rows()) {
      const d = new Date(r.at);
      const key = `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`;
      let g = out[out.length - 1];
      if (!g || g.key !== key) {
        const day = new Date(d); day.setHours(0, 0, 0, 0);
        const diff = Math.round((today.getTime() - day.getTime()) / 86_400_000);
        const label = diff === 0 ? 'Today' : diff === 1 ? 'Yesterday'
          : d.toLocaleDateString('en-US', { weekday: 'short', month: 'short', day: 'numeric', ...(d.getFullYear() !== today.getFullYear() ? { year: 'numeric' } : {}) });
        g = { key, label, rows: [] };
        out.push(g);
      }
      g.rows.push(r);
    }
    return out;
  });

  /** "v2.11.2 → v2.11.3" (same image, new tag) or "api:1.0 → worker:2.0". */
  change(r: Rollout): string {
    const prev = r.prev.images ?? [], cur = r.revision.images ?? [];
    const removed = prev.filter(i => !cur.includes(i)), added = cur.filter(i => !prev.includes(i));
    if (!removed.length || !added.length) return '';
    if (removed.length === 1 && added.length === 1 && imageNameTag(removed[0]).split(':')[0] === imageNameTag(added[0]).split(':')[0]) {
      return `${imageTagOf(removed[0])} → ${imageTagOf(added[0])}`;
    }
    return `${removed.map(imageNameTag).join(', ')} → ${added.map(imageNameTag).join(', ')}`;
  }

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
