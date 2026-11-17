import { Injectable, inject, signal, computed } from '@angular/core';
import { HttpClient, HttpParams } from '@angular/common/http';
import { Observable, forkJoin, of } from 'rxjs';
import { catchError, map } from 'rxjs/operators';

import { API_BASE } from './container.service';
import { RefreshService } from './refresh.service';
import { SourceStateService } from './source-state.service';
import { Issue, IssueQuery, IssueStatus, IssueSummary } from '../models/issue.model';

const OPEN_LIMIT = 500;
const orEmpty = map((l: Issue[] | null) => l ?? []);

/**
 * Issues are detected and tracked by the server (so it can notify); this service only reads them.
 * Holds the shared open-issue snapshot (Overview, Issues page, top-bar badge), reloaded on the refresh bus.
 */
@Injectable({ providedIn: 'root' })
export class IssuesService {
  private http    = inject(HttpClient);
  private refresh = inject(RefreshService);
  private sources = inject(SourceStateService);

  readonly summary = signal<IssueSummary | null>(null);
  /** Open issues of every cluster, critical first. */
  readonly open    = signal<Issue[]>([]);
  readonly loaded  = signal(false);
  readonly error   = signal(false);

  /** Open issues of the cluster picked in the top bar (all connected clusters when none). */
  readonly scopedOpen = computed(() => this.sources.inScope(this.open(), i => i.source_name));

  /** Number on the top-bar Issues tab, scoped like the pages (connected clusters, or the selected one). */
  readonly openCount = computed(() => {
    const s = this.summary();
    if (!s || this.sources.historyMode()) return 0;
    const sel = this.sources.selectedSource();
    if (sel) return s.by_source?.[sel] ?? 0;
    return this.sources.connectedOnly(Object.entries(s.by_source ?? {}), e => e[0]).reduce((n, e) => n + e[1], 0);
  });

  constructor() {
    this.load();
    this.refresh.refresh$.subscribe(() => this.load());
  }

  load(): void {
    forkJoin({
      summary: this.http.get<IssueSummary>(`${API_BASE}/issues/summary`).pipe(catchError(() => of(null))),
      open: this.list({ status: 'open', limit: OPEN_LIMIT }).pipe(catchError(() => of(null))),
    }).subscribe(({ summary, open }) => {
      // A failed refresh keeps the last good snapshot.
      if (summary) this.summary.set(summary);
      if (open) this.open.set(open);
      this.error.set(open === null && !this.loaded());
      this.loaded.set(true);
    });
  }

  list(q: IssueQuery): Observable<Issue[]> {
    let params = new HttpParams();
    for (const [k, v] of Object.entries(q)) if (v !== undefined && v !== '') params = params.set(k, String(v));
    return this.http.get<Issue[] | null>(`${API_BASE}/issues`, { params }).pipe(orEmpty);
  }

  forWorkload(workloadId: string, status: IssueStatus | 'all' = 'open'): Observable<Issue[]> {
    const params = new HttpParams().set('status', status);
    return this.http.get<Issue[] | null>(`${API_BASE}/workloads/${encodeURIComponent(workloadId)}/issues`, { params }).pipe(orEmpty);
  }
}
