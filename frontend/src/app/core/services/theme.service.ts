import { Injectable, signal, DestroyRef, inject } from '@angular/core';
import { readStorage, writeStorage } from '../../shared/utils/storage';

export type Theme = 'dark' | 'light';

const KEY = 'kymo.theme';

const isTheme = (v: unknown): v is Theme => v === 'dark' || v === 'light';

function osPreference(): Theme {
  return typeof window !== 'undefined' && window.matchMedia?.('(prefers-color-scheme: light)').matches
    ? 'light'
    : 'dark';
}

function apply(theme: Theme): void {
  if (typeof document === 'undefined') return;
  document.documentElement.dataset['theme'] = theme;
  document.documentElement.style.colorScheme = theme;
}

/** Dark/light theme: stored choice, else the OS preference. Synced across tabs. */
@Injectable({ providedIn: 'root' })
export class ThemeService {
  private stored = readStorage<unknown>(KEY, null);
  readonly theme = signal<Theme>(isTheme(this.stored) ? this.stored : osPreference());

  constructor() {
    apply(this.theme());
    const onStorage = (e: StorageEvent) => {
      if (e.key !== KEY || e.newValue == null) return;
      try {
        const v: unknown = JSON.parse(e.newValue);
        if (isTheme(v)) this.set(v, false);
      } catch { /* ignore */ }
    };
    if (typeof window !== 'undefined') window.addEventListener('storage', onStorage);
    inject(DestroyRef).onDestroy(() => {
      if (typeof window !== 'undefined') window.removeEventListener('storage', onStorage);
    });
  }

  toggle(): void { this.set(this.theme() === 'dark' ? 'light' : 'dark'); }

  set(theme: Theme, persist = true): void {
    // Apply to the DOM first so consumers reacting to the signal read the new CSS vars.
    apply(theme);
    this.theme.set(theme);
    if (persist) writeStorage(KEY, theme);
  }
}
