import { Component, ChangeDetectionStrategy } from '@angular/core';
import { RouterOutlet } from '@angular/router';
import { SettingsNavComponent } from './settings-nav.component';

/** Shell of every /settings/* page: title, grouped nav and a width-capped content column. */
@Component({
  selector: 'app-settings-layout',
  standalone: true,
  imports: [RouterOutlet, SettingsNavComponent],
  template: `
    <div class="space-y-4 lg:space-y-6">
      <h1 class="text-xl font-semibold text-kymo-ink tracking-tight">Settings</h1>
      <div class="space-y-5 lg:space-y-0 lg:grid lg:grid-cols-[11rem_minmax(0,1fr)] lg:gap-10 items-start">
        <app-settings-nav />
        <div class="min-w-0 max-w-5xl"><router-outlet /></div>
      </div>
    </div>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class SettingsLayoutComponent {}
