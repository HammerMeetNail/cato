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

    test(`people screens have no serious violations (${scheme})`, async ({ page }) => {
      const acc = await account(page, 'a11y-people');
      const h = { 'X-CSRF-Token': acc.csrf };
      const username = `a11y_${Math.random().toString(36).slice(2, 8)}`;
      await page.request.patch('/api/v1/me/profile', { headers: h, data: { username, bio: 'Accessible bio', visibility: 'instance', favourites: [G.hades] } });
      await put(acc, G.hades, { status: 'completed', rating: 80 });
      await page.request.post('/api/v1/reviews', { headers: h, data: { game_id: G.hades, body: 'A review with spoilers', spoiler: true } });
      const list = (await (await page.request.post('/api/v1/lists', { headers: h, data: { title: 'Favourites', ranked: true } })).json()).list;
      await page.request.put(`/api/v1/lists/${list.id}/items/${G.hades}`, { headers: h, data: { note: 'Best' } });
      await page.request.post('/api/v1/users/e2e_admin/follow', { headers: h });
      await page.request.post('/api/v1/sessions', { headers: h, data: { game_id: G.hades, minutes: 60 } });
      await page.request.post('/api/v1/goals', { headers: h, data: { kind: 'finish', target: 10 } });

      const screens: [string, string, (p: Page) => Promise<void>][] = [
        ['profile', `#u/${username}`, p => expect(p.getByRole('heading', { name: 'Favourites' }).first()).toBeVisible()],
        ['someone else', '#u/e2e_admin', p => expect(p.getByRole('button', { name: 'Following' })).toBeVisible()],
        ['game page social', `#game/${G.hades}`, p => expect(p.locator('.review').first()).toBeVisible()],
        ['lists', '#you/lists', p => expect(p.locator('.list-card').first()).toBeVisible()],
        ['list', `#list/${list.id}`, p => expect(p.locator('.list-items li').first()).toBeVisible()],
        ['people', '#you/people', p => expect(p.getByRole('heading', { name: 'Following' })).toBeVisible()],
        ['inbox', '#you/inbox', p => expect(p.getByRole('heading', { name: 'Inbox' })).toBeVisible()],
        ['year in review', `#you/year/${new Date().getFullYear()}`, p => expect(p.locator('.yr-hero')).toBeVisible()],
        ['goals', '#you/goals', p => expect(p.getByRole('heading', { name: 'Goals' })).toBeVisible()],
      ];
      for (const [label, hash, ready] of screens) {
        await gotoApp(page, hash);
        await ready(page);
        await scan(page, label);
      }
      await gotoApp(page, '#you');
      await page.getByRole('button', { name: 'Edit profile' }).click();
      await expect(page.locator('dialog.sheet[aria-label="Edit profile"]')).toBeVisible();
      await scan(page, 'edit profile sheet');
    });

    test(`import screens have no serious violations (${scheme})`, async ({ page }) => {
      await account(page, 'a11y-import');
      for (const source of ['steam', 'file', 'ra', 'history']) {
        await gotoApp(page, `#you/import?source=${source}`);
        await expect(page.getByRole('heading', { name: 'Import' })).toBeVisible();
        await scan(page, `import ${source}`);
      }
      await gotoApp(page, '#you/import?source=file');
      await page.setInputFiles('input[type=file]', { name: 'g.csv', mimeType: 'text/csv', buffer: Buffer.from('Title,Status\nHades,Beaten\nZelda Breath,Playing\nNope Nope 123,Backlog\n') });
      await expect(page.locator('.import-row').first()).toBeVisible();
      await page.getByRole('radio', { name: /^All/ }).click();
      await scan(page, 'import review');
    });

    test(`admin screens have no serious violations (${scheme})`, async ({ page }) => {
      expect((await page.request.post('/api/auth/login', { data: { email: 'admin@e2e.test', password: 'e2e-password-1' } })).status()).toBe(200);
      for (const tab of ['users', 'reports', 'invites', 'system', 'audit']) {
        await gotoApp(page, `#admin?tab=${tab}`);
        await expect(page.locator('.section').first()).not.toContainText('Loading');
        await expect(page.locator('[aria-busy="true"]')).toHaveCount(0);
        await scan(page, `admin ${tab}`);
      }
    });
  });
}
