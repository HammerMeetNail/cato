import { test, expect, Page } from '@playwright/test';

async function waitForWorker(page: Page) {
  await page.evaluate(async () => { await navigator.serviceWorker.ready; });
  await expect.poll(() => page.evaluate(() => !!navigator.serviceWorker.controller)).toBe(true);
}

test('service worker activation replaces stale shell caches and preserves covers', async ({ page, context }) => {
  // Keep the previous document alive while the new document installs the
  // worker. This avoids the observed empty cache entries in WebKit when
  // its only document navigates before any service worker is installed.
  const previousRelease = await context.newPage();
  await previousRelease.goto('/offline.html');
  await previousRelease.evaluate(async () => {
    await (await caches.open('cato-static-e2e-old')).put('/js/old-release.js', new Response('old'));
    await (await caches.open('cato-covers-v1')).put('/covers/e2e-preserved.jpg', await fetch('/icons/icon-192.png'));
    await (await caches.open('unrelated-e2e-cache')).put('/unrelated', new Response('unrelated'));
  });
  await page.goto('/login');
  await waitForWorker(page);
  const keys = await page.evaluate(() => caches.keys());
  expect(keys).not.toContain('cato-static-e2e-old');
  expect(keys).toContain('cato-covers-v1');
  expect(keys).toContain('unrelated-e2e-cache');
  const staticCaches = keys.filter(key => key.startsWith('cato-static-'));
  expect(staticCaches).toHaveLength(1);
  expect(await page.evaluate(async key => {
    const cache = await caches.open(key);
    return Promise.all(['/offline.html', '/js/app.js', '/css/app.css', '/manifest.webmanifest']
      .map(async path => (await cache.match(path))?.ok || false));
  }, staticCaches[0])).toEqual([true, true, true, true]);
  expect(await page.evaluate(async () => {
    const cover = await (await caches.open('cato-covers-v1')).match('/covers/e2e-preserved.jpg');
    return cover ? (await cover.arrayBuffer()).byteLength : 0;
  })).toBeGreaterThan(0);
  expect(await page.evaluate(async () => (await (await caches.open('unrelated-e2e-cache')).match('/unrelated'))?.text())).toBe('unrelated');
  await page.reload();
  await waitForWorker(page);
  const asset = page.waitForResponse(r => r.url().endsWith('/js/app.js'));
  expect(await page.evaluate(async () => (await fetch('/js/app.js')).ok)).toBe(true);
  expect((await asset).fromServiceWorker()).toBe(true);
});
