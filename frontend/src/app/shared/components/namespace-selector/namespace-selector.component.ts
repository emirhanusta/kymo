import {
  Component, Output, EventEmitter, ChangeDetectionStrategy,
  signal, inject, effect, input, untracked,
} from '@angular/core';

import { NamespaceService } from '../../../core/services/namespace.service';

@Component({
  selector: 'app-namespace-selector',
  standalone: true,
  templateUrl: './namespace-selector.component.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class NamespaceSelectorComponent {
  @Output() namespaceChange = new EventEmitter<string>();

  sourceName = input.required<string>();
  selected   = input<string>('');

  private namespaceSvc = inject(NamespaceService);

  namespaces = signal<string[]>([]);
  loading    = signal(false);

  constructor() {
    effect(() => {
      const source = this.sourceName();
      this.namespaceSvc.version(); // re-fetch after the cache is invalidated
      this.loading.set(true);
      this.namespaceSvc.getNamespaces(source).subscribe(ns => {
        this.namespaces.set(ns);
        this.loading.set(false);
        // Selected namespace vanished for this source → fall back (skip when the lookup failed/empty).
        const cur = untracked(() => this.selected());
        if (cur && ns.length && !ns.includes(cur)) {
          this.namespaceChange.emit(ns[0]);
        }
      });
    });
  }

  onChange(value: string): void {
    this.namespaceChange.emit(value);
  }
}
