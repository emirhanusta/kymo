import { Injectable, inject } from '@angular/core';
import { HttpClient, HttpParams } from '@angular/common/http';
import { Observable } from 'rxjs';
import { map } from 'rxjs/operators';

import { API_BASE } from './container.service';
import { Activity, OverviewChange } from '../models/overview.model';

/** Read-only aggregates behind the Overview page. `source` = cluster name (empty = all connected clusters). */
@Injectable({ providedIn: 'root' })
export class OverviewService {
  private http = inject(HttpClient);

  activity(source: string, hours = 24): Observable<Activity> {
    let params = new HttpParams().set('hours', String(hours));
    if (source) params = params.set('source', source);
    return this.http.get<Activity | null>(`${API_BASE}/overview/activity`, { params })
      .pipe(map(a => ({ buckets: a?.buckets ?? [], previous_crashes: a?.previous_crashes ?? 0 })));
  }

  changes(source: string, limit = 6): Observable<OverviewChange[]> {
    let params = new HttpParams().set('limit', String(limit));
    if (source) params = params.set('source', source);
    return this.http.get<OverviewChange[] | null>(`${API_BASE}/overview/changes`, { params }).pipe(map(l => l ?? []));
  }
}
