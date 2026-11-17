import { HttpInterceptorFn } from '@angular/common/http';
import { throwError } from 'rxjs';
import { catchError } from 'rxjs/operators';

export const apiInterceptor: HttpInterceptorFn = (req, next) => {
  // Only declare a JSON body when there actually is one (GET/DELETE without body stay "simple").
  const hasBody = req.body !== null && req.body !== undefined;
  const cloned = hasBody && !req.headers.has('Content-Type')
    ? req.clone({ setHeaders: { 'Content-Type': 'application/json' } })
    : req;

  return next(cloned).pipe(
    catchError((error) => {
      console.error('[API Error]', error);
      return throwError(() => error);
    })
  );
};
