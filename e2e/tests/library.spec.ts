import { test, expect, Page } from '@playwright/test';
import { account, put, item, eventually, gotoApp, undoButton, G, Account } from './helpers';

async function seedLibrary(page: Page): Promise<Account> {
  const acc = await account(page);
  await put(acc, G.hades, { status: 'playing', tags: ['roguelike', 'RPG'], platforms: ['Nintendo Switch'], rating: 90 });
  await put(acc, G.celeste, { status: 'completed', tags: ['platformer'], rating: 100 });
  await put(acc, G.hollow, { status: 'backlog', tags: ['metroidvania'], platforms: ['PC (Microsoft Windows)'] });
  await put(acc, G.disco, { status: 'backlog', tags: ['RPG'], rating: 80 });
  await put(acc, G.balatro, { status: 'wishlist' });
  return acc;
}

const tiles = (page: Page) => page.locator('.game-tile');
const tileNames = async (page: Page) => (await page.locator('.game-tile .tile-name').allTextContents()).map(s => s.trim());

test('shelf chips with counts, instant search and sorting', async ({ page }) => {
  await seedLibrary(page);
  await gotoApp(page, '#library');
  await expect(tiles(page)).toHaveCount(5);
  await expect(page.getByRole('tab', { name: /^Backlog 2/ })).toBeVisible();
  await page.getByRole('tab', { name: /^Backlog/ }).click();
  await expect(tiles(page)).toHaveCount(2);
  await expect(page).toHaveURL(/shelf=backlog/);
  await page.getByRole('tab', { name: /^All/ }).click();

  // Real keystrokes: the box must keep focus while the route updates.
  await page.locator('#lib-search').pressSequentially('hol');
  await expect(tiles(page)).toHaveCount(1);
  await expect(tiles(page).first()).toContainText('Hollow Knight');
  await page.locator('#lib-search').fill('');

  await page.locator('#lib-sort').selectOption('name');
  await expect.poll(() => tileNames(page)).toEqual(['Balatro', 'Celeste', 'Disco Elysium', 'Hades', 'Hollow Knight']);
  await page.locator('#lib-sort').selectOption('rating');
  await expect.poll(async () => (await tileNames(page))[0]).toBe('Celeste');
});

test('$tag and @platform become removable filter chips', async ({ page }) => {
  await seedLibrary(page);
  await gotoApp(page, '#library');
  const search = page.locator('#lib-search');
  await search.fill('$rp');
  // Typing a $ token suggests tags.
  await page.getByRole('option', { name: 'RPG' }).click();
  await expect(search).toHaveValue('$RPG ');
  await expect(page.getByRole('button', { name: 'Remove tag filter RPG' })).toBeVisible();
  await expect(tiles(page)).toHaveCount(2);
  await search.press('End');
  await search.pressSequentially('@switch ');
  await expect(page.getByRole('button', { name: 'Remove platform filter switch' })).toBeVisible();
  await expect(tiles(page)).toHaveCount(1);
  await page.getByRole('button', { name: 'Remove tag filter RPG' }).click();
  // @switch matches owned copies and where the game is available.
  await expect(tiles(page)).toHaveCount(4);
  await page.getByRole('button', { name: 'Remove platform filter switch' }).click();
  await expect(tiles(page)).toHaveCount(5);
});

test('list view and a saved view', async ({ page }) => {
  await seedLibrary(page);
  await gotoApp(page, '#library?shelf=backlog');
  await page.getByRole('radio', { name: 'List' }).click();
  await expect(page.locator('.lib-list .list-row')).toHaveCount(2);
  await expect(page).toHaveURL(/view=list/);

  await page.getByRole('button', { name: 'Saved views' }).click();
  await page.getByRole('menuitem', { name: 'Save this view…' }).click();
  await page.locator('dialog.sheet').getByLabel('Name').fill('My backlog list');
  await page.locator('dialog.sheet').getByRole('button', { name: 'Save' }).click();
  await expect(page.locator('.toast')).toContainText('Saved “My backlog list”');

  await page.goto('/#library');
  await expect(tiles(page)).toHaveCount(5);
  await page.getByRole('button', { name: 'Saved views' }).click();
  await page.getByRole('menuitemradio', { name: 'My backlog list' }).click();
  await expect(page.locator('.lib-list .list-row')).toHaveCount(2);
});

test('multi-select bulk status change, undone in one tap', async ({ page }) => {
  const acc = await seedLibrary(page);
  await gotoApp(page, '#library?shelf=backlog');
  await page.getByRole('button', { name: 'Select games' }).click();
  await tiles(page).nth(0).click();
  await tiles(page).nth(1).click();
  const bar = page.getByRole('toolbar', { name: 'Selected games' });
  await expect(bar).toContainText('2 selected');
  await bar.getByRole('button', { name: 'Status' }).click();
  await page.getByRole('menuitem', { name: 'Want' }).click();
  await eventually(() => item(acc, G.hollow), it => it.status === 'wishlist', 'bulk moved');
  expect((await item(acc, G.disco)).status).toBe('wishlist');
  await undoButton(page).click();
  await eventually(() => item(acc, G.hollow), it => it.status === 'backlog', 'bulk undo');
  expect((await item(acc, G.disco)).status).toBe('backlog');
});

test('right-click quick actions and keyboard navigation', async ({ page }) => {
  const acc = await seedLibrary(page);
  await gotoApp(page, '#library?sort=name');
  const balatro = page.locator(`.game-tile[data-game-id="${G.balatro}"]`);
  await balatro.click({ button: 'right' });
  const menu = page.getByRole('menu', { name: 'Balatro' });
  await expect(menu).toBeVisible();
  await menu.getByRole('menuitemradio', { name: 'Backlog' }).click();
  await eventually(() => item(acc, G.balatro), it => it.status === 'backlog', 'moved via menu');

  await tiles(page).first().focus();
  await page.keyboard.press('j');
  await expect(tiles(page).nth(1)).toBeFocused();
  await page.keyboard.press('k');
  await expect(tiles(page).first()).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(new RegExp(`#game/${G.balatro}$`));
});
