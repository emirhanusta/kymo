import { Component, ChangeDetectionStrategy, inject, signal, computed } from '@angular/core';
import { RouterLink } from '@angular/router';
import { DatePipe } from '@angular/common';

import { ContainerService } from '../../core/services/container.service';
import { SourceStateService } from '../../core/services/source-state.service';
import { LifecycleEvent } from '../../core/models/container.model';
import { reloadOnSourceAndRefresh } from '../../core/utils/page-reload';
import { eventTextClass, showsExitCode } from '../../shared/utils/events';
import { containerRoute, shortId } from '../../shared/utils/format';

/**
 * Fixed bottom "chart paper" strip: the most recent lifecycle events, newest first.
 * Reloads on source change and on the shared refresh bus — no timers of its own.
 */
@Component({
  selector: 'app-event-tape',
  standalone: true,
  imports: [RouterLink, DatePipe],
  template: `
    <div
      class="fixed bottom-0 inset-x-0 z-30 h-[34px] bg-kymo-deep border-t border-kymo-amber hidden sm:flex items-stretch text-[11px]"
      role="region"
      aria-label="Recent lifecycle events"
    >
      <div class="flex items-center gap-1.5 px-3 flex-none border-r border-kymo-rule text-kymo-amber font-medium tracking-[0.08em]">
        <span class="k-led bg-kymo-amber animate-pulse" aria-hidden="true"></span>REC
      </div>
      <ol class="flex items-stretch flex-1 min-w-0 overflow-x-auto overflow-y-hidden scrollbar-none">
        @for (ev of events().slice(0, 15); track ev.id) {
          <li class="flex-none border-r border-kymo-rule">
            <a
              [routerLink]="route(ev)"
              class="flex items-center gap-2 h-full px-3 whitespace-nowrap hover:bg-kymo-raised transition-colors"
              [title]="ev.occurred_at | date:'medium'"
            >
              <span class="text-kymo-faint tabular-nums">{{ ev.occurred_at | date:'HH:mm:ss' }}</span>
              <span class="text-kymo-ink max-w-[180px] truncate">{{ ev.container_name || shortId(ev.container_id) }}</span>
              <span class="uppercase tracking-[0.06em]" [class]="textClass(ev)">{{ ev.event_type }}</span>
              @if (showsExit(ev)) {
                <span class="tabular-nums" [class]="ev.exit_code === 0 ? 'text-kymo-faint' : 'text-kymo-danger'">{{ ev.exit_code }}</span>
              }
            </a>
          </li>
        } @empty {
          @if (loaded()) {
          <li class="flex items-center px-3 text-kymo-faint">
            {{ failed() ? 'event feed unavailable' : 'no events recorded' }}
          </li>
          }
        }
      </ol>
    </div>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class EventTapeComponent {
  private svc     = inject(ContainerService);
  private sources = inject(SourceStateService);

  private loadedEvents = signal<LifecycleEvent[]>([]);
  /** Selected cluster (incl. a removed one) or connected clusters only. */
  events = computed(() => this.sources.inScope(this.loadedEvents(), e => e.source_name));
  failed = signal(false);
  loaded = signal(false);

  readonly textClass = eventTextClass;
  readonly showsExit = showsExitCode;
  readonly shortId   = shortId;

  constructor() {
    reloadOnSourceAndRefresh({ load: () => this.load() });
  }

  route(ev: LifecycleEvent): string[] {
    return containerRoute(ev.container_id, ev.runtime);
  }

  private load(): void {
    // Events of removed clusters are dropped client-side, so ask for a few extra when no cluster is selected.
    const sel = this.sources.selectedSource();
    this.svc.getRecentEvents(sel ? 15 : 30, sel || undefined).subscribe({
      next: list => {
        const sorted = [...(list ?? [])].sort(
          (a, b) => new Date(b.occurred_at).getTime() - new Date(a.occurred_at).getTime(),
        );
        this.loadedEvents.set(sorted);
        this.failed.set(false);
        this.loaded.set(true);
      },
      error: () => { this.failed.set(true); this.loaded.set(true); },
    });
  }
}
