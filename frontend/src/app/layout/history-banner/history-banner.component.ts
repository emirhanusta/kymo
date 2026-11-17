import { Component, ChangeDetectionStrategy, inject, signal } from '@angular/core';
import { Router } from '@angular/router';

import { SourceStateService } from '../../core/services/source-state.service';
import { ConfirmDialogComponent } from '../../shared/components/confirm-dialog/confirm-dialog.component';
import { TimeagoPipe } from '../../shared/pipes/timeago.pipe';
import { purgeMessage } from '../../shared/utils/removed';

/** Slim strip under the top bar while the recorded history of a removed cluster is being viewed. */
@Component({
  selector: 'app-history-banner',
  standalone: true,
  imports: [ConfirmDialogComponent, TimeagoPipe],
  template: `
    @if (sources.selectedRemoved(); as r) {
      <div class="bg-kymo-panel border-b border-kymo-amber/40" role="status">
        <div class="max-w-[1600px] mx-auto px-3 sm:px-6 py-2 flex flex-wrap items-center gap-x-3 gap-y-2 text-xs">
          <svg class="w-3.5 h-3.5 flex-none text-kymo-amber" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2" aria-hidden="true">
            <path stroke-linecap="square" d="M4 4h16v4H4zM6 8v12h12V8M10 12h4" />
          </svg>
          <p class="text-kymo-muted min-w-0 flex-1 basis-64">
            Viewing recorded history of removed cluster
            <strong class="font-semibold text-kymo-ink break-all">{{ r.name }}</strong>
            <span class="text-kymo-faint"> · last seen {{ r.last_seen_at | timeago }}</span>
          </p>
          <div class="flex items-center gap-1.5 flex-wrap">
            <button type="button" class="k-btn k-btn-sm k-btn-primary" (click)="readd(r.name)">Re-add cluster</button>
            <button type="button" class="k-btn k-btn-sm k-btn-danger" (click)="confirming.set(true)">Delete history</button>
            <button type="button" class="k-btn k-btn-sm" (click)="sources.select('')">Back to all clusters</button>
          </div>
        </div>
      </div>
      @if (confirming()) {
        <app-confirm-dialog
          [title]="'Delete history of ' + r.name + '?'"
          [message]="message(r)"
          confirmLabel="Delete history"
          (confirmed)="purge(r.name)"
          (cancelled)="confirming.set(false)"
        />
      }
    }
  `,
  host: { class: 'block' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class HistoryBannerComponent {
  protected sources = inject(SourceStateService);
  private router    = inject(Router);

  confirming = signal(false);
  readonly message = purgeMessage;

  readd(name: string): void {
    this.router.navigate(['/clusters'], { queryParams: { readd: name } });
  }

  purge(name: string): void {
    this.confirming.set(false);
    this.sources.purgeHistory(name);
  }
}
