import { test, expect } from '@playwright/test';
import { account, put, item, sessions, eventually, gotoApp, logButton, logSheet, G } from './helpers';

test('an empty library shows first-run onboarding, which can be hidden', async ({ page }) => {
  await account(page);
  await gotoApp(page);
  const onboarding = page.locator('.onboarding');
  await expect(onboarding).toContainText('Welcome to Cato');
  await expect(page.getByText('Nothing playing right now')).toBeVisible();
  await onboarding.getByRole('button', { name: 'Add games' }).click();
  await expect(logSheet(page)).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(logSheet(page)).toHaveCount(0);
  await onboarding.getByRole('button', { name: 'Hide this' }).click();
  await expect(page.locator('.onboarding')).toHaveCount(0);
  await page.reload();
  await expect(page.getByRole('heading', { name: 'Now playing' })).toBeVisible();
  await expect(page.locator('.onboarding')).toHaveCount(0);
});

test('now-playing cards log time in one tap and show paused games', async ({ page }) => {
  const acc = await account(page);
  await put(acc, G.hades, { status: 'playing' });
  await put(acc, G.hollow, { status: 'paused' });
  await put(acc, G.celeste, { status: 'backlog' });
  await gotoApp(page);
  const hades = page.locator(`.now-card[data-game-id="${G.hades}"]`);
  const hollow = page.locator(`.now-card[data-game-id="${G.hollow}"]`);
  await expect(hades).toBeVisible();
  await expect(hollow).toContainText('Paused');
  await expect(page.locator(`.now-card[data-game-id="${G.celeste}"]`)).toHaveCount(0);
  await hades.getByRole('button', { name: 'Log 30m' }).click();
  await expect(hades).toContainText('30m played');
  await eventually(() => item(acc, G.hades), it => it.minutes === 30, 'logged');
  await hollow.getByRole('button', { name: 'Resume' }).click();
  await eventually(() => item(acc, G.hollow), it => it.status === 'playing', 'resumed');
  // Up Next suggests the backlog.
  await page.reload();
  await expect(page.locator('.upnext-card', { hasText: 'Celeste' })).toBeVisible();
  // Recent activity lists the session.
  await expect(page.getByRole('heading', { name: 'Recent activity' })).toBeVisible();
  await expect(page.locator('.list-row', { hasText: 'Hades' }).first()).toContainText('30m');
});

test('the session timer survives a reload and Stop & log records a session', async ({ page }) => {
  const acc = await account(page);
  await put(acc, G.balatro, { status: 'playing' });
  await gotoApp(page);
  await page.getByRole('button', { name: 'Start a session timer for Balatro' }).click();
  const pill = page.locator('.timer-pill', { hasText: 'Balatro' });
  await expect(pill).toBeVisible();
  await page.reload();
  await expect(pill).toBeVisible();
  // The timer lives on the server: another device sees it.
  const timer = await (await acc.api.get('/api/v1/timer')).json();
  expect(timer.timer.game_id).toBe(G.balatro);
  await pill.click();
  const sheet = page.locator('dialog.sheet', { hasText: 'Stop session' });
  await expect(sheet).toBeVisible();
  await sheet.getByRole('button', { name: '15 minutes more' }).click();
  await sheet.getByRole('button', { name: /Stop & log/ }).click();
  await expect(pill).toHaveCount(0);
  await eventually(() => sessions(acc, G.balatro), list => list.length === 1 && list[0].source === 'timer', 'timer session');
  expect((await item(acc, G.balatro)).minutes).toBeGreaterThanOrEqual(15);
});

test('the ⊕ sheet lists what you are playing and recently touched', async ({ page }) => {
  const acc = await account(page);
  await put(acc, G.hades, { status: 'playing' });
  await put(acc, G.disco, { status: 'backlog' });
  await gotoApp(page);
  await logButton(page).click();
  const sheet = logSheet(page);
  await expect(sheet.getByRole('heading', { name: 'Playing' })).toBeVisible();
  await expect(sheet.locator(`.log-row[data-game-id="${G.hades}"]`)).toBeVisible();
  await expect(sheet.getByRole('heading', { name: 'Recent' })).toBeVisible();
  await sheet.locator(`.log-row[data-game-id="${G.disco}"]`).getByRole('button', { name: 'Start' }).click();
  await expect(sheet).toHaveCount(0);
  await eventually(() => item(acc, G.disco), it => it.status === 'playing', 'started');
  await expect(page.locator(`.now-card[data-game-id="${G.disco}"]`)).toBeVisible();
});
