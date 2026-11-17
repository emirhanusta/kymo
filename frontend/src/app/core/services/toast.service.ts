import { Injectable, signal } from '@angular/core';

export type ToastType = 'success' | 'error' | 'info';

export interface Toast {
  id: number;
  type: ToastType;
  message: string;
}

let nextId = 0;

@Injectable({ providedIn: 'root' })
export class ToastService {
  toasts = signal<Toast[]>([]);

  success(message: string): void { this.push('success', message); }
  error(message: string): void   { this.push('error', message); }
  info(message: string): void    { this.push('info', message); }

  /**
   * Toast an API error. 403 responses are already toasted by the API interceptor
   * (read-only sources, config-managed sources), so they are skipped here.
   */
  apiError(err: unknown, fallback: string): void {
    if (isForbidden(err)) return;
    this.error(apiErrorMessage(err, fallback));
  }

  dismiss(id: number): void {
    this.toasts.update(ts => ts.filter(t => t.id !== id));
  }

  private push(type: ToastType, message: string): void {
    const id = ++nextId;
    this.toasts.update(ts => [...ts, { id, type, message }]);
    setTimeout(() => this.dismiss(id), 4000);
  }
}

/** Best human-readable message from an HttpErrorResponse body (`{message}` or `{error}`). */
export function apiErrorMessage(err: unknown, fallback: string): string {
  const body = (err as { error?: unknown } | null)?.error;
  if (body && typeof body === 'object') {
    const b = body as { message?: unknown; error?: unknown };
    if (typeof b.message === 'string' && b.message) return b.message;
    if (typeof b.error === 'string' && b.error) return b.error;
  }
  if (typeof body === 'string' && body.trim() && body.length < 300) return body.trim();
  return fallback;
}

export function isForbidden(err: unknown): boolean {
  return (err as { status?: number } | null)?.status === 403;
}
