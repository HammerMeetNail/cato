import { test, expect } from '@playwright/test';
import { account, put, session, gotoApp, G, localDay } from './helpers';

// Delight (plan §10): goals with progress rings, Year in Review and its
// share image, mood chips on Up Next.

test('goals: add one, see the ring on Home and in Goals', async ({ page }) => {
  const acc = await account(page, 'goals');
  await put(acc, G.hades, { status: 'completed', rating: 90 });
  await gotoApp(page, '#you/goals');
  await page.getByRole('button', { name: 'Add a goal' }).first().click();
  const sheet = page.locator('dialog.sheet');
  await sheet.getByLabel(/How many games/).fill('4');
  await sheet.getByRole('button', { name: 'Add goal' }).click();
  const year = new Date().getFullYear();
  await expect(page.locator('main')).toContainText(`Finish 4 games in ${year}`);
  await expect(page.getByRole('img', { name: `Finish 4 games in ${year}: 1 of 4` })).toBeVisible();
  await gotoApp(page, '#home');
  await expect(page.getByRole('heading', { name: `Your ${year} goals` })).toBeVisible();
  await expect(page.getByRole('img', { name: /1 of 4/ })).toBeVisible();
});

test('Year in Review shows the year and makes a share image', async ({ page, browserName }) => {
  const acc = await account(page, 'year');
  const today = localDay();
  await put(acc, G.celeste, { status: 'playing', played_on: today });
  await session(acc, G.celeste, 95, today);
  await put(acc, G.celeste, { status: 'completed', rating: 100, played_on: today });
  const year = new Date().getFullYear();
  await gotoApp(page, `#you/year/${year}`);
  await expect(page.getByRole('heading', { name: `${year} in review` })).toBeVisible();
  await expect(page.locator('.yr-hero')).toContainText('1.6 hours');
  await expect(page.locator('main')).toContainText(`Your first finish of ${year}: Celeste`);
  test.skip(browserName !== 'chromium', 'download handling is checked in Chromium');
  const download = page.waitForEvent('download');
  await page.getByRole('button', { name: 'Share' }).click();
  expect((await download).suggestedFilename()).toBe(`cato-${year}.png`);
});

test('Up Next mood chips filter the picks', async ({ page }) => {
  const acc = await account(page, 'mood');
  await put(acc, G.stardew, { status: 'backlog', tags: ['cozy'] });
  await put(acc, G.dead, { status: 'backlog' });
  await gotoApp(page, '#home');
  await page.getByRole('radio', { name: 'cozy' }).click();
  const grid = page.locator('.upnext-grid');
  await expect(grid).toContainText('Stardew Valley');
  await expect(grid).not.toContainText('Dead Cells');
});
