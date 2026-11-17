import { inject } from '@angular/core';
import { Router, Routes } from '@angular/router';
import { ShellComponent } from './layout/shell/shell.component';

/** Old top-level workload pages now live under /workloads?view=…. */
const workloadRedirect = (view: string) => () =>
  inject(Router).createUrlTree(['/workloads'], { queryParams: { view } });

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
        path: 'containers',
        loadComponent: () =>
          import('./features/containers/container-list/container-list.component').then(
            m => m.ContainerListComponent
          ),
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
          import('./features/workloads/workloads.component').then(m => m.WorkloadsComponent),
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
      {
        path: 'archive',
        loadComponent: () =>
          import('./features/archive/archive.component').then(
            m => m.ArchiveComponent
          ),
      },

      // ── legacy links ──────────────────────────────────────────────────────
      {
        path: 'pods',
        pathMatch: 'full',
        redirectTo: () =>
          inject(Router).createUrlTree(['/containers'], { queryParams: { runtime: 'kubernetes' } }),
      },
      {
        path: 'pods/:id',
        redirectTo: ({ params }) => inject(Router).createUrlTree(['/containers', params['id']]),
      },
      { path: 'deployments', pathMatch: 'full', redirectTo: workloadRedirect('deployments') },
      { path: 'cronjobs', pathMatch: 'full', redirectTo: workloadRedirect('cronjobs') },
    ],
  },
];
