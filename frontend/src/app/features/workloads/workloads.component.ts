import { Component, ChangeDetectionStrategy, computed, inject } from '@angular/core';
import { ActivatedRoute, RouterLink } from '@angular/router';
import { toSignal } from '@angular/core/rxjs-interop';
import { map } from 'rxjs/operators';

import { SourceStateService } from '../../core/services/source-state.service';
import { SourceType } from '../../core/models/container.model';
import { PageHeaderComponent } from '../../shared/components/page-header/page-header.component';
import { EmptyStateComponent } from '../../shared/components/empty-state/empty-state.component';
import { DeploymentManagerComponent } from './deployments/deployment-manager.component';
import { CronJobsComponent } from './cronjobs/cronjobs.component';
import { ComposeComponent } from './compose/compose.component';

export type WorkloadView = 'deployments' | 'cronjobs' | 'compose';

interface ViewDef { key: WorkloadView; label: string; type: SourceType; }

const VIEWS: ViewDef[] = [
  { key: 'deployments', label: 'Deployments', type: 'kubernetes' },
  { key: 'cronjobs',    label: 'CronJobs',    type: 'kubernetes' },
  { key: 'compose',     label: 'Compose',     type: 'docker' },
];

const isView = (v: string | null): v is WorkloadView => VIEWS.some(d => d.key === v);

/** Workloads: sub-tabs (?view=) over Kubernetes Deployments / CronJobs and Docker Compose projects. */
@Component({
  selector: 'app-workloads',
  standalone: true,
  imports: [
    RouterLink, PageHeaderComponent, EmptyStateComponent,
    DeploymentManagerComponent, CronJobsComponent, ComposeComponent,
  ],
  template: `
    <div class="space-y-4">
      <app-page-header title="Workloads" subtitle="Kubernetes deployments and cronjobs, Docker Compose projects" />

      <nav class="flex items-stretch border-b border-kymo-rule -mt-2 overflow-x-auto scrollbar-none" aria-label="Workload views">
        @for (v of views; track v.key) {
          <a
            [routerLink]="[]"
            [queryParams]="{ view: v.key }"
            class="flex items-center gap-2 px-3 h-9 text-xs font-medium whitespace-nowrap border-b-2 -mb-px transition-colors"
            [class]="view() === v.key ? 'text-kymo-amber border-kymo-amber' : 'text-kymo-muted border-transparent hover:text-kymo-ink'"
            [attr.aria-current]="view() === v.key ? 'page' : null"
          >
            {{ v.label }}
            <span class="k-label !text-[10px] !text-kymo-faint">{{ v.type === 'kubernetes' ? 'k8s' : 'docker' }}</span>
          </a>
        }
      </nav>

      @switch (availability()) {
        @case ('none') {
          <div class="k-panel">
            <app-empty-state
              icon="box"
              [message]="'No ' + typeLabel() + ' source configured'"
              [hint]="viewDef().label + ' need a ' + typeLabel() + ' source. Add one on the Sources page to start recording.'"
            >
              <a routerLink="/sources" [queryParams]="{ add: 1 }" class="k-btn k-btn-primary k-btn-sm mt-4">Add a source</a>
            </app-empty-state>
          </div>
        }
        @case ('wrong-type') {
          <div class="k-panel">
            <app-empty-state
              icon="search"
              [message]="viewDef().label + ' are not available for ' + sources.selectedSource()"
              [hint]="'The selected source is not a ' + typeLabel() + ' source. Show all sources or pick a ' + typeLabel() + ' source in the top bar.'"
            >
              <div class="flex items-center gap-2 mt-4">
                <button type="button" class="k-btn k-btn-primary k-btn-sm" (click)="sources.select('')">Show all sources</button>
                <a routerLink="/sources" class="k-btn k-btn-sm">Sources</a>
              </div>
            </app-empty-state>
          </div>
        }
        @case ('pending') {
          <div class="h-24"></div>
        }
        @default {
          @switch (view()) {
            @case ('deployments') { <app-deployment-manager /> }
            @case ('cronjobs')    { <app-cronjobs /> }
            @case ('compose')     { <app-compose /> }
          }
        }
      }
    </div>
  `,
  host: { class: 'block' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class WorkloadsComponent {
  private route  = inject(ActivatedRoute);
  protected sources = inject(SourceStateService);

  readonly views = VIEWS;

  private viewParam = toSignal(this.route.queryParamMap.pipe(map(p => p.get('view'))), { initialValue: null });

  /** Explicit ?view= wins; otherwise Deployments, or Compose when only Docker sources exist. */
  view = computed<WorkloadView>(() => {
    const v = this.viewParam();
    if (isView(v)) return v;
    return !this.sources.hasKubernetes() && this.sources.hasDocker() ? 'compose' : 'deployments';
  });

  viewDef   = computed(() => VIEWS.find(v => v.key === this.view())!);
  typeLabel = computed(() => this.viewDef().type === 'kubernetes' ? 'Kubernetes' : 'Docker');

  /** Whether the current view has an applicable source (empty state instead of an API error). */
  availability = computed<'ok' | 'none' | 'wrong-type' | 'pending'>(() => {
    if (!this.sources.loaded()) return 'pending';
    if (this.sources.sourcesError()) return 'ok'; // can't tell — let the view try (and show its own error)
    const type = this.viewDef().type;
    if (!this.sources.sources().some(s => s.type === type)) return 'none';
    const sel = this.sources.selectedSourceInfo();
    if (sel && sel.type !== type) return 'wrong-type';
    return 'ok';
  });
}
