import { test, expect } from '@playwright/test';
import { account, put, item, eventually, gotoApp, logButton, logSheet, undoButton, G } from './helpers';

// P0 correctness regressions (docs/cato_smooth.md §2.1) that still apply
// to the new screens. #1 (Filters tag autocomplete), #2 (edit-sheet
// autosave diff), #4 ($tag search dropdown) and #7 (edit sheet reloading the
// grid) were bugs in screens that no longer exist; #3 is covered in Go.

test('#5 a game can be added again after it was removed', async ({ page }) => {
  const acc = await account(page);
  const want = async () => {
    await logButton(page).click();
    await logSheet(page).getByRole('searchbox').fill('dead cells');
    await logSheet(page).locator(`.log-row[data-game-id="${G.dead}"]`).getByRole('button', { name: 'Want' }).click();
  };
  await gotoApp(page);
  await want();
  await eventually(() => item(acc, G.dead), it => it?.status === 'wishlist', 'added');
  await page.goto(`/#game/${G.dead}`);
  await page.getByRole('button', { name: 'Remove from library' }).click();
  await page.locator('dialog.sheet').getByRole('button', { name: 'Remove', exact: true }).click();
  await eventually(() => item(acc, G.dead), it => it === null, 'removed');
  await page.goto('/#home');
  await want();
  await eventually(() => item(acc, G.dead), it => it?.status === 'wishlist', 'added again');
});

test('#6 finishing goes through the sheet, so a second tap cannot finish the next game; undo restores', async ({ page }) => {
  const acc = await account(page);
  await put(acc, G.hades, { status: 'playing' });
  await put(acc, G.celeste, { status: 'playing' });
  await gotoApp(page);
  const first = page.locator('.now-card').first();
  const firstId = Number(await first.getAttribute('data-game-id'));
  const secondId = firstId === G.hades ? G.celeste : G.hades;
  const finish = first.locator('[data-finish]');
  const box = (await finish.boundingBox())!;
  await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2);
  await page.locator('[data-finish-done]').click();
  // The impatient second tap lands where Finish was: at most it opens a sheet.
  await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2);
  await eventually(() => item(acc, firstId), it => it.status === 'completed', 'first finished');
  expect((await item(acc, secondId)).status).toBe('playing');
  await page.keyboard.press('Escape');
  await page.goto('/#home');
  await expect(page.locator(`.now-card[data-game-id="${secondId}"]`)).toBeVisible();
});

test('#6b undo after finishing restores playing and clears the finish date', async ({ page }) => {
  const acc = await account(page);
  await put(acc, G.tunic, { status: 'playing' });
  await gotoApp(page);
  await page.locator(`.now-card[data-game-id="${G.tunic}"] [data-finish]`).click();
  await page.locator('[data-finish-done]').click();
  await eventually(() => item(acc, G.tunic), it => it.status === 'completed', 'finished');
  await undoButton(page).click();
  await eventually(() => item(acc, G.tunic), it => it.status === 'playing' && !it.completed_at, 'undone');
});

test('#8 a malformed % escape in a search hash still renders the library', async ({ page }) => {
  const errors: string[] = [];
  page.on('pageerror', e => errors.push(e.message));
  await account(page);
  await page.goto('/#search/100%');
  await expect(page.locator('#lib-search')).toHaveValue('100%');
  await expect(page.getByRole('heading', { name: 'Library', exact: true })).toBeVisible();
  expect(errors).toEqual([]);
});

test('#9 the offline page does not promise local saving', async ({ request }) => {
  const html = await (await request.get('/offline.html')).text();
  expect(html).not.toMatch(/saved locally|will sync/i);
  expect(html).toContain("You're offline");
});

test('#10 the Search shortcut opens the library with the search box focused', async ({ page, request }) => {
  const manifest = await (await request.get('/manifest.webmanifest')).json();
  const shortcut = manifest.shortcuts.find((s: any) => s.short_name === 'Search');
  expect(shortcut.url).toBe('/#search');
  await account(page);
  await page.goto(shortcut.url);
  await expect(page).toHaveURL(/#library$/);
  await expect(page.locator('#lib-search')).toBeFocused();
});

// Moving between screens must not flash a placeholder: the tabs' code is
// fetched while the app is idle, and a load that is still quick keeps the
// current screen instead of drawing the loading grid.
test('switching tabs for the first time shows no loading placeholder', async ({ page }) => {
  await account(page);
  await gotoApp(page, '#home');
  await expect.poll(() => page.evaluate(() => performance.getEntriesByType('resource').some(r => r.name.includes('/js/features/you.js'))),
    { message: 'tabs prefetched while idle' }).toBe(true);
  await page.evaluate(() => {
    (window as any).__placeholders = 0;
    new MutationObserver(() => {
      if (document.querySelector('#main > .page > [aria-busy="true"]')) (window as any).__placeholders++;
    }).observe(document.getElementById('app')!, { childList: true, subtree: true });
  });
  for (const [tab, heading] of [['you', null], ['library', 'Library'], ['home', null]] as const) {
    await page.locator(`nav.nav a[data-nav="${tab}"]`).click();
    await expect(page).toHaveURL(new RegExp(`#${tab}`));
    if (heading) await expect(page.getByRole('heading', { level: 1, name: heading })).toBeVisible();
  }
  expect(await page.evaluate(() => (window as any).__placeholders)).toBe(0);
});

// A tapped tile already shows the game's name and cover: the game page
// draws them at once, not a placeholder until its details arrive.
test('a game opened from a tile shows its name before the details load', async ({ page }) => {
  await account(page);
  await gotoApp(page, '#discover/search?q=astro');
  const tile = page.locator(`#main a[href="#game/${G.astro}"]`).first();
  await expect(tile).toBeVisible();
  let release!: () => void;
  const held = new Promise<void>(r => { release = r; });
  await page.route(new RegExp(`/api/v1/games/${G.astro}$`), async route => { await held; await route.continue(); });
  await tile.click();
  await expect(page.getByRole('heading', { level: 1, name: 'Astro Bot' })).toBeVisible();
  release();
  await expect(page.getByText('Not in your library yet.')).toBeVisible();
});

// A page that starts again right after one of its own navigations was
// reloaded under the app (on an iPhone, for one): it reports that, so the
// server log says why. A normal start reports nothing.
test('a reload right after navigating is reported; a normal start is not', async ({ page }) => {
  await account(page);
  const reports: any[] = [];
  page.on('request', r => { if (r.url().endsWith('/api/v1/diagnostics/reload')) reports.push(r.postDataJSON()); });
  await gotoApp(page, '#home');
  // Navigations are noted once the shell is idle after the first paint:
  // idle.js prefetches you.js in the same step that starts listening.
  await expect.poll(() => page.evaluate(() => performance.getEntriesByType('resource').some(r => r.name.includes('/js/features/you.js')))).toBe(true);
  await page.locator('nav.nav a[data-nav="you"]').click();
  await expect(page).toHaveURL(/#you/);
  const sent = page.waitForRequest(r => r.url().endsWith('/api/v1/diagnostics/reload'));
  await page.reload();
  const body = (await sent).postDataJSON();
  expect(body).toMatchObject({ nav_type: 'reload', clean_exit: true, from: '#home', to: '#you' });
  expect(body.since_nav_ms).toBeLessThan(5000);
  expect(reports).toHaveLength(1);
});
