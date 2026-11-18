import { Component, ChangeDetectionStrategy, inject, signal, computed } from '@angular/core';
import { FormsModule } from '@angular/forms';
import { forkJoin, Observable } from 'rxjs';
import { map } from 'rxjs/operators';

import { AuthService } from '../../../core/services/auth.service';
import { NotificationService } from '../../../core/services/notification.service';
import { AuditEvent, AuditQuery, User, userLabel } from '../../../core/models/auth.model';
import { PageHeaderComponent } from '../../../shared/components/page-header/page-header.component';
import { ErrorStateComponent } from '../../../shared/components/error-state/error-state.component';
import { SkeletonComponent } from '../../../shared/components/skeleton/skeleton.component';
import { EmptyStateComponent } from '../../../shared/components/empty-state/empty-state.component';
import { groupByDay } from '../../../shared/utils/days';
import { AUDIT_GROUPS, AuditDescription, describeAudit } from './audit-text';

const PAGE = 50;

interface Row { e: AuditEvent; time: string; actor: string; d: AuditDescription; ip: string; }

/** Settings → Audit log: who did what, as day-grouped readable sentences. */
@Component({
  selector: 'app-audit',
  standalone: true,
  imports: [FormsModule, PageHeaderComponent, ErrorStateComponent, SkeletonComponent, EmptyStateComponent],
  template: `
<div class="space-y-4">
  <app-page-header title="Audit log" subtitle="Who did what, kept for one year">
    <select class="k-input" aria-label="Filter by user" [ngModel]="userId()" (ngModelChange)="userId.set($event); apply()">
      <option value="">All users</option>
      @for (u of users(); track u.id) { <option [value]="u.id">{{ label(u) }}</option> }
    </select>
    <select class="k-input" aria-label="Filter by action group" [ngModel]="group()" (ngModelChange)="group.set($event); apply()">
      <option value="">All actions</option>
      @for (g of groups; track g.value) { <option [value]="g.value">{{ g.label }}</option> }
    </select>
  </app-page-header>

  @if (error()) { <app-error-state message="Failed to load the audit log" (retry)="apply()" /> }

  @if (!loaded()) {
    <app-skeleton [rows]="5" />
  } @else if (events().length === 0 && !error()) {
    <div class="k-panel"><app-empty-state icon="events" message="No audit events" [hint]="userId() || group() ? 'Nothing matches these filters.' : 'Sign-ins, user changes and cluster actions show up here.'" /></div>
  } @else if (events().length) {
    <div class="k-panel">
      @for (g of days(); track g.key) {
        <section [attr.aria-label]="g.label">
          <h3 class="sticky top-12 z-10 px-4 py-1.5 bg-kymo-raised border-y border-kymo-rule first:border-t-0 flex items-center gap-2">
            <span class="k-label !text-kymo-muted">{{ g.label }}</span>
            <span class="text-[11px] tabular-nums text-kymo-faint">{{ g.rows.length }}</span>
          </h3>
          <ol class="divide-y divide-kymo-rule">
            @for (r of g.rows; track r.e.id) {
              <li>
                <div class="flex items-center gap-3 px-4 py-1.5 text-xs min-w-0 cursor-pointer hover:bg-kymo-raised transition-colors" (click)="toggle(r.e.id)">
                  <span class="w-11 flex-none text-kymo-muted tabular-nums" [title]="r.e.time">{{ r.time }}</span>
                  <span class="k-led" [class]="r.d.danger ? 'bg-kymo-danger' : 'bg-transparent'" [title]="r.d.danger ? 'Failed or denied' : ''" aria-hidden="true"></span>
                  @if (r.d.danger) { <span class="sr-only">Failed or denied:</span> }
                  <span class="min-w-0 truncate" [title]="r.actor + ' ' + r.d.text">
                    <span class="text-kymo-ink">{{ r.actor }}</span>
                    <span [class]="r.d.danger ? 'text-kymo-danger' : 'text-kymo-muted'"> {{ r.d.text }}</span>
                    @if (r.d.target) { <span class="text-kymo-faint"> · {{ r.d.target }}</span> }
                  </span>
                  @if (r.ip) { <span class="ml-auto flex-none font-mono text-[11px] text-kymo-faint hidden md:inline">{{ r.ip }}</span> }
                  <button type="button" class="k-btn k-btn-sm k-btn-icon border-transparent text-kymo-muted hover:text-kymo-ink flex-none" [class.ml-auto]="!r.ip"
                          [attr.aria-expanded]="open().has(r.e.id)" [attr.aria-label]="(open().has(r.e.id) ? 'Hide' : 'Show') + ' details of event ' + r.e.id"
                          (click)="toggle(r.e.id); $event.stopPropagation()">
                    <svg class="w-3 h-3 transition-transform" [class.rotate-90]="open().has(r.e.id)" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2.5" aria-hidden="true"><path stroke-linecap="square" d="M9 5l7 7-7 7" /></svg>
                  </button>
                </div>
                @if (open().has(r.e.id)) {
                  <div class="px-4 py-2 bg-kymo-deep border-t border-kymo-rule">
                    <p class="text-[11px] text-kymo-faint mb-1 font-mono">{{ r.e.action }} · {{ r.e.target_type }}{{ r.e.target_id ? ' · ' + r.e.target_id : '' }}@if (r.ip) { · {{ r.ip }} }</p>
                    <pre class="text-[11px] text-kymo-ink whitespace-pre-wrap break-all font-mono">{{ pretty(r.e.details) }}</pre>
                  </div>
                }
              </li>
            }
          </ol>
        </section>
      }
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
  private notifications = inject(NotificationService);
  label = userLabel;
  readonly groups = AUDIT_GROUPS;

  events = signal<AuditEvent[]>([]);
  users = signal<User[]>([]);
  channels = signal<Map<string, string>>(new Map());
  userId = signal('');
  group = signal('');
  loaded = signal(false);
  error = signal(false);
  more = signal(false);
  loadingMore = signal(false);
  open = signal<Set<number>>(new Set());
  private gen = 0;

  days = computed(() => {
    const userNames = new Map(this.users().map(u => [String(u.id), u.username]));
    const chans = this.channels();
    const look = { userName: (id: string) => userNames.get(id) ?? '', channelName: (id: string) => chans.get(id) ?? '' };
    const rows: Row[] = this.events().map(e => ({
      e,
      time: new Date(e.time).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' }),
      actor: e.actor_label || 'system',
      d: describeAudit(e, look),
      ip: e.ip,
    }));
    return groupByDay(rows, r => r.e.time);
  });

  constructor() {
    this.auth.users().subscribe({ next: u => this.users.set(u), error: () => {} });
    this.notifications.channels().subscribe({
      next: l => this.channels.set(new Map(l.map(c => [String(c.id), c.name]))),
      error: () => {},
    });
    this.apply();
  }

  /** One query per action prefix of the selected group (the API filters a single prefix); results are merged by time. */
  private fetch(before?: string): Observable<AuditEvent[][]> {
    const base: AuditQuery = { limit: PAGE };
    if (this.userId()) base.user_id = this.userId();
    if (before) base.before = before;
    const prefixes = AUDIT_GROUPS.find(g => g.value === this.group())?.prefixes;
    if (!prefixes) return this.auth.audit(base).pipe(map(l => [l]));
    return forkJoin(prefixes.map(p => this.auth.audit({ ...base, action: p })));
  }

  private merge(lists: AuditEvent[][]): { list: AuditEvent[]; more: boolean } {
    const seen = new Set<number>();
    const all = lists.flat().filter(e => !seen.has(e.id) && seen.add(e.id))
      .sort((a, b) => b.time.localeCompare(a.time) || b.id - a.id);
    return { list: all.slice(0, PAGE), more: all.length > PAGE || lists.some(l => l.length >= PAGE) };
  }

  apply(): void {
    const gen = ++this.gen;
    this.open.set(new Set());
    this.fetch().subscribe({
      next: p => {
        if (gen !== this.gen) return;
        const m = this.merge(p);
        this.events.set(m.list); this.more.set(m.more); this.loaded.set(true); this.error.set(false);
      },
      error: () => { if (gen !== this.gen) return; this.error.set(true); this.loaded.set(true); },
    });
  }

  /** Cursor = time of the oldest loaded event. */
  loadMore(): void {
    const last = this.events().at(-1);
    if (!last || this.loadingMore()) return;
    const gen = this.gen;
    this.loadingMore.set(true);
    this.fetch(last.time).subscribe({
      next: p => {
        if (gen !== this.gen) return;
        const m = this.merge(p);
        const have = new Set(this.events().map(e => e.id));
        this.events.update(cur => [...cur, ...m.list.filter(e => !have.has(e.id))]);
        this.more.set(m.more);
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
