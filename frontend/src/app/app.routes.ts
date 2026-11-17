import { Routes } from '@angular/router';
import { ShellComponent } from './layout/shell/shell.component';

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
        path: 'pods',
        loadComponent: () =>
          import('./features/pods/pods.component').then(m => m.PodsComponent),
      },
      {
        path: 'pods/:id',
        loadComponent: () =>
          import('./features/containers/container-detail/container-detail.component').then(
            m => m.ContainerDetailComponent
          ),
      },
      {
        path: 'cronjobs',
        loadComponent: () =>
          import('./features/cronjobs/cronjobs.component').then(
            m => m.CronJobsComponent
          ),
      },
      {
        path: 'deployments',
        loadComponent: () =>
          import('./features/deployments/deployment-manager.component').then(
            m => m.DeploymentManagerComponent
          ),
      },
      {
        path: 'archive',
        loadComponent: () =>
          import('./features/archive/archive.component').then(
            m => m.ArchiveComponent
          ),
      },
    ],
  },
];
