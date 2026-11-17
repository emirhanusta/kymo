import { Injectable, inject, signal, computed } from '@angular/core';
import { Subscription, forkJoin, of } from 'rxjs';
import { catchError, map, switchMap } from 'rxjs/operators';

import { ContainerService } from './container.service';
import { RefreshService } from './refresh.service';
import { SourceStateService } from './source-state.service';
import { WorkloadSummary } from '../models/container.model';
import { JobInfo, JobRow, buildJobRow } from '../../shared/utils/jobs';

const RUN_LIMIT = 20;

/**
 * Shared, app-wide snapshot of everything the Overview, Workloads and Jobs pages need. Always loads every recorded
 * cluster (removed ones included), system workloads included;
 * pages scope it client-side via SourceStateService.inScope and their own "show system" toggle. Reloads on the shared refresh bus.
 */
@Injectable({ providedIn: 'root' })
export class WorkloadStateService {
  private svc     = inject(ContainerService);
  private refresh = inject(RefreshService);
  private sources = inject(SourceStateService);

  readonly workloads = signal<WorkloadSummary[]>([]);
  readonly jobInfos  = signal<JobInfo[]>([]);
  readonly loaded = signal(false);
  readonly error  = signal(false);

  private sub?: Subscription;

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
        this.workloads.set(ws);
        this.jobInfos.set(infos);
        this.error.set(false);
        this.loaded.set(true);
        this.refresh.markUpdated();
      },
      error: () => {
        // Silent refresh failures keep the last good snapshot.
        if (!this.workloads().length) this.error.set(true);
        this.loaded.set(true);
      },
    });
  }
}
