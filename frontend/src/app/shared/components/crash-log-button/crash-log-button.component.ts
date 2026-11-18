import { Component, ChangeDetectionStrategy, input, signal, inject, DestroyRef } from '@angular/core';
import { Subscription } from 'rxjs';

import { ContainerService } from '../../../core/services/container.service';
import { ToastService } from '../../../core/services/toast.service';
import { CrashLog } from '../../../core/models/container.model';
import { CrashLogModalComponent } from '../crash-log-modal/crash-log-modal.component';

/** Small "logs" icon button: loads the crash-log snapshot of one termination on demand and opens it in the modal viewer. */
@Component({
  selector: 'app-crash-log-button',
  standalone: true,
  imports: [CrashLogModalComponent],
  template: `
    <button type="button" class="inline-flex items-center justify-center w-6 h-6 text-kymo-amber hover:bg-kymo-raised transition-colors disabled:opacity-50"
      title="View crash logs" aria-label="View crash logs" [disabled]="loading()" (click)="open($event)">
      <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5" aria-hidden="true">
        <path stroke-linecap="square" d="M5 4h14v16H5zM8 9h8M8 13h8M8 17h5" />
      </svg>
    </button>
    @if (log(); as l) {
      <app-crash-log-modal [log]="l" (closed)="log.set(null)" />
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class CrashLogButtonComponent {
  containerId = input.required<string>();
  occurredAt  = input.required<string>();

  private svc   = inject(ContainerService);
  private toast = inject(ToastService);
  private sub?: Subscription;

  loading = signal(false);
  log     = signal<CrashLog | null>(null);

  constructor() {
    inject(DestroyRef).onDestroy(() => this.sub?.unsubscribe());
  }

  open(ev: Event): void {
    ev.stopPropagation();
    if (this.loading()) return;
    this.loading.set(true);
    this.sub?.unsubscribe();
    this.sub = this.svc.getContainerCrashLog(this.containerId(), this.occurredAt()).subscribe({
      next: l => {
        this.loading.set(false);
        if (l) this.log.set(l); else this.toast.info('No log snapshot was kept for this termination.');
      },
      error: err => { this.loading.set(false); this.toast.apiError(err, 'Failed to load logs'); },
    });
  }
}
