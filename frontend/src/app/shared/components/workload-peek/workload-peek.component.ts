import { Component, ChangeDetectionStrategy, input, output, signal, computed, inject, effect, untracked, DestroyRef } from '@angular/core';
import { RouterLink } from '@angular/router';
import { Subscription, forkJoin, of } from 'rxjs';
import { catchError } from 'rxjs/operators';

import { ContainerService } from '../../../core/services/container.service';
import { RefreshService } from '../../../core/services/refresh.service';
import { SourceStateService } from '../../../core/services/source-state.service';
import { IssuesService } from '../../../core/services/issues.service';
import { CrashContext } from '../../../core/services/container.service';
import { K8sEvent, LifecycleEvent, WorkloadDetail } from '../../../core/models/container.model';
import { DialogDirective } from '../../directives/dialog.directive';
import { StatusBadgeComponent } from '../status-badge/status-badge.component';
import { WorkloadBadgeComponent } from '../workload-badge/workload-badge.component';
import { LastCrashCardComponent } from '../last-crash-card/last-crash-card.component';
import { MiniTimelineComponent } from '../mini-timeline/mini-timeline.component';
import { LogViewerComponent } from '../log-viewer/log-viewer.component';
import { SkeletonComponent } from '../skeleton/skeleton.component';
import { ScoreClassPipe } from '../../pipes/score-class.pipe';
import { hourAgoIso, warningsLastHour } from '../../utils/k8s-events';
import { formatScore } from '../../utils/score';
import { podNameFromId } from '../../utils/source';
import { normalizeStatus } from '../../utils/status';

/**
 * Right-hand drawer for the Workloads / Jobs / Issues lists: status, last crash, 24h strip, pods ready, score.
 * A non-modal labelled dialog — the list behind it stays usable (j/k move the selection). Below `lg` it overlays
 * the page with a backdrop.
 */
@Component({
  selector: 'app-workload-peek',
  standalone: true,
  imports: [
    RouterLink, DialogDirective, StatusBadgeComponent, WorkloadBadgeComponent, LastCrashCardComponent,
    MiniTimelineComponent, SkeletonComponent, ScoreClassPipe, LogViewerComponent,
  ],
  template: `
    <div class="fixed inset-0 bg-kymo-deep/70 z-[39] lg:hidden" (click)="closed.emit()" aria-hidden="true"></div>
    <aside appDialog [modal]="false" (dismiss)="onDismiss()" aria-labelledby="peek-title"
           class="fixed top-0 right-0 bottom-0 sm:bottom-[34px] w-full sm:w-[26.25rem] z-40 bg-kymo-bg border-l border-kymo-rule border-t-2 border-t-kymo-amber flex flex-col shadow-[-8px_0_24px_rgba(0,0,0,0.35)]">
      <div class="flex items-start gap-3 px-4 py-3 border-b border-kymo-rule flex-none">
        <div class="min-w-0 flex-1">
          <p class="k-label mb-0.5">Workload</p>
          <h2 id="peek-title" class="text-sm font-semibold text-kymo-ink truncate" [title]="w()?.name ?? ''">{{ w()?.name ?? 'Loading…' }}</h2>
          @if (w(); as wl) {
            <div class="flex items-center gap-x-3 gap-y-1 flex-wrap mt-1.5 text-xs">
              <app-status-badge [status]="wl.status" [text]="issues.reasonOf(wl)" />
              <app-workload-badge [kind]="wl.kind" />
              @if (warnings1h() > 0) {
                <span class="inline-flex items-center gap-1 text-[11px] px-1.5 py-px border border-kymo-amber/50 text-kymo-amber tabular-nums"
                      title="Kubernetes Warning events in the last hour (including repeats)">
                  <span aria-hidden="true">⚠</span> {{ warnings1h() }} warning{{ warnings1h() === 1 ? '' : 's' }} (1h)
                </span>
              }
              <span class="text-kymo-faint truncate">{{ wl.namespace || '—' }} · {{ wl.source_name }}</span>
            </div>
          }
        </div>
        <button type="button" data-autofocus class="p-1.5 text-kymo-muted hover:text-kymo-ink hover:bg-kymo-raised transition-colors flex-none" (click)="closed.emit()" aria-label="Close panel (Esc)" title="Close (Esc)">
          <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2"><path stroke-linecap="round" stroke-linejoin="round" d="M6 18L18 6M6 6l12 12" /></svg>
        </button>
      </div>

      <div class="flex-1 overflow-y-auto scrollbar-thin px-4 py-4 space-y-5" aria-live="polite">
        @if (error()) {
          <p class="text-xs text-kymo-danger">Failed to load this workload.
            <button type="button" class="underline underline-offset-4" (click)="load()">retry</button></p>
        } @else if (!detail()) {
          <app-skeleton [rows]="4" />
        } @else {
          @let wl = w()!;
          <dl class="grid grid-cols-3 border border-kymo-rule divide-x divide-kymo-rule bg-kymo-panel text-center">
            <div class="py-2"><dt class="k-label">Pods ready</dt>
              <dd class="text-lg font-semibold tabular-nums mt-1" [class]="wl.running_instances === wl.active_instances && wl.active_instances > 0 ? 'text-kymo-running' : wl.running_instances === 0 ? 'text-kymo-muted' : 'text-kymo-amber'">{{ wl.running_instances }}/{{ wl.active_instances }}</dd></div>
            <div class="py-2"><dt class="k-label">Score</dt>
              <dd class="text-lg font-semibold tabular-nums mt-1" [class]="wl.score | scoreClass">{{ score(wl.score) }}</dd></div>
            <div class="py-2"><dt class="k-label">Restarts</dt>
              <dd class="text-lg font-semibold tabular-nums mt-1 text-kymo-ink">{{ wl.restart_count }}</dd></div>
          </dl>

          <app-last-crash-card [crashEvents]="ctx().crashEvents" [logs]="ctx().logs" [revisions]="ctx().revisions" />

          <section aria-labelledby="peek-strip">
            <h3 id="peek-strip" class="k-label mb-1.5">Last 24 hours</h3>
            <app-mini-timeline [events]="recent()" [revisions]="ctx().revisions" />
          </section>

          <div class="relative">
            @if (livePods().length) {
              <button type="button" class="k-btn w-full justify-center" [attr.aria-expanded]="livePods().length > 1 ? picker() : null"
                      aria-haspopup="true" (click)="liveClick()">
                <span class="k-led bg-kymo-running animate-pulse" aria-hidden="true"></span> Live logs
                @if (livePods().length > 1) { <span class="text-kymo-faint">· {{ livePods().length }} pods ▾</span> }
              </button>
              @if (picker()) {
                <ul class="absolute z-10 left-0 right-0 mt-1 k-panel max-h-56 overflow-auto scrollbar-thin" role="menu" aria-label="Pick a pod">
                  @for (p of livePods(); track p.name) {
                    <li role="none"><button type="button" role="menuitem" class="w-full text-left px-3 py-1.5 text-xs text-kymo-ink hover:bg-kymo-raised truncate" (click)="openPod(p)">{{ p.name }}</button></li>
                  }
                </ul>
              }
            } @else {
              <p class="text-[11px] text-kymo-faint">{{ sources.isRemoved(wl.source_name) ? 'Removed cluster: live logs are unavailable.' : 'No running pods to tail.' }}</p>
            }
          </div>

          @if (wl.images.length) {
            <p class="text-[11px] text-kymo-faint break-all"><span class="k-label mr-1.5">{{ wl.images.length > 1 ? 'images' : 'image' }}</span>{{ wl.images.join(', ') }}</p>
          }
        }
      </div>

      <div class="px-4 py-3 border-t border-kymo-rule flex-none flex items-center justify-between gap-3">
        <span class="text-[11px] text-kymo-faint hidden sm:inline"><kbd class="k-kbd">j</kbd> <kbd class="k-kbd">k</kbd> next / prev · <kbd class="k-kbd">Esc</kbd> close</span>
        <a [routerLink]="['/workloads', id()]" class="k-btn k-btn-primary k-btn-sm ml-auto">Open workload →</a>
      </div>
    </aside>
    @if (logTarget(); as t) {
      <app-log-viewer [containerId]="t.id" [containerName]="t.name" [containers]="t.siblings" [live]="true" (closed)="logTarget.set(null)" />
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class WorkloadPeekComponent {
  id     = input.required<string>();
  closed = output<void>();

  private svc     = inject(ContainerService);
  private refresh = inject(RefreshService);
  protected issues = inject(IssuesService);
  protected sources = inject(SourceStateService);

  readonly score = formatScore;

  detail = signal<WorkloadDetail | null>(null);
  ctx    = signal<CrashContext>({ crashEvents: [], logs: [], revisions: [] });
  events = signal<LifecycleEvent[]>([]);
  error  = signal(false);
  warnings1h = signal(0);

  w = computed(() => {
    const d = this.detail();
    return d ? { ...d.workload, images: d.workload.images ?? [], containers: d.workload.containers ?? [] } : null;
  });
  recent = computed(() => this.events());

  /** Pods with at least one running container, each with those containers. */
  livePods = computed(() => {
    const map = new Map<string, { id: string; name: string }[]>();
    // Removed clusters have no live pods to tail.
    if (this.sources.isRemoved(this.detail()?.workload.source_name)) return [];
    for (const c of this.detail()?.instances ?? []) {
      if (normalizeStatus(c.current_status) !== 'running') continue;
      const pod = podNameFromId(c.id) || c.id;
      (map.get(pod) ?? map.set(pod, []).get(pod)!).push({ id: c.id, name: c.name });
    }
    return [...map].map(([name, containers]) => ({ name, containers }));
  });
  picker    = signal(false);
  logTarget = signal<{ id: string; name: string; siblings: { id: string; name: string }[] } | null>(null);

  /** Escape belongs to an open log modal first. */
  onDismiss(): void {
    if (document.querySelector('[aria-modal="true"]')) return;
    this.closed.emit();
  }

  liveClick(): void {
    const pods = this.livePods();
    if (pods.length === 1) this.openPod(pods[0]); else this.picker.update(v => !v);
  }
  openPod(p: { name: string; containers: { id: string; name: string }[] }): void {
    this.picker.set(false);
    this.logTarget.set({ id: p.containers[0].id, name: p.containers[0].name, siblings: p.containers });
  }

  private sub?: Subscription;

  constructor() {
    effect(() => {
      this.id();
      untracked(() => { this.detail.set(null); this.ctx.set({ crashEvents: [], logs: [], revisions: [] }); this.events.set([]); this.warnings1h.set(0); this.load(); });
    });
    const rs = this.refresh.refresh$.subscribe(() => this.load(true));
    inject(DestroyRef).onDestroy(() => { this.sub?.unsubscribe(); rs.unsubscribe(); });
  }

  load(silent = false): void {
    const id = this.id();
    this.sub?.unsubscribe();
    if (!silent) this.error.set(false);
    this.sub = forkJoin({
      detail: this.svc.getWorkload(id),
      ctx: this.svc.getCrashContext(id),
      events: this.svc.getWorkloadEvents(id, { limit: 100 }).pipe(catchError(() => of([] as LifecycleEvent[]))),
      k8s: this.svc.getWorkloadK8sEvents(id, { since: hourAgoIso(), limit: 500 }).pipe(catchError(() => of([] as K8sEvent[]))),
    }).subscribe({
      next: ({ detail, ctx, events, k8s }) => {
        this.warnings1h.set(warningsLastHour(k8s));
        this.detail.set(detail);
        this.ctx.set(ctx);
        this.events.set(events ?? []);
        this.error.set(false);
      },
      error: () => { if (!silent || !this.detail()) this.error.set(true); },
    });
  }
}
