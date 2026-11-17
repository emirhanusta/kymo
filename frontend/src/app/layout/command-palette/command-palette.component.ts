import {
  Component, ChangeDetectionStrategy, inject, input, output, signal, computed,
} from '@angular/core';
import { Router } from '@angular/router';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';
import { Subject, forkJoin, of } from 'rxjs';
import { catchError, debounceTime, map, switchMap } from 'rxjs/operators';

import { ContainerService } from '../../core/services/container.service';
import { SourceStateService } from '../../core/services/source-state.service';
import { Container, WorkloadSummary } from '../../core/models/container.model';
import { DialogDirective } from '../../shared/directives/dialog.directive';
import { WorkloadBadgeComponent } from '../../shared/components/workload-badge/workload-badge.component';
import { StatusBadgeComponent } from '../../shared/components/status-badge/status-badge.component';
import { ScoreClassPipe } from '../../shared/pipes/score-class.pipe';
import { containerRoute } from '../../shared/utils/format';
import { formatScore } from '../../shared/utils/score';

interface PaletteTab { path: string; label: string; }

type PaletteItem =
  | { kind: 'page'; key: string; label: string; path: string }
  | { kind: 'workload'; key: string; workload: WorkloadSummary }
  | { kind: 'container'; key: string; container: Container };

/**
 * ⌘K palette: pages (static) + containers from the API (debounced, latest request wins).
 * Arrow keys move the highlight, Enter opens, Escape closes (via appDialog).
 */
@Component({
  selector: 'app-command-palette',
  standalone: true,
  imports: [DialogDirective, StatusBadgeComponent, WorkloadBadgeComponent, ScoreClassPipe],
  templateUrl: './command-palette.component.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class CommandPaletteComponent {
  tabs   = input<PaletteTab[]>([]);
  closed = output<void>();

  private svc    = inject(ContainerService);
  private router = inject(Router);
  private scope  = inject(SourceStateService);

  query      = signal('');
  private foundContainers = signal<Container[]>([]);
  private foundWorkloads  = signal<WorkloadSummary[]>([]);
  /** Connected clusters only (or the removed cluster being viewed). */
  containers = computed(() => this.scope.globalScope(this.foundContainers(), c => c.source_name).slice(0, 8));
  workloads  = computed(() => this.scope.globalScope(this.foundWorkloads(), w => w.source_name).slice(0, 5));
  searching  = signal(false);
  active     = signal(0);

  readonly formatScore = formatScore;

  private query$ = new Subject<string>();

  pages = computed(() => {
    const q = this.query().trim().toLowerCase();
    return this.tabs().filter(t => !q || t.label.toLowerCase().includes(q) || t.path.includes(q));
  });

  items = computed<PaletteItem[]>(() => [
    ...this.pages().map(t => ({ kind: 'page' as const, key: 'p:' + t.path, label: t.label, path: t.path })),
    ...this.workloads().map(w => ({ kind: 'workload' as const, key: 'w:' + w.id, workload: w })),
    ...this.containers().map(c => ({ kind: 'container' as const, key: 'c:' + c.id, container: c })),
  ]);

  constructor() {
    this.query$.pipe(
      debounceTime(200),
      switchMap(q => {
        if (!q.trim()) return of({ workloads: [] as WorkloadSummary[], containers: [] as Container[] });
        this.searching.set(true);
        return forkJoin({
          workloads: this.svc.getWorkloads({ q }).pipe(
            map(list => list ?? []),
            catchError(() => of([] as WorkloadSummary[])),
          ),
          containers: this.svc.getContainersPage({ page: 1, limit: 16, q }).pipe(
            map(p => p?.data ?? []),
            catchError(() => of([] as Container[])),
          ),
        });
      }),
      takeUntilDestroyed(),
    ).subscribe(({ workloads, containers }) => {
      this.foundWorkloads.set(workloads);
      this.foundContainers.set(containers);
      this.searching.set(false);
      this.clampActive();
    });
  }

  onInput(value: string): void {
    this.query.set(value);
    this.active.set(0);
    if (!value.trim()) { this.foundContainers.set([]); this.foundWorkloads.set([]); }
    this.query$.next(value);
  }

  onKeydown(ev: KeyboardEvent): void {
    const n = this.items().length;
    switch (ev.key) {
      case 'ArrowDown':
        ev.preventDefault();
        if (n) this.active.update(i => (i + 1) % n);
        this.scrollActiveIntoView();
        break;
      case 'ArrowUp':
        ev.preventDefault();
        if (n) this.active.update(i => (i - 1 + n) % n);
        this.scrollActiveIntoView();
        break;
      case 'Enter': {
        ev.preventDefault();
        const item = this.items()[this.active()];
        if (item) this.open(item);
        break;
      }
    }
  }

  open(item: PaletteItem): void {
    this.closed.emit();
    if (item.kind === 'page') {
      // Page paths may carry a query string (e.g. /workloads?kind=CronJob).
      this.router.navigateByUrl(item.path);
    } else if (item.kind === 'workload') {
      this.router.navigate(['/workloads', item.workload.id]);
    } else {
      this.router.navigate(containerRoute(item.container.id, item.container.runtime));
    }
  }

  optionId(i: number): string { return `kymo-palette-opt-${i}`; }

  private clampActive(): void {
    const n = this.items().length;
    if (this.active() >= n) this.active.set(Math.max(0, n - 1));
  }

  private scrollActiveIntoView(): void {
    queueMicrotask(() => document.getElementById(this.optionId(this.active()))?.scrollIntoView({ block: 'nearest' }));
  }
}
