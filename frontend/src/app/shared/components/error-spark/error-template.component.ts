import { Component, ChangeDetectionStrategy, input, computed } from '@angular/core';

import { templateParts } from '../../utils/error-logs';

/** A signature template in mono with its `<n>` / `<uuid>` placeholders dimmed. */
@Component({
  selector: 'app-error-template',
  standalone: true,
  template: `@for (p of parts(); track $index) {<span [class]="p.placeholder ? 'text-kymo-faint' : ''">{{ p.text }}</span>}`,
  host: { class: 'font-mono' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ErrorTemplateComponent {
  template = input.required<string>();
  parts = computed(() => templateParts(this.template()));
}
