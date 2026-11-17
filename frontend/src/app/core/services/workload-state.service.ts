import { Injectable, inject, signal, computed } from '@angular/core';
import { Subscription, forkJoin, of } from 'rxjs';
import { catchError, map, switchMap } from 'rxjs/operators';

import { ContainerService } from './container.service';
import { RefreshService } from './refresh.service';
import { SourceStateService } from './source-state.service';
import { K8sEvent, LifecycleEvent, WorkloadSummary } from '../models/container.model';
import { JobInfo, JobRow, buildJobRow } from '../../shared/utils/jobs';
import { K8S_ISSUE_WINDOW_MS, unmatchedContainerIds } from '../../shared/utils/issues';

const RUN_LIMIT = 20;
/** Cap on one-off GET /containers/:id lookups for events that cannot be attributed by pod name. */
const MAX_LOOKUPS = 25;

/**
 * Shared, app-wide snapshot of everything the Overview, Workloads, Jobs and Issues pages (and the top-bar
 * issue badge) need. Always loads every recorded cluster (removed ones included), system workloads included;
 * pages scope it client-side via SourceStateService.inScope and their own "show system" toggle. Reloads on the shared refresh bus.
 */
@Injectable({ providedIn: 'root' })
export class WorkloadStateService {
  private svc     = inject(ContainerService);
  private refresh = inject(RefreshService);
  private sources = inject(SourceStateService);

  readonly workloads = signal<WorkloadSummary[]>([]);
  readonly jobInfos  = signal<JobInfo[]>([]);
  /** Recent OOM + crash events, newest first. */
  readonly events    = signal<LifecycleEvent[]>([]);
  /** Warning Kubernetes events of the last 30 minutes (all clusters), the input of the k8s-derived issues. */
  readonly k8sWarnings = signal<K8sEvent[]>([]);
  /** container id → exact workload id (null = unknown), for events pod-name matching could not place. */
  readonly containerWorkloads = signal<ReadonlyMap<string, string | null>>(new Map());

  readonly loaded = signal(false);
  readonly error  = signal(false);

  private sub?: Subscription;
  private lookupSub?: Subscription;

  readonly selectedCluster = this.sources.selectedSource;

  /** Workloads of the cluster picked in the top bar (all when none). */
  readonly scopedWorkloads = computed(() => this.inScope(this.workloads(), w => w.source_name));

  /** One row per Job/CronJob workload (view-model with run cells, success rate, health). */
  readonly jobRows = computed<JobRow[]>(() => this.jobInfos().map(buildJobRow));
  readonly scopedJobRows = computed(() => this.inScope(this.jobRows(), r => r.w.source_name));

  constructor() {
    this.load();
    this.refresh.refresh$.subscribe(() => this.load());
  }

  /** Cluster scoping is centralised in SourceStateService (connected clusters only, unless one is selected). */
  inScope<T>(list: T[], source: (x: T) => string): T[] {
    return this.sources.inScope(list, source);
  }

  load(): void {
    this.sub?.unsubscribe();
    this.sub = forkJoin({
      workloads: this.svc.getWorkloads({ system: true }),
      oom:   this.svc.queryRecentEvents({ type: 'oom', limit: 100 }).pipe(catchError(() => of(null))),
      crash: this.svc.queryRecentEvents({ type: 'crash', limit: 100 }).pipe(catchError(() => of(null))),
      // One request per refresh; older backends without /k8s-events simply yield no extra issues.
      k8s: this.svc.queryK8sEvents({
        type: 'Warning', since: new Date(Date.now() - K8S_ISSUE_WINDOW_MS).toISOString(), limit: 500,
      }).pipe(catchError(() => of(null))),
    }).pipe(
      switchMap(base => {
        const ws = (base.workloads ?? []).map(w => ({ ...w, images: w.images ?? [], containers: w.containers ?? [] }));
        const crons = ws.filter(w => w.kind === 'CronJob');
        const jobs  = ws.filter(w => w.kind === 'Job' && !w.is_system);
        return forkJoin({
          runs: crons.length
            ? forkJoin(crons.map(w => this.svc.getCronJobExecutions(w.namespace ?? '', w.name, RUN_LIMIT, w.source_name)))
            : of([]),
          pods: jobs.length
            ? forkJoin(jobs.map(w => this.svc.getWorkload(w.id).pipe(catchError(() => of(null)))))
            : of([]),
        }).pipe(map(x => ({ base, ws, crons, jobs, ...x })));
      }),
    ).subscribe({
      next: ({ base, ws, crons, jobs, runs, pods }) => {
        const infos: JobInfo[] = [
          ...crons.map((w, i) => ({ workload: w, executions: runs[i] ?? [], instances: [] })),
          ...jobs.map((w, i) => ({ workload: w, executions: [], instances: pods[i]?.instances ?? [] })),
          // Jobs we did not fetch pods for (system) still appear, without run data.
          ...ws.filter(w => w.kind === 'Job' && w.is_system).map(w => ({ workload: w, executions: [], instances: [] })),
        ];
        const events = [...(base.oom ?? []), ...(base.crash ?? [])]
          .sort((a, b) => new Date(b.occurred_at).getTime() - new Date(a.occurred_at).getTime());
        this.workloads.set(ws);
        this.jobInfos.set(infos);
        this.events.set(events);
        if (base.k8s) this.k8sWarnings.set(base.k8s);
        this.error.set(false);
        this.loaded.set(true);
        this.refresh.markUpdated();
        this.resolveUnmatched(events, ws);
      },
      error: () => {
        // Silent refresh failures keep the last good snapshot.
        if (!this.workloads().length) this.error.set(true);
        this.loaded.set(true);
      },
    });
  }

  /** Looks up the owning workload of events whose pod name matches no known workload (bounded, cached). */
  private resolveUnmatched(events: LifecycleEvent[], ws: WorkloadSummary[]): void {
    const cache = this.containerWorkloads();
    const ids = unmatchedContainerIds(events, ws, cache).slice(0, MAX_LOOKUPS);
    if (!ids.length) return;
    this.lookupSub?.unsubscribe();
    this.lookupSub = forkJoin(ids.map(id =>
      this.svc.getContainer(id).pipe(map(c => [id, c.workload_id ?? null] as const), catchError(() => of([id, null] as const))),
    )).subscribe(pairs => {
      const next = new Map(this.containerWorkloads());
      for (const [id, wid] of pairs) next.set(id, wid);
      this.containerWorkloads.set(next);
    });
  }
}
