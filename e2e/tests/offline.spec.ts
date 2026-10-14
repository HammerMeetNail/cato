import { test, expect, Page } from '@playwright/test';
import { account, put, item, eventually, G } from './helpers';

async function waitForWorker(page: Page) {
  await page.evaluate(async () => { await navigator.serviceWorker.ready; });
  await expect.poll(() => page.evaluate(() => !!navigator.serviceWorker.controller)).toBe(true);
}

test('offline: versioned assets and the app shell come from the worker, other pages get the fallback', async ({ page, context }) => {
  await page.goto('/login');
  await waitForWorker(page);
  const manifest = await (await page.request.get('/asset-manifest.json')).json();
  const appJS = manifest.precache.find((u: string) => u.startsWith('/js/app.js?v='));
  await context.setOffline(true);
  try {
    expect(await page.evaluate(async url => (await fetch(url)).ok, appJS)).toBe(true);
    await page.goto('/offline-navigation-e2e');
    await expect(page.getByRole('heading', { name: "You're offline" })).toBeVisible();
  } finally {
    await context.setOffline(false);
  }
  await page.getByRole('link', { name: 'Retry' }).click();
  await expect(page).toHaveURL(/\/(login)?(#.*)?$/);
});

test('the installed app opens offline with the library, queues a write and sends it on reconnect', async ({ page, context }) => {
  const acc = await account(page);
  await put(acc, G.hades, { status: 'playing' });
  await page.goto('/#home');
  await waitForWorker(page);
  const card = page.locator(`.now-card[data-game-id="${G.hades}"]`);
  await expect(card).toBeVisible();
  // Let every lazily imported module reach the worker's cache.
  await page.goto('/#library');
  await expect(page.locator('.game-tile')).toHaveCount(1);
  await page.goto('/#home');
  await expect(card).toBeVisible();

  await context.setOffline(true);
  try {
    await page.reload();
    // The shell comes from the cache and the library from IndexedDB.
    await expect(card).toBeVisible();
    await card.getByRole('button', { name: 'Log 30m' }).click();
    await expect(card).toContainText('30m played');
    await expect(page.getByRole('status').filter({ hasText: 'waiting' })).toContainText('Offline · 1 change waiting');
    // A reload while offline keeps the queued change.
    await page.reload();
    await expect(card).toContainText('30m played');
    expect((await item(acc, G.hades)).minutes).toBe(0);
  } finally {
    await context.setOffline(false);
  }
  await page.evaluate(() => dispatchEvent(new Event('online')));
  await eventually(() => item(acc, G.hades), it => it.minutes === 30, 'queued session reached the server');
  await expect(page.getByRole('status').filter({ hasText: /waiting|Saving/ })).toHaveCount(0);
});
