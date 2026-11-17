import {
  Component, ChangeDetectionStrategy, Input, Output, EventEmitter,
} from '@angular/core';

@Component({
  selector: 'app-pagination',
  standalone: true,
  templateUrl: './pagination.component.html',
  styleUrl: './pagination.component.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class PaginationComponent {
  @Input() page = 1;
  @Input() totalPages = 1;
  @Input() total = 0;
  @Input() limit = 10;
  @Output() pageChange = new EventEmitter<number>();

  get pages(): (number | '...')[] {
    const p = this.page;
    const t = this.totalPages;
    if (t <= 7) {
      return Array.from({ length: t }, (_, i) => i + 1);
    }
    const result: (number | '...')[] = [1];
    if (p > 3) result.push('...');
    const start = Math.max(2, p - 1);
    const end = Math.min(t - 1, p + 1);
    for (let i = start; i <= end; i++) result.push(i);
    if (p < t - 2) result.push('...');
    result.push(t);
    return result;
  }

  get from(): number { return this.total === 0 ? 0 : (this.page - 1) * this.limit + 1; }
  get to(): number { return Math.min(this.page * this.limit, this.total); }

  goTo(p: number): void {
    if (p >= 1 && p <= this.totalPages && p !== this.page) {
      this.pageChange.emit(p);
    }
  }
}
