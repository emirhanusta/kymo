import { Component, ChangeDetectionStrategy, inject } from '@angular/core';
import { RouterOutlet } from '@angular/router';
import { takeUntilDestroyed } from '@angular/core/rxjs-interop';

import { TopbarComponent } from '../topbar/topbar.component';
import { ScopeBarComponent } from '../scope-bar/scope-bar.component';
import { EventTapeComponent } from '../event-tape/event-tape.component';
import { ToastComponent } from '../../shared/components/toast/toast.component';
import { SourceStateService } from '../../core/services/source-state.service';
import { RefreshService } from '../../core/services/refresh.service';

/** Layout "B": top bar with tabs, full-width content, fixed event tape at the bottom. */
@Component({
  selector: 'app-shell',
  standalone: true,
  imports: [RouterOutlet, TopbarComponent, ScopeBarComponent, EventTapeComponent, ToastComponent],
  templateUrl: './shell.component.html',
  host: { class: 'block' },
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ShellComponent {
  private sources = inject(SourceStateService);

  constructor() {
    this.sources.loadSources();
    inject(RefreshService).refresh$.pipe(takeUntilDestroyed()).subscribe(() => this.sources.loadSources());
  }
}
