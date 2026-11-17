import { Injectable, inject, computed } from '@angular/core';

import { WorkloadStateService } from './workload-state.service';
import { Issue, WorkloadEventInfo, attributeEvents, deriveIssues, statusReason } from '../../shared/utils/issues';
import { WorkloadSummary } from '../models/container.model';

/**
 * "What is broken right now", derived client-side from the shared workload snapshot (see shared/utils/issues.ts).
 * `all` covers every cluster and includes system workloads; use the `scoped*`/`visible` helpers for pages.
 */
@Injectable({ providedIn: 'root' })
export class IssuesService {
  private state = inject(WorkloadStateService);

  readonly eventInfo = computed<ReadonlyMap<string, WorkloadEventInfo>>(() =>
    attributeEvents(this.state.events(), this.state.workloads(), this.state.containerWorkloads()));

  /** Every issue, every cluster, system workloads included. */
  readonly all = computed<Issue[]>(() =>
    deriveIssues(this.state.workloads(), this.state.jobRows(), this.eventInfo()));

  /** Issues of the top-bar cluster, system workloads hidden. */
  readonly open = computed(() => this.forCluster(this.state.selectedCluster(), false));

  /** Number shown on the Issues tab. */
  readonly openCount = computed(() => this.open().length);

  /** Issues for one cluster ('' = all). */
  forCluster(cluster: string, includeSystem: boolean): Issue[] {
    return this.all().filter(i => (!cluster || i.source === cluster) && (includeSystem || !i.isSystem));
  }

  /** "OOMKilled" / "CrashLoop · exit 1" / "Crashing" / plain status. */
  reasonOf(w: WorkloadSummary): string {
    return statusReason(w, this.eventInfo().get(w.id));
  }
}
