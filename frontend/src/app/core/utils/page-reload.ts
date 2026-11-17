import { DestroyRef, inject, untracked } from '@angular/core';
import { takeUntilDestroyed, toObservable } from '@angular/core/rxjs-interop';
import { of } from 'rxjs';

import { RefreshService, RefreshReason } from '../services/refresh.service';
import { SourceStateService } from '../services/source-state.service';

export type LoadReason = 'init' | 'source' | RefreshReason;

export interface PageReloadOptions {
  /** Called once on init, on every source change, and on every refresh tick. */
  load: (reason: LoadReason) => void;
  /** Called before `load` when the selected source changes (also on init). Use to reset page-local filters. */
  onSourceChange?: (source: string) => void;
  /** Pages that always show every source (dashboard) don't reload on source change. */
  ignoreSource?: boolean;
}

/**
 * Single place that wires a page's data loading to source selection + refresh bus.
 * Must be called in an injection context (constructor / field initializer).
 * Replaces the old ngOnInit + effect + NavigationEnd triple-loading.
 */
export function reloadOnSourceAndRefresh(opts: PageReloadOptions): void {
  const sources    = inject(SourceStateService);
  const refresh    = inject(RefreshService);
  const destroyRef = inject(DestroyRef);

  let first = true;
  const source$ = opts.ignoreSource
    ? of(untracked(sources.selectedSource))
    : toObservable(sources.selectedSource);

  source$.pipe(takeUntilDestroyed(destroyRef)).subscribe(src => {
    untracked(() => {
      opts.onSourceChange?.(src);
      opts.load(first ? 'init' : 'source');
    });
    first = false;
  });

  refresh.refresh$.pipe(takeUntilDestroyed(destroyRef)).subscribe(reason => {
    untracked(() => opts.load(reason));
  });
}
