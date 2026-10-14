import { test, expect } from '@playwright/test';

// P0 performance regressions (docs/cato_smooth.md §2.3).

test('F9: no scroll-blocking touch/wheel listeners on document or window', async ({ page }) => {
  await page.addInitScript(() => {
    const seen: string[] = [];
    (window as any).__blockingListeners = seen;
    const original = EventTarget.prototype.addEventListener;
    EventTarget.prototype.addEventListener = function (type: string, listener: any, options?: any) {
      const scrollBlocking = ['touchstart', 'touchmove', 'wheel', 'mousewheel'].includes(type);
      const passive = typeof options === 'object' && options !== null ? options.passive : undefined;
      if (scrollBlocking && (this === document || this === window || this === document.documentElement || this === document.body) && passive !== true) {
        seen.push(type);
      }
      return original.call(this, type, listener, options);
    };
  });
  const email = `perf-${crypto.randomUUID()}@example.com`;
  expect((await page.request.post('/api/auth/signup', { data: { email, password: 'perf-password-1' } })).status()).toBe(201);
  await page.goto('/#library');
  await expect(page.locator('#searchInput')).toBeVisible();
  await page.locator('#searchInput').focus();
  await page.locator('#searchInput').fill('te');
  expect(await page.evaluate(() => (window as any).__blockingListeners)).toEqual([]);
});

test('F1: JS, CSS and HTML travel gzipped', async ({ request }) => {
  for (const path of ['/js/library.js', '/css/app.css', '/login', '/']) {
    const res = await request.get(path, { headers: { 'Accept-Encoding': 'gzip' } });
    expect(res.status(), path).toBe(200);
    expect(res.headers()['content-encoding'], path).toBe('gzip');
    expect(res.headers()['vary'], path).toContain('Accept-Encoding');
  }
});
