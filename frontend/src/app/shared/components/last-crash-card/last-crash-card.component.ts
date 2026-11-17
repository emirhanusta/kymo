import { Component, ChangeDetectionStrategy, input, computed, signal } from '@angular/core';
import { RouterLink } from '@angular/router';

import { CrashLog, LifecycleEvent, Revision } from '../../../core/models/container.model';
import { CrashLogModalComponent } from '../crash-log-modal/crash-log-modal.component';
import { TimeagoPipe } from '../../pipes/timeago.pipe';
import { eventLabel, eventContainerName } from '../../utils/events';
import { formatSpan } from '../../utils/format';
import { podNameFromId } from '../../utils/source';
import { rolloutBefore, shortRev } from '../../utils/timeline';

const TAIL_LINES = 15;
const t = (s: string) => new Date(s).getTime();

/**
 * "Last crash" of a workload: reason, exit code, when and where, the tail of the captured log and what changed
 * right before (a rollout shortly ahead of the crash). Pure view: the parent supplies the newest crash/OOM
 * events, recent crash-log snapshots and the revisions. Renders nothing when there was never a crash.
 */
@Component({
  selector: 'app-last-crash-card',
  standalone: true,
  imports: [RouterLink, TimeagoPipe, CrashLogModalComponent],
  host: { class: 'block' },
  template: `
    @if (view(); as v) {
      <section class="k-panel border-l-2" [class]="v.oom ? 'border-l-kymo-amber' : 'border-l-kymo-danger'" aria-label="Last crash">
        <div class="px-4 py-3 flex items-start gap-x-4 gap-y-1 flex-wrap">
          <div class="min-w-0 flex-1">
            <p class="k-label mb-1">Last crash</p>
            <p class="text-sm font-medium flex items-center gap-2 flex-wrap" [class]="v.oom ? 'text-kymo-amber' : 'text-kymo-danger'">
              <span>{{ v.reason }}</span>
              @if (v.exitCode != null) {
                <span class="text-[11px] border px-1.5 tabular-nums" [class]="v.oom ? 'border-kymo-amber/40' : 'border-kymo-danger/40'">exit {{ v.exitCode }}</span>
              }
              <span class="text-xs font-normal text-kymo-muted" [title]="v.at">{{ v.at | timeago }}</span>
            </p>
            <p class="text-xs text-kymo-muted mt-0.5 truncate">
              @if (v.pod) { <span class="text-kymo-ink">{{ v.pod }}</span> · }<span>{{ v.container }}</span>
              @if (v.containerId && link()) {
                · <a [routerLink]="['/containers', v.containerId]" class="text-kymo-amber hover:underline underline-offset-4">container →</a>
              }
            </p>
            @if (v.change; as c) {
              <p class="text-xs text-kymo-muted mt-1.5">
                <span class="text-kymo-faint">before:</span>
                {{ c.after }} after rollout
                <span class="text-kymo-faint">(rev {{ c.rev }})</span>
                @if (c.images) { · <span class="text-kymo-ink break-all">image {{ c.images }}</span> }
              </p>
            }
          </div>
          @if (v.log) {
            <button type="button" class="k-btn k-btn-sm flex-none" (click)="modal.set(true)">Open full log</button>
          }
        </div>
        @if (v.log; as l) {
          <pre class="mx-4 mb-3 bg-kymo-deep border border-kymo-rule px-3 py-2 text-[11px] leading-relaxed text-kymo-ink/80 max-h-48 overflow-auto scrollbar-thin whitespace-pre-wrap break-all font-mono" tabindex="0" aria-label="Last lines of the crashed container's log">{{ v.tail }}</pre>
          @if (l.truncated) { <p class="px-4 pb-3 -mt-2 text-[11px] text-kymo-faint">log truncated — older output was not kept</p> }
        } @else if (v.noLogs) {
          <p class="px-4 pb-3 text-[11px] text-kymo-faint">No log snapshot was captured for this crash.</p>
        }
      </section>
      @if (modal() && v.log) {
        <app-crash-log-modal [log]="v.log" (closed)="modal.set(false)" />
      }
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class LastCrashCardComponent {
  /** Newest crash and/or OOM events of the workload. */
  crashEvents = input<LifecycleEvent[]>([]);
  /** Crash-log snapshots, newest first. */
  logs        = input<CrashLog[]>([]);
  revisions   = input<Revision[]>([]);
  /** Show the link to the container detail page (off where it is the page itself). */
  link        = input(true);

  modal = signal(false);

  view = computed(() => {
    const ev = [...this.crashEvents()].sort((a, b) => t(b.occurred_at) - t(a.occurred_at))[0] ?? null;
    const logs = this.logs();
    // The snapshot of that very termination; without an event, the newest snapshot stands in for it.
    const log = ev
      ? logs.find(l => l.container_id === ev.container_id && Math.abs(t(l.occurred_at) - t(ev.occurred_at)) < 5000) ?? null
      : logs[0] ?? null;
    if (!ev && !log) return null;

    const at = ev?.occurred_at ?? log!.occurred_at;
    const containerId = ev?.container_id ?? log!.container_id;
    const oom = ev ? ['oom', 'oomkilled'].includes(ev.event_type.toLowerCase()) : /oom/i.test(log!.reason);
    const evReason = typeof ev?.metadata?.reason === 'string' ? ev.metadata.reason : '';
    const reason = log?.reason || evReason || (ev ? eventLabel(ev) : 'Crashed');
    const exitCode = log?.exit_code ?? ev?.exit_code ?? null;

    const roll = rolloutBefore(this.revisions(), t(at));
    const change = roll ? {
      after: formatSpan(t(at) - t(roll.revision.first_seen_at)),
      rev: shortRev(roll.revision.revision),
      images: roll.imageChange,
    } : null;

    const parts = containerId.split('/');
    return {
      at, oom, reason, exitCode, containerId, change, log,
      noLogs: !log && !!ev && ev.has_logs === false,
      tail: log ? log.lines.replace(/\n+$/, '').split('\n').slice(-TAIL_LINES).join('\n') : '',
      pod: podNameFromId(containerId),
      container: ev ? eventContainerName(ev) : (parts.length >= 3 ? parts.slice(2).join('/') : containerId),
    };
  });
}
