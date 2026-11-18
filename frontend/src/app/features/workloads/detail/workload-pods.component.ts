import { Component, ChangeDetectionStrategy, input, signal, computed, inject, effect, untracked, DestroyRef } from '@angular/core';
import { NgTemplateOutlet } from '@angular/common';
import { RouterLink } from '@angular/router';
import { forkJoin, of, Subscription } from 'rxjs';
import { catchError } from 'rxjs/operators';

import { ContainerService } from '../../../core/services/container.service';
import { RefreshService } from '../../../core/services/refresh.service';
import { SourceStateService } from '../../../core/services/source-state.service';
import { Container, LifecycleEvent, Revision } from '../../../core/models/container.model';
import { StatusBadgeComponent } from '../../../shared/components/status-badge/status-badge.component';
import { EmptyStateComponent } from '../../../shared/components/empty-state/empty-state.component';
import { LogViewerComponent } from '../../../shared/components/log-viewer/log-viewer.component';
import { TimeagoPipe } from '../../../shared/pipes/timeago.pipe';
import { podNameFromId } from '../../../shared/utils/source';
import { STATUS_DOT_CLASS, StatusKey, normalizeStatus, worstStatus } from '../../../shared/utils/status';
import { eventLabel, eventReason } from '../../../shared/utils/events';
import { formatSpan, shortImage } from '../../../shared/utils/format';
import { shortRev } from '../../../shared/utils/timeline';

const time = (s: string) => new Date(s).getTime();

interface PodRow {
  key: string;
  name: string;
  node: string;
  revision: string;
  containers: Container[];
  status: StatusKey;
  current: boolean;
  restarts: number;
  crashes: number;
  ooms: number;
  firstSeen: string;
  lastSeen: string;
}
interface Section {
  key: string;
  title: string;
  summary: string;
  pods: PodRow[];
  defaultOpen: boolean;
}

/** Pods tab: pods grouped under their revision (current one open), expandable rows, live-log shortcuts. */
@Component({
  selector: 'app-workload-pods',
  standalone: true,
  imports: [NgTemplateOutlet, RouterLink, StatusBadgeComponent, EmptyStateComponent, LogViewerComponent, TimeagoPipe],
  template: `
    @if (sections().length === 0) {
      <div class="k-panel">
        <app-empty-state icon="box" [compact]="true" [message]="'No ' + noun() + 's recorded'" hint="Pods appear once the collector has seen them." />
      </div>
    }

    <ng-template #podTable let-pods>
      <div class="overflow-x-auto">
        <table class="k-table">
          <thead>
            <tr>
              <th>{{ label() }}</th>
              <th>Status</th>
              <th class="text-right">Restarts</th>
              <th class="text-right hidden sm:table-cell">Age</th>
              <th class="hidden lg:table-cell">Node</th>
              <th class="text-right"><span class="sr-only">Actions</span></th>
            </tr>
          </thead>
          <tbody>
            @for (p of pods; track p.key) {
              <tr class="hover:bg-kymo-raised/60 transition-colors">
                <td class="px-4 py-2.5 max-w-[22rem]">
                  <div class="flex items-center gap-2 min-w-0">
                    <button type="button" class="text-kymo-amber w-4 flex-none" [attr.aria-expanded]="isExpanded(p.key)" [attr.aria-controls]="'pod-' + p.key"
                            [attr.aria-label]="(isExpanded(p.key) ? 'Collapse ' : 'Expand ') + p.name" (click)="toggleRow(p.key)">{{ isExpanded(p.key) ? '▾' : '▸' }}</button>
                    <span class="truncate text-kymo-ink" [title]="p.name">{{ p.name }}</span>
                    @if (p.containers.length > 1) { <span class="text-[11px] text-kymo-faint flex-none tabular-nums">{{ p.containers.length }} containers</span> }
                  </div>
                </td>
                <td class="px-4 py-2.5"><app-status-badge [status]="p.status" /></td>
                <td class="px-4 py-2.5 text-right text-xs tabular-nums" [class]="p.crashes + p.ooms > 0 ? 'text-kymo-danger' : 'text-kymo-muted'"
                    [title]="p.crashes + ' crash' + (p.crashes === 1 ? '' : 'es') + ', ' + p.ooms + ' OOM kill' + (p.ooms === 1 ? '' : 's')">{{ p.restarts }}</td>
                <td class="px-4 py-2.5 text-right text-xs tabular-nums text-kymo-muted whitespace-nowrap hidden sm:table-cell"
                    [title]="'First seen ' + p.firstSeen + ' · last seen ' + p.lastSeen">{{ age(p) }}</td>
                <td class="px-4 py-2.5 text-xs text-kymo-muted hidden lg:table-cell"><div class="truncate max-w-[12rem]" [title]="p.node">{{ p.node || '—' }}</div></td>
                <td class="px-4 py-2.5 text-right whitespace-nowrap">
                  <span class="inline-flex gap-1">
                    @if (p.current && !frozen()) {
                      <button type="button" class="k-btn k-btn-sm k-btn-icon" [attr.aria-label]="'Live logs of ' + p.name" title="Live logs" (click)="openLogs(p)">
                        @if (p.status === 'running') { <span class="k-led bg-kymo-running animate-pulse" aria-hidden="true"></span> }
                        @else { <svg class="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" d="M4 6h16M4 12h16M4 18h7" /></svg> }
                      </button>
                    } @else if (p.crashes + p.ooms > 0) {
                      <button type="button" class="k-btn k-btn-sm k-btn-icon" [attr.aria-label]="'Crash logs of ' + p.name" title="Crash logs" (click)="openLogs(p)">
                        <svg class="w-3 h-3 text-kymo-danger" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" d="M12 9v4m0 4h.01M10.3 3.9L1.8 18a2 2 0 001.7 3h17a2 2 0 001.7-3L13.7 3.9a2 2 0 00-3.4 0z" /></svg>
                      </button>
                    }
                    @if (p.containers.length === 1) {
                      <a [routerLink]="['/containers', p.containers[0].id]" class="k-btn k-btn-sm k-btn-icon" [attr.aria-label]="'Open container of ' + p.name" title="Container detail">
                        <svg class="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" d="M9 5l7 7-7 7" /></svg>
                      </a>
                    }
                  </span>
                </td>
              </tr>
              @if (isExpanded(p.key)) {
                <tr [id]="'pod-' + p.key" class="bg-kymo-deep/60">
                  <td colspan="6" class="px-4 py-3">
                    <dl class="flex gap-x-6 gap-y-1 flex-wrap text-xs mb-3">
                      <div class="flex gap-2"><dt class="k-label">node</dt><dd class="text-kymo-ink">{{ p.node || '—' }}</dd></div>
                      <div class="flex gap-2"><dt class="k-label">revision</dt><dd class="text-kymo-ink">{{ p.revision ? short(p.revision) : 'unknown' }}</dd></div>
                      <div class="flex gap-2"><dt class="k-label">first seen</dt><dd class="text-kymo-ink" [title]="p.firstSeen">{{ p.firstSeen | timeago }}</dd></div>
                      <div class="flex gap-2"><dt class="k-label">last seen</dt><dd class="text-kymo-ink" [title]="p.lastSeen">{{ p.lastSeen | timeago }}</dd></div>
                      <div class="flex gap-2"><dt class="k-label">crashes</dt><dd class="text-kymo-ink tabular-nums">{{ p.crashes }}</dd></div>
                    </dl>
                    <table class="k-table">
                      <thead><tr>
                        <th>Container</th><th>Image</th><th>Status</th>
                        <th class="text-right">Restarts</th><th class="text-right">Crashes</th><th class="text-right">OOMs</th>
                        <th>Last exit</th><th class="text-right"><span class="sr-only">Links</span></th>
                      </tr></thead>
                      <tbody>
                        @for (c of p.containers; track c.id) {
                          <tr>
                            <td class="px-3 py-2 text-xs text-kymo-ink">{{ c.name }}</td>
                            <td class="px-3 py-2 text-xs text-kymo-muted max-w-[16rem]"><div class="truncate" [title]="c.image">{{ shortImg(c.image) }}</div></td>
                            <td class="px-3 py-2"><app-status-badge [status]="c.current_status" /></td>
                            <td class="px-3 py-2 text-right text-xs tabular-nums text-kymo-muted">{{ restartsOf(c) }}</td>
                            <td class="px-3 py-2 text-right text-xs tabular-nums" [class]="c.crash_count ? 'text-kymo-danger' : 'text-kymo-faint'">{{ c.crash_count }}</td>
                            <td class="px-3 py-2 text-right text-xs tabular-nums" [class]="c.oom_count ? 'text-kymo-amber' : 'text-kymo-faint'">{{ c.oom_count }}</td>
                            <td class="px-3 py-2 text-xs text-kymo-muted">
                              @if (lastExit(c.id); as ev) {
                                <span class="text-kymo-ink">{{ evLabel(ev) }}</span>@if (ev.exit_code != null) { <span class="tabular-nums"> · exit {{ ev.exit_code }}</span> }
                                <span class="text-kymo-faint"> · {{ ev.occurred_at | timeago }}</span>
                                @if (evReason(ev); as r) { <div class="truncate max-w-[18rem]" [title]="r">{{ r }}</div> }
                              } @else { <span class="text-kymo-faint">—</span> }
                            </td>
                            <td class="px-3 py-2 text-right text-xs whitespace-nowrap">
                              <a [routerLink]="['/containers', c.id]" class="text-kymo-amber hover:underline underline-offset-4">Container detail</a>
                              @if (isActive(c)) {
                                <button type="button" class="ml-3 text-kymo-amber hover:underline underline-offset-4" (click)="openLogs(p, c)">Logs</button>
                              } @else if (hasSnapshots(c)) {
                                <button type="button" class="ml-3 text-kymo-amber hover:underline underline-offset-4" (click)="openLogs(p, c)">Crash logs</button>
                              } @else {
                                <span class="ml-3 text-kymo-faint" title="Kubernetes deletes logs with the pod">logs gone with the pod</span>
                              }
                            </td>
                          </tr>
                        }
                      </tbody>
                    </table>
                  </td>
                </tr>
              }
            }
          </tbody>
        </table>
      </div>
    </ng-template>

    <div class="space-y-3">
      @for (s of sections(); track s.key) {
        <section class="k-panel overflow-hidden" [attr.aria-label]="s.title">
          <button type="button" class="w-full flex items-center gap-2 px-4 py-2.5 text-left text-xs hover:bg-kymo-raised transition-colors"
                    [attr.aria-expanded]="isOpen(s)" (click)="toggleSection(s)">
              <span class="text-kymo-amber w-3" aria-hidden="true">{{ isOpen(s) ? '▾' : '▸' }}</span>
              <span class="k-label !text-kymo-ink">{{ s.title }}</span>
              <span class="text-kymo-faint">{{ s.summary }}</span>
            </button>
          @if (isOpen(s)) {
            <div class="border-t border-kymo-rule">
              <ng-container *ngTemplateOutlet="podTable; context: { $implicit: s.pods }" />
            </div>
          }
        </section>
      }
    </div>

    @if (logTarget(); as t) {
      <app-log-viewer [containerId]="t.id" [containerName]="t.name" [containers]="t.siblings" [live]="t.live" [gone]="!t.live" (closed)="logTarget.set(null)" />
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class WorkloadPodsComponent {
  workloadId = input.required<string>();
  instances  = input<Container[]>([]);
  revisions  = input<Revision[]>([]);
  /** "Pod" or "Run" (Jobs). */
  label      = input('Pod');
  /** Skip revision grouping (Jobs: every pod is a run). */
  flat       = input(false);

  private svc     = inject(ContainerService);
  private refresh = inject(RefreshService);
  private sources = inject(SourceStateService);

  readonly dotClass = (s: string) => STATUS_DOT_CLASS[normalizeStatus(s)];
  readonly short = shortRev;
  readonly shortImg = shortImage;
  readonly evLabel = eventLabel;
  readonly evReason = eventReason;
  readonly isRunning = (c: Container) => normalizeStatus(c.current_status) === 'running';
  /** Still part of the current pod set (not stopped): the pod exists, so live logs can work. */
  readonly isActive = (c: Container) => !this.sources.isRemoved(c.source_name) && normalizeStatus(c.current_status) !== 'stopped';
  /** Stopped containers keep crash-time snapshots only when they crashed or were OOM-killed. */
  readonly hasSnapshots = (c: Container) => c.crash_count + c.oom_count > 0;
  readonly age = (p: PodRow) => formatSpan(Date.now() - time(p.firstSeen));
  readonly restartsOf = (c: Container) => Math.max(0, c.start_count - 1);

  /** Pods of a removed cluster no longer exist: only crash snapshots can be opened. */
  frozen = computed(() => this.sources.isRemoved(this.instances()[0]?.source_name));
  noun = computed(() => this.label().toLowerCase());
  private openOverrides = signal<ReadonlyMap<string, boolean>>(new Map());
  private expandedRows  = signal<ReadonlySet<string>>(new Set());
  logTarget = signal<{ id: string; name: string; live: boolean; siblings: { id: string; name: string }[] } | null>(null);
  private problemEvents = signal<LifecycleEvent[]>([]);

  pods = computed<PodRow[]>(() => {
    const map = new Map<string, Container[]>();
    for (const c of this.instances()) {
      const key = podNameFromId(c.id) || c.id;
      (map.get(key) ?? map.set(key, []).get(key)!).push(c);
    }
    const out: PodRow[] = [];
    for (const [key, cs] of map) {
      const statuses = cs.map(c => c.current_status);
      out.push({
        key, name: key,
        node: cs.find(c => c.node_name)?.node_name ?? '',
        revision: cs.find(c => c.revision)?.revision ?? '',
        containers: cs,
        status: worstStatus(statuses),
        current: statuses.some(s => normalizeStatus(s) !== 'stopped'),
        restarts: cs.reduce((n, c) => n + Math.max(0, c.start_count - 1), 0),
        crashes: cs.reduce((n, c) => n + c.crash_count, 0),
        ooms: cs.reduce((n, c) => n + c.oom_count, 0),
        firstSeen: cs.reduce((a, c) => (time(c.first_seen_at) < time(a) ? c.first_seen_at : a), cs[0].first_seen_at),
        lastSeen: cs.reduce((a, c) => (time(c.last_seen_at) > time(a) ? c.last_seen_at : a), cs[0].last_seen_at),
      });
    }
    return out.sort((a, b) => time(b.lastSeen) - time(a.lastSeen));
  });

  sections = computed<Section[]>(() => {
    const pods = this.pods();
    if (!pods.length) return [];
    const revs = this.flat() ? [] : this.revisions();
    if (!revs.length) {
      const cur = pods.filter(p => p.current), prev = pods.filter(p => !p.current);
      return [
        ...(cur.length ? [{ key: 'current', title: `Current ${this.noun()}s`, summary: `${cur.length}`, pods: cur, defaultOpen: true }] : []),
        ...(prev.length ? [{ key: 'previous', title: `Previous ${this.noun()}s`, summary: `${prev.length}`, pods: prev, defaultOpen: !cur.length }] : []),
      ];
    }
    const known = new Set(revs.map(r => r.revision).filter(Boolean));
    const out: Section[] = [];
    // Newest first; with an unlabeled "current" fall back to the first revision.
    const hasCurrent = revs.some(r => r.current);
    revs.filter(r => r.revision).forEach((r, i) => {
      const group = pods.filter(p => p.revision === r.revision);
      if (!group.length) return;
      const isCur = hasCurrent ? r.current : i === 0;
      const n = group.length;
      const summary = isCur
        ? `${n} pod${n === 1 ? '' : 's'} · current`
        : `${n} pod${n === 1 ? '' : 's'} · replaced ${formatSpan(Date.now() - time(r.last_seen_at))} ago`;
      out.push({ key: r.revision, title: `rev ${shortRev(r.revision)}`, summary, pods: group, defaultOpen: isCur });
    });
    const unknown = pods.filter(p => !p.revision || !known.has(p.revision));
    if (unknown.length) {
      out.push({ key: '?', title: 'unknown revision', summary: `${unknown.length} pod${unknown.length === 1 ? '' : 's'}`, pods: unknown, defaultOpen: !out.length });
    }
    return out;
  });

  private lastExitByContainer = computed(() => {
    const m = new Map<string, LifecycleEvent>();
    for (const ev of this.problemEvents()) {
      const cur = m.get(ev.container_id);
      if (!cur || time(ev.occurred_at) > time(cur.occurred_at)) m.set(ev.container_id, ev);
    }
    return m;
  });
  lastExit = (id: string) => this.lastExitByContainer().get(id) ?? null;

  private sub?: Subscription;

  constructor() {
    effect(() => { this.workloadId(); untracked(() => this.loadProblems()); });
    const rs = this.refresh.refresh$.subscribe(() => this.loadProblems());
    inject(DestroyRef).onDestroy(() => { rs.unsubscribe(); this.sub?.unsubscribe(); });
  }

  private loadProblems(): void {
    const id = this.workloadId();
    this.sub?.unsubscribe();
    const q = (type: 'crash' | 'oom') => this.svc.getWorkloadEvents(id, { limit: 100, type }).pipe(catchError(() => of([] as LifecycleEvent[])));
    this.sub = forkJoin([q('crash'), q('oom')]).subscribe(([a, b]) => this.problemEvents.set([...(a ?? []), ...(b ?? [])]));
  }

  isOpen(s: Section): boolean { return this.openOverrides().get(s.key) ?? s.defaultOpen; }
  toggleSection(s: Section): void {
    const next = new Map(this.openOverrides());
    next.set(s.key, !this.isOpen(s));
    this.openOverrides.set(next);
  }
  isExpanded(key: string): boolean { return this.expandedRows().has(key); }
  toggleRow(key: string): void {
    const next = new Set(this.expandedRows());
    if (!next.delete(key)) next.add(key);
    this.expandedRows.set(next);
  }

  /** Live logs of a running container; the pod's first running (else first) container when none is given. */
  openLogs(p: PodRow, c?: Container): void {
    const target = c ?? p.containers.find(x => this.isRunning(x))
      ?? p.containers.find(x => this.isActive(x) || this.hasSnapshots(x)) ?? p.containers[0];
    if (!target) return;
    this.logTarget.set({
      id: target.id, name: target.name, live: this.isActive(target),
      siblings: p.containers.map(x => ({ id: x.id, name: x.name })),
    });
  }
}
