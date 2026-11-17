import { ChannelTestResult, NotificationChannel } from '../../../core/models/issue.model';
import { issueTypeLabel } from '../../../shared/utils/issues';

/** "critical only · all clusters · all types" */
export function channelFilterSummary(c: NotificationChannel): string {
  const sev = c.min_severity === 'critical' ? 'critical only' : 'warning+';
  const sources = c.sources?.length
    ? (c.sources.length <= 2 ? c.sources.join(', ') : `${c.sources.length} clusters`)
    : 'all clusters';
  const ns = c.namespaces?.length ? (c.namespaces.length <= 2 ? `ns ${c.namespaces.join(', ')}` : `${c.namespaces.length} namespaces`) : '';
  const types = c.types?.length
    ? (c.types.length <= 2 ? c.types.map(issueTypeLabel).join(', ') : `${c.types.length} types`)
    : 'all types';
  return [sev, sources, ns, types].filter(Boolean).join(' · ');
}

export function channelTestMessage(name: string, r: ChannelTestResult): string {
  if (r.ok) return `Test message delivered to ${name}`;
  const code = r.status_code ? ` (HTTP ${r.status_code})` : '';
  return `Test to ${name} failed${code}${r.error ? `: ${r.error}` : ''}`;
}
