import { Component, ChangeDetectionStrategy, input, output } from '@angular/core';
import { DialogDirective } from '../../directives/dialog.directive';

/** Generic confirmation modal (Escape / backdrop = cancel, primary button autofocused). Extra controls can be projected. */
@Component({
  selector: 'app-confirm-dialog',
  standalone: true,
  imports: [DialogDirective],
  template: `
    <div class="fixed inset-0 bg-kymo-deep/85 z-50 flex items-center justify-center p-4" (click)="cancelled.emit()">
      <div
        appDialog
        (dismiss)="cancelled.emit()"
        aria-labelledby="confirm-dialog-title"
        class="bg-kymo-panel border border-kymo-rule border-t-kymo-amber p-5 max-w-md w-full space-y-4"
        (click)="$event.stopPropagation()"
      >
        <div>
          <h3 id="confirm-dialog-title" class="text-sm font-semibold uppercase tracking-[0.08em] text-kymo-ink">{{ title() }}</h3>
          <p class="text-sm text-kymo-muted mt-1.5 whitespace-pre-line">{{ message() }}</p>
        </div>
        <ng-content />
        <div class="flex justify-end gap-2 pt-1">
          <button type="button" class="k-btn" (click)="cancelled.emit()">
            Cancel
          </button>
          <button
            type="button"
            data-autofocus
            class="k-btn"
            [class.k-btn-danger]="danger()"
            [class.k-btn-primary]="!danger()"
            (click)="confirmed.emit()"
          >{{ confirmLabel() }}</button>
        </div>
      </div>
    </div>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ConfirmDialogComponent {
  title        = input('Are you sure?');
  message      = input('');
  confirmLabel = input('Confirm');
  danger       = input(true);
  confirmed    = output<void>();
  cancelled    = output<void>();
}
