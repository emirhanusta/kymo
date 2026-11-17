import { Injectable, inject } from '@angular/core';
import { HttpClient, HttpParams } from '@angular/common/http';
import { Observable } from 'rxjs';

import { API_BASE } from './container.service';
import {
  ActionResult, KubeContextList, PurgeRemovedResult, RemovedSource, ServerMeta, SourceInput, SourceStatus, SourceTestResult, SyncResult,
} from '../models/container.model';

/** Cluster (API: source) management (CRUD, connection test, kubeconfig context discovery) and server meta. */
@Injectable({ providedIn: 'root' })
export class SourceService {
  private http = inject(HttpClient);
  private base = API_BASE;

  getMeta(): Observable<ServerMeta> {
    return this.http.get<ServerMeta>(`${this.base}/meta`);
  }

  reloadConfig(): Observable<SyncResult> {
    return this.http.post<SyncResult>(`${this.base}/sources/reload`, {});
  }

  test(input: SourceInput): Observable<SourceTestResult> {
    return this.http.post<SourceTestResult>(`${this.base}/sources/test`, input);
  }

  contexts(body: { kubeconfig_path?: string; kubeconfig?: string }): Observable<KubeContextList> {
    return this.http.post<KubeContextList>(`${this.base}/sources/contexts`, body);
  }

  create(input: SourceInput): Observable<SourceStatus> {
    return this.http.post<SourceStatus>(`${this.base}/sources`, input);
  }

  update(name: string, input: SourceInput): Observable<SourceStatus> {
    return this.http.put<SourceStatus>(`${this.base}/sources/${encodeURIComponent(name)}`, input);
  }

  removed(): Observable<RemovedSource[]> {
    return this.http.get<RemovedSource[]>(`${this.base}/sources/removed`);
  }

  /** Permanently deletes the recorded history of a cluster that is no longer registered. */
  purgeRemoved(name: string): Observable<PurgeRemovedResult> {
    const params = new HttpParams().set('confirm', 'true');
    return this.http.delete<PurgeRemovedResult>(`${this.base}/sources/removed/${encodeURIComponent(name)}`, { params });
  }

  delete(name: string, purge: boolean): Observable<ActionResult> {
    const params = new HttpParams().set('purge', String(purge));
    return this.http.delete<ActionResult>(`${this.base}/sources/${encodeURIComponent(name)}`, { params });
  }
}

/** Builds an update body from a stored source (kubeconfig omitted → backend keeps the stored one). */
export function sourceToInput(src: SourceStatus, patch: Partial<SourceInput> = {}): SourceInput {
  const s = src.settings ?? { kubeconfig_uploaded: false };
  const input: SourceInput = {
    name: src.name,
    type: src.type,
    enabled: src.enabled,
    read_only: src.read_only,
  };
  if (s.kubeconfig_path) input.kubeconfig_path = s.kubeconfig_path;
  if (s.kube_context) input.kube_context = s.kube_context;
  if (s.poll_interval_seconds) input.poll_interval_seconds = s.poll_interval_seconds;
  return { ...input, ...patch };
}
