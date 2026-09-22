import { test, expect, Page } from '@playwright/test';

const password = 'journey-password-1';
async function account(page: Page) {
  const email = `journey-${crypto.randomUUID()}@example.com`;
  const response = await page.request.post('/api/auth/signup', { data: { email, password } });
  expect(response.status()).toBe(201);
  return { email, ...(await response.json()) };
}
async function seed(page: Page, csrf: string, id: number, data: object) {
  const response = await page.request.post(`/api/library/${id}`, {
    headers: { 'X-CSRF-Token': csrf }, data,
  });
  expect(response.status()).toBe(200);
}
async function closeAndSave(page: Page) {
  await page.locator('#addGameModal .modal-cancel').click();
  await expect(page.locator('#addGameModal')).toHaveCount(0);
}

// API setup is confined to prerequisites; the mutations under test use the UI.
test('library creation, metadata autosave, date correction, removal and undo survive reload', async ({ page }) => {
  await account(page);
  await page.goto('/#game/1');
  const modal = page.locator('#addGameModal');
  await expect(modal).toBeVisible();
  await modal.getByLabel('Status', { exact: true }).selectOption('completed');
  await modal.getByLabel('Rating, 0 to 100').selectOption('85');
  await modal.getByLabel('Hours played').selectOption('2.5');
  await modal.locator('.modal-tags-input').fill('adventure');
  await modal.locator('.modal-tags-input').press('Enter');
  const notes = '<img src=x onerror=alert(1)> A memorable ending';
  await modal.locator('.modal-notes').fill(notes);
  await modal.locator('.plat-chip[data-full="Nintendo Switch"]').click();
  await modal.getByRole('button', { name: 'Digital', exact: true }).click();
  await closeAndSave(page);
  await page.goto('/#game/1');
  await expect(modal.locator('.modal-notes')).toHaveValue(notes);
  await expect(modal.getByLabel('Rating, 0 to 100')).toHaveValue('85');
  await expect(modal.getByLabel('Hours played')).toHaveValue('2.5');
  await expect(modal.locator('.tag-chip-removable')).toHaveText('adventure×');
  await expect(modal.locator('.plat-chip[data-full="Nintendo Switch"]')).toHaveClass(/selected/);
  await expect(modal.getByRole('button', { name: 'Digital', exact: true })).toHaveClass(/selected/);
  await modal.locator('[data-datekey="completed"]').fill('2024-05-06');
  await closeAndSave(page);
  await page.goto('/#game/1');
  await expect(modal.locator('[data-datekey="completed"]')).toHaveValue('2024-05-06');
  const item = await (await page.request.get('/api/library/1')).json();
  expect(item).toMatchObject({ status: 'completed', rating: 85, playtime_minutes: 150,
    notes, tags: ['adventure'], medium: 'digital', owned_platforms: ['Nintendo Switch'] });
  const removed = page.waitForResponse(r => r.url().endsWith('/api/library/1') && r.request().method() === 'DELETE');
  await modal.getByRole('button', { name: 'Remove', exact: true }).click();
  expect((await removed).ok()).toBeTruthy();
  expect((await page.request.get('/api/library/1')).status()).toBe(404);
  await page.getByRole('button', { name: 'Undo', exact: true }).click();
  await expect.poll(async () => (await page.request.get('/api/library/1')).status()).toBe(200);
  await page.goto('/#game/1');
  await expect(modal.locator('.modal-notes')).toHaveValue(notes);
  await expect(modal.locator('[data-datekey="completed"]')).toHaveValue('2024-05-06');
  await modal.getByRole('button', { name: 'Remove', exact: true }).click();
  await expect(modal).toHaveCount(0);
  await page.goto('/#library');
  await expect(page.locator('#gameGrid .game-card')).toHaveCount(0);
  expect((await page.request.get('/api/library/1')).status()).toBe(404);
});

test('infinite scroll appends the second page exactly once and filters reset pagination', async ({ page }) => {
  const user = await account(page);
  for (let id = 100; id <= 164; id++) {
    await seed(page, user.csrf_token, id, { status: id === 164 ? 'playing' : 'backlog' });
  }
  await page.goto('/#library');
  const cards = page.locator('#gameGrid .game-card');
  await expect(cards).toHaveCount(60);
  const nextPage = page.waitForResponse(r => {
    const url = new URL(r.url());
    return url.pathname === '/api/library' && url.searchParams.get('offset') === '60';
  });
  await cards.last().scrollIntoViewIfNeeded();
  expect((await nextPage).ok()).toBeTruthy();
  await expect(cards).toHaveCount(65);
  expect(new Set(await cards.evaluateAll(nodes => nodes.map(n => n.getAttribute('data-game-id')))).size).toBe(65);
  await page.locator('#statusFilterBtn').click();
  await page.locator('#statusFilterPanel [data-status="playing"]').click();
  await expect(cards).toHaveCount(1);
  await expect(cards).toContainText('Pagination Game 164');
  await page.locator('#statusFilterPanel [data-status="playing"]').click();
  await expect(cards).toHaveCount(60);
  await page.locator('#statusFilterBtn').click();
  await cards.last().scrollIntoViewIfNeeded();
  await expect(cards).toHaveCount(65);
});

test('Playing finish refreshes cached Stats and Settings aggregates', async ({ page }) => {
  const user = await account(page);
  await seed(page, user.csrf_token, 1, { status: 'playing', playtime_minutes: 60, rating: 80, tags: ['favorite'], platforms: ['Nintendo Switch'] });
  await seed(page, user.csrf_token, 2, { status: 'completed', playtime_minutes: 120, rating: 60, tags: ['favorite'] });
  await page.goto('/#stats');
  const stat = (label: string) => page.locator('.stat-cell').filter({ has: page.locator('.stat-label', { hasText: new RegExp(`^${label}$`) }) }).locator('.stat-value');
  await expect(stat('games')).toHaveText('2');
  await expect(stat('finished')).toHaveText('1');
  await expect(stat('logged')).toHaveText('3h');
  await expect(stat('avg rating')).toHaveText('70.0');
  await expect(page.locator('.stat-tags')).toContainText('favorite · 2');
  await expect(page.locator('.stat-list-row')).toContainText('Nintendo Switch');
  await page.locator('[data-route="settings"]').click();
  await expect(page.locator('#librarySummary')).toHaveText('2 games · 1 finished · ~3h logged');
  await page.locator('[data-route="now"]').click();
  const card = page.locator('.hero-card[data-game-id="1"]');
  await card.locator('[data-hero-time="60"]').click();
  await expect(card.locator('.hero-sub')).toContainText('2h');
  await card.locator('[data-hero-finish]').click();
  await expect(page.locator('#playingView')).toContainText('All caught up!');
  await page.locator('[data-route="stats"]').click();
  await expect(stat('finished')).toHaveText('2');
  await expect(stat('logged')).toHaveText('4h');
  await page.locator('[data-route="settings"]').click();
  await expect(page.locator('#librarySummary')).toHaveText('2 games · 2 finished · ~4h logged');
  await page.reload();
  await expect(page.locator('#librarySummary')).toHaveText('2 games · 2 finished · ~4h logged');
});

test('Settings saves profile and theme, rejects invalid passwords, and requires typed account deletion', async ({ page }) => {
  const user = await account(page);
  await seed(page, user.csrf_token, 1, { status: 'backlog' });
  await page.goto('/#settings');
  await page.locator('#displayNameInput').fill('Journey Player');
  await page.locator('#saveNameBtn').click();
  await expect(page.locator('#nameFeedback')).toHaveText('Saved');
  await page.locator('#themeSelect').selectOption('light');
  await page.reload();
  await expect(page.locator('#displayNameInput')).toHaveValue('Journey Player');
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  await page.locator('#currentPasswordInput').fill(password);
  await page.locator('#newPasswordInput').fill('new-journey-password');
  await page.locator('#confirmPasswordInput').fill('mismatching-password');
  await page.getByRole('button', { name: 'Update password' }).click();
  await expect(page.locator('#passwordFeedback')).toHaveText('New passwords do not match');
  await page.locator('#confirmPasswordInput').fill('new-journey-password');
  await page.locator('#currentPasswordInput').fill('wrong-current-password');
  await page.getByRole('button', { name: 'Update password' }).click();
  await expect(page.locator('#passwordFeedback')).toHaveClass(/settings-feedback-error/);
  await expect(page.locator('#passwordFeedback')).not.toHaveText('New passwords do not match');
  await expect(page.locator('#passwordFeedback')).not.toBeEmpty();
  await page.locator('#deleteAccountBtn').click();
  await expect(page.locator('#confirmDeleteBtn')).toBeDisabled();
  await page.locator('#deleteConfirmInput').fill('delete');
  await expect(page.locator('#confirmDeleteBtn')).toBeDisabled();
  await page.locator('#deleteConfirmInput').fill('DELETE');
  await expect(page.locator('#confirmDeleteBtn')).toBeEnabled();
  await page.locator('#cancelDeleteBtn').click();
  await expect(page.locator('#deleteAccountConfirm')).toBeHidden();
  expect((await page.request.get('/api/library/1')).status()).toBe(200);
  await page.locator('#deleteAccountBtn').click();
  await expect(page.locator('#deleteConfirmInput')).toHaveValue('');
  await expect(page.locator('#confirmDeleteBtn')).toBeDisabled();
  await page.locator('#deleteConfirmInput').fill('DELETE');
  await page.locator('#confirmDeleteBtn').click();
  await expect(page).toHaveURL(/\/login$/);
  expect((await page.request.get('/api/library')).status()).toBe(401);
  await page.goto('/');
  await expect(page).toHaveURL(/\/login$/);
  await page.locator('#email').fill(user.email);
  await page.locator('#password').fill(password);
  await page.locator('#submitBtn').click();
  await expect(page.locator('#errorMsg')).toContainText(/invalid/i);
});

test('login form validates required fields, email and password length before signup', async ({ page }) => {
  await page.goto('/login');
  await page.locator('#toggleLink').click();
  await page.locator('#submitBtn').click();
  expect(await page.locator('#email').evaluate((el: HTMLInputElement) => el.validity.valueMissing)).toBe(true);
  await page.locator('#email').fill('not-an-email');
  expect(await page.locator('#email').evaluate((el: HTMLInputElement) => el.validity.typeMismatch)).toBe(true);
  await page.locator('#email').fill(`validation-${crypto.randomUUID()}@example.com`);
  await page.locator('#password').pressSequentially('short');
  await page.locator('#submitBtn').click();
  expect(await page.locator('#password').evaluate((el: HTMLInputElement) => el.validity.tooShort)).toBe(true);
  await expect(page).toHaveURL(/\/login$/);
});

test('separate browser sessions isolate libraries and reject another user’s CSRF token', async ({ page, browser, baseURL }) => {
  const a = await account(page);
  await seed(page, a.csrf_token, 1, { status: 'playing', notes: 'Private owner notes' });
  const context = await browser.newContext({ baseURL });
  try {
    const other = await context.newPage();
    const b = await account(other);
    await page.goto('/#library');
    await other.goto('/#library');
    await expect(page.locator('#gameGrid .game-card')).toHaveCount(1);
    await expect(other.locator('#gameGrid .game-card')).toHaveCount(0);
    expect((await other.request.get('/api/library/1')).status()).toBe(404);
    // Execute fetch in the browser so the actual session cookie accompanies it.
    for (const token of ['', a.csrf_token]) {
      const status = await other.evaluate(async token => (await fetch('/api/library/1', {
        method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': token },
        body: JSON.stringify({ status: 'completed', notes: 'Must not be saved' }),
      })).status, token);
      expect(status).toBe(403);
    }
    await seed(other, b.csrf_token, 1, { status: 'wishlist', notes: 'Second owner notes' });
    expect((await (await page.request.get('/api/library/1')).json()).notes).toBe('Private owner notes');
    await other.goto('/#game/1');
    await expect(other.locator('.modal-notes')).toHaveValue('Second owner notes');
    await other.locator('.modal-remove').click();
    await expect(other.locator('#addGameModal')).toHaveCount(0);
    await page.reload();
    await expect(page.locator('#gameGrid .game-card')).toHaveCount(1);
    expect((await (await page.request.get('/api/library/1')).json()).notes).toBe('Private owner notes');
  } finally {
    await context.close();
  }
});
