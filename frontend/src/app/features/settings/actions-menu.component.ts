import { Component, ChangeDetectionStrategy, input, signal } from '@angular/core';

/**
 * "…" row actions menu. Project `<button role="menuitem" class="k-menu-item">` children; any click inside closes it.
 * The panel is fixed-positioned so a table's overflow does not clip it.
 */
@Component({
  selector: 'app-actions-menu',
  standalone: true,
  template: `
    <button type="button" class="k-btn k-btn-sm k-btn-icon border-transparent text-kymo-muted hover:text-kymo-ink"
            aria-haspopup="menu" [attr.aria-expanded]="pos() !== null" [attr.aria-label]="label()"
            [disabled]="disabled()" (click)="toggle($event)">
      <svg class="w-3.5 h-3.5" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><circle cx="5" cy="12" r="1.6" /><circle cx="12" cy="12" r="1.6" /><circle cx="19" cy="12" r="1.6" /></svg>
    </button>
    @if (pos(); as p) {
      <div class="fixed inset-0 z-40" (click)="close(); $event.stopPropagation()"></div>
      <div class="fixed z-50 w-52 bg-kymo-deep border border-kymo-rule py-1 flex flex-col text-left" role="menu"
           [style.left.px]="p.x" [style.top.px]="p.y" (click)="close(); $event.stopPropagation()">
        <ng-content />
      </div>
    }
  `,
  host: { class: 'inline-block', '(document:keydown.escape)': 'close()' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ActionsMenuComponent {
  label = input('Actions');
  disabled = input(false);
  pos = signal<{ x: number; y: number } | null>(null);

  toggle(ev: MouseEvent): void {
    ev.stopPropagation();
    if (this.pos()) { this.close(); return; }
    const r = (ev.currentTarget as HTMLElement).getBoundingClientRect();
    this.pos.set({ x: Math.max(8, r.right - 208), y: r.bottom + 4 });
  }
  close(): void { this.pos.set(null); }
}
