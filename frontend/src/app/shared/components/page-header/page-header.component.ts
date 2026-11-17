import { Component, ChangeDetectionStrategy, input } from '@angular/core';
import { RouterLink } from '@angular/router';

/**
 * Consistent page title row.
 * Project actions as default content; extra subtitle markup via `[subtitle]`.
 * Detail pages pass `crumbLabel` / `crumbLink` to render a back-link breadcrumb above the title.
 */
@Component({
  selector: 'app-page-header',
  standalone: true,
  imports: [RouterLink],
  template: `
    <div class="flex items-end justify-between gap-4 flex-wrap border-b border-kymo-rule pb-3">
      <div class="min-w-0">
        @if (crumbLabel()) {
          <nav class="k-label mb-1.5 flex items-center gap-1.5" aria-label="Breadcrumb">
            <a [routerLink]="crumbLink()" class="hover:text-kymo-amber transition-colors">{{ crumbLabel() }}</a>
            <span class="text-kymo-faint" aria-hidden="true">/</span>
          </nav>
        }
        <h1 class="text-lg font-semibold text-kymo-ink truncate tracking-tight" [title]="title()">{{ title() }}</h1>
        <div class="text-xs text-kymo-muted mt-1 min-h-4">
          {{ subtitle() }}<ng-content select="[subtitle]" />
        </div>
      </div>
      <div class="flex items-center gap-2 flex-wrap">
        <ng-content />
      </div>
    </div>
  `,
  host: { class: 'block' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class PageHeaderComponent {
  title      = input.required<string>();
  subtitle   = input<string>('');
  crumbLabel = input<string>('');
  crumbLink  = input<string | unknown[]>('');
}
