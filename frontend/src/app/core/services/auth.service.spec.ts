import { TestBed } from '@angular/core/testing';
import { provideHttpClient } from '@angular/common/http';
import { HttpTestingController, provideHttpClientTesting } from '@angular/common/http/testing';
import { describe, it, expect, beforeEach } from 'vitest';

import { AuthService } from './auth.service';

describe('AuthService', () => {
  let auth: AuthService;
  let http: HttpTestingController;
  beforeEach(() => {
    TestBed.configureTestingModule({ providers: [provideHttpClient(), provideHttpClientTesting()] });
    auth = TestBed.inject(AuthService);
    http = TestBed.inject(HttpTestingController);
  });

  it('posts {login, password} to /auth/login', () => {
    auth.login('alice', 'pw').subscribe();
    const req = http.expectOne(r => r.url.endsWith('/auth/login'));
    expect(req.request.body).toEqual({ login: 'alice', password: 'pw' });
    req.flush({ user: {} });
  });

  it('patches /me for profile updates', () => {
    auth.updateProfile({ email: '' }).subscribe();
    const req = http.expectOne(r => r.url.endsWith('/me'));
    expect(req.request.method).toBe('PATCH');
    expect(req.request.body).toEqual({ email: '' });
    req.flush({ user: { id: 1 } });
  });
});
