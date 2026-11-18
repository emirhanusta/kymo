import { HttpErrorResponse, HttpInterceptorFn } from '@angular/common/http';
import { inject } from '@angular/core';
import { Router } from '@angular/router';
import { throwError } from 'rxjs';
import { catchError } from 'rxjs/operators';

import { API_BASE } from '../services/container.service';
import { AuthService } from '../services/auth.service';
import { ToastService } from '../services/toast.service';
import { changePasswordTree } from '../guards/auth.guards';

/**
 * Cookie sessions + CSRF header. API_BASE is relative (same-origin), so cookies travel by default;
 * withCredentials is only added when the API base were ever an absolute cross-origin URL.
 */
const CROSS_ORIGIN = /^https?:\/\//i.test(API_BASE) && !API_BASE.startsWith(typeof location !== 'undefined' ? location.origin : '\0');

export const authInterceptor: HttpInterceptorFn = (req, next) => {
  const auth = inject(AuthService);
  const router = inject(Router);
  const toast = inject(ToastService);

  const setHeaders: Record<string, string> = {};
  if (req.method !== 'GET' && req.method !== 'HEAD' && req.method !== 'OPTIONS') setHeaders['X-KYMO-Request'] = '1';
  const cloned = req.clone({ setHeaders, ...(CROSS_ORIGIN ? { withCredentials: true } : {}) });
  const isAuthCall = req.url.includes('/auth/');

  return next(cloned).pipe(
    catchError(err => {
      if (err instanceof HttpErrorResponse) {
        if (err.status === 401 && !isAuthCall) {
          auth.clear();
          const ret = router.url;
          if (!ret.startsWith('/login')) router.navigate(['/login'], { queryParams: ret && ret !== '/' ? { return: ret } : {} });
        } else if (err.status === 403 && (err.error as { code?: string } | null)?.code === 'password_change_required') {
          // The server says a password change is pending: resync state, then send the user to the change page.
          const ret = router.url;
          if (!ret.startsWith('/change-password')) {
            auth.loadMe().subscribe(() => {
              if (auth.user()?.must_change_password) router.navigateByUrl(changePasswordTree(router, ret));
            });
          }
        } else if (err.status === 403 && auth.user()) {
          // Role refusals get a role-specific toast (apiInterceptor skips its generic one for these).
          const body = err.error as { code?: string; required_role?: string } | null;
          if (body?.code === 'forbidden' || body?.required_role) {
            toast.error(`Your role (${auth.role()}) can't do this`);
          }
        }
      }
      return throwError(() => err);
    }),
  );
};
