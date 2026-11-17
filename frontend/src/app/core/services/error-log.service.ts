import { Injectable, inject } from '@angular/core';
import { HttpClient, HttpParams } from '@angular/common/http';
import { Observable } from 'rxjs';
import { map } from 'rxjs/operators';

import { API_BASE } from './container.service';
import {
  ErrorOccurrence, ErrorQuery, ErrorSignature, ErrorWindow, LogCaptureState, LogCaptureStatus,
  LogCaptureTarget, LogCaptureTargetInput,
} from '../models/error-log.model';

const orEmpty = <T>() => map((l: T[] | null) => l ?? []);

/** Opt-in error-log capture: signatures, occurrences, capture state and targets. */
@Injectable({ providedIn: 'root' })
export class ErrorLogService {
  private http = inject(HttpClient);

  /** Signatures across workloads (Overview, filtered by cluster / namespace / workload). */
  list(q: ErrorQuery = {}): Observable<ErrorSignature[]> {
    let params = new HttpParams();
    for (const [k, v] of Object.entries(q)) if (v !== undefined && v !== '') params = params.set(k, String(v));
    return this.http.get<ErrorSignature[] | null>(`${API_BASE}/errors`, { params }).pipe(orEmpty<ErrorSignature>());
  }

  forWorkload(workloadId: string, since: ErrorWindow = '24h', limit = 100): Observable<ErrorSignature[]> {
    const params = new HttpParams().set('since', since).set('limit', limit);
    return this.http
      .get<ErrorSignature[] | null>(`${API_BASE}/workloads/${encodeURIComponent(workloadId)}/errors`, { params })
      .pipe(orEmpty<ErrorSignature>());
  }

  occurrences(signatureId: string, limit = 50, before?: string): Observable<ErrorOccurrence[]> {
    let params = new HttpParams().set('limit', limit);
    if (before) params = params.set('before', before);
    return this.http
      .get<ErrorOccurrence[] | null>(`${API_BASE}/errors/${encodeURIComponent(signatureId)}/occurrences`, { params })
      .pipe(orEmpty<ErrorOccurrence>());
  }

  captureState(workloadId: string): Observable<LogCaptureState> {
    return this.http.get<LogCaptureState>(`${API_BASE}/workloads/${encodeURIComponent(workloadId)}/log-capture`);
  }

  setCapture(workloadId: string, enabled: boolean): Observable<LogCaptureState> {
    return this.http.put<LogCaptureState>(`${API_BASE}/workloads/${encodeURIComponent(workloadId)}/log-capture`, { enabled });
  }

  status(): Observable<LogCaptureStatus> {
    return this.http.get<LogCaptureStatus>(`${API_BASE}/log-capture/status`);
  }

  targets(): Observable<LogCaptureTarget[]> {
    return this.http.get<LogCaptureTarget[] | null>(`${API_BASE}/log-capture/targets`).pipe(orEmpty<LogCaptureTarget>());
  }

  addTarget(input: LogCaptureTargetInput): Observable<LogCaptureTarget> {
    return this.http.post<LogCaptureTarget>(`${API_BASE}/log-capture/targets`, input);
  }

  deleteTarget(id: number): Observable<unknown> {
    return this.http.delete(`${API_BASE}/log-capture/targets/${id}`);
  }
}
