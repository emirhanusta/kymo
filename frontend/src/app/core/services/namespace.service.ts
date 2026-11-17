import { Injectable, inject, signal } from '@angular/core';
import { Observable, of } from 'rxjs';
import { tap, catchError } from 'rxjs/operators';
import { ContainerService } from './container.service';
import { RefreshService } from './refresh.service';

/** Cached namespace lookups per source ('' = all sources). Failed lookups are not cached. */
@Injectable({ providedIn: 'root' })
export class NamespaceService {
  private svc   = inject(ContainerService);
  private cache = new Map<string, string[]>();

  /** Bumped whenever the cache is cleared so selectors know to re-fetch. */
  readonly version = signal(0);

  constructor() {
    // A manual refresh should also pick up namespaces created since the last lookup.
    inject(RefreshService).refresh$.subscribe(reason => {
      if (reason === 'manual') this.invalidate();
    });
  }

  getNamespaces(sourceName?: string): Observable<string[]> {
    const key = sourceName ?? '';
    const cached = this.cache.get(key);
    if (cached) return of(cached);

    return this.svc.getNamespaces(sourceName || undefined).pipe(
      tap(ns => this.cache.set(key, ns)),
      catchError(() => of([]))
    );
  }

  invalidate(sourceName?: string): void {
    if (sourceName !== undefined) {
      this.cache.delete(sourceName);
    } else {
      this.cache.clear();
    }
    this.version.update(v => v + 1);
  }
}
