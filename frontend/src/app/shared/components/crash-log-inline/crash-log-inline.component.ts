import { Component, ChangeDetectionStrategy, input, signal, inject, DestroyRef } from '@angular/core';
import { HttpErrorResponse } from '@angular/common/http';

import { ContainerService } from '../../../core/services/container.service';
import { CrashLog } from '../../../core/models/container.model';
import { CrashLogModalComponent } from '../crash-log-modal/crash-log-modal.component';
import { Subscription } from 'rxjs';

const TAIL_LINES = 20;

/**
 * "▸ logs" toggle for a crash/OOM event: lazily loads the log snapshot of that termination
 * (GET /containers/:id/crash-logs?occurred_at=…) on first expand and shows its tail inline.
 */
@Component({
  selector: 'app-crash-log-inline',
  standalone: true,
  imports: [CrashLogModalComponent],
  host: { class: 'block' },
  template: `
    <button type="button"
      class="text-[11px] text-kymo-amber hover:underline underline-offset-4 inline-flex items-center gap-1"
      [attr.aria-expanded]="open()" [attr.aria-controls]="panelId"
      (click)="toggle()">
      <span aria-hidden="true">{{ open() ? '▾' : '▸' }}</span> logs
    </button>
    @if (open()) {
      <div [id]="panelId" class="mt-1.5">
        @if (loading()) {
          <p class="text-[11px] text-kymo-faint" aria-live="polite">loading log snapshot…</p>
        } @else if (state() === 'missing') {
          <p class="text-[11px] text-kymo-faint">No log snapshot was kept for this termination.</p>
        } @else if (state() === 'error') {
          <p class="text-[11px] text-kymo-danger">Failed to load logs.
            <button type="button" class="underline underline-offset-4" (click)="load()">retry</button></p>
        } @else if (log(); as l) {
          <pre class="bg-kymo-deep border border-kymo-rule px-3 py-2 text-[11px] leading-relaxed text-kymo-ink/80 max-h-52 overflow-auto scrollbar-thin whitespace-pre-wrap break-all font-mono m-0" tabindex="0" aria-label="Log tail">{{ tail() }}</pre>
          <p class="mt-1 text-[11px] text-kymo-faint flex items-center gap-3 flex-wrap">
            <span>{{ l.line_count }} line{{ l.line_count === 1 ? '' : 's' }}@if (l.previous) { · previous container}</span>
            @if (l.truncated) { <span class="text-kymo-amber">truncated — older output not kept</span> }
            <button type="button" class="text-kymo-amber hover:underline underline-offset-4" (click)="modal.set(true)">open full log</button>
          </p>
        }
      </div>
    }
    @if (modal() && log()) {
      <app-crash-log-modal [log]="log()!" (closed)="modal.set(false)" />
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class CrashLogInlineComponent {
  containerId = input.required<string>();
  occurredAt  = input.required<string>();

  private svc = inject(ContainerService);
  private sub?: Subscription;

  readonly panelId = `crash-log-${Math.random().toString(36).slice(2, 8)}`;
  open    = signal(false);
  loading = signal(false);
  state   = signal<'idle' | 'ok' | 'missing' | 'error'>('idle');
  log     = signal<CrashLog | null>(null);
  modal   = signal(false);

  constructor() {
    inject(DestroyRef).onDestroy(() => this.sub?.unsubscribe());
  }

  tail(): string {
    const lines = (this.log()?.lines ?? '').replace(/\n+$/, '').split('\n');
    return lines.slice(-TAIL_LINES).join('\n');
  }

  toggle(): void {
    this.open.update(o => !o);
    if (this.open() && this.state() !== 'ok' && this.state() !== 'missing' && !this.loading()) this.load();
  }

  load(): void {
    this.loading.set(true);
    this.sub?.unsubscribe();
    this.sub = this.svc.getContainerCrashLog(this.containerId(), this.occurredAt()).subscribe({
      next: l => { this.log.set(l); this.state.set(l ? 'ok' : 'missing'); this.loading.set(false); },
      error: (err: HttpErrorResponse) => { this.state.set(err?.status === 404 ? 'missing' : 'error'); this.loading.set(false); },
    });
  }
}
