import {
  Component, ChangeDetectionStrategy, signal, computed, inject, input, effect, untracked, DestroyRef,
} from '@angular/core';
import { RouterLink } from '@angular/router';
import { DatePipe } from '@angular/common';
import { Subscription } from 'rxjs';

import { ErrorLogService } from '../../../core/services/error-log.service';
import { RefreshService } from '../../../core/services/refresh.service';
import { ToastService } from '../../../core/services/toast.service';
import {
  ErrorOccurrence, ErrorSignature, ErrorWindow, LogCaptureState,
} from '../../../core/models/error-log.model';
import { ErrorSparkComponent } from '../../../shared/components/error-spark/error-spark.component';
import { ErrorTemplateComponent } from '../../../shared/components/error-spark/error-template.component';
import { EmptyStateComponent } from '../../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../../shared/components/error-state/error-state.component';
import { TimeagoPipe } from '../../../shared/pipes/timeago.pipe';
import { CAPTURE_PRIVACY_NOTE, ERROR_WINDOWS, captureLockReason } from '../../../shared/utils/error-logs';
import { AuthService } from '../../../core/services/auth.service';

const PAGE = 50;

interface OccState {
  items: ErrorOccurrence[];
  loading: boolean;
  error: boolean;
  more: boolean;
}

/** Workload detail → Errors tab: capture toggle, window selector and the list of error signatures. */
@Component({
  selector: 'app-workload-errors',
  standalone: true,
  imports: [
    RouterLink, DatePipe, TimeagoPipe, ErrorSparkComponent, ErrorTemplateComponent, EmptyStateComponent,
    ErrorStateComponent,
  ],
  template: `
    <section class="space-y-3" aria-labelledby="wl-err-title">
      <!-- Header: capture toggle + window selector -->
      <div class="flex items-center justify-between gap-3 flex-wrap">
        <div class="flex items-center gap-3 flex-wrap min-w-0">
          <h2 id="wl-err-title" class="k-label">Error logs</h2>
          @if (capture(); as c) {
            <button type="button" role="switch" [attr.aria-checked]="c.enabled" aria-label="Capture error logs"
              class="relative w-8 h-4 border transition-colors disabled:opacity-40 disabled:cursor-not-allowed flex-none"
              [class]="c.enabled ? 'border-kymo-amber bg-kymo-amber/20' : 'border-kymo-rule bg-kymo-deep'"
              [disabled]="toggleDisabled()" [title]="auth.denyHint('logcapture:manage') || lockReason() || disabledReason()" (click)="toggle()">
              <span class="absolute top-0.5 w-2.5 h-2.5 transition-all" [class]="c.enabled ? 'left-[1.1rem] bg-kymo-amber' : 'left-0.5 bg-kymo-faint'"></span>
            </button>
            <span class="text-xs text-kymo-ink">Capture error logs</span>
            @if (lockReason(); as r) {
              <span class="text-[11px] text-kymo-muted">{{ r }}</span>
            } @else if (disabledReason(); as r) {
              <span class="text-[11px] text-kymo-muted">{{ r }}</span>
            }
            @if (c.enabled && c.global_enabled) {
              <span class="text-[11px] text-kymo-muted tabular-nums" aria-live="polite">streaming {{ c.streams }} container{{ c.streams === 1 ? '' : 's' }}</span>
            }
          } @else if (captureError()) {
            <span class="text-[11px] text-kymo-danger">capture state unavailable</span>
          }
        </div>
        <div class="flex border border-kymo-rule" role="group" aria-label="Time window">
          @for (w of windows; track w) {
            <button type="button"
              class="px-3 h-7 text-xs transition-colors border-r border-kymo-rule last:border-r-0"
              [class]="window() === w ? 'bg-kymo-amber text-kymo-deep font-medium' : 'text-kymo-muted hover:text-kymo-ink hover:bg-kymo-raised'"
              [attr.aria-pressed]="window() === w" (click)="setWindow(w)">{{ w }}</button>
          }
        </div>
      </div>

      @if (capture() && !capture()!.global_enabled) {
        <p class="text-xs text-kymo-muted border border-kymo-rule bg-kymo-deep px-3 py-2" role="note">
          Error-log capture is switched off globally (<span class="font-mono">log_capture.enabled: false</span> in config.yaml). Nothing is streamed.
        </p>
      }

      @if (loadError()) {
        <app-error-state message="Failed to load error logs" (retry)="load(false)" />
      } @else if (!loaded()) {
        <div class="h-40 bg-kymo-panel border border-kymo-rule animate-pulse" aria-busy="true"></div>
      } @else if (sigs().length === 0) {
        <div class="k-panel">
          @if (!capture()?.enabled) {
            <app-empty-state icon="events" message="Error capture is off"
              hint="Turn on capture to start recording error lines of this workload's running containers. Only error-level lines are kept; secrets are masked (best effort)." />
          } @else {
            <app-empty-state icon="check" tone="success" message="No errors in the last {{ window() }}"
              hint="Capture is on and streaming. Error lines will show up here, grouped by message." />
          }
        </div>
      } @else {
        <div class="k-panel overflow-hidden">
          <ul class="divide-y divide-kymo-rule">
            @for (s of sigs(); track s.id) {
              <li>
                <div class="px-4 py-3 flex items-start gap-3 hover:bg-kymo-raised/60 transition-colors cursor-pointer outline-none focus-visible:bg-kymo-raised"
                     role="button" tabindex="0" [attr.aria-expanded]="expanded() === s.id" [attr.aria-controls]="'err-occ-' + s.id"
                     (click)="toggleRow(s)" (keydown.enter)="toggleRow(s)" (keydown.space)="toggleRow(s); $event.preventDefault()">
                  <span class="text-kymo-faint text-xs mt-0.5 w-3 flex-none" aria-hidden="true">{{ expanded() === s.id ? '▾' : '▸' }}</span>
                  <div class="flex-1 min-w-0">
                    <div class="flex items-center gap-2 flex-wrap">
                      <span class="inline-flex items-center h-5 px-1.5 text-[10px] uppercase tracking-[0.06em] border flex-none"
                            [class]="s.level === 'fatal' ? 'border-kymo-danger/60 text-kymo-danger' : 'border-kymo-amber/50 text-kymo-amber'">{{ s.level }}</span>
                      @if (s.is_new) {
                        <span class="inline-flex items-center h-5 px-1.5 text-[10px] tracking-[0.06em] bg-kymo-amber text-kymo-deep font-medium flex-none">NEW</span>
                      }
                    </div>
                    <p class="text-xs text-kymo-ink mt-1.5 break-words"><app-error-template [template]="s.template" /></p>
                    <p class="text-[11px] text-kymo-faint mt-1">
                      {{ s.pods }} pod{{ s.pods === 1 ? '' : 's' }} · first <span [title]="s.first_seen">{{ s.first_seen | timeago }}</span> · last <span [title]="s.last_seen">{{ s.last_seen | timeago }}</span>
                    </p>
                  </div>
                  <app-error-spark class="hidden sm:inline-block mt-0.5" [buckets]="s.buckets" />
                  <div class="text-right flex-none w-20 tabular-nums">
                    <p class="text-sm font-medium text-kymo-ink">{{ s.count }}<span class="text-[11px] font-normal text-kymo-faint"> in {{ window() }}</span></p>
                    <p class="text-[11px] text-kymo-faint">{{ s.total_count }} total</p>
                  </div>
                </div>

                @if (expanded() === s.id) {
                  <div [id]="'err-occ-' + s.id" class="border-t border-kymo-rule bg-kymo-deep px-4 py-3 space-y-3">
                    @let o = occ()[s.id];
                    @if (!o || (o.loading && o.items.length === 0)) {
                      <div class="h-16 bg-kymo-panel animate-pulse" aria-busy="true"></div>
                    } @else if (o.error && o.items.length === 0) {
                      <p class="text-xs text-kymo-danger">Failed to load occurrences. <button type="button" class="underline underline-offset-4" (click)="loadOcc(s, true)">Retry</button></p>
                    } @else if (o.items.length === 0) {
                      <p class="text-xs text-kymo-faint">No stored occurrences (only the newest ones are kept).</p>
                    } @else {
                      @for (x of o.items; track x.time + x.container_id + $index) {
                        <div>
                          <div class="flex items-center gap-x-3 gap-y-1 flex-wrap text-[11px]">
                            <span class="text-kymo-ink tabular-nums" [title]="x.time">{{ x.time | date:'MMM d, HH:mm:ss' }}</span>
                            <span class="text-kymo-faint">{{ x.time | timeago }}</span>
                            <a [routerLink]="['/containers', x.container_id]" class="text-kymo-amber hover:underline underline-offset-4 truncate max-w-[24rem]"
                               [title]="'Open container ' + x.container + ' in ' + x.pod">{{ x.pod }} / {{ x.container }}</a>
                            <button type="button" class="k-btn k-btn-sm ml-auto" (click)="copy(x.message)" aria-label="Copy message">Copy</button>
                          </div>
                          <pre class="mt-1.5 max-h-56 overflow-auto scrollbar-thin bg-kymo-panel border border-kymo-rule px-3 py-2 text-[11px] leading-relaxed text-kymo-ink whitespace-pre-wrap break-words font-mono">{{ x.message }}</pre>
                        </div>
                      }
                      @if (o.more) {
                        <div class="flex justify-center">
                          <button type="button" class="k-btn k-btn-sm" [disabled]="o.loading" (click)="loadOcc(s, false)">{{ o.loading ? 'Loading…' : 'Load more' }}</button>
                        </div>
                      }
                    }
                  </div>
                }
              </li>
            }
          </ul>
        </div>
        <p class="text-[11px] text-kymo-faint">{{ privacy }} Only the newest occurrences per message are stored; counts stay exact.</p>
      }
    </section>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class WorkloadErrorsComponent {
  readonly auth = inject(AuthService);
  private api   = inject(ErrorLogService);
  private toast = inject(ToastService);

  workloadId = input.required<string>();
  namespace  = input<string>('');
  /** Recorded history of a removed cluster: capture cannot be changed. */
  historyOnly = input(false);

  readonly windows = ERROR_WINDOWS;
  readonly privacy = CAPTURE_PRIVACY_NOTE;

  window    = signal<ErrorWindow>('24h');
  sigs      = signal<ErrorSignature[]>([]);
  loaded    = signal(false);
  loadError = signal(false);
  capture   = signal<LogCaptureState | null>(null);
  captureError = signal(false);
  capBusy   = signal(false);
  expanded  = signal<string | null>(null);
  occ       = signal<Record<string, OccState>>({});

  lockReason = computed(() => captureLockReason(this.capture(), this.namespace()));
  disabledReason = computed(() => (this.historyOnly() ? 'read-only (removed cluster)' : ''));
  toggleDisabled = computed(() => !this.auth.can('logcapture:manage') || this.capBusy() || this.historyOnly() || !!this.lockReason() || !this.capture()?.global_enabled);

  private sigSub?: Subscription;
  private seq = 0;

  constructor() {
    effect(() => {
      this.workloadId();
      untracked(() => {
        this.expanded.set(null);
        this.occ.set({});
        this.sigs.set([]);
        this.capture.set(null);
        this.loaded.set(false);
        this.load(false);
      });
    });
    const sub = inject(RefreshService).refresh$.subscribe(() => this.load(true));
    inject(DestroyRef).onDestroy(() => { sub.unsubscribe(); this.sigSub?.unsubscribe(); });
  }

  load(silent: boolean): void {
    const id = this.workloadId();
    if (!id) return;
    const seq = ++this.seq;
    if (!silent) { this.loaded.set(false); this.loadError.set(false); }
    this.sigSub?.unsubscribe();
    this.sigSub = this.api.forWorkload(id, this.window()).subscribe({
      next: list => {
        if (seq !== this.seq) return;
        this.sigs.set(list);
        this.loadError.set(false);
        this.loaded.set(true);
      },
      error: () => {
        if (seq !== this.seq) return;
        if (!silent || !this.loaded()) this.loadError.set(true);
        this.loaded.set(true);
      },
    });
    this.api.captureState(id).subscribe({
      next: s => { if (seq === this.seq) { this.capture.set(s); this.captureError.set(false); } },
      error: () => { if (seq === this.seq && !this.capture()) this.captureError.set(true); },
    });
  }

  setWindow(w: ErrorWindow): void {
    if (w === this.window()) return;
    this.window.set(w);
    this.load(false);
  }

  toggle(): void {
    const c = this.capture();
    if (!c || this.toggleDisabled()) return;
    this.capBusy.set(true);
    this.api.setCapture(this.workloadId(), !c.enabled).subscribe({
      next: s => {
        this.capBusy.set(false);
        this.capture.set(s);
        this.toast.success(s.enabled ? 'Error-log capture enabled' : 'Error-log capture disabled');
      },
      error: err => {
        this.capBusy.set(false);
        this.toast.apiError(err, 'Failed to change error-log capture');
        this.load(true);
      },
    });
  }

  toggleRow(s: ErrorSignature): void {
    if (this.expanded() === s.id) { this.expanded.set(null); return; }
    this.expanded.set(s.id);
    if (!this.occ()[s.id]) this.loadOcc(s, true);
  }

  loadOcc(s: ErrorSignature, reset: boolean): void {
    const cur = reset ? undefined : this.occ()[s.id];
    const before = cur?.items.at(-1)?.time;
    this.setOcc(s.id, { items: cur?.items ?? [], loading: true, error: false, more: false });
    this.api.occurrences(s.id, PAGE, before).subscribe({
      next: list => {
        const seen = new Set((cur?.items ?? []).map(i => i.time + i.container_id));
        const fresh = list.filter(i => !seen.has(i.time + i.container_id));
        this.setOcc(s.id, { items: [...(cur?.items ?? []), ...fresh], loading: false, error: false, more: list.length >= PAGE && fresh.length > 0 });
      },
      error: () => this.setOcc(s.id, { items: cur?.items ?? [], loading: false, error: true, more: false }),
    });
  }

  private setOcc(id: string, st: OccState): void {
    this.occ.update(m => ({ ...m, [id]: st }));
  }

  copy(text: string): void {
    navigator.clipboard?.writeText(text).then(
      () => this.toast.success('Copied'),
      () => this.toast.error('Copy failed'),
    );
  }
}
