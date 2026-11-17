import { Injectable, inject } from '@angular/core';
import { HttpClient, HttpParams } from '@angular/common/http';
import { Observable, of } from 'rxjs';
import { catchError } from 'rxjs/operators';
import { Container, ContainerQuery, Page, ResourceSample, StabilityScore, LifecycleEvent, DeploymentInfo, ActionResult, SourceStatus, CronJobExecution, PodDetail, DeploymentDetail, RecentEventsQuery } from '../models/container.model';

export const API_BASE = '/api/v1';

@Injectable({ providedIn: 'root' })
export class ContainerService {
  private http = inject(HttpClient);
  /** Relative so it works behind the dev proxy and when the Go server serves the SPA. */
  private base = API_BASE;

  getSources(): Observable<SourceStatus[]> {
    return this.http.get<SourceStatus[]>(`${this.base}/sources`);
  }

  /** URL for the SSE log stream (EventSource cannot go through HttpClient). */
  logStreamUrl(id: string, tail = 200): string {
    return `${this.base}/containers/${encodeURIComponent(id)}/logs?tail=${tail}&follow=true`;
  }

  getContainers(
    runtime?: string,
    sourceName?: string,
    filters: { q?: string; status?: string; workload_kind?: string; workload?: string } = {},
  ): Observable<Container[]> {
    let params = new HttpParams();
    if (filters.q?.trim()) params = params.set('q', filters.q.trim());
    if (filters.status) params = params.set('status', filters.status);
    if (filters.workload_kind) params = params.set('workload_kind', filters.workload_kind);
    if (filters.workload) params = params.set('workload', filters.workload);
    if (runtime) {
      params = params.set('runtime', runtime);
    }
    if (sourceName) {
      params = params.set('source', sourceName);
    }
    return this.http.get<Container[]>(`${this.base}/containers`, { params });
  }

  getContainersPage(query: ContainerQuery): Observable<Page<Container>> {
    let params = new HttpParams()
      .set('page', query.page.toString())
      .set('limit', query.limit.toString())
      .set('sort', query.sort ?? 'last_seen_at')
      .set('order', query.order ?? 'desc');
    if (query.runtime) params = params.set('runtime', query.runtime);
    if (query.source) params = params.set('source', query.source);
    if (query.q?.trim()) params = params.set('q', query.q.trim());
    if (query.status) params = params.set('status', query.status);
    if (query.workload_kind) params = params.set('workload_kind', query.workload_kind);
    if (query.workload) params = params.set('workload', query.workload);
    return this.http.get<Page<Container>>(`${this.base}/containers`, { params });
  }

  getContainer(id: string): Observable<Container> {
    return this.http.get<Container>(`${this.base}/containers/${encodeURIComponent(id)}`);
  }

  getStabilityScore(id: string): Observable<StabilityScore | null> {
    return this.http.get<StabilityScore>(`${this.base}/containers/${encodeURIComponent(id)}/score`).pipe(
      catchError(() => of(null))
    );
  }

  getScoreHistory(id: string, limit = 10): Observable<StabilityScore[]> {
    const params = new HttpParams().set('n', limit.toString());
    return this.http.get<StabilityScore[]>(`${this.base}/containers/${encodeURIComponent(id)}/score/history`, { params }).pipe(
      catchError(() => of([]))
    );
  }

  getResourceSamples(id: string, from: string, to: string): Observable<ResourceSample[]> {
    const params = new HttpParams().set('from', from).set('to', to);
    return this.http.get<ResourceSample[]>(`${this.base}/containers/${encodeURIComponent(id)}/resources`, { params }).pipe(
      catchError(() => of([]))
    );
  }

  /** Backend returns newest-last, capped (default 200). */
  getLifecycleEvents(id: string, limit = 200): Observable<LifecycleEvent[]> {
    const params = new HttpParams().set('limit', limit.toString());
    return this.http.get<LifecycleEvent[]>(`${this.base}/containers/${encodeURIComponent(id)}/events`, { params }).pipe(
      catchError(() => of([]))
    );
  }

  // --- Management ---

  startContainer(id: string): Observable<ActionResult> {
    return this.http.post<ActionResult>(`${this.base}/containers/${encodeURIComponent(id)}/start`, {});
  }

  stopContainer(id: string): Observable<ActionResult> {
    return this.http.post<ActionResult>(`${this.base}/containers/${encodeURIComponent(id)}/stop`, {});
  }

  /** The backend resolves the container's own source; no `source` param needed. */
  restartContainer(id: string): Observable<ActionResult> {
    return this.http.post<ActionResult>(`${this.base}/containers/${encodeURIComponent(id)}/restart`, {});
  }

  killContainer(id: string): Observable<ActionResult> {
    return this.http.delete<ActionResult>(`${this.base}/containers/${encodeURIComponent(id)}/kill`);
  }

  getContainerLogs(id: string, tail = 200): Observable<string> {
    const params = new HttpParams().set('tail', tail.toString());
    return this.http.get(`${this.base}/containers/${encodeURIComponent(id)}/logs`, {
      params,
      responseType: 'text',
    });
  }

  // --- Archive / Ignore / Restore ---

  softDeleteContainer(id: string): Observable<ActionResult> {
    return this.http.request<ActionResult>('DELETE', `${this.base}/containers/${encodeURIComponent(id)}`, {
      body: { mode: 'soft' },
    });
  }

  ignoreContainer(id: string): Observable<ActionResult> {
    return this.http.request<ActionResult>('DELETE', `${this.base}/containers/${encodeURIComponent(id)}`, {
      body: { mode: 'ignore' },
    });
  }

  restoreContainer(id: string): Observable<ActionResult> {
    return this.http.post<ActionResult>(`${this.base}/containers/${encodeURIComponent(id)}/restore`, {});
  }

  hardDeleteContainer(id: string): Observable<ActionResult> {
    return this.http.request<ActionResult>('DELETE', `${this.base}/containers/${encodeURIComponent(id)}/permanent?confirm=true`, {});
  }

  getArchivedContainers(sourceName?: string): Observable<Container[]> {
    let params = new HttpParams();
    if (sourceName) params = params.set('source', sourceName);
    return this.http.get<Container[]>(`${this.base}/archive`, { params });
  }

  getIgnoredContainers(sourceName?: string): Observable<Container[]> {
    let params = new HttpParams();
    if (sourceName) params = params.set('source', sourceName);
    return this.http.get<Container[]>(`${this.base}/ignored`, { params });
  }

  purgeArchive(sourceName?: string): Observable<ActionResult> {
    let params = new HttpParams().set('confirm', 'true');
    if (sourceName) params = params.set('source', sourceName);
    return this.http.request<ActionResult>('DELETE', `${this.base}/archive`, { params });
  }

  purgeIgnored(sourceName?: string): Observable<ActionResult> {
    let params = new HttpParams().set('confirm', 'true');
    if (sourceName) params = params.set('source', sourceName);
    return this.http.request<ActionResult>('DELETE', `${this.base}/ignored`, { params });
  }

  // --- Deployments ---

  /** namespace '' lists deployments across all namespaces. */
  getDeployments(namespace = '', sourceName?: string): Observable<DeploymentInfo[]> {
    let params = new HttpParams().set('namespace', namespace);
    if (sourceName) params = params.set('source', sourceName);
    return this.http.get<DeploymentInfo[]>(`${this.base}/deployments`, { params });
  }

  scaleDeployment(namespace: string, name: string, replicas: number, sourceName?: string): Observable<ActionResult> {
    let params = new HttpParams();
    if (sourceName) params = params.set('source', sourceName);
    return this.http.post<ActionResult>(`${this.base}/deployments/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/scale`, { replicas }, { params });
  }

  restartDeployment(namespace: string, name: string, sourceName?: string): Observable<ActionResult> {
    let params = new HttpParams();
    if (sourceName) params = params.set('source', sourceName);
    return this.http.post<ActionResult>(`${this.base}/deployments/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/restart`, {}, { params });
  }

  // --- Pod / Deployment detail ---

  getPodDetail(namespace: string, podName: string, sourceName?: string): Observable<PodDetail | null> {
    let params = new HttpParams();
    if (sourceName) params = params.set('source', sourceName);
    return this.http.get<PodDetail>(
      `${this.base}/pods/${encodeURIComponent(namespace)}/${encodeURIComponent(podName)}`,
      { params }
    ).pipe(catchError(() => of(null)));
  }

  getDeploymentDetail(namespace: string, name: string, sourceName?: string): Observable<DeploymentDetail | null> {
    let params = new HttpParams();
    if (sourceName) params = params.set('source', sourceName);
    return this.http.get<DeploymentDetail>(
      `${this.base}/deployments/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`,
      { params }
    ).pipe(catchError(() => of(null)));
  }

  // --- Recent Events ---

  getRecentEvents(limit = 20, sourceName?: string): Observable<LifecycleEvent[]> {
    return this.queryRecentEvents({ limit, source: sourceName });
  }

  /** Newest first; `before` pages backwards (events strictly older than that timestamp). Max limit 200. */
  queryRecentEvents(q: RecentEventsQuery): Observable<LifecycleEvent[]> {
    let params = new HttpParams().set('limit', String(Math.min(q.limit ?? 20, 200)));
    if (q.source) params = params.set('source', q.source);
    if (q.type) params = params.set('type', q.type);
    if (q.before) params = params.set('before', q.before);
    return this.http.get<LifecycleEvent[]>(`${this.base}/events/recent`, { params });
  }

  // --- Namespaces ---

  getNamespaces(sourceName?: string): Observable<string[]> {
    let params = new HttpParams();
    if (sourceName) params = params.set('source', sourceName);
    return this.http.get<string[]>(`${this.base}/namespaces`, { params });
  }

  // --- CronJobs ---

  getCronJobs(namespace = '', sourceName?: string): Observable<CronJobExecution[]> {
    let params = new HttpParams();
    if (namespace) params = params.set('namespace', namespace);
    if (sourceName) params = params.set('source', sourceName);
    return this.http.get<CronJobExecution[]>(`${this.base}/cronjobs`, { params });
  }

  getCronJobExecutions(namespace: string, name: string, limit = 50, sourceName?: string): Observable<CronJobExecution[]> {
    let params = new HttpParams().set('limit', limit.toString());
    if (sourceName) params = params.set('source', sourceName);
    return this.http.get<CronJobExecution[]>(
      `${this.base}/cronjobs/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/executions`,
      { params }
    ).pipe(
      catchError(() => of([]))
    );
  }
}
