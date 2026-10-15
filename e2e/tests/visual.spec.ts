import { test, expect, Page, PageScreenshotOptions } from '@playwright/test';
import fs from 'node:fs';
import { account, put, gotoApp, G } from './helpers';

// Visual snapshots (docs/cato_smooth.md §11) of the component gallery and
// Home, light and dark, at phone (mobile-chromium) and desktop (chromium)
// sizes. Baselines are per platform because fonts render differently; on a
// platform without one (e.g. the first CI run on Linux) the screenshot is
// recorded as the baseline and the test passes with an annotation.

test.use({ reducedMotion: 'reduce' });

async function snapshot(page: Page, name: string, opts: PageScreenshotOptions & { mask?: any[] }) {
  // The app frame is viewport-sized and <main> scrolls; for a full-page
  // capture let the frame grow to its content just for the screenshot.
  if (opts.fullPage) {
    await page.addStyleTag({ content: '#app, .app { height: auto !important; } .main { overflow: visible !important; }' });
  }
  const baseline = test.info().snapshotPath(name);
  if (!fs.existsSync(baseline)) {
    await page.screenshot({ ...opts, path: baseline, animations: 'disabled', maskColor: '#FF00FF' });
    test.info().annotations.push({ type: 'visual', description: `recorded new baseline ${name}` });
    return;
  }
  await expect(page).toHaveScreenshot(name, { ...opts, animations: 'disabled', maxDiffPixelRatio: 0.01 });
}

for (const scheme of ['light', 'dark'] as const) {
  test(`gallery (${scheme})`, async ({ page }) => {
    await account(page, 'visual');
    await page.emulateMedia({ colorScheme: scheme, reducedMotion: 'reduce' });
    await gotoApp(page, '#dev/ui');
    await expect(page.getByRole('heading', { name: 'Component gallery' })).toBeVisible();
    // The header avatar shows the account's initial.
    await snapshot(page, `gallery-${scheme}.png`, { fullPage: true, mask: [page.locator('.topbar .avatar')] });
  });

  test(`home (${scheme})`, async ({ page }) => {
    const acc = await account(page, 'visual');
    await put(acc, G.hades, { status: 'playing', rating: 90 });
    await put(acc, G.celeste, { status: 'paused' });
    await put(acc, G.tunic, { status: 'backlog' });
    await put(acc, G.balatro, { status: 'backlog' });
    await page.emulateMedia({ colorScheme: scheme, reducedMotion: 'reduce' });
    await gotoApp(page, '#home');
    await expect(page.locator('.now-card')).toHaveCount(2);
    await expect(page.locator('.upnext-card').first()).toBeVisible();
    // Time of day, today's date and "last played" depend on the clock.
    await snapshot(page, `home-${scheme}.png`, {
      mask: [page.locator('.greeting'), page.locator('.now-card .meta'), page.locator('.topbar .avatar')],
    });
  });
}
