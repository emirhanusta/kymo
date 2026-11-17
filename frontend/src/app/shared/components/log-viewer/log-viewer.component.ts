import {
  Component, ChangeDetectionStrategy, Input, Output, EventEmitter,
  signal, computed, OnDestroy, OnInit, inject, ElementRef, viewChild, AfterViewChecked,
} from '@angular/core';
import { ContainerService } from '../../../core/services/container.service';
import { ToastService } from '../../../core/services/toast.service';
import { DialogDirective } from '../../directives/dialog.directive';

const MAX_LINES = 500;
const TAIL = 200;

interface LinePart { text: string; match: boolean; }
interface LogRow { idx: number; cls: string; parts: LinePart[]; }

function lineClass(line: string): string {
  const l = line.toLowerCase();
  if (/\b(error|err|fatal|exception|panic)\b/.test(l)) return 'text-kymo-danger';
  if (/\b(warn|warning)\b/.test(l))                     return 'text-kymo-amber';
  return 'text-kymo-ink/80';
}

function splitMatches(line: string, q: string): LinePart[] {
  const lower = line.toLowerCase();
  const parts: LinePart[] = [];
  let pos = 0;
  let hit = lower.indexOf(q, pos);
  while (hit !== -1) {
    if (hit > pos) parts.push({ text: line.slice(pos, hit), match: false });
    parts.push({ text: line.slice(hit, hit + q.length), match: true });
    pos = hit + q.length;
    hit = lower.indexOf(q, pos);
  }
  if (pos < line.length) parts.push({ text: line.slice(pos), match: false });
  return parts;
}

@Component({
  selector: 'app-log-viewer',
  standalone: true,
  imports: [DialogDirective],
  templateUrl: './log-viewer.component.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class LogViewerComponent implements OnInit, OnDestroy, AfterViewChecked {
  @Input() containerId = '';
  @Input() containerName = '';
  @Output() closed = new EventEmitter<void>();

  private svc   = inject(ContainerService);
  private toast = inject(ToastService);

  lines       = signal<string[]>([]);
  loading     = signal(true);
  follow      = signal(false);
  wrap        = signal(true);
  query       = signal('');
  onlyMatches = signal(false);

  view = computed(() => {
    const q = this.query().trim().toLowerCase();
    const only = this.onlyMatches();
    const rows: LogRow[] = [];
    let matches = 0;
    this.lines().forEach((line, idx) => {
      if (!q) {
        rows.push({ idx, cls: lineClass(line), parts: [{ text: line, match: false }] });
        return;
      }
      const parts = splitMatches(line, q);
      const matched = parts.some(p => p.match);
      if (matched) matches++;
      if (only && !matched) return;
      rows.push({ idx, cls: lineClass(line), parts });
    });
    return { rows, matches, hasQuery: !!q };
  });

  private es: EventSource | null = null;
  private shouldScroll = false;

  logBody = viewChild<ElementRef<HTMLElement>>('logBody');

  ngOnInit(): void {
    this.fetchLogs();
  }

  toggleFollow(): void {
    this.follow.update(f => !f);
    if (this.follow()) {
      this.startStream();
    } else {
      this.stopStream();
      this.fetchLogs();
    }
  }

  fetchLogs(): void {
    this.loading.set(true);
    this.svc.getContainerLogs(this.containerId, TAIL).subscribe({
      next: text => {
        this.lines.set(text.split('\n').filter(l => l.length > 0).slice(-MAX_LINES));
        this.loading.set(false);
        this.shouldScroll = true;
      },
      error: err => {
        this.toast.error(err?.error?.message || 'Failed to fetch logs');
        this.loading.set(false);
      },
    });
  }

  startStream(): void {
    this.stopStream();
    this.lines.set([]);
    this.loading.set(false);
    this.es = new EventSource(this.svc.logStreamUrl(this.containerId, TAIL));
    this.es.onmessage = (ev) => {
      this.lines.update(ls => {
        const next = [...ls, ev.data];
        return next.length > MAX_LINES ? next.slice(-MAX_LINES) : next;
      });
      this.shouldScroll = true;
    };
    this.es.onerror = () => {
      // No auto-reconnect: drop back to the static view and tell the user.
      this.stopStream();
      if (this.follow()) {
        this.follow.set(false);
        this.toast.info('Log stream ended');
      }
    };
  }

  stopStream(): void {
    if (this.es) {
      this.es.close();
      this.es = null;
    }
  }

  clear(): void {
    this.lines.set([]);
  }

  download(): void {
    const blob = new Blob([this.lines().join('\n')], { type: 'text/plain' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `${this.containerName}-logs.txt`;
    a.click();
    URL.revokeObjectURL(url);
  }

  close(): void {
    this.stopStream();
    this.closed.emit();
  }

  ngAfterViewChecked(): void {
    if (this.shouldScroll) {
      this.shouldScroll = false;
      const el = this.logBody()?.nativeElement;
      if (el) el.scrollTop = el.scrollHeight;
    }
  }

  ngOnDestroy(): void {
    this.stopStream();
  }
}
