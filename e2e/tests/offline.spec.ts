import { test, expect, Page } from '@playwright/test';

async function waitForWorker(page: Page) {
  await page.evaluate(async () => { await navigator.serviceWorker.ready; });
  await expect.poll(() => page.evaluate(() => !!navigator.serviceWorker.controller)).toBe(true);
}

test('offline navigation uses the worker fallback and online retry recovers', async ({ page, context }) => {
  await page.goto('/login');
  await waitForWorker(page);
  await context.setOffline(true);
  try {
    expect(await page.evaluate(async () => (await fetch('/js/app.js')).ok)).toBe(true);
    await page.goto('/offline-navigation-e2e');
    await expect(page.getByRole('heading', { name: "You're offline" })).toBeVisible();
  } finally {
    await context.setOffline(false);
  }
  await page.getByRole('link', { name: 'Retry' }).click();
  await expect(page).toHaveURL(/\/login$/);
  await expect(page.locator('#loginForm')).toBeVisible();
});
