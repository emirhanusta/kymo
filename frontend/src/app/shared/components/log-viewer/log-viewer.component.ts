import {
  Component, ChangeDetectionStrategy, Input, Output, EventEmitter,
  signal, computed, OnDestroy, OnInit, inject, ElementRef, viewChild, AfterViewChecked,
} from '@angular/core';
import { DatePipe } from '@angular/common';
import { ContainerService } from '../../../core/services/container.service';
import { ToastService } from '../../../core/services/toast.service';
import { CrashLog } from '../../../core/models/container.model';
import { CrashLogModalComponent } from '../crash-log-modal/crash-log-modal.component';
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

/** The `{error, code, crash_logs}` body of a failed logs request (text responses arrive as a JSON string). */
function parseErrorBody(err: unknown): { code?: string; crash_logs?: number } | null {
  let b = (err as { error?: unknown } | null)?.error;
  if (typeof b === 'string') { try { b = JSON.parse(b); } catch { return null; } }
  return b && typeof b === 'object' ? b as { code?: string; crash_logs?: number } : null;
}

@Component({
  selector: 'app-log-viewer',
  standalone: true,
  imports: [DialogDirective, DatePipe, CrashLogModalComponent],
  templateUrl: './log-viewer.component.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class LogViewerComponent implements OnInit, OnDestroy, AfterViewChecked {
  @Input() containerId = '';
  @Input() containerName = '';
  /** Start in live (follow) mode. */
  @Input() live = false;
  /** Other containers of the same pod; with more than one entry the header shows a switcher. */
  @Input() containers: { id: string; name: string }[] = [];
  /** The pod is known to be gone: open straight in snapshot mode (no live attempt). */
  @Input() gone = false;
  @Output() closed = new EventEmitter<void>();

  private svc   = inject(ContainerService);
  private toast = inject(ToastService);

  /** Pod no longer exists in the cluster: show stored crash-log snapshots instead of logs. */
  podGone     = signal(false);
  snapshots   = signal<CrashLog[]>([]);
  snapLoading = signal(false);
  snapIdx     = signal(0);
  snapModal   = signal(false);
  /** Friendly error for a failed (non-410) logs request; null when fine. */
  errorMsg    = signal<string | null>(null);
  currentId   = signal('');
  currentName = signal('');
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
    this.currentId.set(this.containerId);
    this.currentName.set(this.containerName);
    if (this.gone) {
      this.enterGone();
    } else if (this.live) {
      this.follow.set(true);
      this.startStream();
    } else {
      this.fetchLogs();
    }
  }

  /** Header dropdown: reconnect to another container of the pod, keeping the live/static mode. */
  switchTo(id: string): void {
    const c = this.containers.find(x => x.id === id);
    if (!c || id === this.currentId()) return;
    this.stopStream();
    this.currentId.set(c.id);
    this.currentName.set(c.name);
    this.lines.set([]);
    this.podGone.set(false);
    this.errorMsg.set(null);
    if (this.gone) this.enterGone();
    else if (this.follow()) this.startStream(); else this.fetchLogs();
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

  /** Live attempt from the snapshot view ("try the cluster anyway"). */
  retryLive(): void {
    this.gone = false;
    this.podGone.set(false);
    this.fetchLogs();
  }

  fetchLogs(): void {
    this.loading.set(true);
    this.errorMsg.set(null);
    this.svc.getContainerLogs(this.currentId(), TAIL).subscribe({
      next: text => {
        this.lines.set(text.split('\n').filter(l => l.length > 0).slice(-MAX_LINES));
        this.loading.set(false);
        this.shouldScroll = true;
      },
      error: err => this.onLogsError(err),
    });
  }

  /** 410 pod_gone -> snapshot mode; anything else -> short friendly message with Retry. */
  private onLogsError(err: unknown): void {
    this.loading.set(false);
    this.stopStream();
    this.follow.set(false);
    const body = parseErrorBody(err);
    if ((err as { status?: number })?.status === 410 || body?.code === 'pod_gone') {
      this.enterGone(body?.crash_logs);
      return;
    }
    const status = (err as { status?: number })?.status;
    this.errorMsg.set(status === 0
      ? 'Could not reach KYMO. Check your connection and retry.'
      : 'Logs are unavailable right now. The cluster could not return them.');
  }

  private enterGone(count?: number): void {
    this.stopStream();
    this.follow.set(false);
    this.loading.set(false);
    this.errorMsg.set(null);
    this.lines.set([]);
    this.podGone.set(true);
    this.snapIdx.set(0);
    if (count === 0) { this.snapshots.set([]); return; }
    this.snapLoading.set(true);
    const id = this.currentId();
    this.svc.getContainerCrashLogs(id, 20).subscribe(l => {
      if (id !== this.currentId()) return;
      this.snapshots.set(l);
      this.snapLoading.set(false);
    });
  }

  selectedSnap(): CrashLog | null { return this.snapshots()[this.snapIdx()] ?? null; }
  snapTail(): string {
    const lines = (this.selectedSnap()?.lines ?? '').replace(/\n+$/, '').split('\n');
    return lines.slice(-MAX_LINES).join('\n');
  }

  startStream(): void {
    this.stopStream();
    this.lines.set([]);
    this.loading.set(false);
    this.es = new EventSource(this.svc.logStreamUrl(this.currentId(), TAIL));
    this.es.onmessage = (ev) => {
      this.lines.update(ls => {
        const next = [...ls, ev.data];
        return next.length > MAX_LINES ? next.slice(-MAX_LINES) : next;
      });
      this.shouldScroll = true;
    };
    this.es.onerror = () => {
      // No auto-reconnect: drop back to the static view. EventSource errors carry no body,
      // so one quick non-follow GET tells us why (pod gone vs. transient).
      this.stopStream();
      if (!this.follow()) return;
      this.follow.set(false);
      const id = this.currentId();
      this.svc.getContainerLogs(id, 1).subscribe({
        next: () => { if (id === this.currentId()) this.toast.info('Log stream ended'); },
        error: err => { if (id === this.currentId()) this.onLogsError(err); },
      });
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
    a.download = `${this.currentName()}-logs.txt`;
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
