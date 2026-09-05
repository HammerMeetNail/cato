import { test, expect } from '@playwright/test';

// Filter panels are transactional on every viewport: staging a control
// (sort select, Library chips, search-bar fields) must not reload, every
// close path (X, backdrop, Escape) must discard, and only Apply/Clear/Enter
// commits. The bottom status FAB is the separate instant path.

async function createUser(request: any, email: string, password: string) {
  const res = await request.post('/api/auth/signup', {
    data: { email, password },
  });
  expect(res.status()).toBe(201);
  return await res.json(); // { user_id, csrf_token, ... }
}

async function addLibraryItem(request: any, csrf: string, gameID: number, item: object) {
  const res = await request.post(`/api/library/${gameID}`, {
    headers: { 'X-CSRF-Token': csrf },
    data: item,
  });
  expect(res.status()).toBe(200);
}

async function loginViaUI(page: any, email: string, password: string) {
  await page.goto('/login');
  await page.locator('#email').fill(email);
  await page.locator('#password').fill(password);
  await page.locator('#submitBtn').click();
  await page.waitForURL((u: URL) => !u.pathname.includes('/login'));
}

test.describe('transactional filters', () => {
  test('advanced panel discards on X/backdrop/Escape and commits on Apply', async ({ page, request }) => {
    const email = `filters-${Date.now()}@example.com`;
    const user = await createUser(request, email, 'correct-password-1');
    await addLibraryItem(request, user.csrf_token, 1, { status: 'playing' });
    await addLibraryItem(request, user.csrf_token, 2, { status: 'backlog' });

    await loginViaUI(page, email, 'correct-password-1');
    await page.goto('/#library');
    await expect(page.locator('#gameGrid')).toBeVisible();
    await page.waitForTimeout(500);

    const libraryReqs: string[] = [];
    page.on('request', (r: any) => {
      if (r.url().includes('/api/library?')) libraryReqs.push(r.url());
    });

    const openPanel = async () => {
      await page.locator('#searchAdvancedBtn').click();
      await expect(page.locator('#libFilterPanel')).toBeVisible();
    };
    const stageSortAndChip = async () => {
      await page.locator('#lfSort').selectOption('name');
      await page.locator('#lfLibraryChips .lib-filter-chip[data-status="backlog"]').click();
      // Chip highlights as staged, but nothing reloads.
      await expect(page.locator('#lfLibraryChips .lib-filter-chip[data-status="backlog"]')).toHaveClass(/active/);
    };

    // Staging alone never reloads.
    await openPanel();
    await stageSortAndChip();
    await page.waitForTimeout(600);
    expect(libraryReqs.length).toBe(0);

    // X discards: no request, grid still shows both items, no filter bar.
    await page.locator('#libFilterClose').click();
    await expect(page.locator('#libFilterPanel')).toBeHidden();
    await page.waitForTimeout(400);
    expect(libraryReqs.length).toBe(0);
    expect(await page.locator('#tagFilterBar').count()).toBe(0);
    await expect(page.locator('#gameGrid')).toContainText('Test Game');
    await expect(page.locator('#gameGrid')).toContainText('Game Two');

    // Reopening shows the applied state, not the discarded staging.
    await openPanel();
    await expect(page.locator('#lfLibraryChips .lib-filter-chip[data-status="backlog"]')).not.toHaveClass(/active/);
    await expect(page.locator('#lfSort')).toHaveValue('');

    // Escape discards too.
    await stageSortAndChip();
    await page.keyboard.press('Escape');
    await expect(page.locator('#libFilterPanel')).toBeHidden();
    await page.waitForTimeout(400);
    expect(libraryReqs.length).toBe(0);

    // Backdrop discards too.
    await openPanel();
    await stageSortAndChip();
    await page.locator('#libFilterBackdrop').click({ position: { x: 10, y: 10 } });
    await expect(page.locator('#libFilterPanel')).toBeHidden();
    await page.waitForTimeout(400);
    expect(libraryReqs.length).toBe(0);

    // Apply commits staged chips + sort in one request.
    await openPanel();
    await stageSortAndChip();
    await Promise.all([
      page.waitForRequest((req) => req.url().includes('/api/library?'), { timeout: 7000 }),
      page.locator('#lfApply').click(),
    ]);
    expect(libraryReqs.length).toBeGreaterThan(0);
    expect(libraryReqs[libraryReqs.length - 1]).toContain('status=backlog');
    expect(libraryReqs[libraryReqs.length - 1]).toContain('sort=name');
    await expect(page.locator('#gameGrid')).toContainText('Game Two');
    await expect(page.locator('#gameGrid')).not.toContainText('Test Game');
    await expect(page.locator('#tagFilterBar')).toBeVisible();
  });

  test('search filter bar stages discrete controls until Apply', async ({ page, request }) => {
    const email = `sfilters-${Date.now()}@example.com`;
    const user = await createUser(request, email, 'correct-password-1');
    await addLibraryItem(request, user.csrf_token, 1, { status: 'backlog' });

    await loginViaUI(page, email, 'correct-password-1');
    await page.goto('/#search/' + encodeURIComponent('test game'));
    await expect(page.locator('#searchResultsHeader')).toBeVisible();
    await page.waitForTimeout(800);

    const searchReqs: string[] = [];
    page.on('request', (r: any) => {
      if (r.url().includes('/api/games/search')) searchReqs.push(r.url());
    });

    await page.locator('.search-filterbar summary').click();
    await page.locator('#sfEditions').check();
    await page.locator('#sfSort').selectOption('name');
    await page.locator('#sfInLibrary').selectOption('owned');
    // Collection toggle only reveals the Status row — no reload.
    await expect(page.locator('#sfLibraryStatusWrap')).toBeVisible();
    await page.waitForTimeout(600);
    expect(searchReqs.length).toBe(0);
    // Badge still shows the applied (empty) state while staging.
    await expect(page.locator('#sfBadge')).toBeHidden();

    await page.locator('#sfApply').click();
    await page.waitForTimeout(1000);
    expect(searchReqs.length).toBeGreaterThan(0);
    const last = searchReqs[searchReqs.length - 1];
    expect(last).toContain('sort=name');
    expect(last).toContain('in_library=1');
    expect(last).toContain('include_editions=1');
    await expect(page.locator('#sfBadge')).toContainText('3');
  });

  test('status FAB still applies instantly', async ({ page, request }) => {
    const email = `fab-${Date.now()}@example.com`;
    const user = await createUser(request, email, 'correct-password-1');
    await addLibraryItem(request, user.csrf_token, 1, { status: 'playing' });
    await addLibraryItem(request, user.csrf_token, 2, { status: 'backlog' });

    await loginViaUI(page, email, 'correct-password-1');
    await page.goto('/#library');
    await expect(page.locator('#gameGrid')).toBeVisible();
    await page.waitForTimeout(500);

    const libraryReqs: string[] = [];
    page.on('request', (r: any) => {
      if (r.url().includes('/api/library?')) libraryReqs.push(r.url());
    });

    await page.locator('#statusFilterBtn').click();
    await page.locator('#statusFilterPanel .lib-filter-chip[data-status="backlog"]').click();
    // Instant: reloads without Apply, panel stays open for multi-select.
    await expect(page.locator('#statusFilterPanel')).toBeVisible();
    await page.waitForTimeout(1000);
    expect(libraryReqs.length).toBeGreaterThan(0);
    expect(libraryReqs[libraryReqs.length - 1]).toContain('status=backlog');
    await expect(page.locator('#gameGrid')).toContainText('Game Two');
    await expect(page.locator('#gameGrid')).not.toContainText('Test Game');
  });
});
