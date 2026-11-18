import { Injectable, computed, inject, signal } from '@angular/core';
import { HttpClient, HttpParams } from '@angular/common/http';
import { Observable, firstValueFrom, of } from 'rxjs';
import { catchError, map, tap } from 'rxjs/operators';

import { API_BASE } from './container.service';
import {
  AddUserRequest, AddUserResult, ApiToken, AuditEvent, AuditQuery, AuthConfig, CreatedInvite, CreatedToken, Invite, InviteRequest, LinkInfo, LoginRequest, MeResponse, ProfilePatch,
  PERMISSION_ROLE, Permission, ROLES, ResetLink, Role, User,
} from '../models/auth.model';

const orEmpty = <T>() => map((l: T[] | null) => l ?? []);

/** Session state (config + current user) and the auth / users / tokens / audit API. */
@Injectable({ providedIn: 'root' })
export class AuthService {
  private http = inject(HttpClient);
  private base = API_BASE;

  readonly config = signal<AuthConfig | null>(null);
  readonly user = signal<User | null>(null);
  readonly permissions = signal<Permission[]>([]);
  /** Defaults to true until /auth/me has said otherwise. */
  readonly authEnabled = signal(true);
  readonly role = computed<Role | null>(() => this.user()?.role ?? null);
  readonly mustChangePassword = computed(() => !!this.user()?.must_change_password);

  /** Loads /auth/config and /auth/me. Never rejects (a failing API leaves the user empty → login). */
  async init(): Promise<void> {
    const cfg = await firstValueFrom(this.http.get<AuthConfig>(`${this.base}/auth/config`).pipe(catchError(() => of(null))));
    if (cfg) {
      this.config.set(cfg);
      this.authEnabled.set(cfg.auth_enabled);
    }
    await firstValueFrom(this.loadMe());
  }

  loadMe(): Observable<MeResponse | null> {
    return this.http.get<MeResponse>(`${this.base}/auth/me`).pipe(
      tap(m => this.setSession(m.user, m.permissions, m.auth_enabled)),
      catchError(() => { this.clear(); return of(null); }),
    );
  }

  clear(): void {
    this.user.set(null);
    this.permissions.set([]);
  }

  private setSession(user: User, permissions: Permission[], authEnabled = this.authEnabled()): void {
    this.user.set(user);
    this.permissions.set(permissions ?? []);
    this.authEnabled.set(authEnabled);
  }

  can(permission: Permission): boolean {
    return this.permissions().includes(permission);
  }

  /** True when the current role is at least `min`. */
  hasRole(min: Role): boolean {
    const r = this.role();
    return !!r && ROLES.indexOf(r) >= ROLES.indexOf(min);
  }

  /** Tooltip for a control the user may not use ('' when allowed). */
  denyHint(permission: Permission): string {
    return this.can(permission) ? '' : `Requires ${PERMISSION_ROLE[permission]}`;
  }

  // ── public auth ──────────────────────────────────────────────
  /** `login` is a username or an email address. */
  login(login: string, password: string): Observable<User> {
    const body: LoginRequest = { login, password };
    return this.http.post<{ user: User }>(`${this.base}/auth/login`, body).pipe(map(r => r.user));
  }

  link(token: string): Observable<LinkInfo> {
    return this.http.get<LinkInfo>(`${this.base}/auth/links/${encodeURIComponent(token)}`);
  }

  acceptLink(token: string, body: { name?: string; password: string }): Observable<User> {
    return this.http.post<{ user: User }>(`${this.base}/auth/links/${encodeURIComponent(token)}`, body).pipe(map(r => r.user));
  }

  /** Reloads session state after login / link acceptance. */
  async refreshSession(): Promise<void> {
    const cfg = await firstValueFrom(this.http.get<AuthConfig>(`${this.base}/auth/config`).pipe(catchError(() => of(null))));
    if (cfg) { this.config.set(cfg); this.authEnabled.set(cfg.auth_enabled); }
    await firstValueFrom(this.loadMe());
  }

  oidcLogin(returnUrl = '/'): void {
    window.location.href = `${this.base}/auth/oidc/login?return_to=${encodeURIComponent(returnUrl)}`;
  }

  logout(): Observable<void> {
    return this.http.post<void>(`${this.base}/auth/logout`, {}).pipe(tap(() => this.clear()));
  }

  changePassword(current_password: string, new_password: string): Observable<void> {
    return this.http.post<void>(`${this.base}/me/password`, { current_password, new_password });
  }

  /** Updates the signed-in user's own name / email ("" clears the email), then refreshes session state. */
  updateProfile(patch: ProfilePatch): Observable<User> {
    return this.http.patch<{ user: User }>(`${this.base}/me`, patch).pipe(
      map(r => r.user),
      tap(u => this.user.set(u)),
    );
  }

  // ── users ────────────────────────────────────────────────────
  users(): Observable<User[]> {
    return this.http.get<User[] | null>(`${this.base}/users`).pipe(orEmpty<User>());
  }

  updateUser(id: number, patch: { name?: string; email?: string; role?: Role; status?: 'active' | 'disabled' }): Observable<User> {
    return this.http.patch<User>(`${this.base}/users/${id}`, patch);
  }

  resetLink(id: number): Observable<ResetLink> {
    return this.http.post<ResetLink>(`${this.base}/users/${id}/reset-link`, {});
  }

  transferOwnership(id: number): Observable<void> {
    return this.http.post<void>(`${this.base}/users/${id}/transfer-ownership`, {});
  }

  invites(): Observable<Invite[]> {
    return this.http.get<Invite[] | null>(`${this.base}/users/invites`).pipe(orEmpty<Invite>());
  }

  createUser(body: AddUserRequest): Observable<AddUserResult> {
    return this.http.post<AddUserResult>(`${this.base}/users`, body);
  }

  createInvite(body: InviteRequest): Observable<CreatedInvite> {
    return this.http.post<CreatedInvite>(`${this.base}/users/invites`, body);
  }

  revokeInvite(id: number): Observable<void> {
    return this.http.delete<void>(`${this.base}/users/invites/${id}`);
  }

  // ── tokens ───────────────────────────────────────────────────
  tokens(all = false): Observable<ApiToken[]> {
    const params = all ? new HttpParams().set('all', '1') : undefined;
    return this.http.get<ApiToken[] | null>(`${this.base}/tokens`, { params }).pipe(orEmpty<ApiToken>());
  }

  createToken(name: string, role: Role, expires_in_days: number | null): Observable<CreatedToken> {
    return this.http.post<CreatedToken>(`${this.base}/tokens`, { name, role, expires_in_days });
  }

  revokeToken(id: number): Observable<void> {
    return this.http.delete<void>(`${this.base}/tokens/${id}`);
  }

  // ── audit ────────────────────────────────────────────────────
  audit(q: AuditQuery = {}): Observable<AuditEvent[]> {
    let params = new HttpParams();
    for (const [k, v] of Object.entries(q)) if (v !== undefined && v !== '' && v !== null) params = params.set(k, String(v));
    return this.http.get<AuditEvent[] | null>(`${this.base}/audit`, { params }).pipe(orEmpty<AuditEvent>());
  }
}
