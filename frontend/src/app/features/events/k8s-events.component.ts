import { Component, ChangeDetectionStrategy, signal, computed, inject, effect, untracked, DestroyRef } from '@angular/core';
import { RouterLink } from '@angular/router';
import { DatePipe } from '@angular/common';
import { Subject, Subscription } from 'rxjs';
import { debounceTime, distinctUntilChanged } from 'rxjs/operators';

import { ContainerService } from '../../core/services/container.service';
import { NamespaceService } from '../../core/services/namespace.service';
import { RefreshService } from '../../core/services/refresh.service';
import { SourceStateService } from '../../core/services/source-state.service';
import { LoadReason, reloadOnSourceAndRefresh } from '../../core/utils/page-reload';
import { K8sEvent, K8sEventType, K8sReasonCount } from '../../core/models/container.model';
import { EmptyStateComponent } from '../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../shared/components/error-state/error-state.component';
import { SkeletonComponent } from '../../shared/components/skeleton/skeleton.component';
import { TimeagoPipe } from '../../shared/pipes/timeago.pipe';
import { TYPE_BADGE, byLastSeen, k8sObject, occurrences } from '../../shared/utils/k8s-events';

const PAGE_SIZE = 100;
const TOP_REASONS = 12;
const TYPES: { value: K8sEventType | ''; label: string }[] = [
  { value: 'Warning', label: 'Warning' },
  { value: 'Normal',  label: 'Normal' },
  { value: '',        label: 'All' },
];

/** Kubernetes view of the Events page: persisted cluster events, newest `last_seen` first, with cursor paging. */
@Component({
  selector: 'app-k8s-events',
  standalone: true,
  imports: [RouterLink, DatePipe, TimeagoPipe, EmptyStateComponent, ErrorStateComponent, SkeletonComponent],
  template: `
    <div class="flex items-center gap-2 flex-wrap">
      <div class="flex border border-kymo-rule" role="group" aria-label="Filter by event type">
        @for (t of types; track t.value) {
          <button type="button"
            class="px-3 h-[1.875rem] text-xs transition-colors border-r border-kymo-rule last:border-r-0"
            [class]="type() === t.value ? 'bg-kymo-amber text-kymo-deep font-medium' : 'text-kymo-muted hover:text-kymo-ink hover:bg-kymo-raised'"
            [attr.aria-pressed]="type() === t.value" (click)="setType(t.value)">{{ t.label }}</button>
        }
      </div>
      <select aria-label="Filter by namespace" class="k-input cursor-pointer max-w-[14rem]" [value]="namespace()"
              (change)="setNamespace($any($event.target).value)">
        <option value="">All namespaces</option>
        @for (n of namespaces(); track n) {
          <option [value]="n" [selected]="n === namespace()">{{ n }}</option>
        }
      </select>
      <input type="search" class="k-input flex-1 min-w-[12rem] max-w-sm" placeholder="Search message, reason, object…"
             aria-label="Search Kubernetes events" [value]="q()" (input)="onSearch($any($event.target).value)" />
    </div>

    @if (reasonChips().length) {
      <div class="flex items-center gap-1.5 flex-wrap" role="group" aria-label="Filter by reason">
        <span class="k-label mr-1">Reason</span>
        @for (r of reasonChips(); track r.reason) {
          <button type="button"
            class="px-2 h-6 text-[11px] border transition-colors tabular-nums"
            [class]="reason() === r.reason ? 'border-kymo-amber bg-kymo-amber text-kymo-deep font-medium' : 'border-kymo-rule text-kymo-muted hover:text-kymo-ink hover:bg-kymo-raised'"
            [attr.aria-pressed]="reason() === r.reason" (click)="toggleReason(r.reason)">
            {{ r.reason }} <span [class]="reason() === r.reason ? '' : 'text-kymo-faint'">{{ r.count }}</span>
          </button>
        }
        @if (hiddenReasons() > 0) {
          <button type="button" class="px-2 h-6 text-[11px] text-kymo-muted hover:text-kymo-ink underline underline-offset-4"
                  [attr.aria-expanded]="allReasons()" (click)="allReasons.set(!allReasons())">
            {{ allReasons() ? 'fewer' : hiddenReasons() + ' more' }}
          </button>
        }
      </div>
    }

    @if (error()) {
      <app-error-state message="Failed to load Kubernetes events" (retry)="reset()" />
    }

    @if (loading()) {
      <app-skeleton [rows]="10" />
    } @else if (rows().length === 0 && !error()) {
      <div class="k-panel">
        @if (hasFilters()) {
          <app-empty-state icon="search" message="No Kubernetes events match your filters" hint="Try another type, reason or namespace.">
            <button type="button" class="k-btn k-btn-primary k-btn-sm mt-4" (click)="clearFilters()">Clear filters</button>
          </app-empty-state>
        } @else {
          <app-empty-state icon="events" message="No Kubernetes events recorded yet"
            hint="Warnings and rollout activity (scheduling, image pulls, probes, scaling) show up here as the collectors see them." />
        }
      </div>
    } @else if (rows().length > 0) {
      <div class="k-panel overflow-x-auto scrollbar-thin">
        <table class="k-table">
          <thead>
            <tr>
              <th>Last seen</th>
              <th>Type</th>
              <th>Reason</th>
              <th>Object</th>
              <th>Message</th>
              @if (showCluster()) { <th class="hidden md:table-cell">Cluster</th> }
            </tr>
          </thead>
          <tbody>
            @for (e of rows(); track e.id) {
              <tr class="align-top">
                <td class="px-4 py-2 whitespace-nowrap">
                  <div class="text-xs text-kymo-ink tabular-nums" [title]="'First seen ' + e.first_seen">{{ e.last_seen | date:'MMM d, HH:mm:ss' }}</div>
                  <div class="text-[11px] text-kymo-faint" [title]="'First seen ' + (e.first_seen | date:'MMM d, HH:mm:ss')">{{ e.last_seen | timeago }}</div>
                </td>
                <td class="px-4 py-2 whitespace-nowrap">
                  <span class="inline-flex items-center gap-1 text-[11px] uppercase tracking-wide px-1.5 py-px" [class]="badge[e.type]">
                    @if (e.type === 'Warning') { <span aria-hidden="true">⚠</span> }{{ e.type }}
                  </span>
                </td>
                <td class="px-4 py-2 whitespace-nowrap">
                  <span class="text-xs text-kymo-ink">{{ e.reason }}</span>
                  @if (times(e) > 1) {
                    <span class="text-[11px] text-kymo-amber tabular-nums ml-1.5" [title]="times(e) + ' occurrences'">×{{ times(e) }}</span>
                  }
                </td>
                <td class="px-4 py-2 max-w-[14rem]">
                  @if (e.workload_id) {
                    <a [routerLink]="['/workloads', e.workload_id]" class="block text-xs text-kymo-ink truncate hover:text-kymo-amber hover:underline underline-offset-4" [title]="object(e)">{{ object(e) }}</a>
                  } @else {
                    <span class="block text-xs text-kymo-ink truncate" [title]="object(e)">{{ object(e) }}</span>
                  }
                  <span class="block text-[11px] text-kymo-faint truncate">{{ e.namespace || '—' }}</span>
                </td>
                <td class="px-4 py-2 text-xs text-kymo-muted min-w-[14rem]">
                  <div class="flex items-start gap-2">
                    <span class="flex-1 min-w-0" [class.truncate]="!isOpen(e.id)" [class.break-words]="isOpen(e.id)" [class.whitespace-pre-wrap]="isOpen(e.id)"
                          [class.max-w-[22rem]]="!isOpen(e.id)" [title]="isOpen(e.id) ? '' : e.message">{{ e.message }}</span>
                    @if (e.message.length > 80 || e.message.includes('\n')) {
                      <button type="button" class="flex-none text-[11px] text-kymo-faint hover:text-kymo-amber underline underline-offset-4"
                              [attr.aria-expanded]="isOpen(e.id)" (click)="toggle(e.id)">{{ isOpen(e.id) ? 'less' : 'more' }}</button>
                    }
                  </div>
                </td>
                @if (showCluster()) {
                  <td class="px-4 py-2 text-xs text-kymo-muted hidden md:table-cell truncate max-w-[10rem]">{{ e.source_name || '—' }}</td>
                }
              </tr>
            }
          </tbody>
        </table>
      </div>

      <div class="flex items-center justify-center gap-3 py-2">
        @if (hasMore()) {
          <button type="button" class="k-btn" [disabled]="loadingMore()" (click)="loadOlder()">
            @if (loadingMore()) {
              <span class="w-3 h-3 border border-kymo-rule border-t-kymo-amber rounded-full animate-spin" aria-hidden="true"></span>
              Loading…
            } @else {
              Load older
            }
          </button>
        } @else {
          <span class="text-xs text-kymo-faint">— start of recorded Kubernetes events —</span>
        }
        @if (moreError()) {
          <span class="text-xs text-kymo-danger" role="alert">failed to load older events</span>
        }
      </div>
    }
  `,
  host: { class: 'block space-y-4' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class K8sEventsComponent {
  private svc        = inject(ContainerService);
  private refreshSvc = inject(RefreshService);
  private nsSvc      = inject(NamespaceService);
  private sourceSvc  = inject(SourceStateService);

  readonly types = TYPES;
  readonly badge = TYPE_BADGE;
  readonly object = k8sObject;
  readonly times = occurrences;

  type      = signal<K8sEventType | ''>('Warning');
  namespace = signal('');
  reason    = signal('');
  q         = signal('');
  /** Global cluster scope ('' = all connected). */
  source    = this.sourceSvc.selectedSource;

  loading     = signal(true);
  error       = signal(false);
  loadingMore = signal(false);
  moreError   = signal(false);
  hasMore     = signal(true);
  private loaded   = signal<K8sEvent[]>([]);
  namespaces      = signal<string[]>([]);
  reasons         = signal<K8sReasonCount[]>([]);
  allReasons      = signal(false);
  private expanded = signal<ReadonlySet<number>>(new Set());

  /** Without a cluster selected, rows of removed clusters stay out (same rule as every other page). */
  rows = computed(() => this.sourceSvc.inScope(this.loaded(), e => e.source_name));
  showCluster = computed(() => !this.source());
  hasFilters = computed(() => this.type() !== 'Warning' || !!this.namespace() || !!this.reason() || !!this.q().trim());

  /** Reasons for the active type, most frequent first. */
  private reasonsByType = computed(() => {
    const t = this.type();
    const merged = new Map<string, number>();
    for (const r of this.reasons()) if (!t || r.type === t) merged.set(r.reason, (merged.get(r.reason) ?? 0) + r.count);
    return [...merged].map(([reason, count]) => ({ reason, count })).sort((a, b) => b.count - a.count || a.reason.localeCompare(b.reason));
  });
  reasonChips = computed(() => {
    const all = this.reasonsByType();
    if (this.allReasons()) return all;
    const top = all.slice(0, TOP_REASONS);
    const sel = this.reason();
    // The active reason stays visible even when it is not among the most frequent.
    return sel && !top.some(r => r.reason === sel) ? [...top, ...all.filter(r => r.reason === sel)] : top;
  });
  hiddenReasons = computed(() => Math.max(0, this.reasonsByType().length - TOP_REASONS));

  private search$ = new Subject<string>();
  private sub?: Subscription;
  private moreSub?: Subscription;
  private metaSub?: Subscription;

  constructor() {
    reloadOnSourceAndRefresh({
      load: reason => this.load(reason),
    });
    // Namespaces and reason chips follow the cluster; a reason that does not exist there is dropped.
    effect(() => {
      const src = this.source();
      this.nsSvc.version();
      untracked(() => this.loadMeta(src));
    });
    this.search$.pipe(debounceTime(300), distinctUntilChanged()).subscribe(v => { this.q.set(v); this.reset(); });
    inject(DestroyRef).onDestroy(() => { this.sub?.unsubscribe(); this.moreSub?.unsubscribe(); this.metaSub?.unsubscribe(); });
  }

  private loadMeta(src: string): void {
    this.metaSub?.unsubscribe();
    this.metaSub = this.nsSvc.getNamespaces(src).subscribe(ns => {
      this.namespaces.set(ns ?? []);
      if (this.namespace() && !(ns ?? []).includes(this.namespace())) { this.namespace.set(''); this.reset(); }
    });
    this.svc.getK8sEventReasons(src || undefined).subscribe({ next: r => this.reasons.set(r ?? []), error: () => this.reasons.set([]) });
  }

  setType(t: K8sEventType | ''): void { this.type.set(t); this.reason.set(''); this.reset(); }
  setNamespace(ns: string): void { this.namespace.set(ns); this.reset(); }
  toggleReason(r: string): void { this.reason.set(this.reason() === r ? '' : r); this.reset(); }
  onSearch(v: string): void { this.search$.next(v.trim()); }

  clearFilters(): void {
    this.type.set('Warning'); this.namespace.set(''); this.reason.set(''); this.q.set('');
    this.reset();
  }

  isOpen(id: number): boolean { return this.expanded().has(id); }
  toggle(id: number): void {
    const next = new Set(this.expanded());
    if (!next.delete(id)) next.add(id);
    this.expanded.set(next);
  }

  reset(): void {
    this.loading.set(true);
    this.loaded.set([]);
    this.load('init');
  }

  private query(before?: string) {
    return {
      limit: PAGE_SIZE,
      source: this.source() || undefined,
      namespace: this.namespace() || undefined,
      type: this.type(),
      reason: this.reason() || undefined,
      q: this.q() || undefined,
      before,
    };
  }

  /** Initial / filter loads replace the list; refresh ticks merge the newest page so "Load older" pages survive. */
  load(reason: LoadReason): void {
    const merge = (reason === 'auto' || reason === 'manual') && this.loaded().length > 0;
    this.sub?.unsubscribe();
    if (!merge) this.moreSub?.unsubscribe();
    this.sub = this.svc.queryK8sEvents(this.query()).subscribe({
      next: page => {
        if (merge) {
          const byId = new Map(this.loaded().map(e => [e.id, e]));
          for (const e of page) byId.set(e.id, e);   // aggregated rows update in place (count / last_seen)
          this.loaded.set([...byId.values()].sort(byLastSeen));
        } else {
          this.loaded.set([...page].sort(byLastSeen));
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
    const list = this.loaded();
    if (!list.length || this.loadingMore()) return;
    const oldest = list[list.length - 1];
    this.loadingMore.set(true);
    this.moreError.set(false);
    this.moreSub = this.svc.queryK8sEvents(this.query(oldest.last_seen)).subscribe({
      next: page => {
        const seen = new Set(this.loaded().map(e => e.id));
        this.loaded.set([...this.loaded(), ...page.filter(e => !seen.has(e.id))].sort(byLastSeen));
        this.hasMore.set(page.length >= PAGE_SIZE);
        this.loadingMore.set(false);
      },
      error: () => { this.loadingMore.set(false); this.moreError.set(true); },
    });
  }
}
