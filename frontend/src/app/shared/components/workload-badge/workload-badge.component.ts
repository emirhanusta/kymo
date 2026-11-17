import { Component, Input, computed, signal, ChangeDetectionStrategy } from '@angular/core';

interface BadgeStyle {
  label: string;
  classes: string;
}

const STYLES: Record<string, BadgeStyle> = {
  Deployment:  { label: 'DEP',  classes: 'border-kymo-rule text-kymo-ink' },
  DaemonSet:   { label: 'DS',   classes: 'border-kymo-paused/40 text-kymo-paused' },
  StatefulSet: { label: 'SS',   classes: 'border-kymo-stopped/40 text-kymo-stopped' },
  Job:         { label: 'JOB',  classes: 'border-kymo-rule text-kymo-muted' },
  CronJob:     { label: 'CRON', classes: 'border-kymo-amber/50 text-kymo-amber' },
  ReplicaSet:  { label: 'RS',   classes: 'border-kymo-rule text-kymo-muted' },
  Compose:     { label: 'COMPOSE', classes: 'border-kymo-running/40 text-kymo-running' },
  Container:   { label: 'CTR',  classes: 'border-kymo-rule text-kymo-muted' },
  Pod:         { label: 'POD',  classes: 'border-kymo-rule text-kymo-faint' },
};

@Component({
  selector: 'app-workload-badge',
  standalone: true,
  imports: [],
  template: `
    @if (style()) {
      <span class="text-[10px] tracking-[0.08em] px-1.5 py-0.5 border {{ style()!.classes }}">
        {{ style()!.label }}
      </span>
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class WorkloadBadgeComponent {
  private _kind = signal('');

  @Input({ required: true }) set kind(v: string) { this._kind.set(v ?? ''); }

  style = computed((): BadgeStyle | null => STYLES[this._kind()] ?? null);
}
