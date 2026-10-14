import { test, expect } from '@playwright/test';

// Every project runs over HTTPS with no machine setup (e2e/tls.ts). These
// checks prove the features that need a secure context actually work.
test.describe('HTTPS everywhere', () => {
  test('pages load in a secure context with a registered service worker', async ({ page, browserName }) => {
    await page.goto('/login');
    const env = await page.evaluate(async () => {
      // Bounded: without a trusted certificate Chromium never registers
      // the worker and ready would hang until the test timeout.
      const reg = await Promise.race([
        navigator.serviceWorker.ready,
        new Promise<null>(resolve => setTimeout(() => resolve(null), 10_000)),
      ]);
      return {
        protocol: location.protocol,
        hostname: location.hostname,
        secure: window.isSecureContext,
        sw: !!reg?.active,
        subtle: typeof crypto.subtle?.digest === 'function',
        uuid: typeof crypto.randomUUID === 'function',
        webauthn: typeof window.PublicKeyCredential === 'function',
      };
    });
    expect(env).toMatchObject({ protocol: 'https:', secure: true, sw: true, subtle: true, uuid: true, webauthn: true });
    if (browserName === 'chromium') {
      // A non-loopback name: loopback secure-context exemptions don't apply.
      expect(env.hostname).toBe('cato.test');
    }
  });

  test('WebAuthn creates a passkey with the hostname as relying party (Chromium virtual authenticator)', async ({ browserName, playwright, baseURL }, testInfo) => {
    test.skip(browserName !== 'chromium', 'virtual authenticators are a Chromium DevTools feature');
    // A dedicated browser: headless Chromium intermittently segfaults while
    // closing a context that used a virtual authenticator, which would take
    // down the worker's shared browser and fail whichever test runs next.
    const browser = await playwright.chromium.launch(testInfo.project.use.launchOptions);
    try {
      const context = await browser.newContext({ baseURL, ignoreHTTPSErrors: true });
      const page = await context.newPage();
      await page.goto('/login');
      const cdp = await context.newCDPSession(page);
      await cdp.send('WebAuthn.enable');
      await cdp.send('WebAuthn.addVirtualAuthenticator', {
        options: { protocol: 'ctap2', transport: 'internal', hasResidentKey: true, hasUserVerification: true, isUserVerified: true },
      });
      const created = await page.evaluate(async () => {
        const cred = await navigator.credentials.create({
          publicKey: {
            rp: { name: 'Cato', id: location.hostname },
            user: { id: crypto.getRandomValues(new Uint8Array(16)), name: 'e2e@example.com', displayName: 'E2E' },
            challenge: crypto.getRandomValues(new Uint8Array(32)),
            pubKeyCredParams: [{ type: 'public-key', alg: -7 }, { type: 'public-key', alg: -257 }],
            authenticatorSelection: { residentKey: 'required', userVerification: 'required' },
          },
        }) as PublicKeyCredential;
        return { type: cred.type, rawIdBytes: cred.rawId.byteLength };
      });
      expect(created.type).toBe('public-key');
      expect(created.rawIdBytes).toBeGreaterThan(0);
    } finally {
      await browser.close().catch(() => {});
    }
  });

  test('responses carry HTTPS security headers and __Host- session cookies', async ({ request }) => {
    const res = await request.get('/healthz');
    const headers = res.headers();
    expect(headers['strict-transport-security']).toBe('max-age=31536000');
    expect(headers['content-security-policy']).toContain('upgrade-insecure-requests');
    expect(headers['permissions-policy']).toContain('camera=(self)');

    const signup = await request.post('/api/auth/signup', {
      data: { email: `https-${Date.now()}@example.com`, password: 'correct-password-1' },
    });
    expect(signup.status()).toBe(201);
    const setCookie = signup.headersArray().find(h => h.name.toLowerCase() === 'set-cookie')?.value ?? '';
    expect(setCookie).toMatch(/^__Host-cato_session=/);
    expect(setCookie).toContain('Secure');
    expect(setCookie).toContain('Path=/');
    expect(setCookie).not.toMatch(/Domain=/i);
  });
});
