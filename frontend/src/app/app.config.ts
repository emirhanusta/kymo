import { ApplicationConfig, inject, provideAppInitializer } from '@angular/core';
import { provideRouter, withComponentInputBinding, withRouterConfig } from '@angular/router';
import { provideHttpClient, withFetch, withInterceptors } from '@angular/common/http';

import { routes } from './app.routes';
import { apiInterceptor } from './core/interceptors/api.interceptor';
import { authInterceptor } from './core/interceptors/auth.interceptor';
import { AuthService } from './core/services/auth.service';

export const appConfig: ApplicationConfig = {
  providers: [
    provideRouter(routes, withComponentInputBinding(), withRouterConfig({ onSameUrlNavigation: 'reload' })),
    provideHttpClient(withFetch(), withInterceptors([authInterceptor, apiInterceptor])),
    // Load /auth/config + /auth/me before the first navigation so guards can decide synchronously.
    provideAppInitializer(() => inject(AuthService).init()),
  ],
};
