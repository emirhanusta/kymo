import { Component, ChangeDetectionStrategy, inject, signal } from '@angular/core';
import { FormsModule } from '@angular/forms';

import { AuthService } from '../../../core/services/auth.service';
import { AuditEvent, User, userLabel } from '../../../core/models/auth.model';
import { PageHeaderComponent } from '../../../shared/components/page-header/page-header.component';
import { ErrorStateComponent } from '../../../shared/components/error-state/error-state.component';
import { SkeletonComponent } from '../../../shared/components/skeleton/skeleton.component';
import { EmptyStateComponent } from '../../../shared/components/empty-state/empty-state.component';
import { TimeagoPipe } from '../../../shared/pipes/timeago.pipe';
import { SettingsNavComponent } from '../settings-nav.component';

const PAGE = 50;

@Component({
  selector: 'app-audit',
  standalone: true,
  imports: [FormsModule, TimeagoPipe, PageHeaderComponent, ErrorStateComponent, SkeletonComponent, EmptyStateComponent, SettingsNavComponent],
  template: `
<div class="space-y-4">
  <app-settings-nav />
  <app-page-header title="Audit log" subtitle="Who did what, kept for one year" />

  <form class="k-panel p-4 flex items-end gap-3 flex-wrap" (ngSubmit)="apply()" aria-label="Filter audit log">
    <label class="flex flex-col gap-1">
      <span class="k-label">User</span>
      <select class="k-input" name="user" [ngModel]="userId()" (ngModelChange)="userId.set($event)">
        <option value="">All users</option>
        @for (u of users(); track u.id) { <option [value]="u.id">{{ label(u) }}</option> }
      </select>
    </label>
    <label class="flex flex-col gap-1">
      <span class="k-label">Action</span>
      <input class="k-input w-56" name="action" autocomplete="off" placeholder="e.g. login, token.create" [ngModel]="action()" (ngModelChange)="action.set($event)" />
    </label>
    <button type="submit" class="k-btn k-btn-primary">Apply</button>
  </form>

  @if (error()) { <app-error-state message="Failed to load the audit log" (retry)="apply()" /> }

  @if (!loaded()) {
    <app-skeleton [rows]="5" />
  } @else if (events().length === 0 && !error()) {
    <div class="k-panel"><app-empty-state icon="events" [compact]="true" message="No audit events" hint="Nothing matches these filters." /></div>
  } @else if (events().length) {
    <div class="k-panel overflow-x-auto scrollbar-thin">
      <table class="k-table">
        <thead><tr><th>Time</th><th>Actor</th><th>Action</th><th>Target</th><th class="hidden md:table-cell">IP</th><th><span class="sr-only">Details</span></th></tr></thead>
        <tbody>
          @for (e of events(); track e.id) {
            <tr class="cursor-pointer hover:bg-kymo-raised" (click)="toggle(e.id)">
              <td class="px-4 py-2 text-xs text-kymo-muted whitespace-nowrap" [title]="e.time">{{ e.time | timeago }}</td>
              <td class="px-4 py-2 text-xs text-kymo-ink">{{ e.actor_label }}</td>
              <td class="px-4 py-2 text-xs font-mono text-kymo-amber">{{ e.action }}</td>
              <td class="px-4 py-2 text-xs text-kymo-muted">{{ e.target_type }}{{ e.target_id ? ' · ' + e.target_id : '' }}</td>
              <td class="px-4 py-2 text-xs font-mono text-kymo-muted hidden md:table-cell">{{ e.ip }}</td>
              <td class="px-4 py-2 text-right">
                <button type="button" class="k-btn k-btn-sm" [attr.aria-expanded]="open().has(e.id)" [attr.aria-label]="'Details of event ' + e.id" (click)="toggle(e.id); $event.stopPropagation()">{{ open().has(e.id) ? 'Hide' : 'Details' }}</button>
              </td>
            </tr>
            @if (open().has(e.id)) {
              <tr><td colspan="6" class="px-4 py-2 bg-kymo-deep"><pre class="text-[11px] text-kymo-ink whitespace-pre-wrap break-all font-mono">{{ pretty(e.details) }}</pre></td></tr>
            }
          }
        </tbody>
      </table>
    </div>
    @if (more()) {
      <div class="flex justify-center"><button type="button" class="k-btn" [disabled]="loadingMore()" (click)="loadMore()">{{ loadingMore() ? 'Loading…' : 'Load more' }}</button></div>
    }
  }
</div>
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class AuditComponent {
  private auth = inject(AuthService);
  label = userLabel;

  events = signal<AuditEvent[]>([]);
  users = signal<User[]>([]);
  userId = signal('');
  action = signal('');
  loaded = signal(false);
  error = signal(false);
  more = signal(false);
  loadingMore = signal(false);
  open = signal<Set<number>>(new Set());

  constructor() {
    this.auth.users().subscribe({ next: u => this.users.set(u), error: () => {} });
    this.apply();
  }

  private query(before?: string): Record<string, string | number> {
    const q: Record<string, string | number> = { limit: PAGE };
    if (this.userId()) q['user_id'] = this.userId();
    if (this.action().trim()) q['action'] = this.action().trim();
    if (before) q['before'] = before;
    return q;
  }

  apply(): void {
    this.auth.audit(this.query()).subscribe({
      next: l => { this.events.set(l); this.more.set(l.length >= PAGE); this.loaded.set(true); this.error.set(false); },
      error: () => { this.error.set(true); this.loaded.set(true); },
    });
  }

  /** Cursor = time of the oldest loaded event. */
  loadMore(): void {
    const last = this.events().at(-1);
    if (!last || this.loadingMore()) return;
    this.loadingMore.set(true);
    this.auth.audit(this.query(last.time)).subscribe({
      next: l => {
        const have = new Set(this.events().map(e => e.id));
        this.events.update(cur => [...cur, ...l.filter(e => !have.has(e.id))]);
        this.more.set(l.length >= PAGE);
        this.loadingMore.set(false);
      },
      error: () => this.loadingMore.set(false),
    });
  }

  toggle(id: number): void {
    this.open.update(s => { const n = new Set(s); if (!n.delete(id)) n.add(id); return n; });
  }

  pretty(d: unknown): string {
    if (d == null || (typeof d === 'object' && !Object.keys(d as object).length)) return '(no details)';
    try { return JSON.stringify(typeof d === 'string' ? JSON.parse(d) : d, null, 2); } catch { return String(d); }
  }
}
