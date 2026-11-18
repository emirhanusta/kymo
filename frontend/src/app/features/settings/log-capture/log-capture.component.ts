import { Component, ChangeDetectionStrategy, signal, computed, inject } from '@angular/core';
import { forkJoin, of } from 'rxjs';
import { catchError } from 'rxjs/operators';

import { ErrorLogService } from '../../../core/services/error-log.service';
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
import { AuthService } from '../../../core/services/auth.service';
import { AddTargetDialogComponent } from './add-target-dialog.component';

/** Settings → Log capture: which namespaces / workloads are recorded, and the capture status per cluster. */
@Component({
  selector: 'app-log-capture',
  standalone: true,
  imports: [
    TimeagoPipe, PageHeaderComponent, EmptyStateComponent, ErrorStateComponent, SkeletonComponent,
    ConfirmDialogComponent, AddTargetDialogComponent,
  ],
  template: `
<div class="space-y-4">
  <app-page-header title="Log capture">
    <ng-container subtitle>
      @if (!loaded()) { Loading… }
      @else if (targets().length) { Recording error-level log lines from {{ targets().length }} target{{ targets().length === 1 ? '' : 's' }} }
      @else { Opt-in recording of error-level log lines, grouped into signatures }
    </ng-container>
    <button type="button" class="k-btn k-btn-primary" [disabled]="!auth.can('logcapture:manage')" [title]="auth.denyHint('logcapture:manage') || 'Add a capture target'" (click)="adding.set(true)">
      <svg class="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2.5" aria-hidden="true"><path stroke-linecap="square" d="M12 5v14M5 12h14" /></svg>
      Add target
    </button>
  </app-page-header>

  <div class="text-xs text-kymo-muted">
    <button type="button" class="inline-flex items-center gap-1.5 text-left hover:text-kymo-ink" [attr.aria-expanded]="hintOpen()" aria-controls="lc-privacy" (click)="hintOpen.set(!hintOpen())"
            [title]="fullNote">
      <svg class="w-3.5 h-3.5 flex-none" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5" aria-hidden="true"><circle cx="12" cy="12" r="9" /><path stroke-linecap="square" d="M12 11v5M12 8v.01" /></svg>
      Nothing is captured unless you opt in. {{ privacy }}
    </button>
    @if (hintOpen()) {
      <p id="lc-privacy" class="mt-1.5 ml-5 text-kymo-faint max-w-2xl">{{ fullNote }}</p>
    }
  </div>

  @if (error()) {
    <app-error-state message="Failed to load log capture settings" (retry)="load()" />
  }

  @if (!loaded()) {
    <app-skeleton [rows]="3" />
  } @else {
    @if (status(); as st) {
      @if (!st.enabled) {
        <p class="text-xs text-kymo-muted flex items-center gap-2">
          <span class="k-led bg-kymo-unknown" aria-hidden="true"></span>
          Capture is switched off by <span class="font-mono">log_capture.enabled: false</span> in config.yaml. No logs are streamed whatever the targets say.
        </p>
      } @else if (st.sources.length) {
        <ul class="space-y-1" aria-label="Capture status per cluster">
          @for (s of st.sources; track s.source_name) {
            <li class="flex items-center gap-2 text-xs text-kymo-muted min-w-0">
              <span class="k-led" [class]="s.last_error ? 'bg-kymo-danger' : 'bg-kymo-running'" aria-hidden="true"></span>
              <span class="text-kymo-ink">{{ s.source_name }}</span>
              <span class="tabular-nums" [class.text-kymo-amber]="s.max_streams > 0 && s.streams >= s.max_streams"
                    [title]="s.max_streams > 0 && s.streams >= s.max_streams ? 'Stream limit reached; further containers are not followed' : 'Containers followed / limit'">· {{ s.streams }}/{{ s.max_streams }} streams</span>
              <span class="tabular-nums" [class.text-kymo-amber]="s.dropped_lines > 0" title="Lines over the per-container rate limit">· {{ s.dropped_lines }} dropped</span>
              @if (s.last_error) { <span class="text-kymo-danger truncate" [title]="s.last_error">· {{ s.last_error }}</span> }
            </li>
          }
        </ul>
      }
    }

    @if (targets().length === 0) {
      <div class="k-panel">
        <app-empty-state icon="events" message="No capture targets"
          hint="Add a namespace or workload to record its error logs. You can also annotate pods with kymo.io/capture-error-logs: &quot;true&quot; or turn capture on from a workload's Errors tab.">
          <button type="button" class="k-btn k-btn-primary k-btn-sm mt-4" [disabled]="!auth.can('logcapture:manage')" [title]="auth.denyHint('logcapture:manage')" (click)="adding.set(true)">Add target</button>
        </app-empty-state>
      </div>
    } @else {
      <div class="k-panel overflow-x-auto scrollbar-thin">
        <table class="k-table">
          <thead>
            <tr>
              <th>Scope</th>
              <th>Cluster</th>
              <th class="hidden sm:table-cell">Origin</th>
              <th class="hidden md:table-cell">Added</th>
              <th class="w-10"><span class="sr-only">Actions</span></th>
            </tr>
          </thead>
          <tbody>
            @for (t of sortedTargets(); track t.id) {
              <tr [class.opacity-60]="busyId() === t.id">
                <td class="px-4 py-2.5 text-xs max-w-[22rem]">
                  <div class="flex items-center gap-2 min-w-0">
                    <span class="text-kymo-ink truncate" [title]="t.workload_id ? t.workload_id : 'Whole namespace'">{{ t.workload_id ? workloadLabel(t) : t.namespace }}</span>
                    @if (t.workload_id && t.namespace) { <span class="text-kymo-faint truncate">· {{ t.namespace }}</span> }
                    @if (!t.workload_id) { <span class="text-kymo-faint flex-none">namespace</span> }
                  </div>
                </td>
                <td class="px-4 py-2.5 text-xs text-kymo-muted whitespace-nowrap">{{ t.source_name }}</td>
                <td class="px-4 py-2.5 hidden sm:table-cell">
                  @if (t.origin === 'config') {
                    <span class="inline-flex items-center h-4 px-1 text-[10px] border border-kymo-rule text-kymo-muted cursor-help" tabindex="0" [title]="configHint" [attr.aria-label]="'config.yaml. ' + configHint">config.yaml</span>
                  } @else {
                    <span class="inline-flex items-center h-4 px-1 text-[10px] border border-kymo-rule text-kymo-muted">UI</span>
                  }
                </td>
                <td class="px-4 py-2.5 text-xs text-kymo-muted whitespace-nowrap hidden md:table-cell" [title]="t.created_at">{{ t.created_at | timeago }}</td>
                <td class="px-2 py-1.5 text-right">
                  @if (t.origin === 'ui') {
                    <button type="button" class="k-btn k-btn-sm k-btn-icon border-transparent text-kymo-faint hover:!text-kymo-danger hover:!border-kymo-danger/50"
                      [disabled]="busyId() === t.id || !auth.can('logcapture:manage')" aria-label="Delete target" [title]="auth.denyHint('logcapture:manage') || 'Delete target'" (click)="deleteTarget.set(t)">
                      <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5" aria-hidden="true">
                        <path stroke-linecap="round" stroke-linejoin="round" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16" />
                      </svg>
                    </button>
                  }
                </td>
              </tr>
            }
          </tbody>
        </table>
      </div>
    }
  }
</div>

@if (adding()) {
  <app-add-target-dialog (closed)="adding.set(false)" (saved)="adding.set(false); load()" />
}
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
  readonly auth = inject(AuthService);
  private api     = inject(ErrorLogService);
  private wlState = inject(WorkloadStateService);
  private toast   = inject(ToastService);
  private refresh = inject(RefreshService);

  readonly privacy    = CAPTURE_PRIVACY_NOTE;
  readonly configHint = CONFIG_ORIGIN_HINT;
  readonly fullNote   = `For every running container of an opted-in namespace or workload, KYMO follows the log stream. ${CAPTURE_PRIVACY_NOTE} Stack traces are kept with their error line; a few masked samples per message are stored, counts stay exact.`;

  status  = signal<LogCaptureStatus | null>(null);
  targets = signal<LogCaptureTarget[]>([]);
  loaded  = signal(false);
  error   = signal(false);
  busyId  = signal<number | null>(null);
  adding  = signal(false);
  hintOpen = signal(false);
  deleteTarget = signal<LogCaptureTarget | null>(null);

  sortedTargets = computed(() =>
    [...this.targets()].sort((a, b) =>
      (a.origin === b.origin ? 0 : a.origin === 'config' ? -1 : 1)
      || a.source_name.localeCompare(b.source_name) || a.namespace.localeCompare(b.namespace)
      || a.workload_id.localeCompare(b.workload_id)));

  private workloadIndex = computed(() => new Map(this.wlState.workloads().map(w => [w.id, w])));

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
