import { Injectable, inject, signal, computed } from '@angular/core';
import { ContainerService } from './container.service';
import { SourceStatus } from '../models/container.model';

@Injectable({ providedIn: 'root' })
export class SourceStateService {
  private svc = inject(ContainerService);

  readonly sources = signal<SourceStatus[]>([]);
  readonly sourcesError = signal(false);
  /** Empty string means "all sources". */
  readonly selectedSource = signal<string>('');

  readonly selectedSourceInfo = computed(() => {
    const name = this.selectedSource();
    return name ? this.sources().find(s => s.name === name) ?? null : null;
  });

  /** Load (or refresh) the source list from the backend. */
  loadSources(): void {
    this.svc.getSources().subscribe({
      next: sources => {
        this.sources.set(sources ?? []);
        this.sourcesError.set(false);
      },
      error: () => this.sourcesError.set(true),
    });
  }

  select(name: string): void {
    this.selectedSource.set(name);
  }
}
