import { test, expect, Page } from '@playwright/test';

// Routing must own these deliberate failures; SW behavior has its own spec.
test.use({ serviceWorkers: 'block' });

async function setup(page: Page) {
  const signup = await page.request.post('/api/auth/signup', {
    data: { email: `resilience-${crypto.randomUUID()}@example.com`, password: 'resilience-password' },
  });
  expect(signup.status()).toBe(201);
  const { csrf_token } = await signup.json();
  expect((await page.request.post('/api/library/1', {
    headers: { 'X-CSRF-Token': csrf_token },
    data: { status: 'backlog', notes: 'Original notes', rating: 75, playtime_minutes: 90 },
  })).status()).toBe(200);
}

test('failed autosave preserves the open draft and closing retries the persisted edit', async ({ page }) => {
  await setup(page);
  await page.goto('/#game/1');
  const modal = page.locator('#addGameModal');
  await expect(modal.locator('.modal-notes')).toHaveValue('Original notes');
  let failedSaves = 0;
  await page.route('**/api/library/1', async route => {
    if (route.request().method() === 'POST') {
      failedSaves++;
      await route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: 'unavailable', message: `Temporary save failure ${failedSaves}` }) });
    } else {
      await route.continue();
    }
  });
  await modal.locator('.modal-notes').fill('Draft retained after a failed save');
  await expect(page.locator('#toast')).toContainText('Temporary save failure 1');
  expect(failedSaves).toBe(1);
  expect((await (await page.request.get('/api/library/1')).json()).notes).toBe('Original notes');
  // A failed close must preserve both the visible draft and its game route.
  const rejected = page.waitForResponse(r => r.url().endsWith('/api/library/1') && r.request().method() === 'POST' && r.status() === 503);
  await modal.locator('.modal-cancel').click();
  await rejected;
  await expect(page.locator('#toast')).toContainText('Temporary save failure 2');
  await expect(modal).toBeVisible();
  await expect(modal.locator('.modal-notes')).toHaveValue('Draft retained after a failed save');
  await expect(page).toHaveURL(/#game\/1$/);
  await page.unroute('**/api/library/1');
  const saved = page.waitForResponse(r => r.url().endsWith('/api/library/1') && r.request().method() === 'POST' && r.status() === 200);
  await modal.locator('.modal-cancel').click();
  await saved;
  await expect(modal).toHaveCount(0);
  await page.goto('/#game/1');
  await expect(modal.locator('.modal-notes')).toHaveValue('Draft retained after a failed save');
});

test('Stats retry recovers a failed fetch and recent activity opens the persisted game', async ({ page }) => {
  await setup(page);
  await page.route('**/api/library/stats', route => route.fulfill({
    status: 503, contentType: 'application/json', body: '{"error":"unavailable"}',
  }));
  await page.goto('/#stats');
  await expect(page.locator('#statsView')).toContainText('Failed to load stats.');
  await page.unroute('**/api/library/stats');
  const recovered = page.waitForResponse(r => r.url().endsWith('/api/library/stats') && r.status() === 200);
  await page.locator('#statsView').getByRole('button', { name: 'Retry', exact: true }).click();
  await recovered;
  await expect(page.locator('.stat-grid')).toContainText('1.5h');
  await page.locator('.stat-recent-row[data-game-id="1"]').click();
  await expect(page).toHaveURL(/#game\/1$/);
  await expect(page.locator('#addGameModal .modal-notes')).toHaveValue('Original notes');
});

test('Settings exports the authenticated library as a downloadable CSV', async ({ page }) => {
  await setup(page);
  await page.goto('/#settings');
  const downloaded = page.waitForEvent('download');
  await page.getByRole('link', { name: 'Export library as CSV' }).click();
  const download = await downloaded;
  expect(download.suggestedFilename()).toMatch(/^cato-library-\d{4}-\d{2}-\d{2}\.csv$/);
  expect(await download.failure()).toBeNull();
  const stream = await download.createReadStream();
  expect(stream).not.toBeNull();
  const chunks: Buffer[] = [];
  for await (const chunk of stream!) chunks.push(Buffer.from(chunk));
  const csv = Buffer.concat(chunks).toString('utf8');
  expect(csv).toContain('game,status,platform,medium,rating,playtime_hours,started_at,completed_at,added_at,tags,notes');
  expect(csv).toContain('Test Game,backlog,,,75,1.50,');
  expect(csv).toContain('Original notes');
  expect(csv).not.toContain('Game Two');
});
