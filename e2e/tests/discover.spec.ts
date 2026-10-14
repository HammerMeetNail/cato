import { test, expect } from '@playwright/test';
import { account, put, item, eventually, gotoApp, G } from './helpers';

test('Discover suggests popular games you don\'t have', async ({ page }) => {
  const acc = await account(page);
  await put(acc, G.hades, { status: 'playing' });
  await gotoApp(page, '#discover');
  // Exactly the "Popular" shelf: "Popular on this server" appears once other
  // people on the shared E2E server have games.
  const popular = page.getByRole('region', { name: 'Popular', exact: true });
  await expect(popular.locator('.upnext-card').first()).toBeVisible();
  await expect(popular.locator(`.upnext-card[data-game-id="${G.hades}"]`)).toHaveCount(0);
  await popular.locator(`.upnext-card[data-game-id="${G.celeste}"]`).getByRole('button', { name: 'Want to play Celeste' }).click();
  await eventually(() => item(acc, G.celeste), it => it?.status === 'wishlist', 'wanted');
});

test('the search results page filters with facets and adds to Want', async ({ page }) => {
  const acc = await account(page);
  await put(acc, G.totk, { status: 'playing' });
  await gotoApp(page, '#discover');
  await page.getByRole('searchbox', { name: 'Search every game' }).fill('zelda');
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/#discover\/search\?q=zelda/);
  const results = page.locator('.lib-grid .game-tile');
  await expect(results).toHaveCount(3);
  await expect(page.locator(`.game-tile:has(a[href="#game/${G.totk}"])`)).toContainText('Playing');

  await page.getByLabel('From').fill('2019');
  await page.getByLabel('From').press('Tab');
  await expect(page).toHaveURL(/year_from=2019/);
  await expect(results).toHaveCount(2);
  await page.getByLabel('In my library').selectOption('0');
  await expect(results).toHaveCount(1);
  await results.first().getByRole('button', { name: 'Want' }).click();
  await eventually(() => item(acc, G.links), it => it?.status === 'wishlist', 'wanted from results');
});
