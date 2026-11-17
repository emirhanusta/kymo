import { Injectable, inject, signal, computed } from '@angular/core';
import { ContainerService } from './container.service';
import { SourceStatus, SourceType } from '../models/container.model';

@Injectable({ providedIn: 'root' })
export class SourceStateService {
  private svc = inject(ContainerService);

  readonly sources = signal<SourceStatus[]>([]);
  readonly sourcesError = signal(false);
  /** True once the first /sources request has finished (successfully or not). */
  readonly loaded = signal(false);
  /** Empty string means "all sources". */
  readonly selectedSource = signal<string>('');

  readonly selectedSourceInfo = computed(() => {
    const name = this.selectedSource();
    return name ? this.sources().find(s => s.name === name) ?? null : null;
  });

  private byName = computed(() => new Map(this.sources().map(s => [s.name, s])));

  readonly hasDocker     = computed(() => this.sources().some(s => s.type === 'docker'));
  readonly hasKubernetes = computed(() => this.sources().some(s => s.type === 'kubernetes'));

  /** Load (or refresh) the source list from the backend. */
  loadSources(): void {
    this.svc.getSources().subscribe({
      next: sources => {
        const list = sources ?? [];
        this.sources.set(list);
        this.sourcesError.set(false);
        this.loaded.set(true);
        // The selected source was deleted → fall back to "all sources".
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

  /** Read-only sources refuse management actions (start/stop/restart/kill/scale/…). */
  isReadOnly(name: string | null | undefined): boolean {
    return !!this.find(name)?.read_only;
  }

  /**
   * The source the backend uses when no `source` param is sent for a given type:
   * the first configured (non-disabled) source of that type.
   */
  defaultSourceOf(type: SourceType): SourceStatus | null {
    const ofType = this.sources().filter(s => s.type === type);
    return ofType.find(s => s.status !== 'disabled') ?? ofType[0] ?? null;
  }
}
