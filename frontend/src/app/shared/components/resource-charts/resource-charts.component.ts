import {
  Component, ChangeDetectionStrategy, input, computed, effect,
  viewChild, ElementRef, inject, DestroyRef, untracked,
} from '@angular/core';
import {
  Chart, ChartOptions, LineController, LineElement, PointElement,
  LinearScale, TimeScale, Filler, Tooltip, Legend, TooltipItem,
} from 'chart.js';
import 'chartjs-adapter-date-fns';
import { ResourceSample } from '../../../core/models/container.model';
import { ThemeService } from '../../../core/services/theme.service';
import { formatBytes, formatRate } from '../../utils/format';

Chart.register(LineController, LineElement, PointElement, LinearScale, TimeScale, Filler, Tooltip, Legend);

type Point = { x: number; y: number };
type TimeUnit = 'minute' | 'hour' | 'day';

type Palette = Record<'grid' | 'rule' | 'panel' | 'ink' | 'muted' | 'amber' | 'green' | 'blue', string>;

/** Reads the --color-kymo-* tokens (canvas can't use CSS vars); call again after a theme change. */
function readPalette(): Palette {
  const cs = getComputedStyle(document.documentElement);
  const v = (n: string) => cs.getPropertyValue(`--color-kymo-${n}`).trim();
  return {
    grid: v('grid'), rule: v('rule'), panel: v('panel'), ink: v('ink'), muted: v('muted'),
    amber: v('amber'), green: v('running'), blue: v('paused'),
  };
}
const FONT_FAMILY = '"IBM Plex Mono", ui-monospace, monospace';
const TICK_FONT  = { size: 10, family: FONT_FAMILY };
const HOUR = 3_600_000;

const ts = (s: ResourceSample) => new Date(s.sampled_at).getTime();

const MINUTE = 60_000;

/** CPU axis labels with precision that keeps small values distinct (0.05%, 0.1%, 12%). */
function cpuTick(v: number): string {
  const abs = Math.abs(v);
  const digits = abs === 0 ? 0 : abs < 1 ? 2 : abs < 10 ? 1 : 0;
  return `${+v.toFixed(digits)}%`;
}

/** Pick an x-axis unit that gives a sensible number of ticks for the visible span. */
function unitFor(spanMs: number): TimeUnit {
  if (spanMs <= 3 * HOUR) return 'minute';
  if (spanMs <= 72 * HOUR) return 'hour';
  return 'day';
}

/**
 * Convert cumulative byte counters to a per-second rate.
 * Negative deltas mean the counter reset (container restart) and are skipped.
 */
export function counterRate(samples: ResourceSample[], key: 'net_rx_bytes' | 'net_tx_bytes'): Point[] {
  const out: Point[] = [];
  for (let i = 1; i < samples.length; i++) {
    const prev = samples[i - 1][key];
    const cur  = samples[i][key];
    if (prev == null || cur == null) continue;
    const dt = (ts(samples[i]) - ts(samples[i - 1])) / 1000;
    const delta = cur - prev;
    if (dt <= 0 || delta < 0) continue;
    out.push({ x: ts(samples[i]), y: delta / dt });
  }
  return out;
}

function dataset(label: string, data: Point[], color: string, fill = true) {
  return {
    label,
    data,
    borderColor: color,
    backgroundColor: color + '14', // faint flat fill
    fill,
    tension: 0,
    pointRadius: 0,
    pointHoverRadius: 3,
    borderWidth: 1.5,
  };
}

function options(
  C: Palette, unit: TimeUnit, min: number | undefined, max: number | undefined,
  yTick: (v: number) => string, yValue: (v: number) => string, legend = false,
): ChartOptions<'line'> {
  return {
    responsive: true,
    maintainAspectRatio: false,
    animation: false,
    interaction: { mode: 'index', intersect: false },
    scales: {
      x: {
        type: 'time',
        min, max,
        time: {
          unit,
          tooltipFormat: 'MMM d, HH:mm:ss',
          displayFormats: { minute: 'HH:mm', hour: 'HH:mm', day: 'MMM d' },
        },
        grid: { color: C.grid, tickColor: C.grid },
        border: { color: C.rule },
        ticks: { color: C.muted, font: TICK_FONT, maxRotation: 0, autoSkip: true, maxTicksLimit: 8 },
      },
      y: {
        // No hard max: multi-core containers legitimately exceed 100% CPU.
        beginAtZero: true,
        grace: '10%',
        grid: { color: C.grid, tickColor: C.grid },
        border: { color: C.rule },
        ticks: { color: C.muted, font: TICK_FONT, maxTicksLimit: 6, callback: v => yTick(+v) },
      },
    },
    plugins: {
      legend: {
        display: legend,
        position: 'top',
        align: 'end',
        labels: { color: C.muted, boxWidth: 10, boxHeight: 1, font: { size: 10, family: FONT_FAMILY } },
      },
      tooltip: {
        backgroundColor: C.panel,
        borderColor: C.rule,
        borderWidth: 1,
        cornerRadius: 0,
        caretSize: 0,
        displayColors: false,
        titleColor: C.muted,
        bodyColor: C.ink,
        titleFont: { size: 11, family: FONT_FAMILY },
        bodyFont: { size: 11, family: FONT_FAMILY },
        callbacks: {
          label: (ctx: TooltipItem<'line'>) => ` ${ctx.dataset.label}: ${yValue(ctx.parsed.y ?? 0)}`,
        },
      },
    },
  };
}

@Component({
  selector: 'app-resource-charts',
  standalone: true,
  templateUrl: './resource-charts.component.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ResourceChartsComponent {
  samples = input<ResourceSample[]>([]);
  /** Visible window (epoch ms). Falls back to the sample extent. */
  from = input<number | null>(null);
  to   = input<number | null>(null);
  /** Hide the network panel entirely (e.g. aggregated series without network counters). */
  hideNetwork = input(false);

  private theme = inject(ThemeService);
  private cpuCanvas = viewChild<ElementRef<HTMLCanvasElement>>('cpuCanvas');
  private memCanvas = viewChild<ElementRef<HTMLCanvasElement>>('memCanvas');
  private netCanvas = viewChild<ElementRef<HTMLCanvasElement>>('netCanvas');

  private charts: Record<'cpu' | 'mem' | 'net', Chart<'line', Point[]> | null> = { cpu: null, mem: null, net: null };

  sorted = computed(() => [...this.samples()].sort((a, b) => ts(a) - ts(b)));
  rx     = computed(() => counterRate(this.sorted(), 'net_rx_bytes'));
  tx     = computed(() => counterRate(this.sorted(), 'net_tx_bytes'));
  hasNet = computed(() => this.rx().length > 0 || this.tx().length > 0);

  latest = computed(() => {
    const s = this.sorted();
    const last = s[s.length - 1];
    const rx = this.rx(), tx = this.tx();
    return {
      cpu: last ? `${last.cpu_percent.toFixed(1)}%` : '—',
      mem: last ? formatBytes(last.mem_bytes) : '—',
      rx:  rx.length ? formatRate(rx[rx.length - 1].y) : '—',
      tx:  tx.length ? formatRate(tx[tx.length - 1].y) : '—',
    };
  });

  constructor() {
    effect(() => {
      this.theme.theme(); // re-render with fresh palette on theme change
      const s = this.sorted();
      const rx = this.rx();
      const tx = this.tx();
      const canvases = { cpu: this.cpuCanvas(), mem: this.memCanvas(), net: this.netCanvas() };
      // Fit the x-axis to the data when history is shorter than the selected
      // range (e.g. a container first seen 5 minutes ago in the 24h view), so
      // the traces aren't squeezed against the right edge.
      const rangeFrom = this.from();
      const firstTs = s.length ? ts(s[0]) : undefined;
      const from = firstTs === undefined
        ? rangeFrom ?? undefined
        : Math.max(rangeFrom ?? -Infinity, firstTs - MINUTE);
      const to   = this.to()   ?? (s.length ? ts(s[s.length - 1]) : undefined);
      const unit = unitFor(from !== undefined && to !== undefined ? to - from : HOUR);

      untracked(() => {
        const C = readPalette();
        this.render('cpu', canvases.cpu,
          [dataset('CPU', s.map(r => ({ x: ts(r), y: r.cpu_percent })), C.amber)],
          options(C, unit, from, to, v => cpuTick(+v), v => `${v.toFixed(2)}%`));
        this.render('mem', canvases.mem,
          [dataset('Memory', s.map(r => ({ x: ts(r), y: r.mem_bytes })), C.green)],
          options(C, unit, from, to, v => formatBytes(v, 0), v => formatBytes(v, 1)));
        this.render('net', canvases.net,
          [dataset('RX', rx, C.blue, false), dataset('TX', tx, C.green, false)],
          options(C, unit, from, to, v => formatRate(v), v => formatRate(v), true));
      });
    });

    inject(DestroyRef).onDestroy(() => {
      Object.values(this.charts).forEach(c => c?.destroy());
    });
  }

  private render(
    key: 'cpu' | 'mem' | 'net',
    canvas: ElementRef<HTMLCanvasElement> | undefined,
    datasets: ReturnType<typeof dataset>[],
    opts: ChartOptions<'line'>,
  ): void {
    if (!canvas) return;
    const existing = this.charts[key];
    if (existing && existing.canvas === canvas.nativeElement) {
      existing.data.datasets = datasets;
      existing.options = opts;
      existing.update('none');
      return;
    }
    existing?.destroy();
    this.charts[key] = new Chart<'line', Point[]>(canvas.nativeElement, {
      type: 'line',
      data: { datasets },
      options: opts,
    });
  }
}
