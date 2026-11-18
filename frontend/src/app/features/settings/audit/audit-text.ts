import { AuditEvent } from '../../../core/models/auth.model';

export interface AuditGroup { value: string; label: string; prefixes: string[]; }

/** Action groups of the filter. The API matches one action or `prefix.*`, so a group may need several queries. */
export const AUDIT_GROUPS: AuditGroup[] = [
  { value: 'signins',       label: 'Sign-ins',         prefixes: ['login', 'logout'] },
  { value: 'access',        label: 'Users & access',   prefixes: ['user', 'invite', 'token'] },
  { value: 'clusters',      label: 'Clusters',         prefixes: ['source'] },
  { value: 'notifications', label: 'Notifications',    prefixes: ['channel'] },
  { value: 'logcapture',    label: 'Log capture',      prefixes: ['logcapture'] },
  { value: 'workloads',     label: 'Workload actions', prefixes: ['pod', 'deployment', 'container'] },
];

export interface AuditDescription {
  /** Verb phrase that follows the actor ("signed in", "removed cluster kymo-test"). */
  text: string;
  /** Muted trailing context (only when the sentence does not already name the target). */
  target: string;
  /** Failed sign-in or a denied action. */
  danger: boolean;
}

export interface AuditLookups {
  userName(id: string): string;
  channelName(id: string): string;
}

type Details = Record<string, unknown>;

function details(e: AuditEvent): Details {
  let d = e.details;
  if (typeof d === 'string') { try { d = JSON.parse(d); } catch { return {}; } }
  return d && typeof d === 'object' && !Array.isArray(d) ? (d as Details) : {};
}

const str = (v: unknown): string => (typeof v === 'string' || typeof v === 'number' ? String(v) : '');

/** True for events that should carry the danger LED. */
export function isAuditFailure(action: string): boolean {
  return action === 'login.failure' || action.endsWith('.denied') || action.endsWith('.failure');
}

/** Turns an audit event into a readable sentence. Unknown actions fall back to the raw action name. */
export function describeAudit(e: AuditEvent, look: AuditLookups): AuditDescription {
  const d = details(e);
  const id = e.target_id;
  const danger = isAuditFailure(e.action);
  const ok = (text: string, target = ''): AuditDescription => ({ text, target, danger });

  const user = () => str(d['username']) || look.userName(id) || (id ? `#${id}` : 'a user');
  const channel = () => str(d['name']) || look.channelName(id) || (id ? `#${id}` : '');
  const cluster = () => id || 'a cluster';

  switch (e.action) {
    case 'login.success': return ok('signed in');
    case 'login.failure': {
      const reason = str(d['reason']);
      return ok('failed to sign in', reason ? reason.replace(/_/g, ' ') : '');
    }
    case 'logout': return ok('signed out');

    case 'source.create': return ok(`added cluster ${cluster()}`);
    case 'source.update': return ok(`updated cluster ${cluster()}`);
    case 'source.delete': return ok(`removed cluster ${cluster()}${d['purge'] ? ' and its history' : ''}`);
    case 'source.purge': return ok(`purged history of ${cluster()}`);
    case 'source.reload':
    case 'source.config_reload': return ok('reloaded cluster config');

    case 'channel.create': return ok(`created channel ${channel()}`.trim());
    case 'channel.update': return ok(`updated channel ${channel()}`.trim());
    case 'channel.delete': return ok(`deleted channel ${channel()}`.trim());
    case 'channel.test': return ok(`sent a test message to channel ${channel()}`.trim());

    case 'logcapture.target_create': {
      const scope = [str(d['source']), str(d['namespace'])].filter(Boolean).join('/');
      return ok(`added a log capture target${scope ? ' for ' + scope : ''}`);
    }
    case 'logcapture.target_delete': return ok('removed a log capture target', id ? `target #${id}` : '');
    case 'logcapture.workload_toggle': return ok(`turned log capture ${d['enabled'] ? 'on' : 'off'} for a workload`, id);

    case 'token.create': return ok(`created API token ${str(d['name']) || '#' + id}`);
    case 'token.revoke': return ok(`revoked API token ${str(d['name']) || '#' + id}`);

    case 'invite.create': return ok(`created a setup link for ${user()}`);
    case 'invite.accept': return ok('accepted a setup link');
    case 'invite.delete': return ok('revoked a setup link', id ? `link #${id}` : '');

    case 'user.create': return ok(`created user ${user()}${d['via'] === 'oidc' ? ' (SSO)' : ''}`);
    case 'user.password_change': return ok('changed their password');
    case 'user.password_reset': return ok(`reset the password of ${user()}`);
    case 'user.profile_update': return ok('updated their profile');
    case 'user.reset_link': return ok(`created a password reset link for ${user()}`);
    case 'user.ownership_transfer': return ok(`transferred ownership to ${str(d['to']) || user()}`);
    case 'user.role_change': {
      const to = str(d['to']);
      return ok(`changed role of ${user()}${to ? ' to ' + to : ''}${d['via'] === 'oidc' ? ' (SSO)' : ''}`);
    }
    case 'user.update': {
      const ch = (d['changes'] && typeof d['changes'] === 'object' ? d['changes'] : {}) as Record<string, { to?: unknown }>;
      const parts: string[] = [];
      if (ch['role']) parts.push(`changed role of ${user()} to ${str(ch['role'].to)}`);
      if (ch['status']) parts.push(`${ch['status'].to === 'disabled' ? 'disabled' : 'enabled'} ${user()}`);
      const rest = Object.keys(ch).filter(k => k !== 'role' && k !== 'status');
      if (rest.length) parts.push(`updated ${rest.join(', ')} of ${user()}`);
      return ok(parts.length ? parts.join(' and ') : `updated user ${user()}`);
    }

    case 'pod.restart': return ok('restarted a pod', id);
    case 'deployment.restart': return ok(`restarted deployment ${id}`, str(d['source']));
    case 'deployment.scale': return ok(`scaled deployment ${id} to ${str(d['replicas'])} replica${d['replicas'] === 1 ? '' : 's'}`, str(d['source']));
    case 'container.restore': return ok('restored a workload from the archive', id);
    case 'container.hard_delete': return ok('permanently deleted a container', id);
    case 'container.purge_archive': return ok(`purged the archive of ${id}`);
    case 'container.purge_ignored': return ok(`purged ignored workloads of ${id}`);
  }

  if (e.action.endsWith('.denied')) {
    return { text: `was denied ${e.action.slice(0, -'.denied'.length).replace(/[._]/g, ' ')}`, target: id, danger: true };
  }
  // container.<mode> (archive / delete modes of the container delete API)
  if (e.action.startsWith('container.')) return ok(`${e.action.slice(10).replace(/_/g, ' ')} a container`, id);

  return { text: e.action, target: [e.target_type, id].filter(Boolean).join(' · '), danger };
}
