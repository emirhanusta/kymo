import { Injectable, signal, effect, DestroyRef, inject } from '@angular/core';
import { Subject } from 'rxjs';
import { readStorage, writeStorage } from '../../shared/utils/storage';

export type RefreshReason = 'manual' | 'auto';

const AUTO_KEY = 'kymo.autoRefresh';
export const AUTO_REFRESH_MS = 30_000;

/**
 * Central refresh bus.
 * - `trigger()` = manual refresh (navbar button).
 * - When auto-refresh is on, emits 'auto' every 30s while the tab is visible.
 * - Pages call `markUpdated()` after a successful load to feed the navbar "updated Xs ago" label.
 */
@Injectable({ providedIn: 'root' })
export class RefreshService {
  private subject = new Subject<RefreshReason>();
  readonly refresh$ = this.subject.asObservable();

  readonly autoRefresh = signal<boolean>(readStorage(AUTO_KEY, true));
  readonly lastUpdated = signal<Date | null>(null);

  constructor() {
    effect(onCleanup => {
      const on = this.autoRefresh();
      writeStorage(AUTO_KEY, on);
      if (!on) return;
      const id = setInterval(() => {
        if (typeof document === 'undefined' || document.visibilityState === 'visible') {
          this.subject.next('auto');
        }
      }, AUTO_REFRESH_MS);
      onCleanup(() => clearInterval(id));
    });
    // Keep every open tab on the same Live/Paused state.
    const onStorage = (e: StorageEvent) => {
      if (e.key !== AUTO_KEY || e.newValue == null) return;
      try { this.autoRefresh.set(JSON.parse(e.newValue) === true); } catch { /* ignore */ }
    };
    if (typeof window !== 'undefined') window.addEventListener('storage', onStorage);
    inject(DestroyRef).onDestroy(() => {
      if (typeof window !== 'undefined') window.removeEventListener('storage', onStorage);
      this.subject.complete();
    });
  }

  trigger(): void { this.subject.next('manual'); }

  toggleAuto(): void { this.autoRefresh.update(v => !v); }

  markUpdated(): void { this.lastUpdated.set(new Date()); }
}
