import { inject } from '@angular/core';
import { CanActivateFn, Route, Router, Routes } from '@angular/router';
import { ShellComponent } from './layout/shell/shell.component';
import { AuthLayoutComponent } from './layout/auth-layout/auth-layout.component';
import { publicOnly, requirePasswordChange, requirePermission, requireUser } from './core/guards/auth.guards';

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

function authPage(path: string, loadComponent: Route['loadComponent'], canActivate: CanActivateFn[] = []): Route {
  return { path, component: AuthLayoutComponent, canActivate, children: [{ path: '', loadComponent }] };
}

export const routes: Routes = [
  // Public auth pages: minimal layout, no topbar / event tape. Each page is its
  // own top-level route so the empty path always belongs to the app shell.
  authPage('login', () => import('./features/auth/login.component').then(m => m.LoginComponent), [publicOnly('login')]),
  { path: 'setup', redirectTo: 'login', pathMatch: 'full' },
  authPage('change-password', () => import('./features/auth/change-password.component').then(m => m.ChangePasswordComponent), [requirePasswordChange]),
  authPage('invite/:token', () => import('./features/auth/link.component').then(m => m.LinkComponent)),
  authPage('reset/:token', () => import('./features/auth/link.component').then(m => m.LinkComponent)),
  {
    path: '',
    component: ShellComponent,
    canActivate: [requireUser],
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
        loadComponent: () =>
          import('./features/settings/settings-layout.component').then(m => m.SettingsLayoutComponent),
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
          {
            path: 'users',
            canActivate: [requirePermission('users:manage')],
            loadComponent: () =>
              import('./features/settings/users/users.component').then(m => m.UsersComponent),
          },
          {
            path: 'tokens',
            loadComponent: () =>
              import('./features/settings/tokens/tokens.component').then(m => m.TokensComponent),
          },
          {
            path: 'audit',
            canActivate: [requirePermission('audit:read')],
            loadComponent: () =>
              import('./features/settings/audit/audit.component').then(m => m.AuditComponent),
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
