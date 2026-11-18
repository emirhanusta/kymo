import { Component, ChangeDetectionStrategy, input, output, signal } from '@angular/core';
import { DialogDirective } from '../../directives/dialog.directive';

/** Shows a value that will not be displayed again (invite link, reset link, API token secret) with a copy button. */
@Component({
  selector: 'app-copy-dialog',
  standalone: true,
  imports: [DialogDirective],
  template: `
    <div class="fixed inset-0 bg-kymo-deep/85 z-50 flex items-center justify-center p-4" (click)="closed.emit()">
      <div appDialog (dismiss)="closed.emit()" aria-labelledby="copy-dialog-title"
           class="bg-kymo-panel border border-kymo-rule border-t-kymo-amber p-5 max-w-lg w-full space-y-4" (click)="$event.stopPropagation()">
        <div>
          <h3 id="copy-dialog-title" class="text-sm font-semibold uppercase tracking-[0.08em] text-kymo-ink">{{ title() }}</h3>
          @if (message()) { <p class="text-sm text-kymo-muted mt-1.5">{{ message() }}</p> }
        </div>
        <div class="flex gap-2">
          <input class="k-input flex-1 min-w-0 font-mono" readonly [value]="value()" aria-label="Value" (focus)="$any($event.target).select()" />
          <button type="button" data-autofocus class="k-btn k-btn-primary" (click)="copy()">{{ copied() ? 'Copied' : 'Copy' }}</button>
        </div>
        @if (warning()) {
          <p class="text-xs text-kymo-amber border border-kymo-amber/40 px-3 py-2" role="note">{{ warning() }}</p>
        }
        @if (note()) {
          <p class="text-xs text-kymo-muted border border-kymo-rule px-3 py-2 whitespace-pre-line">{{ note() }}</p>
        }
        @if (expiresAt()) { <p class="text-xs text-kymo-muted">Expires {{ expiresAt() }}</p> }
        <div class="flex justify-end"><button type="button" class="k-btn" (click)="closed.emit()">Done</button></div>
      </div>
    </div>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class CopyDialogComponent {
  title = input('');
  message = input('');
  value = input.required<string>();
  warning = input('');
  expiresAt = input('');
  /** Optional instructions for the admin to pass on along with the value. */
  note = input('');
  closed = output<void>();
  copied = signal(false);

  async copy(): Promise<void> {
    try {
      await navigator.clipboard.writeText(this.value());
    } catch {
      const el = document.createElement('textarea');
      el.value = this.value();
      document.body.appendChild(el);
      el.select();
      document.execCommand('copy');
      el.remove();
    }
    this.copied.set(true);
    setTimeout(() => this.copied.set(false), 2000);
  }
}
