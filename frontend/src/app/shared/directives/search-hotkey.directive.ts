import { Directive, ElementRef, inject } from '@angular/core';

/** Pressing `/` anywhere (outside of form fields) focuses the host input. */
@Directive({
  selector: 'input[appSearchHotkey]',
  standalone: true,
  host: { '(document:keydown)': 'onKey($event)' },
})
export class SearchHotkeyDirective {
  private el = inject<ElementRef<HTMLInputElement>>(ElementRef);

  onKey(ev: KeyboardEvent): void {
    if (ev.key !== '/' || ev.ctrlKey || ev.metaKey || ev.altKey) return;
    const t = ev.target as HTMLElement | null;
    if (t && (t.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(t.tagName))) return;
    if (document.querySelector('[aria-modal="true"]')) return;
    ev.preventDefault();
    this.el.nativeElement.focus();
    this.el.nativeElement.select();
  }
}
