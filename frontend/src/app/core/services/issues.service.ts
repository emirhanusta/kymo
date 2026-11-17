import { Injectable, inject, computed } from '@angular/core';

import { SourceStateService } from './source-state.service';
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
  private sources = inject(SourceStateService);

  /** Removed clusters are frozen at their last status, so they never produce issues. */
  private liveWorkloads = computed(() => this.sources.connectedOnly(this.state.workloads(), w => w.source_name));
  private liveJobRows = computed(() => this.sources.connectedOnly(this.state.jobRows(), r => r.w.source_name));

  readonly eventInfo = computed<ReadonlyMap<string, WorkloadEventInfo>>(() =>
    attributeEvents(this.state.events(), this.liveWorkloads(), this.state.containerWorkloads()));

  private liveK8sWarnings = computed(() => this.sources.connectedOnly(this.state.k8sWarnings(), e => e.source_name));

  /** Every issue of the connected clusters, system workloads included (status-derived + Kubernetes Warning events). */
  readonly all = computed<Issue[]>(() =>
    deriveIssues(this.liveWorkloads(), this.liveJobRows(), this.eventInfo(), this.liveK8sWarnings()));

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
