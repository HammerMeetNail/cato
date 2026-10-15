import { test, expect } from '@playwright/test';
import { account, put, session, item, sessions, playthroughs, eventually, gotoApp, undoButton, toast, G, localDay } from './helpers';

test('a game not in the library offers the shelves; adding works', async ({ page }) => {
  const acc = await account(page);
  await gotoApp(page, `#game/${G.astro}`);
  await expect(page.getByRole('heading', { level: 1, name: 'Astro Bot' })).toBeVisible();
  await expect(page.getByText('Not in your library yet.')).toBeVisible();
  await page.locator('[data-add-status="backlog"]').click();
  await expect(page.getByRole('radiogroup', { name: 'Status' })).toBeVisible();
  await eventually(() => item(acc, G.astro), it => it?.status === 'backlog', 'added');
});

test('status, half-star rating by keyboard and time chips', async ({ page }) => {
  const acc = await account(page);
  await put(acc, G.hollow, { status: 'backlog' });
  await gotoApp(page, `#game/${G.hollow}`);
  await page.getByRole('radio', { name: 'Playing' }).click();
  await eventually(() => item(acc, G.hollow), it => it.status === 'playing', 'playing');

  const rating = page.getByRole('slider', { name: 'Your rating' });
  await rating.focus();
  for (let i = 0; i < 7; i++) await page.keyboard.press('ArrowRight');
  await expect(rating).toHaveAttribute('aria-valuenow', '3.5');
  await expect(rating).toHaveAttribute('aria-valuetext', 'Rated 3.5 out of 5 stars');
  await eventually(() => item(acc, G.hollow), it => it.rating === 70, 'rated 3.5 stars = 70');

  await page.getByRole('button', { name: 'Log 2h' }).click();
  await eventually(() => item(acc, G.hollow), it => it.minutes === 120, 'logged 2h');
  await expect(page.locator('.your-row')).toContainText('2h played');
});

test('finishing then playing again starts a second playthrough', async ({ page }) => {
  const acc = await account(page);
  // The app always sends the person's local day; so does this setup.
  await put(acc, G.spire, { status: 'playing', played_on: localDay() });
  await gotoApp(page, `#game/${G.spire}`);
  await page.getByRole('radio', { name: 'Finished' }).click();
  const sheet = page.locator('dialog.sheet[aria-label="Finish Slay the Spire"]');
  await sheet.getByRole('button', { name: 'Main + extras' }).click();
  await sheet.locator('[data-finish-done]').click();
  await eventually(() => playthroughs(acc, G.spire), p => p.length === 1 && p[0].completion === 'main_extras', 'first run finished');
  await page.getByRole('radio', { name: 'Playing' }).click();
  await eventually(() => playthroughs(acc, G.spire), p => p.length === 2, 'replay');
  await page.reload();
  const runs = page.locator('ol.timeline > li');
  await expect(runs).toHaveCount(2);
  await expect(runs.nth(1)).toContainText('Main + extras');
});

test('deleting a session can be undone', async ({ page }) => {
  const acc = await account(page);
  await put(acc, G.tunic, { status: 'playing' });
  await session(acc, G.tunic, 45, '2026-10-01');
  await session(acc, G.tunic, 30, '2026-10-02');
  await gotoApp(page, `#game/${G.tunic}`);
  await page.getByRole('button', { name: /Delete the 45m session/ }).click();
  await expect(toast(page)).toContainText('Session deleted');
  await eventually(() => item(acc, G.tunic), it => it.minutes === 30, 'deleted');
  await undoButton(page).click();
  await eventually(() => item(acc, G.tunic), it => it.minutes === 75, 'restored');
  await expect(page.getByRole('button', { name: /Delete the 45m session/ })).toBeVisible();
  expect(await sessions(acc, G.tunic)).toHaveLength(2);
});

test('notes and tags save; removing the game can be undone', async ({ page }) => {
  const acc = await account(page);
  await put(acc, G.metroid, { status: 'backlog' });
  await gotoApp(page, `#game/${G.metroid}`);
  const notes = page.getByLabel('Notes (private)');
  await notes.fill('Save the screw attack for last');
  await notes.blur();
  await eventually(() => item(acc, G.metroid), it => it.notes === 'Save the screw attack for last', 'notes saved');
  const tag = page.getByPlaceholder('Add a tag and press Enter');
  await tag.fill('metroidvania');
  await tag.press('Enter');
  await eventually(() => item(acc, G.metroid), it => it.tags.includes('metroidvania'), 'tagged');
  await expect(page.getByRole('button', { name: 'Remove tag metroidvania' })).toBeVisible();

  await page.getByRole('button', { name: 'Remove from library' }).click();
  await page.locator('dialog.sheet').getByRole('button', { name: 'Remove', exact: true }).click();
  await eventually(() => item(acc, G.metroid), it => it === null, 'removed');
  await expect(page.getByText('Not in your library yet.')).toBeVisible();
  await undoButton(page).click();
  await eventually(() => item(acc, G.metroid), it => it?.notes === 'Save the screw attack for last', 'restored with notes');
});
