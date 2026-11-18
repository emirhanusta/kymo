import { inject } from '@angular/core';
import { CanActivateFn, Router } from '@angular/router';

import { AuthService } from '../services/auth.service';
import { Permission } from '../models/auth.model';

/** Every app route: needs a signed-in user (or auth disabled), who has no pending forced password change. */
export const requireUser: CanActivateFn = (_route, state) => {
  const auth = inject(AuthService);
  const router = inject(Router);
  const u = auth.user();
  if (u) {
    if (u.must_change_password) return changePasswordTree(router, state.url);
    return true;
  }
  const qp = state.url && state.url !== '/' ? { return: state.url } : {};
  return router.createUrlTree(['/login'], { queryParams: qp });
};

/** /change-password: needs a session, and only makes sense while a change is required. */
export const requirePasswordChange: CanActivateFn = () => {
  const auth = inject(AuthService);
  const router = inject(Router);
  const u = auth.user();
  if (!u) return router.createUrlTree(['/login']);
  return u.must_change_password ? true : router.createUrlTree(['/dashboard']);
};

/** Public auth pages: bounce signed-in users into the app. */
export const publicOnly = (page: 'login' | 'link'): CanActivateFn => (route) => {
  const auth = inject(AuthService);
  const router = inject(Router);
  if (page === 'link') return true;
  const u = auth.user();
  if (u) {
    const target = safeReturn(route.queryParamMap.get('return'));
    return u.must_change_password ? changePasswordTree(router, target) : router.parseUrl(target);
  }
  return true;
};

export function changePasswordTree(router: Router, returnUrl: string | null | undefined) {
  const ret = safeReturn(returnUrl);
  return router.createUrlTree(['/change-password'], { queryParams: ret !== '/' ? { returnUrl: ret } : {} });
}

export const requirePermission = (perm: Permission): CanActivateFn => () => {
  const auth = inject(AuthService);
  return auth.can(perm) ? true : inject(Router).createUrlTree(['/settings/tokens']);
};

/** Only same-origin absolute paths are accepted as a post-login target. */
export function safeReturn(url: string | null | undefined): string {
  return url && url.startsWith('/') && !url.startsWith('//') && !url.startsWith('/login') && !url.startsWith('/change-password') ? url : '/';
}
