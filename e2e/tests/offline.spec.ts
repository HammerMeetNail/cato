import { test, expect, Page } from '@playwright/test';

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
    // The installed app itself still opens: "/" is served from the cache.
    await page.goto('/');
    await expect(page).toHaveTitle(/Cato/);
    expect(await page.locator('script[type="importmap"]').count()).toBe(1);
  } finally {
    await context.setOffline(false);
  }
  await page.goto('/offline-navigation-e2e');
  await page.goto('/offline.html');
  await page.getByRole('link', { name: 'Retry' }).click();
  await expect(page).toHaveURL(/\/(login)?$/);
});
