import { test, expect, Page } from '@playwright/test';

async function waitForWorker(page: Page) {
  await page.evaluate(async () => { await navigator.serviceWorker.ready; });
  await expect.poll(() => page.evaluate(() => !!navigator.serviceWorker.controller)).toBe(true);
}

test('service worker precaches the asset manifest, replaces stale shells and keeps covers', async ({ page, context }) => {
  // Keep the previous document alive while the new document installs the
  // worker. This avoids the observed empty cache entries in WebKit when
  // its only document navigates before any service worker is installed.
  const previousRelease = await context.newPage();
  await previousRelease.goto('/offline.html');
  await previousRelease.evaluate(async () => {
    await (await caches.open('cato-static-v27')).put('/js/old-release.js', new Response('old'));
    await (await caches.open('cato-shell-e2e-old')).put('/js/old-release.js', new Response('old'));
    await (await caches.open('cato-covers-v1')).put('/covers/e2e-preserved.jpg', await fetch('/icons/icon-192.png'));
    await (await caches.open('unrelated-e2e-cache')).put('/unrelated', new Response('unrelated'));
  });
  await page.goto('/login');
  await waitForWorker(page);
  const manifest = await (await page.request.get('/asset-manifest.json')).json();
  const keys = await page.evaluate(() => caches.keys());
  expect(keys).not.toContain('cato-static-v27');
  expect(keys).not.toContain('cato-shell-e2e-old');
  expect(keys).toContain(`cato-shell-${manifest.version}`);
  expect(keys).toContain('cato-covers-v1');
  expect(keys).toContain('unrelated-e2e-cache');
  const appJS = manifest.precache.find((u: string) => u.startsWith('/js/app.js?v='));
  expect(appJS).toBeTruthy();
  expect(await page.evaluate(async ([key, paths]) => {
    const cache = await caches.open(key);
    return Promise.all(paths.map(async path => (await cache.match(path))?.ok || false));
  }, [`cato-shell-${manifest.version}`, ['/', '/offline.html', appJS, '/manifest.webmanifest']] as const)).toEqual([true, true, true, true]);
  expect(await page.evaluate(async () => {
    const cover = await (await caches.open('cato-covers-v1')).match('/covers/e2e-preserved.jpg');
    return cover ? (await cover.arrayBuffer()).byteLength : 0;
  })).toBeGreaterThan(0);
  expect(await page.evaluate(async () => (await (await caches.open('unrelated-e2e-cache')).match('/unrelated'))?.text())).toBe('unrelated');
  await page.reload();
  await waitForWorker(page);
  const asset = page.waitForResponse(r => r.url().endsWith(appJS));
  expect(await page.evaluate(async url => (await fetch(url)).ok, appJS)).toBe(true);
  expect((await asset).fromServiceWorker()).toBe(true);
});
