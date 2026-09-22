import { test, expect } from '@playwright/test';

test('Playing time, Stats and Settings stay consistent across tabs', async ({ page }) => {
  const email = `release-${crypto.randomUUID()}@example.com`;
  const signup = await page.request.post('/api/auth/signup', {
    data: { email, password: 'release-password-1' },
  });
  expect(signup.status()).toBe(201);
  const { csrf_token } = await signup.json();
  const added = await page.request.post('/api/library/1', {
    headers: { 'X-CSRF-Token': csrf_token },
    data: { status: 'playing', playtime_minutes: 60 },
  });
  expect(added.ok()).toBeTruthy();
  await page.goto('/#now');
  const card = page.locator('.hero-card[data-game-id="1"]');
  await expect(card).toBeVisible();
  const save = page.waitForResponse(r => r.url().endsWith('/api/library/1') && r.request().method() === 'PATCH');
  await card.locator('[data-hero-time="30"]').click();
  expect((await save).ok()).toBeTruthy();
  await expect(card.locator('.hero-sub')).toContainText('1.5h');
  const item = await page.request.get('/api/library/1');
  expect((await item.json()).playtime_minutes).toBe(90);
  await page.locator('#bottom-tabs [data-route="stats"]').click();
  await expect(page.locator('#statsView')).toBeVisible();
  await expect(page.locator('#statsView .stat-grid')).toContainText('1.5h');
  await expect(page.locator('#playingView')).toBeHidden();
  await page.locator('#bottom-tabs [data-route="settings"]').click();
  await expect(page.locator('#accountEmail')).toHaveText(email);
  await expect(page.locator('#statsView')).toBeHidden();
  await page.locator('#bottom-tabs [data-route="library"]').click();
  await expect(page).toHaveURL(/#library$/);
  await expect(page.locator('#libraryView')).toBeVisible();
  await page.goBack();
  await expect(page.locator('#settingsView')).toBeVisible();
});

test('password change retains this session and revokes other sessions', async ({ page, playwright, baseURL }) => {
  const email = `password-${crypto.randomUUID()}@example.com`;
  const oldPassword = 'old-release-password';
  const newPassword = 'new-release-password';
  const signup = await page.request.post('/api/auth/signup', { data: { email, password: oldPassword } });
  expect(signup.status()).toBe(201);
  const other = await playwright.request.newContext({ baseURL });
  const fresh = await playwright.request.newContext({ baseURL });
  try {
    expect((await other.post('/api/auth/login', { data: { email, password: oldPassword } })).status()).toBe(200);
    await page.goto('/#settings');
    await page.locator('#currentPasswordInput').fill(oldPassword);
    await page.locator('#newPasswordInput').fill(newPassword);
    await page.locator('#confirmPasswordInput').fill(newPassword);
    await page.locator('#changePasswordForm button[type="submit"]').click();
    await expect(page.locator('#passwordFeedback')).toHaveText('Password updated');
    expect((await (await page.request.get('/api/me')).json()).authenticated).toBe(true);
    expect((await (await other.get('/api/me')).json()).authenticated).toBe(false);
    expect((await other.get('/api/library')).status()).toBe(401);
    expect((await fresh.post('/api/auth/login', { data: { email, password: oldPassword } })).status()).toBe(401);
    expect((await fresh.post('/api/auth/login', { data: { email, password: newPassword } })).status()).toBe(200);
  } finally {
    await other.dispose();
    await fresh.dispose();
  }
});
