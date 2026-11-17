import { Component, Input, ChangeDetectionStrategy } from '@angular/core';
import { scoreHex } from '../../utils/score';

@Component({
  selector: 'app-stability-gauge',
  standalone: true,
  templateUrl: './stability-gauge.component.html',
  styleUrl: './stability-gauge.component.scss',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class StabilityGaugeComponent {
  @Input() score = 0;
  @Input() size = 120;

  get r() { return 45; }
  get cx() { return this.size / 2; }
  get cy() { return this.size / 2; }
  get circumference() { return 2 * Math.PI * this.r; }
  get display() { return Math.round(Math.max(0, Math.min(100, this.score))); }
  get dashOffset() { return this.circumference * (1 - this.display / 100); }

  get strokeColor(): string { return scoreHex(this.display); }

  /** Short radial ticks just outside the ring, like a dial face. */
  get ticks() {
    const out = [];
    for (let i = 0; i < 10; i++) {
      const a = (i / 10) * 2 * Math.PI - Math.PI / 2;
      const r1 = this.r + 5, r2 = this.r + 8;
      out.push({
        i,
        x1: this.cx + r1 * Math.cos(a), y1: this.cy + r1 * Math.sin(a),
        x2: this.cx + r2 * Math.cos(a), y2: this.cy + r2 * Math.sin(a),
      });
    }
    return out;
  }

  get transform(): string {
    return `rotate(-90 ${this.cx} ${this.cy})`;
  }
}
