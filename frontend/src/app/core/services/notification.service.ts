import { Injectable, inject } from '@angular/core';
import { HttpClient, HttpParams } from '@angular/common/http';
import { Observable } from 'rxjs';
import { map } from 'rxjs/operators';

import { API_BASE } from './container.service';
import {
  ChannelInput, ChannelTestResult, NotificationChannel, NotificationDelivery,
} from '../models/issue.model';

/** Notification channels (Slack / Discord / webhook) and their delivery log. */
@Injectable({ providedIn: 'root' })
export class NotificationService {
  private http = inject(HttpClient);
  private base = `${API_BASE}/notifications`;

  channels(): Observable<NotificationChannel[]> {
    return this.http.get<NotificationChannel[] | null>(`${this.base}/channels`).pipe(map(l => l ?? []));
  }

  create(input: ChannelInput): Observable<NotificationChannel> {
    return this.http.post<NotificationChannel>(`${this.base}/channels`, input);
  }

  update(id: number, input: ChannelInput): Observable<NotificationChannel> {
    return this.http.put<NotificationChannel>(`${this.base}/channels/${id}`, input);
  }

  delete(id: number): Observable<unknown> {
    return this.http.delete(`${this.base}/channels/${id}`);
  }

  test(id: number): Observable<ChannelTestResult> {
    return this.http.post<ChannelTestResult>(`${this.base}/channels/${id}/test`, {});
  }

  deliveries(channelId: number | null, limit = 50): Observable<NotificationDelivery[]> {
    let params = new HttpParams().set('limit', limit);
    if (channelId != null) params = params.set('channel_id', channelId);
    return this.http.get<NotificationDelivery[] | null>(`${this.base}/deliveries`, { params }).pipe(map(l => l ?? []));
  }
}

/** Request body that re-saves a channel unchanged (url / secret omitted = keep), with `patch` applied. */
export function channelToInput(c: NotificationChannel, patch: Partial<ChannelInput> = {}): ChannelInput {
  return {
    name: c.name, type: c.type, enabled: c.enabled, min_severity: c.min_severity,
    sources: c.sources ?? [], namespaces: c.namespaces ?? [], types: c.types ?? [],
    notify_resolved: c.notify_resolved, include_logs: c.include_logs, repeat_minutes: c.repeat_minutes,
    ...patch,
  };
}
