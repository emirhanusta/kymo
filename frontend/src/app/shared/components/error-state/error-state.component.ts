import { Component, ChangeDetectionStrategy, input, output } from '@angular/core';

/** Inline error card with retry — used instead of silently showing "no data" when the API fails. */
@Component({
  selector: 'app-error-state',
  standalone: true,
  template: `
    <div class="border border-kymo-danger/50 border-l-2 border-l-kymo-danger bg-kymo-panel px-4 py-3 flex items-center gap-3" role="alert">
      <span class="text-xs font-semibold text-kymo-danger tracking-[0.08em] flex-none">ERR</span>
      <div class="flex-1 min-w-0">
        <p class="text-sm text-kymo-ink">{{ message() }}</p>
        <p class="text-xs text-kymo-muted mt-0.5">{{ hint() }}</p>
      </div>
      <button type="button" class="k-btn k-btn-sm flex-none" (click)="retry.emit()">Retry</button>
    </div>
  `,
  host: { class: 'block' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ErrorStateComponent {
  message = input('Failed to load data');
  hint    = input('The API did not respond. Check that the backend is running.');
  retry   = output<void>();
}
