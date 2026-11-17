import { Injectable, inject, signal, computed } from '@angular/core';
import { ContainerService } from './container.service';
import { SourceStatus } from '../models/container.model';

@Injectable({ providedIn: 'root' })
export class SourceStateService {
  private svc = inject(ContainerService);

  readonly sources = signal<SourceStatus[]>([]);
  readonly sourcesError = signal(false);
  /** True once the first /sources request has finished (successfully or not). */
  readonly loaded = signal(false);
  /** Empty string means "all clusters". */
  readonly selectedSource = signal<string>('');

  readonly selectedSourceInfo = computed(() => {
    const name = this.selectedSource();
    return name ? this.sources().find(s => s.name === name) ?? null : null;
  });

  private byName = computed(() => new Map(this.sources().map(s => [s.name, s])));

  /** Load (or refresh) the source list from the backend. */
  loadSources(): void {
    this.svc.getSources().subscribe({
      next: sources => {
        const list = sources ?? [];
        this.sources.set(list);
        this.sourcesError.set(false);
        this.loaded.set(true);
        // The selected cluster was deleted → fall back to "all clusters".
        const sel = this.selectedSource();
        if (sel && !list.some(s => s.name === sel)) this.selectedSource.set('');
      },
      error: () => {
        this.sourcesError.set(true);
        this.loaded.set(true);
      },
    });
  }

  select(name: string): void {
    this.selectedSource.set(name);
  }

  find(name: string | null | undefined): SourceStatus | null {
    return name ? this.byName().get(name) ?? null : null;
  }

  /** Read-only clusters refuse management actions (restart/scale/…). */
  isReadOnly(name: string | null | undefined): boolean {
    return !!this.find(name)?.read_only;
  }
}
