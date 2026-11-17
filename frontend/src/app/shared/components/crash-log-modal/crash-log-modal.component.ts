import { Component, ChangeDetectionStrategy, input, output, signal, computed } from '@angular/core';
import { DatePipe } from '@angular/common';

import { CrashLog } from '../../../core/models/container.model';
import { DialogDirective } from '../../directives/dialog.directive';
import { podNameFromId } from '../../utils/source';

function lineClass(line: string): string {
  const l = line.toLowerCase();
  if (/\b(error|err|fatal|exception|panic)\b/.test(l)) return 'text-kymo-danger';
  if (/\b(warn|warning)\b/.test(l)) return 'text-kymo-amber';
  return 'text-kymo-ink/80';
}

/** Full-screen viewer for a static crash-log snapshot (same look as the live log viewer, without streaming). */
@Component({
  selector: 'app-crash-log-modal',
  standalone: true,
  imports: [DialogDirective, DatePipe],
  template: `
    <div class="fixed inset-0 bg-kymo-deep/85 z-[1000]" (click)="closed.emit()"></div>
    <div appDialog (dismiss)="closed.emit()" aria-label="Crash log snapshot"
         class="fixed inset-2 sm:inset-8 z-[1001] flex flex-col bg-kymo-deep border border-kymo-rule border-t-kymo-amber overflow-hidden">
      <div class="flex items-center gap-3 px-4 py-2.5 border-b border-kymo-rule flex-none flex-wrap">
        <div class="min-w-0 text-sm font-semibold text-kymo-ink flex items-center gap-2">
          <span class="truncate" [title]="log().container_id">{{ pod() }}</span>
          <span class="text-kymo-muted font-normal">crash log</span>
          <span class="text-xs font-normal text-kymo-danger border border-kymo-danger/40 px-1.5 tabular-nums">{{ log().reason || 'terminated' }} · exit {{ log().exit_code }}</span>
        </div>
        <div class="flex-1"></div>
        <input data-autofocus type="text" placeholder="Search…" class="k-input w-48 sm:w-64" aria-label="Search log"
               [value]="query()" (input)="query.set($any($event.target).value)" />
        <label class="flex items-center gap-1.5 text-xs text-kymo-muted cursor-pointer select-none" title="Show only matching lines">
          <input type="checkbox" [checked]="onlyMatches()" (change)="onlyMatches.set($any($event.target).checked)" /> filter
        </label>
        <button type="button" class="k-btn k-btn-sm" (click)="download()">Download</button>
        <button type="button" class="p-1.5 text-kymo-muted hover:text-kymo-danger hover:bg-kymo-raised transition-colors" (click)="closed.emit()" title="Close (Esc)" aria-label="Close log">
          <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2"><path stroke-linecap="round" stroke-linejoin="round" d="M6 18L18 6M6 6l12 12" /></svg>
        </button>
      </div>
      <div class="flex-1 overflow-auto scrollbar-thin px-4 py-3 text-xs leading-relaxed font-mono">
        @for (row of rows(); track row.idx) {
          <div [class]="row.cls" class="whitespace-pre-wrap break-all" [class.bg-kymo-raised]="!!query().trim() && row.hit">{{ row.text }}</div>
        } @empty {
          <p class="text-kymo-faint">{{ lines().length ? 'no lines match' : 'empty snapshot' }}</p>
        }
      </div>
      <div class="flex items-center gap-3 px-4 py-1.5 border-t border-kymo-rule text-xs text-kymo-muted flex-none">
        <span>{{ lines().length }} line{{ lines().length === 1 ? '' : 's' }}</span>
        @if (log().truncated) { <span class="text-kymo-amber">· truncated, older output not kept</span> }
        @if (log().previous) { <span>· previous container</span> }
        <div class="flex-1"></div>
        <span>terminated {{ log().occurred_at | date:'MMM d, HH:mm:ss' }} · captured {{ log().captured_at | date:'HH:mm:ss' }}</span>
      </div>
    </div>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class CrashLogModalComponent {
  log    = input.required<CrashLog>();
  closed = output<void>();

  query       = signal('');
  onlyMatches = signal(false);

  pod   = computed(() => podNameFromId(this.log().container_id) || this.log().container_id);
  lines = computed(() => (this.log().lines ?? '').replace(/\n+$/, '').split('\n').filter((l, i, a) => a.length > 1 || l));
  rows  = computed(() => {
    const q = this.query().trim().toLowerCase();
    return this.lines()
      .map((text, idx) => ({ idx, text, cls: lineClass(text), hit: !q || text.toLowerCase().includes(q) }))
      .filter(r => !this.onlyMatches() || r.hit);
  });

  download(): void {
    const blob = new Blob([this.log().lines], { type: 'text/plain' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `${this.pod()}-crash-${this.log().occurred_at}.txt`;
    a.click();
    URL.revokeObjectURL(url);
  }
}
