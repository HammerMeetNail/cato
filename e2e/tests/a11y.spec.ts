import { test, expect, Page } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { account, put, session, gotoApp, logButton, logSheet, localDay, G } from './helpers';

// axe-core gate (docs/cato_smooth.md §4.3): no serious or critical
// violations on any key screen, in light and dark.

async function scan(page: Page, label: string) {
  // Measure settled UI: wait for finite animations (sheets sliding in).
  await page.evaluate(() => Promise.all(document.getAnimations()
    .filter(a => a.effect?.getComputedTiming().iterations !== Infinity)
    .map(a => a.finished.catch(() => {}))));
  const results = await new AxeBuilder({ page })
    .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'])
    .analyze();
  const bad = results.violations.filter(v => v.impact === 'serious' || v.impact === 'critical');
  const report = bad.map(v => `${v.id} (${v.impact}): ${v.help}\n  ${v.nodes.slice(0, 3).map(n => n.target.join(' ') + ' — ' + n.failureSummary?.split('\n')[1]).join('\n  ')}`);
  expect(report, `${label}\n${report.join('\n')}`).toEqual([]);
}

for (const scheme of ['light', 'dark'] as const) {
  test.describe(`${scheme} theme`, () => {
    // Reduced motion: contrast is measured on settled sheets, not mid-animation.
    test.use({ colorScheme: scheme, reducedMotion: 'reduce' });

    test(`login has no serious violations (${scheme})`, async ({ page }) => {
      await page.goto('/login');
      await expect(page.locator('#loginForm')).toBeVisible();
      await scan(page, 'login');
    });

    test(`app screens have no serious violations (${scheme})`, async ({ page }) => {
      const acc = await account(page, 'a11y');
      await put(acc, G.hades, { status: 'playing', rating: 90, tags: ['roguelike'] });
      await put(acc, G.celeste, { status: 'completed', rating: 70 });
      await put(acc, G.hollow, { status: 'paused' });
      await put(acc, G.balatro, { status: 'wishlist' });
      await put(acc, G.disco, { status: 'abandoned' });
      await put(acc, G.tunic, { status: 'backlog' });
      await session(acc, G.hades, 60, localDay());

      const screens: [string, string, (p: Page) => Promise<void>][] = [
        ['home', '#home', p => expect(p.locator('.now-card').first()).toBeVisible()],
        ['library grid', '#library', p => expect(p.locator('.game-tile').first()).toBeVisible()],
        ['library list', '#library?view=list', p => expect(p.locator('.lib-list .list-row').first()).toBeVisible()],
        ['game page', `#game/${G.hades}`, p => expect(p.locator('.your-row')).toBeVisible()],
        ['game page, not in library', `#game/${G.astro}`, p => expect(p.getByText('Not in your library yet.')).toBeVisible()],
        ['discover', '#discover', p => expect(p.locator('.shelf').first()).toBeVisible()],
        ['search results', '#discover/search?q=zelda', p => expect(p.locator('.game-tile').first()).toBeVisible()],
        ['you', '#you', p => expect(p.locator('.settings-list')).toBeVisible()],
        ['stats', '#you/stats', p => expect(p.locator('.stat-tile').first()).toBeVisible()],
        ['diary', '#you/diary', p => expect(p.locator('.calendar')).toBeVisible()],
        ['settings', '#you/settings', p => expect(p.locator('#set-devices .row').first()).toBeVisible()],
        ['help', '#you/help', p => expect(p.getByRole('heading', { name: 'Keyboard shortcuts' })).toBeVisible()],
        ['gallery', '#dev/ui', p => expect(p.getByRole('heading', { name: 'Component gallery' })).toBeVisible()],
      ];
      for (const [label, hash, ready] of screens) {
        await gotoApp(page, hash);
        await ready(page);
        await scan(page, label);
      }

      await gotoApp(page, '#home');
      await logButton(page).click();
      await logSheet(page).getByRole('searchbox').fill('zel');
      await expect(logSheet(page).locator('.log-row').first()).toBeVisible();
      await scan(page, 'log sheet');
      await page.keyboard.press('Escape');

      await page.locator(`.now-card[data-game-id="${G.hades}"] [data-finish]`).click();
      await expect(page.locator('dialog.sheet[aria-label="Finish Hades"]')).toBeVisible();
      await scan(page, 'finish sheet');
    });
  });
}
