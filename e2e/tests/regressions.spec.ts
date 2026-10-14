import { test, expect, Page } from '@playwright/test';

// P0 correctness regressions (docs/cato_smooth.md §2.1). API calls only set up
// prerequisites or simulate another device; the behavior under test is driven
// through the UI.

const password = 'regression-password-1';
async function account(page: Page) {
  const email = `regress-${crypto.randomUUID()}@example.com`;
  const response = await page.request.post('/api/auth/signup', { data: { email, password } });
  expect(response.status()).toBe(201);
  return (await response.json()) as { csrf_token: string };
}
async function seed(page: Page, csrf: string, id: number, data: object) {
  const response = await page.request.post(`/api/library/${id}`, { headers: { 'X-CSRF-Token': csrf }, data });
  expect(response.status()).toBe(200);
}
async function item(page: Page, id: number) {
  return (await page.request.get(`/api/library/${id}`)).json();
}
function collectErrors(page: Page) {
  const errors: string[] = [];
  page.on('pageerror', e => errors.push(e.message));
  return errors;
}

test('#1 picking a Filters tag suggestion completes the tag without a ReferenceError', async ({ page }) => {
  const errors = collectErrors(page);
  const user = await account(page);
  await seed(page, user.csrf_token, 1, { status: 'backlog', tags: ['RPG'] });
  await seed(page, user.csrf_token, 2, { status: 'backlog', tags: ['Shooter'] });
  await page.goto('/#library');
  await expect(page.locator('#gameGrid .game-card')).toHaveCount(2);
  await page.locator('#searchAdvancedBtn').click();
  const tags = page.locator('#lfTags');
  await tags.fill('shooter rp');
  const option = page.locator('#lfTagsList .lib-filter-autocomplete-option', { hasText: 'RPG' });
  await expect(option).toBeVisible();
  await option.click();
  await expect(tags).toHaveValue('shooter RPG ');
  expect(errors).toEqual([]);
});

test('#2 autosave PATCHes only edited fields and keeps playtime logged elsewhere', async ({ page }) => {
  const user = await account(page);
  await seed(page, user.csrf_token, 1, { status: 'playing', playtime_minutes: 60, notes: 'before', rating: 70 });
  await page.goto('/#game/1');
  const modal = page.locator('#addGameModal');
  await expect(modal.locator('.modal-notes')).toHaveValue('before');

  // Another device logs +30m while the sheet is open.
  const other = await page.request.patch('/api/library/1', {
    headers: { 'X-CSRF-Token': user.csrf_token }, data: { playtime_delta_minutes: 30 },
  });
  expect(other.status()).toBe(200);

  const writes: { method: string; body: any }[] = [];
  page.on('request', r => {
    if (new URL(r.url()).pathname === '/api/library/1' && r.method() !== 'GET') {
      writes.push({ method: r.method(), body: r.postDataJSON() });
    }
  });
  const saved = page.waitForResponse(r => new URL(r.url()).pathname === '/api/library/1' && r.request().method() === 'PATCH');
  await modal.locator('.modal-notes').fill('after');
  expect((await saved).status()).toBe(200);
  expect(writes).toEqual([{ method: 'PATCH', body: { notes: 'after' } }]);
  let stored = await item(page, 1);
  expect(stored).toMatchObject({ notes: 'after', playtime_minutes: 90, rating: 70, status: 'playing' });

  // Editing hours sends the change as a delta, so the other device's time survives.
  const hoursSaved = page.waitForResponse(r => new URL(r.url()).pathname === '/api/library/1' && r.request().method() === 'PATCH');
  await modal.getByLabel('Hours played').selectOption('1.5');
  await hoursSaved;
  expect(writes[1]).toEqual({ method: 'PATCH', body: { playtime_delta_minutes: 30 } });
  stored = await item(page, 1);
  expect(stored.playtime_minutes).toBe(120);
  await page.locator('#addGameModal .modal-cancel').click();
  await expect(modal).toHaveCount(0);
  expect(writes.every(w => w.method === 'PATCH')).toBe(true);
});

test('#4 $tag search lists the library games with that tag', async ({ page }) => {
  const user = await account(page);
  await seed(page, user.csrf_token, 1, { status: 'backlog', tags: ['couch-coop'] });
  await page.goto('/#library');
  const lookup = page.waitForResponse(r => {
    const u = new URL(r.url());
    return u.pathname === '/api/library' && u.searchParams.get('tag') === 'couch-coop';
  });
  await page.locator('#searchInput').fill('$couch-coop');
  await lookup;
  const results = page.locator('#searchResults');
  await expect(results.locator('.tag-result')).toHaveCount(1);
  await expect(results.locator('.tag-result')).toContainText('Test Game');
  await expect(results).not.toContainText('No games tagged');
});

test('#5 quick-add works again after the game is removed (ownership cache invalidated)', async ({ page }) => {
  await account(page);
  await page.goto('/#library');
  const search = async () => {
    await page.locator('#searchInput').fill('');
    await page.locator('#searchInput').fill('Test Game');
    await expect(page.locator('#searchResults .search-result-item').first()).toBeVisible();
  };
  await search();
  const addBacklog = page.locator('#searchResults .qa-add[data-add-id="1"][data-add-status="backlog"]');
  await addBacklog.click();
  await expect(page.locator('#toast')).toContainText('Added Test Game to Backlog');

  // Remove it through the edit sheet.
  await page.goto('/#game/1');
  const removed = page.waitForResponse(r => new URL(r.url()).pathname === '/api/library/1' && r.request().method() === 'DELETE');
  await page.locator('#addGameModal .modal-remove').click();
  expect((await removed).status()).toBe(200);
  await expect(page.locator('#addGameModal')).toHaveCount(0);

  await search();
  await expect(addBacklog).toBeVisible();
  await addBacklog.click();
  await expect(page.locator('#toast')).toContainText('Added Test Game to Backlog');
  expect((await item(page, 1)).status).toBe('backlog');
});

test('#6 Finish offers undo and a second tap cannot finish the next game', async ({ page }) => {
  const user = await account(page);
  await seed(page, user.csrf_token, 1, { status: 'playing' });
  await seed(page, user.csrf_token, 2, { status: 'playing' });
  await page.goto('/#now');
  const cards = page.locator('#playingView .hero-card');
  await expect(cards).toHaveCount(2);
  const firstId = Number(await cards.first().getAttribute('data-game-id'));
  const secondId = firstId === 1 ? 2 : 1;
  const finish = cards.first().locator('[data-hero-finish]');
  const box = (await finish.boundingBox())!;
  const done = page.waitForResponse(r => new URL(r.url()).pathname === `/api/library/${firstId}` && r.request().method() === 'PATCH');
  await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2);
  expect((await done).status()).toBe(200);
  // The impatient second tap lands where the Finish button was.
  await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2);
  await expect(page.locator('#toast')).toContainText('Finished');
  expect((await item(page, firstId)).status).toBe('completed');
  expect((await item(page, secondId)).status).toBe('playing');

  const undone = page.waitForResponse(r => new URL(r.url()).pathname === `/api/library/${firstId}` && r.request().method() === 'PATCH');
  await page.locator('#toast .toast-action', { hasText: 'Undo' }).click();
  expect((await undone).status()).toBe(200);
  const restored = await item(page, firstId);
  expect(restored.status).toBe('playing');
  expect(restored.completed_at ?? null).toBeNull();
  await expect(page.locator('#playingView .hero-card:not(.hero-finished)')).toHaveCount(2);
  await expect(cards.first().locator('[data-hero-finish]')).toBeEnabled();
});

test('#7 closing the edit sheet keeps loaded pages and updates the card in place', async ({ page }) => {
  const user = await account(page);
  for (let id = 100; id <= 164; id++) await seed(page, user.csrf_token, id, { status: 'backlog' });
  await page.goto('/#library');
  const cards = page.locator('#gameGrid .game-card');
  await expect(cards).toHaveCount(60);
  await cards.last().scrollIntoViewIfNeeded();
  await expect(cards).toHaveCount(65);

  const target = page.locator('#gameGrid .game-card[data-game-id="100"]');
  await target.scrollIntoViewIfNeeded();
  await target.click();
  const modal = page.locator('#addGameModal');
  await expect(modal).toBeVisible();
  const listReloads: string[] = [];
  page.on('request', r => { if (new URL(r.url()).pathname === '/api/library') listReloads.push(r.url()); });
  await modal.getByLabel('Rating, 0 to 100').selectOption('90');
  await page.locator('#addGameModal .modal-cancel').click();
  await expect(modal).toHaveCount(0);
  await expect(target.locator('.rating-display')).toHaveText('90');
  await expect(cards).toHaveCount(65);
  await expect(target).toBeInViewport();
  expect(listReloads).toEqual([]);

  // A status change that leaves the active filter removes just that card.
  await page.locator('#statusFilterBtn').click();
  await page.locator('#statusFilterPanel [data-status="backlog"]').click();
  await page.locator('#statusFilterBtn').click();
  await expect(cards).toHaveCount(60);
  const first = cards.first();
  const firstId = await first.getAttribute('data-game-id');
  await first.click();
  await expect(modal).toBeVisible();
  await modal.getByLabel('Status', { exact: true }).selectOption('wishlist');
  await page.locator('#addGameModal .modal-cancel').click();
  await expect(modal).toHaveCount(0);
  await expect(page.locator(`#gameGrid .game-card[data-game-id="${firstId}"]`)).toHaveCount(0);
  await expect(cards).toHaveCount(59);
});

test('#8 a malformed % escape in the search hash still renders the library', async ({ page }) => {
  const errors = collectErrors(page);
  await account(page);
  await page.goto('/#search/100%');
  await expect(page.locator('#libraryView')).toBeVisible();
  await expect(page.locator('#searchInput')).toHaveValue('100%');
  expect(errors).toEqual([]);
});

test('#9 the offline page does not promise local saving or sync', async ({ request }) => {
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
  await expect(page.locator('#libraryView')).toBeVisible();
  await expect(page.locator('#searchInput')).toBeFocused();
  await expect(page).toHaveURL(/#library$/);
});
