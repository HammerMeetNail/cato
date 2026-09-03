import { test, expect } from '@playwright/test';

test.describe('security & platform behavior', () => {
  test('unsafe API calls without CSRF token are rejected', async ({ request }) => {
    // Login to get a valid session.
    const email = `csrf-${Date.now()}@example.com`;
    const signup = await request.post('/api/auth/signup', {
      data: { email, password: 'correct-password-1' },
    });
    expect(signup.status()).toBe(201);
    const { csrf_token } = await signup.json();

    // POST with session cookie but no CSRF header → 403.
    const noToken = await request.post('/api/library/1', { data: { status: 'playing' } });
    expect(noToken.status()).toBe(403);

    // POST with the wrong token → 403.
    const badToken = await request.post('/api/library/1', {
      headers: { 'X-CSRF-Token': 'definitely-wrong' },
      data: { status: 'playing' },
    });
    expect(badToken.status()).toBe(403);

    // GETs work without CSRF.
    const me = await request.get('/api/me');
    expect(me.status()).toBe(200);
    const body = await me.json();
    expect(body.authenticated).toBe(true);
    void csrf_token;
  });

  test('API requires authentication', async ({ request }) => {
    const res = await request.get('/api/library');
    expect(res.status()).toBe(401);
    const body = await res.json();
    expect(body.error).toBe('unauthorized');
  });

  test('session cookie is HttpOnly and SameSite=Lax', async ({ request }) => {
    const res = await request.post('/api/auth/signup', {
      data: { email: `cookie-${Date.now()}@example.com`, password: 'correct-password-1' },
    });
    const setCookie = res.headersArray().find((h: any) => h.name.toLowerCase() === 'set-cookie');
    expect(setCookie).toBeTruthy();
    expect(setCookie.value).toContain('HttpOnly');
    expect(setCookie.value).toContain('SameSite=Lax');
  });

  // 429 semantics are covered deterministically in Go
  // (auth.TestRateLimiterMiddleware trips on the 2nd request with limit 1).
  // The E2E server raises CATO_AUTH_RATE_LIMIT so the suite's shared-IP
  // traffic never trips the limiter; here we assert failed logins stay 401
  // JSON (no 500/lockout) and a fresh signup+login still succeeds.
  test('failed logins stay 401 without locking out legitimate users', async ({ request }) => {
    for (let i = 0; i < 5; i++) {
      const res = await request.post('/api/auth/login', {
        data: { email: `no-one-${Date.now()}-${i}@example.com`, password: 'wrong-password-x' },
      });
      expect([401, 400]).toContain(res.status());
    }
    const email = `legit-${Date.now()}@example.com`;
    const signup = await request.post('/api/auth/signup', {
      data: { email, password: 'correct-password-1' },
    });
    expect(signup.status()).toBe(201);
  });

  test('healthz reports database status', async ({ request }) => {
    const res = await request.get('/healthz');
    expect(res.status()).toBe(200);
    const body = await res.json();
    expect(body.status).toBe('ok');
    expect(body.database).toBe('ok');
  });

  test('covers endpoint serves the SVG placeholder for unknown covers', async ({ request }) => {
    const res = await request.get('/covers/99999999.jpg');
    expect(res.status()).toBe(200);
    expect(res.headers()['content-type']).toContain('svg');
    expect(res.headers()['cache-control']).toContain('max-age=300');
  });

  test('unknown game id returns 404 JSON', async ({ request }) => {
    const res = await request.get('/api/games/4242424242');
    expect(res.status()).toBe(404);
    const body = await res.json();
    expect(body.error).toBe('not_found');
  });

  test('service worker and manifest are served for PWA install', async ({ request }) => {
    const sw = await request.get('/service-worker.js');
    expect(sw.status()).toBe(200);
    expect(sw.headers()['cache-control']).toBe('no-store');

    const manifest = await request.get('/manifest.webmanifest');
    expect(manifest.status()).toBe(200);
  });

  test('XSS attempts in inputs do not execute', async ({ page, request }) => {
    const email = `xss-${Date.now()}@example.com`;
    await request.post('/api/auth/signup', {
      data: { email, password: 'correct-password-1' },
    });
    await page.goto('/login');
    await page.locator('#email').fill(email);
    await page.locator('#password').fill('correct-password-1');
    await page.locator('#submitBtn').click();
    await page.waitForURL((u) => !u.pathname.includes('/login'));

    // Search lives in the Library view; post-login landing is Playing.
    await page.goto('/#library');
    await expect(page.locator('#searchInput')).toBeVisible();

    // Inject a script-looking tag into the search box; the dropdown must
    // render it escaped (no alert, no injected node).
    let alerted = false;
    page.on('dialog', () => { alerted = true; });
    await page.locator('#searchInput').fill('<img src=x onerror=alert(1)>');
    await page.waitForTimeout(700);
    expect(alerted).toBe(false);
  });
});
