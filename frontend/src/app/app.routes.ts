import { inject } from '@angular/core';
import { Router, Routes } from '@angular/router';
import { ShellComponent } from './layout/shell/shell.component';

/** Old top-level workload pages now live under /workloads. */
const workloadRedirect = (queryParams: Record<string, string>) => () =>
  inject(Router).createUrlTree(['/workloads'], { queryParams });

export const routes: Routes = [
  {
    path: '',
    component: ShellComponent,
    children: [
      { path: '', redirectTo: 'dashboard', pathMatch: 'full' },
      {
        path: 'dashboard',
        loadComponent: () =>
          import('./features/dashboard/dashboard.component').then(m => m.DashboardComponent),
      },
      {
        path: 'containers/:id',
        loadComponent: () =>
          import('./features/containers/container-detail/container-detail.component').then(
            m => m.ContainerDetailComponent
          ),
      },
      {
        path: 'workloads',
        loadComponent: () =>
          import('./features/workloads/list/workload-list.component').then(m => m.WorkloadListComponent),
      },
      // Must precede `workloads/:id` so "archived" is not taken as a workload id.
      {
        path: 'workloads/archived',
        loadComponent: () =>
          import('./features/workloads/archived/archive.component').then(m => m.ArchiveComponent),
      },
      {
        path: 'workloads/:id',
        loadComponent: () =>
          import('./features/workloads/detail/workload-detail.component').then(m => m.WorkloadDetailComponent),
      },
      {
        path: 'events',
        loadComponent: () =>
          import('./features/events/events.component').then(m => m.EventsComponent),
      },
      {
        path: 'sources',
        loadComponent: () =>
          import('./features/sources/sources.component').then(m => m.SourcesComponent),
      },

      // ── legacy links ──────────────────────────────────────────────────────
      // The container list is gone: old filters map onto the workloads table (workload_kind→kind, workload→q).
      {
        path: 'containers',
        pathMatch: 'full',
        redirectTo: ({ queryParams }) => {
          const qp: Record<string, string> = {};
          if (queryParams['workload_kind']) qp['kind'] = queryParams['workload_kind'];
          const q = queryParams['workload'] || queryParams['q'];
          if (q) qp['q'] = q;
          if (queryParams['source']) qp['source'] = queryParams['source'];
          return inject(Router).createUrlTree(['/workloads'], { queryParams: qp });
        },
      },
      { path: 'pods', pathMatch: 'full', redirectTo: workloadRedirect({}) },
      {
        path: 'pods/:id',
        redirectTo: ({ params }) => inject(Router).createUrlTree(['/containers', params['id']]),
      },
      { path: 'deployments', pathMatch: 'full', redirectTo: workloadRedirect({ kind: 'Deployment' }) },
      { path: 'cronjobs', pathMatch: 'full', redirectTo: workloadRedirect({ kind: 'CronJob' }) },
      { path: 'archive', pathMatch: 'full', redirectTo: '/workloads/archived' },
    ],
  },
];
