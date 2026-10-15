import { test, expect, Page, CDPSession } from '@playwright/test';
import { account, put, gotoApp, G } from './helpers';

// Swipe navigation on touch screens (app.js): right goes back, left goes
// forward; edge swipes (the OS's), swipes inside horizontal scrollers and
// the setting turned off do nothing. Real touch events through CDP, so
// Chromium only (the phone project).

async function swipe(cdp: CDPSession, x0: number, y: number, x1: number) {
  await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: x0, y }] });
  const steps = 8;
  for (let i = 1; i <= steps; i++) {
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x: x0 + ((x1 - x0) * i) / steps, y }] });
  }
  await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
}

async function open(page: Page, name: string) {
  await page.locator(`nav.nav [data-nav="${name}"]`).click();
  await expect(page).toHaveURL(new RegExp(`#${name}`));
}

test('swipe right goes back, swipe left goes forward; edges and scrollers are left alone', async ({ page }) => {
  const acc = await account(page, 'swipe');
  await put(acc, G.hades, { status: 'playing' });
  await gotoApp(page, '#home');
  const cdp = await page.context().newCDPSession(page);
  const { width } = page.viewportSize()!;
  // Each sideways gesture reports what came of it, for phones we can't test.
  const outcomes: string[] = [];
  page.on('request', r => { if (r.url().endsWith('/api/v1/diagnostics/swipe')) outcomes.push(r.postDataJSON().outcome); });
  await open(page, 'library');
  await open(page, 'you');
  const y = 420; // the You page's list: no horizontal scrollers

  await swipe(cdp, 60, y, width - 60);
  await expect(page).toHaveURL(/#library/);
  await swipe(cdp, width - 60, y, 60);
  await expect(page).toHaveURL(/#you$/);

  // From the very edge: the browser's or OS's own gesture, not ours.
  await swipe(cdp, 5, y, width - 60);
  await page.waitForTimeout(400);
  await expect(page).toHaveURL(/#you$/);

  // Inside a horizontally scrolling shelf: scrolls the shelf instead.
  await open(page, 'discover');
  const row = await page.locator('.shelf-row').first().boundingBox();
  await swipe(cdp, 60, row!.y + row!.height / 2, width - 60);
  await page.waitForTimeout(400);
  await expect(page).toHaveURL(/#discover/);

  // Turned off in Settings: nothing happens.
  await gotoApp(page, '#you/settings');
  await page.getByLabel(/Swipe right or left to go back or forward/).uncheck();
  await expect.poll(async () => (await (await page.request.get('/api/v1/me')).json()).prefs.swipe_nav).toBe(false);
  await open(page, 'library');
  await open(page, 'you');
  await swipe(cdp, 60, y, width - 60);
  await page.waitForTimeout(400);
  await expect(page).toHaveURL(/#you$/);

  await expect.poll(() => outcomes).toEqual(['back', 'forward', 'ignored: screen edge', expect.stringMatching(/^ignored: scroller div\.shelf-row/), 'ignored: turned off']);
});
