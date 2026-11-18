import { Component, ChangeDetectionStrategy, input, computed } from '@angular/core';

import { LifecycleEvent, Revision } from '../../../core/models/container.model';
import { isProblemEvent } from '../../utils/events';
import { rolloutsOf } from '../../utils/timeline';

const HOUR = 3_600_000;
type MarkKind = 'crash' | 'oom' | 'rollout';
interface Mark { kind: MarkKind; pct: number; title: string; }

const COLOR: Record<MarkKind, string> = { crash: 'bg-kymo-danger', oom: 'bg-kymo-amber', rollout: 'bg-kymo-paused' };

/** Compact strip of the last 24h (default): a dot per crash, OOM kill and rollout. */
@Component({
  selector: 'app-mini-timeline',
  standalone: true,
  host: { class: 'block' },
  template: `
    <div class="relative h-5 bg-kymo-deep border border-kymo-rule" role="img" [attr.aria-label]="label()">
      @for (h of ticks; track h) {
        <span class="absolute top-0 bottom-0 w-px bg-kymo-grid" [style.left.%]="h" aria-hidden="true"></span>
      }
      @for (m of marks(); track $index) {
        <span class="absolute top-1/2 w-2 h-2 -mt-1 -ml-1" [class]="color[m.kind]" [class.rounded-full]="m.kind !== 'rollout'"
              [class.rotate-45]="m.kind === 'rollout'" [style.left.%]="m.pct" [title]="m.title"></span>
      }
    </div>
    <div class="flex justify-between text-[10px] text-kymo-faint mt-0.5" aria-hidden="true">
      <span>{{ hours() }}h ago</span><span>now</span>
    </div>
    <p class="flex items-center gap-3 text-[11px] text-kymo-muted mt-1 flex-wrap">
      <span class="inline-flex items-center gap-1"><span class="w-2 h-2 rounded-full bg-kymo-danger" aria-hidden="true"></span>{{ counts().crash }} crash{{ counts().crash === 1 ? '' : 'es' }}</span>
      <span class="inline-flex items-center gap-1"><span class="w-2 h-2 rounded-full bg-kymo-amber" aria-hidden="true"></span>{{ counts().oom }} OOM</span>
      @if (showRollouts()) {
        <span class="inline-flex items-center gap-1"><span class="w-2 h-2 rotate-45 bg-kymo-paused" aria-hidden="true"></span>{{ counts().rollout }} rollout{{ counts().rollout === 1 ? '' : 's' }}</span>
      }
    </p>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class MiniTimelineComponent {
  events    = input<LifecycleEvent[]>([]);
  revisions = input<Revision[]>([]);
  hours     = input(24);
  /** Hide the rollout count (Jobs/CronJobs have runs, not rollouts). */
  showRollouts = input(true);
  /** Time reference; fixed per render so the strip is stable between refreshes. */
  now       = input(Date.now());

  readonly color = COLOR;
  readonly ticks = [25, 50, 75];

  marks = computed<Mark[]>(() => {
    const span = this.hours() * HOUR;
    const to = this.now();
    const from = to - span;
    const pct = (ms: number) => Math.min(100, Math.max(0, ((ms - from) / span) * 100));
    const out: Mark[] = [];
    for (const ev of this.events()) {
      const at = new Date(ev.occurred_at).getTime();
      if (!isProblemEvent(ev) || at < from || at > to) continue;
      const oom = ['oom', 'oomkilled'].includes(ev.event_type.toLowerCase());
      out.push({ kind: oom ? 'oom' : 'crash', pct: pct(at), title: `${oom ? 'OOM killed' : 'Crashed'} · ${new Date(at).toLocaleString()}` });
    }
    for (const r of rolloutsOf(this.revisions())) {
      const at = new Date(r.revision.first_seen_at).getTime();
      if (at < from || at > to) continue;
      out.push({ kind: 'rollout', pct: pct(at), title: `Rollout · ${new Date(at).toLocaleString()}` });
    }
    return out;
  });

  counts = computed(() => {
    const c = { crash: 0, oom: 0, rollout: 0 };
    for (const m of this.marks()) c[m.kind]++;
    return c;
  });

  label = computed(() => {
    const c = this.counts();
    return `Last ${this.hours()} hours: ${c.crash} crashes, ${c.oom} OOM kills, ${c.rollout} rollouts`;
  });
}
