import { Component, ChangeDetectionStrategy, input } from '@angular/core';
import { RouterLink } from '@angular/router';

import { Issue } from '../../../core/models/issue.model';
import { severityLedClass, severityTextClass } from '../../utils/issues';

/** One small chip per open issue of a workload (title in the label, detail on hover); links to the Issues page. */
@Component({
  selector: 'app-issue-chips',
  standalone: true,
  imports: [RouterLink],
  template: `
    @for (i of issues(); track i.id) {
      <a routerLink="/issues" class="inline-flex items-center gap-1.5 text-[11px] px-1.5 py-px border hover:bg-kymo-raised transition-colors"
         [class]="i.severity === 'critical' ? 'border-kymo-danger/50' : 'border-kymo-amber/50'"
         [title]="i.detail ? i.title + ' — ' + i.detail : i.title"
         [attr.aria-label]="i.severity + ' issue: ' + i.title">
        <span class="k-led" [class]="led(i.severity)" aria-hidden="true"></span>
        <span [class]="text(i.severity)">{{ i.title }}</span>
        @if (i.flaps > 0) { <span class="text-kymo-muted tabular-nums">×{{ i.flaps }}</span> }
      </a>
    }
  `,
  host: { class: 'contents' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class IssueChipsComponent {
  issues = input<Issue[]>([]);
  readonly led  = severityLedClass;
  readonly text = severityTextClass;
}
