import { Component, ChangeDetectionStrategy, signal, computed, inject } from '@angular/core';
import { FormsModule } from '@angular/forms';
import { forkJoin, of } from 'rxjs';
import { catchError } from 'rxjs/operators';

import { ErrorLogService } from '../../../core/services/error-log.service';
import { NamespaceService } from '../../../core/services/namespace.service';
import { SourceStateService } from '../../../core/services/source-state.service';
import { WorkloadStateService } from '../../../core/services/workload-state.service';
import { ToastService } from '../../../core/services/toast.service';
import { RefreshService } from '../../../core/services/refresh.service';
import { LogCaptureStatus, LogCaptureTarget } from '../../../core/models/error-log.model';
import { reloadOnSourceAndRefresh } from '../../../core/utils/page-reload';
import { PageHeaderComponent } from '../../../shared/components/page-header/page-header.component';
import { EmptyStateComponent } from '../../../shared/components/empty-state/empty-state.component';
import { ErrorStateComponent } from '../../../shared/components/error-state/error-state.component';
import { SkeletonComponent } from '../../../shared/components/skeleton/skeleton.component';
import { ConfirmDialogComponent } from '../../../shared/components/confirm-dialog/confirm-dialog.component';
import { TimeagoPipe } from '../../../shared/pipes/timeago.pipe';
import { CONFIG_ORIGIN_HINT } from '../../../shared/utils/source';
import { CAPTURE_PRIVACY_NOTE } from '../../../shared/utils/error-logs';
import { SettingsNavComponent } from '../settings-nav.component';

/** Settings → Log capture: global status, which namespaces / workloads are recorded, and the privacy note. */
@Component({
  selector: 'app-log-capture',
  standalone: true,
  imports: [
    FormsModule, TimeagoPipe, PageHeaderComponent, EmptyStateComponent, ErrorStateComponent, SkeletonComponent,
    ConfirmDialogComponent, SettingsNavComponent,
  ],
  template: `
<div class="space-y-4">
  <app-settings-nav />
  <app-page-header title="Log capture">
    <ng-container subtitle>
      @if (!loaded()) { Loading… }
      @else { Opt-in recording of error-level log lines, grouped into signatures }
    </ng-container>
  </app-page-header>

  <p class="text-xs text-kymo-ink border border-kymo-rule bg-kymo-deep px-3 py-2.5" role="note">
    <strong class="font-medium text-kymo-amber">Nothing is captured unless you opt in.</strong>
    For every running container of an opted-in namespace or workload, KYMO follows the log stream. {{ privacy }}
    Stack traces are kept with their error line; a few masked samples per message are stored, counts stay exact.
  </p>

  @if (error()) {
    <app-error-state message="Failed to load log capture settings" (retry)="load()" />
  }

  @if (!loaded()) {
    <app-skeleton [rows]="3" />
  } @else {
    <!-- Global status -->
    @if (status(); as st) {
      <section class="space-y-2" aria-labelledby="lc-status-title">
        <h2 id="lc-status-title" class="k-label !text-kymo-ink flex items-center gap-2">
          Status
          <span class="inline-flex items-center gap-1.5 normal-case tracking-normal text-xs" [class]="st.enabled ? 'text-kymo-running' : 'text-kymo-muted'">
            <span class="k-led" [class]="st.enabled ? 'bg-kymo-running' : 'bg-kymo-unknown'" aria-hidden="true"></span>{{ st.enabled ? 'enabled' : 'disabled globally' }}
          </span>
        </h2>
        @if (!st.enabled) {
          <p class="text-xs text-kymo-muted">Capture is switched off by <span class="font-mono">log_capture.enabled: false</span> in config.yaml. No logs are streamed whatever the targets say.</p>
        }
        @if (st.sources.length) {
          <div class="k-panel overflow-x-auto scrollbar-thin">
            <table class="k-table">
              <thead>
                <tr>
                  <th>Cluster</th>
                  <th class="!text-right">Streams</th>
                  <th class="!text-right">Dropped lines</th>
                  <th>Last error</th>
                </tr>
              </thead>
              <tbody>
                @for (s of st.sources; track s.source_name) {
                  <tr>
                    <td class="px-4 py-2 text-xs text-kymo-ink">{{ s.source_name }}</td>
                    <td class="px-4 py-2 text-xs text-right tabular-nums" [class]="s.max_streams > 0 && s.streams >= s.max_streams ? 'text-kymo-amber' : 'text-kymo-ink'"
                        [title]="s.streams >= s.max_streams && s.max_streams > 0 ? 'Stream limit reached; further containers are not followed' : ''">{{ s.streams }} / {{ s.max_streams }}</td>
                    <td class="px-4 py-2 text-xs text-right tabular-nums" [class]="s.dropped_lines ? 'text-kymo-amber' : 'text-kymo-faint'"
                        title="Lines over the per-container rate limit">{{ s.dropped_lines }}</td>
                    <td class="px-4 py-2 text-xs max-w-[24rem]"><span class="block truncate" [class]="s.last_error ? 'text-kymo-danger' : 'text-kymo-faint'" [title]="s.last_error">{{ s.last_error || '—' }}</span></td>
                  </tr>
                }
              </tbody>
            </table>
          </div>
        } @else {
          <p class="text-xs text-kymo-faint">No cluster is streaming yet.</p>
        }
      </section>
    }

    <!-- Targets -->
    <section class="space-y-2 pt-2" aria-labelledby="lc-targets-title">
      <h2 id="lc-targets-title" class="k-label !text-kymo-ink">Targets</h2>
      @if (targets().length === 0) {
        <div class="k-panel">
          <app-empty-state icon="events" [compact]="true" message="No capture targets"
            hint="Add a namespace or workload below, set the pod annotation kymo.io/capture-error-logs: &quot;true&quot;, or turn capture on from a workload's Errors tab." />
        </div>
      } @else {
        <div class="k-panel overflow-x-auto scrollbar-thin">
          <table class="k-table">
            <thead>
              <tr>
                <th>Scope</th>
                <th>Cluster</th>
                <th class="hidden sm:table-cell">Added</th>
                <th>Origin</th>
                <th><span class="sr-only">Actions</span></th>
              </tr>
            </thead>
            <tbody>
              @for (t of sortedTargets(); track t.id) {
                <tr [class.opacity-60]="busyId() === t.id">
                  <td class="px-4 py-2.5 text-xs">
                    @if (t.workload_id) {
                      <span class="inline-flex items-center h-5 px-1.5 text-[10px] tracking-[0.06em] border border-kymo-rule text-kymo-muted mr-2">workload</span>
                      <span class="text-kymo-ink" [title]="t.workload_id">{{ workloadLabel(t) }}</span>
                    } @else {
                      <span class="inline-flex items-center h-5 px-1.5 text-[10px] tracking-[0.06em] border border-kymo-rule text-kymo-muted mr-2">namespace</span>
                      <span class="text-kymo-ink">{{ t.namespace }}</span>
                    }
                    @if (t.workload_id && t.namespace) { <span class="text-kymo-faint"> · {{ t.namespace }}</span> }
                  </td>
                  <td class="px-4 py-2.5 text-xs text-kymo-muted">{{ t.source_name }}</td>
                  <td class="px-4 py-2.5 text-xs text-kymo-muted whitespace-nowrap hidden sm:table-cell" [title]="t.created_at">{{ t.created_at | timeago }}</td>
                  <td class="px-4 py-2.5">
                    @if (t.origin === 'config') {
                      <span class="inline-flex items-center gap-1 h-5 px-1.5 text-[10px] tracking-[0.06em] border border-kymo-rule text-kymo-muted cursor-help" tabindex="0" [title]="configHint" [attr.aria-label]="'config.yaml. ' + configHint">
                        <svg class="w-2.5 h-2.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2.5" aria-hidden="true"><rect x="5" y="11" width="14" height="10" /><path stroke-linecap="square" d="M8 11V7a4 4 0 018 0v4" /></svg>
                        config
                      </span>
                    } @else {
                      <span class="text-xs text-kymo-muted">ui</span>
                    }
                  </td>
                  <td class="px-4 py-2.5">
                    <div class="flex justify-end">
                      @if (t.origin === 'ui') {
                        <button type="button" class="k-btn k-btn-sm k-btn-icon text-kymo-faint hover:!text-kymo-danger hover:!border-kymo-danger/50"
                          [disabled]="busyId() === t.id" aria-label="Delete target" title="Delete target" (click)="deleteTarget.set(t)">
                          <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5" aria-hidden="true">
                            <path stroke-linecap="round" stroke-linejoin="round" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16" />
                          </svg>
                        </button>
                      }
                    </div>
                  </td>
                </tr>
              }
            </tbody>
          </table>
        </div>
      }

      <!-- Add target -->
      <form class="k-panel p-4 flex items-end gap-3 flex-wrap" (ngSubmit)="add()" aria-label="Add capture target">
        <label class="flex flex-col gap-1">
          <span class="k-label">Cluster</span>
          <select class="k-input" name="source" [ngModel]="formSource()" (ngModelChange)="onSource($event)" required>
            <option value="" disabled>Select…</option>
            @for (s of clusters(); track s.name) { <option [value]="s.name">{{ s.name }}</option> }
          </select>
        </label>
        <label class="flex flex-col gap-1">
          <span class="k-label">Namespace</span>
          <input class="k-input w-48" name="ns" list="lc-namespaces" autocomplete="off" placeholder="e.g. shop" required
                 [ngModel]="formNs()" (ngModelChange)="formNs.set($event); formWorkload.set('')" />
          <datalist id="lc-namespaces">
            @for (n of namespaces(); track n) { <option [value]="n"></option> }
          </datalist>
        </label>
        <label class="flex flex-col gap-1">
          <span class="k-label">Workload <span class="normal-case tracking-normal text-kymo-faint">· optional</span></span>
          <select class="k-input w-56" name="wl" [ngModel]="formWorkload()" (ngModelChange)="formWorkload.set($event)">
            <option value="">Whole namespace</option>
            @for (w of workloadOptions(); track w.id) { <option [value]="w.id">{{ w.kind }}/{{ w.name }}</option> }
          </select>
        </label>
        <button type="submit" class="k-btn k-btn-primary" [disabled]="!canAdd() || adding()">{{ adding() ? 'Adding…' : 'Add target' }}</button>
      </form>
    </section>
  }
</div>

@if (deleteTarget(); as t) {
  <app-confirm-dialog
    title="Delete capture target?"
    [message]="'KYMO stops recording error logs for ' + (t.workload_id ? workloadLabel(t) : 'namespace ' + t.namespace) + ' on ' + t.source_name + ' (unless another target or annotation still covers it). Already captured errors are kept until retention expires.'"
    confirmLabel="Delete target"
    (confirmed)="confirmDelete()"
    (cancelled)="deleteTarget.set(null)"
  />
}
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class LogCaptureComponent {
  private api       = inject(ErrorLogService);
  private nsSvc     = inject(NamespaceService);
  private sourceSvc = inject(SourceStateService);
  private wlState   = inject(WorkloadStateService);
  private toast     = inject(ToastService);
  private refresh   = inject(RefreshService);

  readonly privacy    = CAPTURE_PRIVACY_NOTE;
  readonly configHint = CONFIG_ORIGIN_HINT;

  status  = signal<LogCaptureStatus | null>(null);
  targets = signal<LogCaptureTarget[]>([]);
  loaded  = signal(false);
  error   = signal(false);
  busyId  = signal<number | null>(null);
  adding  = signal(false);
  deleteTarget = signal<LogCaptureTarget | null>(null);

  formSource   = signal('');
  formNs       = signal('');
  formWorkload = signal('');
  namespaces   = signal<string[]>([]);

  /** Registered clusters; capture needs a live Kubernetes cluster. */
  clusters = computed(() => this.sourceSvc.sources());

  sortedTargets = computed(() =>
    [...this.targets()].sort((a, b) =>
      (a.origin === b.origin ? 0 : a.origin === 'config' ? -1 : 1)
      || a.source_name.localeCompare(b.source_name) || a.namespace.localeCompare(b.namespace)
      || a.workload_id.localeCompare(b.workload_id)));

  private workloadIndex = computed(() => new Map(this.wlState.workloads().map(w => [w.id, w])));

  workloadOptions = computed(() => {
    const src = this.formSource(), ns = this.formNs().trim();
    if (!src || !ns) return [];
    return this.wlState.workloads().filter(w => w.source_name === src && w.namespace === ns && !w.is_system)
      .sort((a, b) => a.kind.localeCompare(b.kind) || a.name.localeCompare(b.name));
  });

  canAdd = computed(() => !!this.formSource() && !!this.formNs().trim());

  constructor() {
    reloadOnSourceAndRefresh({ ignoreSource: true, load: () => this.load() });
  }

  workloadLabel(t: LogCaptureTarget): string {
    const w = this.workloadIndex().get(t.workload_id);
    return w ? `${w.kind}/${w.name}` : t.workload_id;
  }

  load(): void {
    forkJoin({
      status: this.api.status().pipe(catchError(() => of(null))),
      targets: this.api.targets().pipe(catchError(() => of(null))),
    }).subscribe(({ status, targets }) => {
      if (status) this.status.set(status);
      if (targets) this.targets.set(targets);
      this.error.set(status === null && targets === null);
      this.loaded.set(true);
      this.refresh.markUpdated();
    });
  }

  onSource(name: string): void {
    this.formSource.set(name);
    this.formWorkload.set('');
    this.namespaces.set([]);
    if (name) this.nsSvc.getNamespaces(name).subscribe(ns => this.namespaces.set(ns ?? []));
  }

  add(): void {
    if (!this.canAdd() || this.adding()) return;
    this.adding.set(true);
    const workload = this.formWorkload();
    this.api.addTarget({
      source_name: this.formSource(), namespace: this.formNs().trim(),
      ...(workload ? { workload_id: workload } : {}),
    }).subscribe({
      next: () => {
        this.adding.set(false);
        this.toast.success('Capture target added');
        this.formNs.set(''); this.formWorkload.set('');
        this.load();
      },
      error: err => {
        this.adding.set(false);
        if (err?.status === 409) this.toast.error('That target already exists');
        else this.toast.apiError(err, 'Failed to add target');
      },
    });
  }

  confirmDelete(): void {
    const t = this.deleteTarget();
    if (!t) return;
    this.deleteTarget.set(null);
    this.busyId.set(t.id);
    this.api.deleteTarget(t.id).subscribe({
      next: () => { this.busyId.set(null); this.toast.success('Capture target deleted'); this.load(); },
      error: err => { this.busyId.set(null); this.toast.apiError(err, 'Failed to delete target'); },
    });
  }
}
