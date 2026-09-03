import { test, expect } from '@playwright/test';

const uniq = () => `e2e-${Date.now()}-${Math.floor(Math.random() * 1e6)}`;

async function signupViaUI(page, email: string, password: string) {
  await page.goto('/login');
  await page.locator('#toggleLink').click();
  await page.locator('#email').fill(email);
  await page.locator('#password').fill(password);
  await page.locator('#submitBtn').click();
  await page.waitForURL((u) => !u.pathname.includes('/login'), { timeout: 10_000 });
}

test.describe('auth journeys', () => {
  test('unauthenticated user is redirected from the app to /login', async ({ page }) => {
    await page.goto('/');
    await page.waitForURL(/\/login/);
    await expect(page.locator('#loginForm')).toBeVisible();
  });

  test('signup → lands in library → logout → login again', async ({ page }) => {
    const email = `${uniq()}@example.com`;
    const password = 'sup3r-secret-pw';

    // Sign up through the login page's toggle.
    await signupViaUI(page, email, password);

    // The shell is now authenticated: user menu visible, login link hidden.
    await expect(page.locator('#userMenuBtn')).toBeVisible();
    await expect(page.locator('#loginLink')).toBeHidden();

    // Log out through the user menu.
    await page.locator('#userMenuBtn').click();
    await page.locator('#logoutLink').click();
    await page.waitForURL(/\/login/);
    await expect(page.locator('#loginForm')).toBeVisible();

    // Log back in with the same credentials.
    await page.locator('#email').fill(email);
    await page.locator('#password').fill(password);
    await page.locator('#submitBtn').click();
    await page.waitForURL((u) => !u.pathname.includes('/login'));
    await expect(page.locator('#userMenuBtn')).toBeVisible();
  });

  test('wrong password shows an error', async ({ page, request }) => {
    const email = `${uniq()}@example.com`;
    const res = await request.post('/api/auth/signup', {
      data: { email, password: 'correct-password-1' },
    });
    expect(res.status()).toBe(201);

    await page.goto('/login');
    await page.locator('#email').fill(email);
    await page.locator('#password').fill('wrong-password-1');
    await page.locator('#submitBtn').click();
    await expect(page.locator('#errorMsg')).toContainText(/invalid/i);
    // Still on the login page.
    await expect(page.locator('#loginForm')).toBeVisible();
  });

  test('already-authenticated visits to /login bounce back to the app', async ({ page, request }) => {
    const email = `${uniq()}@example.com`;
    const res = await request.post('/api/auth/signup', {
      data: { email, password: 'correct-password-1' },
    });
    expect(res.status()).toBe(201);
    // Propagate the session cookie from the API context to the browser.
    const cookies = await (request as any).context?.().cookies?.();
    void cookies;

    await page.request.post('/api/auth/login', {
      data: { email, password: 'correct-password-1' },
    });
    await page.goto('/login');
    // checkAuth() in login.html should redirect authenticated users away.
    await page.waitForURL((u) => !u.pathname.includes('/login'), { timeout: 10_000 });
  });
});
