import { inject } from '@angular/core';
import { CanActivateFn, Router, Routes } from '@angular/router';
import { ShellComponent } from './layout/shell/shell.component';

/** Old top-level workload pages now live under /workloads. */
const workloadRedirect = (queryParams: Record<string, string>) => () =>
  inject(Router).createUrlTree(['/workloads'], { queryParams });

/** CronJobs and Jobs moved to /jobs: old `/workloads?kind=CronJob|Job` links land there. */
const jobsKindRedirect: CanActivateFn = route => {
  const kind = route.queryParamMap.get('kind');
  if (kind !== 'CronJob' && kind !== 'Job') return true;
  const qp: Record<string, string> = {};
  const q = route.queryParamMap.get('q');
  if (q) qp['q'] = q;
  return inject(Router).createUrlTree(['/jobs'], { queryParams: qp });
};

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
        path: 'issues',
        loadComponent: () =>
          import('./features/issues/issues.component').then(m => m.IssuesComponent),
      },
      {
        path: 'jobs',
        loadComponent: () =>
          import('./features/jobs/jobs.component').then(m => m.JobsComponent),
      },
      {
        path: 'workloads',
        canActivate: [jobsKindRedirect],
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
        path: 'clusters',
        loadComponent: () =>
          import('./features/clusters/clusters.component').then(m => m.ClustersComponent),
      },
      {
        path: 'settings',
        children: [
          { path: '', pathMatch: 'full', redirectTo: 'notifications' },
          {
            path: 'notifications',
            loadComponent: () =>
              import('./features/settings/notifications/notifications.component').then(m => m.NotificationsComponent),
          },
          {
            path: 'log-capture',
            loadComponent: () =>
              import('./features/settings/log-capture/log-capture.component').then(m => m.LogCaptureComponent),
          },
        ],
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
      { path: 'cronjobs', pathMatch: 'full', redirectTo: '/jobs' },
      {
        path: 'sources',
        pathMatch: 'full',
        redirectTo: ({ queryParams }) => inject(Router).createUrlTree(['/clusters'], { queryParams }),
      },
      { path: 'archive', pathMatch: 'full', redirectTo: '/workloads/archived' },
    ],
  },
];
