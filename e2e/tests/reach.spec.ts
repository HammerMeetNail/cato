import { test, expect } from '@playwright/test';
import { account, put, item, gotoApp, G, PASSWORD } from './helpers';

// Reach (plan §8): importing with a review step and a batch undo, passkeys,
// and the share target. Push delivery is covered by the Go tests (RFC 8291
// vector, delivery rules) and the service worker harness (push and
// notificationclick handlers): headless browsers can't reach a real push
// service.

test('CSV import: review uncertain matches, import, then undo the whole batch', async ({ page }) => {
  const acc = await account(page, 'import');
  await put(acc, G.tunic, { status: 'playing' });
  await gotoApp(page, '#you/import?source=file');
  const csv = [
    'Title,Platform,Status,Rating,Hours played,Date completed',
    'Hades,PC,Beaten,5,40,2024-02-01',
    'Celeste,Switch,Want to play,,,',
    'Tunic,PC,Beaten,4,15,2024-03-01',
    'Zelda Breath,Switch,Beaten,4.5,90,2023-01-01',
    'Definitely Not A Game 12345,PC,Backlog,,,',
  ].join('\n');
  await page.setInputFiles('input[type=file]', { name: 'my-games.csv', mimeType: 'text/csv', buffer: Buffer.from(csv) });

  // Uncertain matches are shown first and aren't ticked until confirmed.
  const zelda = page.locator('.import-row', { hasText: 'Zelda Breath' });
  await expect(zelda).toContainText('Is this right?');
  await expect(zelda.getByRole('checkbox')).not.toBeChecked();
  await zelda.getByRole('button', { name: 'Yes' }).click();
  await expect(zelda.getByRole('checkbox')).toBeChecked();

  await page.getByRole('radio', { name: /^All/ }).click();
  await expect(page.locator('.import-row', { hasText: 'Definitely Not A Game' })).toContainText('Not found');
  await expect(page.locator('.import-row', { hasText: 'Tunic' })).toContainText('In your library: fills gaps only');
  await page.getByRole('button', { name: /^Import \d+ games$/ }).click();
  await expect(page.getByRole('heading', { name: 'Imported' })).toBeVisible();

  const hades = await item(acc, G.hades);
  expect(hades.status).toBe('completed');
  expect(hades.rating).toBe(100);
  expect(hades.minutes).toBe(2400);
  expect((await item(acc, G.celeste)).status).toBe('wishlist');
  // An existing game keeps its shelf; it only gains what it lacked.
  const tunic = await item(acc, G.tunic);
  expect(tunic.status).toBe('playing');
  expect(tunic.rating).toBe(80);

  await page.getByRole('button', { name: 'Undo this import' }).click();
  await page.locator('dialog.sheet').getByRole('button', { name: 'Undo import' }).click();
  await expect.poll(async () => (await item(acc, G.hades)) === null).toBe(true);
  expect((await item(acc, G.tunic)).rating).toBe(0);
  await page.getByRole('radio', { name: 'History' }).click();
  await expect(page.locator('main')).toContainText('undone');
});

test('passkeys: add one in Settings, then sign in with it', async ({ browserName, playwright, baseURL }, testInfo) => {
  test.skip(browserName !== 'chromium', 'virtual authenticators are a Chromium DevTools feature (and WebKit runs on an IP address)');
  // A dedicated browser: see https.spec.ts on virtual authenticators.
  const browser = await playwright.chromium.launch(testInfo.project.use.launchOptions);
  try {
    const context = await browser.newContext({ baseURL, ignoreHTTPSErrors: true });
    const page = await context.newPage();
    const cdp = await context.newCDPSession(page);
    await cdp.send('WebAuthn.enable');
    await cdp.send('WebAuthn.addVirtualAuthenticator', {
      options: { protocol: 'ctap2', transport: 'internal', hasResidentKey: true, hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true },
    });
    const acc = await account(page, 'passkey');
    await gotoApp(page, '#you/settings?section=passkeys');
    await page.getByRole('button', { name: 'Add a passkey' }).click();
    await page.locator('dialog.sheet').getByLabel('Name').fill('E2E key');
    await page.locator('dialog.sheet').getByRole('button', { name: 'Continue' }).click();
    await expect(page.locator('#set-passkeys')).toContainText('E2E key');

    // Sign out, then back in with the passkey alone.
    await page.request.post('/api/auth/logout', { headers: { 'X-CSRF-Token': acc.csrf } });
    await page.goto('/login');
    await page.getByRole('button', { name: 'Sign in with a passkey' }).click();
    await expect(page.locator('nav.nav')).toBeVisible();
    const me = await (await page.request.get('/api/v1/me')).json();
    expect(me.user.email).toBe(acc.email);
    await context.close();
  } finally {
    await browser.close();
  }
});

test('share target: a shared store link opens the ⊕ sheet searched for the game', async ({ page }) => {
  await account(page, 'share');
  await page.goto('/share?title=' + encodeURIComponent('Hollow Knight on Steam') + '&url=' + encodeURIComponent('https://store.steampowered.com/app/367520/Hollow_Knight/'));
  const sheet = page.locator('dialog.sheet[aria-label="Log"]');
  await expect(sheet.getByRole('searchbox')).toHaveValue('Hollow Knight');
  await expect(sheet.locator('.log-row', { hasText: 'Hollow Knight' }).first()).toBeVisible();
});

test('a reset link from an admin sets a new password', async ({ browser, baseURL }) => {
  const ctx = await browser.newContext({ baseURL, ignoreHTTPSErrors: true });
  const admin = await ctx.newPage();
  const userCtx = await browser.newContext({ baseURL, ignoreHTTPSErrors: true });
  const userPage = await userCtx.newPage();
  try {
    const acc = await account(userPage, 'forgot');
    const me = await (await userPage.request.get('/api/v1/me')).json();
    expect((await admin.request.post('/api/auth/login', { data: { email: 'admin@e2e.test', password: PASSWORD } })).status()).toBe(200);
    const csrf = (await (await admin.request.get('/api/v1/me')).json()).csrf_token;
    const link = await (await admin.request.post(`/api/v1/admin/users/${me.user.id}/reset-link`, { headers: { 'X-CSRF-Token': csrf } })).json();
    const fresh = await browser.newContext({ baseURL, ignoreHTTPSErrors: true });
    const p = await fresh.newPage();
    await p.goto(link.path);
    await expect(p.locator('#formTitle')).toHaveText('Choose a new password');
    await p.fill('#password', 'a-brand-new-pass');
    await p.click('#submitBtn');
    await expect(p.locator('nav.nav')).toBeVisible();
    await fresh.close();
    // The old session was signed out.
    expect((await userPage.request.get('/api/v1/me')).status()).toBe(401);
    const login = await userPage.request.post('/api/auth/login', { data: { email: acc.email, password: 'a-brand-new-pass' } });
    expect(login.status()).toBe(200);
  } finally {
    await ctx.close();
    await userCtx.close();
  }
});
