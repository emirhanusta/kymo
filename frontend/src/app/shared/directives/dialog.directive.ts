import { Directive, ElementRef, AfterViewInit, inject, input, output } from '@angular/core';

/**
 * Lightweight modal behaviour: Escape emits `dismiss`, and the element marked
 * `data-autofocus` (or the first button) inside the host gets focus on open.
 * Focus is restored to the previously focused element on destroy.
 */
@Directive({
  selector: '[appDialog]',
  standalone: true,
  host: {
    role: 'dialog',
    '[attr.aria-modal]': 'modal()',
    '(document:keydown.escape)': 'onEscape($event)',
  },
})
export class DialogDirective implements AfterViewInit {
  dismiss = output<void>();
  /** False for non-blocking panels (side drawer next to a live page). */
  modal = input(true);

  private el = inject<ElementRef<HTMLElement>>(ElementRef);
  private previouslyFocused = (typeof document !== 'undefined' ? document.activeElement : null) as HTMLElement | null;

  ngAfterViewInit(): void {
    queueMicrotask(() => {
      const root = this.el.nativeElement;
      const target = root.querySelector<HTMLElement>('[data-autofocus]') ?? root.querySelector<HTMLElement>('button:not([disabled])');
      target?.focus();
    });
  }

  onEscape(ev: Event): void {
    ev.preventDefault();
    this.dismiss.emit();
  }

  ngOnDestroy(): void {
    this.previouslyFocused?.focus?.();
  }
}
