import { HttpErrorResponse, HttpInterceptorFn } from '@angular/common/http';
import { inject } from '@angular/core';
import { throwError } from 'rxjs';
import { catchError } from 'rxjs/operators';

import { ToastService, apiErrorMessage } from '../services/toast.service';

/** Role refusals (`code: forbidden`) are toasted by authInterceptor; `password_change_required` redirects instead of toasting. */
const isRoleRefusal = (e: HttpErrorResponse) => {
  const code = (e.error as { code?: string } | null)?.code;
  return code === 'forbidden' || code === 'password_change_required';
};

export const apiInterceptor: HttpInterceptorFn = (req, next) => {
  const toast = inject(ToastService);

  // Only declare a JSON body when there actually is one (GET/DELETE without body stay "simple").
  const hasBody = req.body !== null && req.body !== undefined;
  const cloned = hasBody && !req.headers.has('Content-Type')
    ? req.clone({ setHeaders: { 'Content-Type': 'application/json' } })
    : req;

  return next(cloned).pipe(
    catchError((error) => {
      console.error('[API Error]', error);
      // 403 = refused by policy (read-only source, config-managed source): always tell the user why.
      // Callers use ToastService.apiError(), which skips 403s so they are not toasted twice.
      if (error instanceof HttpErrorResponse && error.status === 403 && !isRoleRefusal(error)) {
        toast.error(apiErrorMessage(error, 'This action is not allowed'));
      }
      return throwError(() => error);
    })
  );
};
