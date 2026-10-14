import { test, expect, Page } from '@playwright/test';
import { account, put, session, localDay, G } from './helpers';

// Performance budgets (docs/cato_smooth.md §4.2), Chromium only: a cold
// load of Home with an empty HTTP cache and the service worker blocked.
test.use({ serviceWorkers: 'block' });

// requestsBeforePaint counts what blocks the first paint of the shell: the
// document, stylesheets and parser-blocking scripts. Without a build step
// every ES module is its own request, so the module graph is checked for a
// waterfall instead: the import map plus modulepreload let the browser
// request every eager module straight from the HTML, in one wave.
const BUDGET = {
  requestsBeforePaint: 12,
  initialJSBytes: 60 * 1024,
  cssBytes: 20 * 1024,
  lcpMs: 1000,
  inpMs: 100,
};

async function seed(page: Page) {
  const acc = await account(page, 'perf');
  for (const id of [G.hades, G.celeste, G.hollow]) await put(acc, id, { status: 'playing' });
  for (const id of [G.tunic, G.balatro, G.disco, G.outer]) await put(acc, id, { status: 'backlog' });
  await session(acc, G.hades, 60, localDay());
  return acc;
}

test('cold load of Home stays within the request, JS, CSS and LCP budgets, with no module waterfall', async ({ page, browserName }, testInfo) => {
  test.skip(browserName !== 'chromium', 'budgets are measured in Chromium');
  await seed(page);
  await page.addInitScript(() => {
    (window as any).__lcp = 0;
    new PerformanceObserver(list => {
      for (const e of list.getEntries()) (window as any).__lcp = Math.max((window as any).__lcp, e.startTime);
    }).observe({ type: 'largest-contentful-paint', buffered: true });
  });
  const requests: { url: string; type: string; start: number }[] = [];
  const t0 = Date.now();
  page.on('request', r => requests.push({ url: r.url(), type: r.resourceType(), start: Date.now() - t0 }));
  // Who discovered each script: 'parser' means the HTML (modulepreload,
  // script tags); 'script' means another module's import, a waterfall.
  const cdp = await page.context().newCDPSession(page);
  await cdp.send('Network.enable');
  const initiators = new Map<string, string>();
  cdp.on('Network.requestWillBeSent', (e: any) => {
    if (e.type === 'Script') initiators.set(new URL(e.request.url).pathname, e.initiator?.type ?? '');
  });
  const finished: { url: string; type: string; bytes: number; end: number }[] = [];
  page.on('requestfinished', async r => {
    const sizes = await r.sizes().catch(() => null);
    finished.push({ url: r.url(), type: r.resourceType(), bytes: sizes?.responseBodySize ?? 0, end: Date.now() - t0 });
  });
  await page.goto('/#home');
  await expect(page.locator('.now-card').first()).toBeVisible();
  const paintedAt = Date.now() - t0;
  await page.waitForLoadState('networkidle');
  const lcp = await page.evaluate(() => (window as any).__lcp);

  const beforePaint = requests.filter(r => r.start <= paintedAt);
  const blocking = beforePaint.filter(r => r.type === 'document' || r.type === 'stylesheet' ||
    (r.type === 'script' && /\/js\/(theme|head)-init\.js/.test(r.url)));
  const lazyPaths = new Set(finished.filter(r => r.type === 'script' && r.end > paintedAt).map(r => new URL(r.url).pathname));
  const waterfall = [...initiators].filter(([path, type]) => type !== 'parser' && !lazyPaths.has(path)).map(([path]) => path);
  const js = finished.filter(r => r.type === 'script' && r.end <= paintedAt);
  const jsBytes = js.reduce((n, r) => n + r.bytes, 0);
  const css = finished.filter(r => r.type === 'stylesheet');
  const cssBytes = css.reduce((n, r) => n + r.bytes, 0);
  const lazy = finished.filter(r => r.type === 'script' && r.end > paintedAt).map(r => new URL(r.url).pathname);
  const report = {
    renderBlockingRequests: blocking.length,
    requestsBeforePaint: beforePaint.length,
    scriptsBeforePaint: js.length,
    initialJSKB: +(jsBytes / 1024).toFixed(1),
    cssKB: +(cssBytes / 1024).toFixed(1),
    lcpMs: Math.round(lcp),
    paintedAtMs: paintedAt,
    scriptsAfterPaint: lazy,
    modulesFromImports: waterfall,
  };
  testInfo.annotations.push({ type: 'perf', description: JSON.stringify(report) });
  console.log(`[perf ${testInfo.project.name}] ${JSON.stringify(report)}`);
  expect.soft(blocking.length, `render-blocking requests:\n${blocking.map(r => new URL(r.url).pathname).join('\n')}`).toBeLessThanOrEqual(BUDGET.requestsBeforePaint);
  expect.soft(waterfall, 'eager modules discovered by imports instead of the HTML (a waterfall)').toEqual([]);
  expect.soft(jsBytes, 'initial JS (gzipped bytes)').toBeLessThanOrEqual(BUDGET.initialJSBytes);
  expect.soft(cssBytes, 'CSS (gzipped bytes)').toBeLessThanOrEqual(BUDGET.cssBytes);
  expect.soft(lcp, 'LCP (ms)').toBeLessThan(BUDGET.lcpMs);
  // Every static text asset traveled compressed.
  for (const r of finished.filter(f => /\.(js|css)(\?|$)/.test(f.url))) {
    const res = await page.request.get(r.url, { headers: { 'Accept-Encoding': 'gzip' } });
    expect(res.headers()['content-encoding'], r.url).toBe('gzip');
  }
});

// INP is the slowest interaction of a visit. One visit on a shared machine
// is noisy, so three visits run and their median INP must meet the budget
// (field data uses the 75th percentile across visits for the same reason).
test('interactions stay responsive on a throttled phone (INP)', async ({ page, browserName }, testInfo) => {
  test.skip(browserName !== 'chromium', 'CPU throttling is a Chromium DevTools feature');
  await page.setViewportSize({ width: 412, height: 915 });
  await seed(page);
  await page.addInitScript(() => {
    (window as any).__events = [];
    new PerformanceObserver(list => {
      for (const e of list.getEntries() as any[]) {
        if (e.interactionId) (window as any).__events.push({ id: e.interactionId, name: e.name, duration: e.duration, target: e.target ? (e.target.getAttribute?.('aria-label') || e.target.tagName) : '' });
      }
    }).observe({ type: 'event', buffered: true, durationThreshold: 16 } as any);
  });
  const cdp = await page.context().newCDPSession(page);
  const visits: { inp: number; worst: object[] }[] = [];
  for (let visit = 0; visit < 3; visit++) {
    // A real load each time (a hash change would keep the same document).
    await page.goto(`/?visit=${visit}#home`);
    const card = page.locator(`.now-card[data-game-id="${G.hades}"]`);
    await expect(card).toBeVisible();
    await cdp.send('Emulation.setCPUThrottlingRate', { rate: 4 });
    try {
      await card.getByRole('button', { name: 'Log 30m' }).click();
      await expect(page.locator('.toast')).toContainText('Logged 30m');
      await page.locator('nav.nav .nav-log').click();
      await expect(page.locator('dialog.sheet[aria-label="Log"]')).toBeVisible();
      await page.keyboard.type('zel');
      await page.keyboard.press('Escape');
      await page.locator('nav.nav a[data-nav="library"]').click();
      await expect(page.locator('.game-tile').first()).toBeVisible();
      await page.locator('#lib-search').pressSequentially('hol');
      await page.waitForTimeout(500);
    } finally {
      await cdp.send('Emulation.setCPUThrottlingRate', { rate: 1 });
    }
    const events: { id: number; name: string; duration: number }[] = await page.evaluate(() => (window as any).__events);
    const byInteraction = new Map<number, { name: string; duration: number }>();
    for (const e of events) {
      const cur = byInteraction.get(e.id);
      if (!cur || e.duration > cur.duration) byInteraction.set(e.id, e);
    }
    const worst = [...byInteraction.values()].sort((a, b) => b.duration - a.duration);
    visits.push({ inp: worst[0]?.duration ?? 0, worst: worst.slice(0, 2) });
  }
  const inps = visits.map(v => v.inp).sort((a, b) => a - b);
  const median = inps[1];
  console.log(`[inp ${testInfo.project.name}] median ${median} ms of ${JSON.stringify(inps)}; worst: ${JSON.stringify(visits.map(v => v.worst[0]))}`);
  testInfo.annotations.push({ type: 'inp', description: `median ${median} ms (${inps.join(', ')})` });
  expect(median, 'INP with 4× CPU throttling, median of three visits (ms)').toBeLessThan(BUDGET.inpMs);
});
