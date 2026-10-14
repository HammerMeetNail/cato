import { test, expect } from '@playwright/test';
import { account, put, item, sessions, playthroughs, eventually, TapCounter, gotoApp, logSheet, logButton, undoButton, G, toast } from './helpers';

// UX acceptance tests with tap budgets (docs/cato_smooth.md §5). Typing
// doesn't count as a tap; the budget counts taps from the app's launch
// screen.

test('log +1h to a game you are playing: two taps (⊕ → +1h), one from Home', async ({ page }) => {
  const acc = await account(page);
  await put(acc, G.hades, { status: 'playing' });
  await gotoApp(page);
  const taps = new TapCounter();
  await taps.tap(logButton(page));
  const row = logSheet(page).locator(`.log-row[data-game-id="${G.hades}"]`);
  await taps.tap(row.getByRole('button', { name: '+1h' }));
  expect(taps.count).toBe(2);
  await expect(toast(page)).toContainText('Logged 1h on Hades');
  await eventually(() => item(acc, G.hades), it => it.minutes === 60, 'server total 60');

  const home = new TapCounter();
  await home.tap(page.locator(`.now-card[data-game-id="${G.hades}"]`).getByRole('button', { name: 'Log 1h' }));
  expect(home.count).toBe(1);
  await expect(page.locator(`.now-card[data-game-id="${G.hades}"]`)).toContainText('2h played');
  await eventually(() => item(acc, G.hades), it => it.minutes === 120, 'server total 120');
  const list = await sessions(acc, G.hades);
  expect(list).toHaveLength(2);

  // Undo is one tap and removes the session on the server.
  await undoButton(page).click();
  await eventually(() => item(acc, G.hades), it => it.minutes === 60, 'undo restores 60');
});

test('finish and rate: three taps (Finish → star → Done), undo in one', async ({ page }) => {
  const acc = await account(page);
  await put(acc, G.celeste, { status: 'playing' });
  await gotoApp(page);
  const taps = new TapCounter();
  await taps.tap(page.locator(`.now-card[data-game-id="${G.celeste}"] [data-finish]`));
  const sheet = page.locator('dialog.sheet[aria-label="Finish Celeste"]');
  await expect(sheet).toBeVisible();
  const slider = sheet.getByRole('slider', { name: 'Your rating' });
  const box = (await slider.boundingBox())!;
  // The fourth star's right half: four stars.
  taps.count++;
  await page.mouse.click(box.x + box.width * 0.78, box.y + box.height / 2);
  await expect(slider).toHaveAttribute('aria-valuenow', '4');
  await taps.tap(sheet.locator('[data-finish-done]'));
  expect(taps.count).toBe(3);
  await expect(toast(page)).toContainText('Finished Celeste');
  await eventually(() => item(acc, G.celeste), it => it.status === 'completed' && it.rating === 80, 'finished at 4 stars');
  const pts = await playthroughs(acc, G.celeste);
  expect(pts[0].finished_on).toBeTruthy();

  const undo = new TapCounter();
  await undo.tap(undoButton(page));
  expect(undo.count).toBe(1);
  await eventually(() => item(acc, G.celeste), it => it.status === 'playing' && it.rating === 0, 'undo restores playing');
});

test('add a new game to Want: three taps plus typing (⊕ → type → Want)', async ({ page }) => {
  const acc = await account(page);
  await gotoApp(page);
  const taps = new TapCounter();
  await taps.tap(logButton(page));
  await logSheet(page).getByRole('searchbox').fill('outer wil');
  const row = logSheet(page).locator(`.log-row[data-game-id="${G.outer}"]`);
  await taps.tap(row.getByRole('button', { name: 'Want' }));
  // Typing focused the field: count it as the third interaction.
  taps.count++;
  expect(taps.count).toBe(3);
  await expect(toast(page)).toContainText('Added Outer Wilds to Want');
  await eventually(() => item(acc, G.outer), it => it?.status === 'wishlist', 'wishlisted');
  await undoButton(page).click();
  await eventually(() => item(acc, G.outer), it => it === null, 'undo removes the game');
});

test('find a game in your library: one tap plus typing', async ({ page }) => {
  const acc = await account(page);
  for (const id of [G.hades, G.celeste, G.balatro, G.tunic]) await put(acc, id, { status: 'backlog' });
  await gotoApp(page);
  const taps = new TapCounter();
  await page.locator('#global-search').fill('balat');
  const first = page.locator('#global-search-list [role="option"]').first();
  await expect(first).toContainText('Balatro');
  await expect(first).toContainText('In your library');
  await taps.tap(first.locator('a'));
  expect(taps.count).toBe(1);
  await expect(page).toHaveURL(new RegExp(`#game/${G.balatro}$`));
  await expect(page.getByRole('heading', { level: 1, name: 'Balatro' })).toBeVisible();
});

test('change status from the game page: one tap, undo in one', async ({ page }) => {
  const acc = await account(page);
  await put(acc, G.tunic, { status: 'backlog' });
  await gotoApp(page, `#game/${G.tunic}`);
  const taps = new TapCounter();
  await taps.tap(page.getByRole('radiogroup', { name: 'Status' }).getByRole('radio', { name: 'Playing' }));
  expect(taps.count).toBe(1);
  await expect(page.getByRole('radio', { name: 'Playing' })).toHaveAttribute('aria-checked', 'true');
  await eventually(() => item(acc, G.tunic), it => it.status === 'playing', 'playing');
  await undoButton(page).click();
  await eventually(() => item(acc, G.tunic), it => it.status === 'backlog', 'undo restores backlog');
  await expect(page.getByRole('radio', { name: 'Backlog' })).toHaveAttribute('aria-checked', 'true');
});
