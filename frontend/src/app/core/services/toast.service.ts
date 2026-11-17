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

  dismiss(id: number): void {
    this.toasts.update(ts => ts.filter(t => t.id !== id));
  }

  private push(type: ToastType, message: string): void {
    const id = ++nextId;
    this.toasts.update(ts => [...ts, { id, type, message }]);
    setTimeout(() => this.dismiss(id), 4000);
  }
}
