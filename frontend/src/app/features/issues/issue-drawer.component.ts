import { Component, ChangeDetectionStrategy, input, output, computed } from '@angular/core';
import { RouterLink } from '@angular/router';
import { DatePipe } from '@angular/common';

import { Issue } from '../../core/models/issue.model';
import { DialogDirective } from '../../shared/directives/dialog.directive';
import { WorkloadBadgeComponent } from '../../shared/components/workload-badge/workload-badge.component';
import { TimeagoPipe } from '../../shared/pipes/timeago.pipe';
import { formatSpan } from '../../shared/utils/format';
import { issueTypeLabel, issueWorkloadQuery, severityLedClass, severityTextClass } from '../../shared/utils/issues';

/** Right-hand details drawer of one issue (same shell as the workload peek). Esc / outside click closes it. */
@Component({
  selector: 'app-issue-drawer',
  standalone: true,
  imports: [RouterLink, DatePipe, DialogDirective, WorkloadBadgeComponent, TimeagoPipe],
  template: `
    <div class="fixed inset-0 z-[39] lg:bg-transparent bg-kymo-deep/70" (click)="closed.emit()" aria-hidden="true"></div>
    <aside appDialog [modal]="false" (dismiss)="closed.emit()" aria-labelledby="issue-title"
           class="fixed top-0 right-0 bottom-0 sm:bottom-[34px] w-full sm:w-[26.25rem] z-40 bg-kymo-bg border-l border-kymo-rule border-t-2 flex flex-col shadow-[-8px_0_24px_rgba(0,0,0,0.35)]"
           [class]="issue().severity === 'critical' ? 'border-t-kymo-danger' : 'border-t-kymo-amber'">
      <div class="flex items-start gap-3 px-4 py-3 border-b border-kymo-rule flex-none">
        <div class="min-w-0 flex-1">
          <p class="k-label mb-0.5">Issue</p>
          <h2 id="issue-title" class="text-sm font-semibold text-kymo-ink break-words">{{ issue().title }}</h2>
          <div class="flex items-center gap-x-3 gap-y-1 flex-wrap mt-1.5 text-xs">
            <span class="inline-flex items-center gap-1.5" [class]="text()">
              <span class="k-led" [class]="led()" aria-hidden="true"></span>{{ issue().severity }}
            </span>
            <span class="text-[10px] px-1.5 py-px border border-kymo-rule text-kymo-muted">{{ typeLabel() }}</span>
            <span [class]="issue().status === 'open' ? 'text-kymo-amber' : 'text-kymo-running'">{{ issue().status }}</span>
            @if (issue().flaps > 0) {
              <span class="text-[10px] px-1.5 py-px border border-kymo-amber/50 text-kymo-amber tabular-nums">flapping ×{{ issue().flaps }}</span>
            }
          </div>
        </div>
        <button type="button" data-autofocus class="p-1.5 text-kymo-muted hover:text-kymo-ink hover:bg-kymo-raised transition-colors flex-none" (click)="closed.emit()" aria-label="Close panel (Esc)" title="Close (Esc)">
          <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2"><path stroke-linecap="round" stroke-linejoin="round" d="M6 18L18 6M6 6l12 12" /></svg>
        </button>
      </div>

      <div class="flex-1 overflow-y-auto scrollbar-thin px-4 py-4 space-y-5">
        @if (issue().detail) {
          <section>
            <h3 class="k-label mb-1.5">Detail</h3>
            <p class="text-xs text-kymo-ink whitespace-pre-wrap break-words">{{ issue().detail }}</p>
          </section>
        }

        <section>
          <h3 class="k-label mb-1.5">Timeline</h3>
          <dl class="grid grid-cols-[6.5rem_1fr] gap-y-1.5 text-xs">
            <dt class="text-kymo-muted">First seen</dt>
            <dd class="text-kymo-ink"><span [title]="issue().first_seen">{{ issue().first_seen | date:'MMM d, HH:mm' }}</span> <span class="text-kymo-faint">· {{ issue().first_seen | timeago }}</span></dd>
            <dt class="text-kymo-muted">Last seen</dt>
            <dd class="text-kymo-ink"><span [title]="issue().last_seen">{{ issue().last_seen | date:'MMM d, HH:mm' }}</span> <span class="text-kymo-faint">· {{ issue().last_seen | timeago }}</span></dd>
            @if (issue().resolved_at; as r) {
              <dt class="text-kymo-muted">Resolved</dt>
              <dd class="text-kymo-ink"><span [title]="r">{{ r | date:'MMM d, HH:mm' }}</span> <span class="text-kymo-faint">· {{ r | timeago }}</span></dd>
            }
            <dt class="text-kymo-muted">Duration</dt>
            <dd class="text-kymo-ink tabular-nums">{{ duration() }}@if (!issue().resolved_at) { <span class="text-kymo-faint"> (ongoing)</span> }</dd>
            <dt class="text-kymo-muted">Flaps</dt>
            <dd class="text-kymo-ink tabular-nums" title="Times the issue resolved and re-opened">{{ issue().flaps }}</dd>
          </dl>
        </section>

        <section>
          <h3 class="k-label mb-1.5">Workload</h3>
          <div class="flex items-center gap-2 min-w-0">
            <app-workload-badge class="flex-none" [kind]="issue().kind" />
            <span class="text-xs text-kymo-ink truncate" [title]="issue().name">{{ issue().name }}</span>
          </div>
          <p class="text-[11px] text-kymo-faint mt-1">
            {{ issue().namespace || '—' }} · {{ issue().source_name }}
            @if (removed()) { <span class="ml-1 text-[10px] px-1 py-px border border-kymo-rule text-kymo-muted">removed</span> }
          </p>
          <div class="flex items-center gap-2 flex-wrap mt-3">
            <a class="k-btn k-btn-primary k-btn-sm" [routerLink]="['/workloads', issue().workload_id]" [queryParams]="wlQuery()">Open workload</a>
            <a class="k-btn k-btn-sm" [routerLink]="['/workloads', issue().workload_id]" [queryParams]="{ tab: 'activity' }">View activity</a>
          </div>
        </section>
      </div>
    </aside>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class IssueDrawerComponent {
  issue   = input.required<Issue>();
  removed = input(false);
  closed  = output<void>();

  readonly led       = computed(() => severityLedClass(this.issue().severity));
  readonly text      = computed(() => severityTextClass(this.issue().severity));
  readonly typeLabel = computed(() => issueTypeLabel(this.issue().type));
  readonly wlQuery   = computed(() => issueWorkloadQuery(this.issue()));
  readonly duration  = computed(() => {
    const i = this.issue();
    const end = i.resolved_at ? new Date(i.resolved_at).getTime() : Date.now();
    return formatSpan(end - new Date(i.first_seen).getTime());
  });
}
