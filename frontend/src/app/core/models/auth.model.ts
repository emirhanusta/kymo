export type Role = 'viewer' | 'operator' | 'admin' | 'owner';
export const ROLES: Role[] = ['viewer', 'operator', 'admin', 'owner'];

export type Permission =
  | 'workloads:manage' | 'logcapture:manage' | 'sources:manage' | 'channels:manage'
  | 'users:manage' | 'tokens:manage_all' | 'audit:read' | 'owner';

export interface User {
  id: number;
  username: string;
  /** "" when the user has no email. */
  email: string;
  name: string;
  role: Role;
  status: 'active' | 'disabled';
  has_password: boolean;
  sso: boolean;
  must_change_password: boolean;
  created_at: string;
  last_login_at: string | null;
}

export interface AuthConfig {
  auth_enabled: boolean;
  password_login: boolean;
  oidc: { enabled: boolean; name: string };
}

export interface MeResponse {
  auth_enabled: boolean;
  user: User;
  permissions: Permission[];
}

export interface LinkInfo {
  kind: 'invite' | 'password_reset';
  username: string;
  email: string;
  role?: Role;
  expires_at: string;
}

export interface Invite {
  id: number;
  username: string;
  email: string;
  role: Role;
  created_by: string;
  created_by_id?: number;
  created_at?: string;
  expires_at: string;
}

export interface LoginRequest { login: string; password: string; }
export interface InviteRequest { username: string; role: Role; email?: string; }
export interface ProfilePatch { name?: string; email?: string; }

export interface CreatedInvite { id: number; username: string; email: string; role: Role; url: string; expires_at: string; }
export interface ResetLink { url: string; expires_at: string; }

export interface ApiToken {
  id: number;
  name: string;
  prefix: string;
  role: Role;
  user_id: number;
  user_username?: string;
  user_email?: string;
  created_at: string;
  expires_at: string | null;
  last_used_at: string | null;
  revoked_at: string | null;
}

export interface CreatedToken { token: ApiToken; secret: string; }

export interface AuditEvent {
  id: number;
  time: string;
  actor_user_id: number | null;
  actor_label: string;
  action: string;
  target_type: string;
  target_id: string;
  details: unknown;
  ip: string;
}

export interface AuditQuery { user_id?: number | string; action?: string; before?: string | number; limit?: number; }

/** Minimum role that holds each permission (for "Requires <role>" hints). */
export const PERMISSION_ROLE: Record<Permission, Role> = {
  'workloads:manage': 'operator',
  'logcapture:manage': 'operator',
  'sources:manage': 'admin',
  'channels:manage': 'admin',
  'users:manage': 'admin',
  'tokens:manage_all': 'admin',
  'audit:read': 'admin',
  owner: 'owner',
};

/** Best human label for a user: username, then name, then email. */
export const userLabel = (u: { username?: string; name?: string; email?: string }): string =>
  u.username || u.name || u.email || '';

export const USERNAME_RE = /^[a-z0-9][a-z0-9._-]{1,63}$/;
