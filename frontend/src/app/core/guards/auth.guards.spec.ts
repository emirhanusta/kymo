import { TestBed } from '@angular/core/testing';
import { Router, ActivatedRouteSnapshot, RouterStateSnapshot, UrlTree } from '@angular/router';
import { provideRouter } from '@angular/router';
import { describe, it, expect, beforeEach } from 'vitest';

import { requirePasswordChange, requireUser, safeReturn } from './auth.guards';
import { AuthService } from '../services/auth.service';
import { User } from '../models/auth.model';

const user = (over: Partial<User> = {}): User => ({
  id: 1, username: 'admin', email: '', name: 'Admin', role: 'owner', status: 'active',
  has_password: true, sso: false, must_change_password: false, created_at: '', last_login_at: null, ...over,
});

const run = (guard: typeof requireUser, url: string) =>
  TestBed.runInInjectionContext(() => guard({} as ActivatedRouteSnapshot, { url } as RouterStateSnapshot));

describe('auth guards', () => {
  let auth: AuthService;
  let router: Router;
  beforeEach(() => {
    TestBed.configureTestingModule({ providers: [provideRouter([])] });
    auth = TestBed.inject(AuthService);
    router = TestBed.inject(Router);
  });

  it('sends anonymous users to /login with the return url', () => {
    const r = run(requireUser, '/workloads') as UrlTree;
    expect(router.serializeUrl(r)).toBe('/login?return=%2Fworkloads');
  });

  it('lets a normal user through', () => {
    auth.user.set(user());
    expect(run(requireUser, '/workloads')).toBe(true);
  });

  it('redirects must_change_password users to /change-password', () => {
    auth.user.set(user({ must_change_password: true }));
    const r = run(requireUser, '/workloads?kind=Deployment') as UrlTree;
    expect(router.serializeUrl(r)).toBe('/change-password?returnUrl=%2Fworkloads%3Fkind%3DDeployment');
  });

  it('change-password page requires a flagged session', () => {
    expect(router.serializeUrl(run(requirePasswordChange, '/change-password') as UrlTree)).toBe('/login');
    auth.user.set(user());
    expect(router.serializeUrl(run(requirePasswordChange, '/change-password') as UrlTree)).toBe('/dashboard');
    auth.user.set(user({ must_change_password: true }));
    expect(run(requirePasswordChange, '/change-password')).toBe(true);
  });

  it('safeReturn only accepts relative app paths', () => {
    expect(safeReturn('//evil.com')).toBe('/');
    expect(safeReturn('https://evil.com')).toBe('/');
    expect(safeReturn('/change-password')).toBe('/');
    expect(safeReturn('/issues')).toBe('/issues');
  });
});
