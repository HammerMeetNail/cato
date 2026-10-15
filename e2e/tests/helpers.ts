import { expect, Locator, Page, APIRequestContext } from '@playwright/test';

// Shared E2E helpers. Accounts and data are set up through /api/v1 only;
// the behaviour under test is driven through the UI.

export const PASSWORD = 'e2e-password-1';

// Seeded catalog games (scripts/e2e-server.sh).
export const G = {
  test: 1, two: 2, hades: 200, celeste: 201, hollow: 202, balatro: 203, outer: 204, stardew: 205,
  disco: 206, spire: 207, tunic: 208, dead: 209, botw: 210, totk: 211, links: 212, pikmin: 213, astro: 214, metroid: 215,
};

export type Account = { email: string; csrf: string; api: APIRequestContext };

// account signs a fresh user up in page's context (so the browser is
// signed in) and returns its CSRF token for API seeding.
export async function account(page: Page, prefix = 'e2e'): Promise<Account> {
  const email = `${prefix}-${crypto.randomUUID()}@example.com`;
  const res = await page.request.post('/api/auth/signup', { data: { email, password: PASSWORD } });
  expect(res.status()).toBe(201);
  const { csrf_token } = await res.json();
  return { email, csrf: csrf_token, api: page.request };
}

export async function put(acc: Account, id: number, data: object = {}) {
  const res = await acc.api.put(`/api/v1/library/${id}`, { headers: { 'X-CSRF-Token': acc.csrf }, data });
  expect(res.status(), await res.text()).toBeLessThan(300);
  return (await res.json()).item;
}

export async function session(acc: Account, id: number, minutes: number, played_on?: string) {
  const res = await acc.api.post('/api/v1/sessions', { headers: { 'X-CSRF-Token': acc.csrf }, data: { game_id: id, minutes, played_on } });
  expect(res.status(), await res.text()).toBe(201);
  return res.json();
}

export async function item(acc: Account, id: number) {
  const res = await acc.api.get(`/api/v1/library/${id}`);
  if (res.status() === 404) return null;
  expect(res.status()).toBe(200);
  return res.json();
}

export async function playthroughs(acc: Account, id: number) {
  return (await (await acc.api.get(`/api/v1/library/${id}/playthroughs`)).json()).items;
}

export async function sessions(acc: Account, id: number) {
  return (await (await acc.api.get(`/api/v1/sessions?game_id=${id}`)).json()).items;
}

// eventually polls an API read until it matches (writes are optimistic and
// reach the server a moment after the UI updates).
export async function eventually<T>(fn: () => Promise<T>, check: (v: T) => boolean, message = 'condition') {
  await expect.poll(async () => check(await fn()), { message, timeout: 7000 }).toBe(true);
}

export function localDay(d = new Date()) {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}

// TapCounter counts taps (clicks) for the tap-budget journeys (plan §5).
export class TapCounter {
  count = 0;
  async tap(target: Locator) {
    this.count++;
    await target.click();
  }
}

export function toast(page: Page) {
  return page.locator('.toast-region .toast');
}

export function undoButton(page: Page) {
  return page.locator('.toast-region .toast-action', { hasText: 'Undo' });
}

// gotoApp opens a route and waits for the app to render its shell (the
// static frame in index.html looks the same but isn't the app yet).
export async function gotoApp(page: Page, hash = '#home') {
  await page.goto('/' + hash);
  await expect(page.locator('#app[data-rendered] nav.nav .nav-item').first()).toBeVisible();
}

// logButton is the ⊕ in the navigation.
export function logButton(page: Page) {
  return page.locator('nav.nav .nav-log');
}

export function logSheet(page: Page) {
  return page.locator('dialog.sheet[aria-label="Log"]');
}
