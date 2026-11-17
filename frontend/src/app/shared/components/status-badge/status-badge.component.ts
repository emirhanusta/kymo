import { Component, Input, ChangeDetectionStrategy, booleanAttribute } from '@angular/core';
import { STATUS_DOT_CLASS, STATUS_TEXT_CLASS, StatusKey, normalizeStatus } from '../../utils/status';

/**
 * The single place container status is rendered. Use `compact` for a bare
 * coloured dot (the status is then exposed via title/aria-label).
 */
@Component({
  selector: 'app-status-badge',
  standalone: true,
  templateUrl: './status-badge.component.html',
  styleUrl: './status-badge.component.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class StatusBadgeComponent {
  @Input() status = '';
  /** Overrides the label, e.g. a crash reason like "OOMKilled". */
  @Input() text = '';
  @Input({ transform: booleanAttribute }) compact = false;

  get normalized(): StatusKey { return normalizeStatus(this.status); }
  get label(): string { return this.text || this.status || 'unknown'; }
  get dotClass(): string { return STATUS_DOT_CLASS[this.normalized]; }
  get textClass(): string { return STATUS_TEXT_CLASS[this.normalized]; }
}
