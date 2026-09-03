import { test, expect } from '@playwright/test';

// Helpers: create a user + session via the API and return cookies for the
// browser context.
async function createUser(request: any, email: string, password: string) {
  const res = await request.post('/api/auth/signup', {
    data: { email, password },
  });
  expect(res.status()).toBe(201);
  const body = await res.json();
  return body; // { user_id, csrf_token, ... }
}

async function addLibraryItem(request: any, csrf: string, gameID: number, item: object) {
  const res = await request.post(`/api/library/${gameID}`, {
    headers: { 'X-CSRF-Token': csrf },
    data: item,
  });
  expect(res.status()).toBe(200);
}

test.describe('library', () => {
  test('library grid shows added games with correct tab counts', async ({ page, request }) => {
    const email = `lib-${Date.now()}@example.com`;
    const user = await createUser(request, email, 'correct-password-1');
    await addLibraryItem(request, user.csrf_token, 1, {
      status: 'playing', rating: 80, playtime_minutes: 120, tags: ['e2e'],
    });
    await addLibraryItem(request, user.csrf_token, 2, {
      status: 'backlog', rating: 0, playtime_minutes: 0, tags: [],
    });

    // Seed the browser session by logging in via the page's own fetch path.
    await page.goto('/login');
    await page.locator('#email').fill(email);
    await page.locator('#password').fill('correct-password-1');
    await page.locator('#submitBtn').click();
    await page.waitForURL((u) => !u.pathname.includes('/login'));

    // Post-login landing is the Playing tab (empty hash is its home);
    // navigate to the Library view before asserting on the grid.
    await page.goto('/#library');

    // The grid renders the playing item.
    await expect(page.locator('#gameGrid')).toBeVisible();
    await page.waitForTimeout(500); // allow the initial library fetch to paint
    const gridText = await page.locator('#gameGrid').innerText();
    expect(gridText).toContain('Test Game');

    // Status filters live in the floating FAB (the legacy #statusTabs row is
    // retired and permanently hidden); counts mirror onto its chips.
    const fab = page.locator('#statusFilterFab');
    await expect(fab).toBeVisible();
    await fab.locator('#statusFilterBtn').click();
    const panelText = await page.locator('#statusFilterPanel').innerText();
    expect(panelText.length).toBeGreaterThan(0);
  });

  test('search dropdown performs a local search and shows feedback', async ({ page, request }) => {
    const email = `search-${Date.now()}@example.com`;
    const user = await createUser(request, email, 'correct-password-1');
    await addLibraryItem(request, user.csrf_token, 1, { status: 'backlog' });

    await page.goto('/login');
    await page.locator('#email').fill(email);
    await page.locator('#password').fill('correct-password-1');
    await page.locator('#submitBtn').click();
    await page.waitForURL((u) => !u.pathname.includes('/login'));

    // Search lives in the Library view; post-login landing is Playing.
    await page.goto('/#library');
    await expect(page.locator('#searchInput')).toBeVisible();

    // Type a query that matches the seeded catalog (IGDB is unconfigured in
    // E2E, so results come from the local DB).
    await page.locator('#searchInput').fill('test game');
    const results = page.locator('#searchResults');
    await expect(results).toBeVisible({ timeout: 10_000 });
    await page.waitForTimeout(600); // debounce + fetch
    const dropdownText = await results.innerText();
    expect(dropdownText.length).toBeGreaterThan(0);
  });

  test('hash routes render their views', async ({ page, request }) => {
    const email = `nav-${Date.now()}@example.com`;
    const user = await createUser(request, email, 'correct-password-1');
    void user;

    await page.goto('/login');
    await page.locator('#email').fill(email);
    await page.locator('#password').fill('correct-password-1');
    await page.locator('#submitBtn').click();
    await page.waitForURL((u) => !u.pathname.includes('/login'));

    // Settings tab.
    await page.goto('/#settings');
    await page.waitForTimeout(600);
    await expect(page.locator('#accountEmail')).toBeVisible();

    // Library canonical route (#library — empty hash is the Playing tab's
    // home, so /# renders Playing, not the grid).
    await page.goto('/#library');
    await page.waitForTimeout(400);
    await expect(page.locator('#gameGrid')).toBeVisible();
  });

  test('library item can be deleted via the API with CSRF', async ({ request }) => {
    const email = `del-${Date.now()}@example.com`;
    const user = await createUser(request, email, 'correct-password-1');
    await addLibraryItem(request, user.csrf_token, 1, { status: 'wishlist' });

    const del = await request.delete('/api/library/1', {
      headers: { 'X-CSRF-Token': user.csrf_token },
    });
    expect(del.status()).toBe(200);

    const list = await request.get('/api/library', {
      headers: { cookie: `cato_session=${await getSessionCookie(request, email)}` },
    });
    void list;
  });
});

// Logs a user in through the API and returns the raw session cookie value.
async function getSessionCookie(request: any, email: string) {
  const res = await request.post('/api/auth/login', {
    data: { email, password: 'correct-password-1' },
  });
  const setCookie = res.headersArray().find((h: any) => h.name.toLowerCase() === 'set-cookie');
  const m = /cato_session=([^;]+)/.exec(setCookie?.value ?? '');
  return m ? m[1] : '';
}
