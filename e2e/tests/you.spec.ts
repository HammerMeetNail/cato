import { test, expect } from '@playwright/test';
import { account, put, session, gotoApp, localDay, G, PASSWORD } from './helpers';

test('stats show this year\'s numbers and the diary lists sessions', async ({ page }) => {
  const acc = await account(page);
  const today = localDay();
  await put(acc, G.hades, { status: 'playing' });
  await session(acc, G.hades, 90, today);
  await session(acc, G.hades, 60, today);
  await put(acc, G.celeste, { status: 'completed', rating: 90 });
  await gotoApp(page, '#you');
  await expect(page.getByRole('heading', { level: 1 })).toContainText('@example.com');
  await page.getByRole('link', { name: /Stats/ }).click();
  const tiles = page.locator('.stat-tile');
  await expect(tiles.nth(0)).toContainText('2h 30m');
  await expect(tiles.nth(0)).toContainText(`played in ${today.slice(0, 4)}`);
  await expect(tiles.nth(1).locator('.value')).toHaveText('1');
  await expect(page.locator('.heatmap span[data-l="3"]')).toHaveCount(1);

  await page.goto('/#you/diary');
  await expect(page.locator('.diary-day .list-row')).toHaveCount(2);
  await expect(page.locator('.diary-day h3').first()).toHaveText('Today');
  await expect(page.locator('.calendar .day.played')).toHaveCount(1);
});

test('theme and rating display persist across reloads', async ({ page }) => {
  const acc = await account(page);
  await put(acc, G.celeste, { status: 'completed', rating: 80 });
  await gotoApp(page, '#you/settings');
  await page.getByRole('radiogroup', { name: 'Theme' }).getByRole('radio', { name: 'Dark' }).click();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  await page.reload();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  await expect(page.getByRole('radiogroup', { name: 'Theme' }).getByRole('radio', { name: 'Dark' })).toHaveAttribute('aria-checked', 'true');

  await page.getByRole('radiogroup', { name: 'Rating display' }).getByRole('radio', { name: '10 points' }).click();
  await expect(page.locator('.toast')).toContainText('Saved');
  await page.goto('/#library');
  await expect(page.locator(`.game-tile[data-game-id="${G.celeste}"]`)).toContainText('8/10');
  await page.goto('/#you/settings');
  await page.getByRole('radiogroup', { name: 'Theme' }).getByRole('radio', { name: 'System' }).click();
  await expect(page.locator('html')).not.toHaveAttribute('data-theme', /.*/);
});

test('devices: this one is marked and another can be signed out', async ({ page, playwright, baseURL }) => {
  const acc = await account(page);
  const other = await playwright.request.newContext({ baseURL, ignoreHTTPSErrors: true, userAgent: 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) Safari/604.1' });
  try {
    expect((await other.post('/api/auth/login', { data: { email: acc.email, password: PASSWORD } })).status()).toBe(200);
    await gotoApp(page, '#you/settings');
    const section = page.locator('#set-devices');
    await expect(section).toContainText('(this device)');
    await expect(section).toContainText('Safari on iOS');
    await section.getByRole('button', { name: 'Sign out', exact: true }).click();
    await expect(page.locator('.toast')).toContainText('Signed out');
    await expect(section).not.toContainText('Safari on iOS');
    expect((await other.get('/api/v1/me')).status()).toBe(401);
  } finally {
    await other.dispose();
  }
});

test('help lists the keyboard shortcuts and IGDB attribution', async ({ page }) => {
  await account(page);
  await gotoApp(page, '#you/help');
  await expect(page.getByRole('heading', { name: 'Keyboard shortcuts' })).toBeVisible();
  await expect(page.getByRole('link', { name: 'powered by IGDB' })).toBeVisible();
});
