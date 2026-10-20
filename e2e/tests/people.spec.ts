import { test, expect, Browser, Page } from '@playwright/test';
import { account, put, session, gotoApp, G, PASSWORD, Account, localDay } from './helpers';

// People (plan §7): profiles and privacy, follows, the Home feed, reviews
// on game pages, lists, the inbox, invites and the admin pages. Each person
// gets their own browser context; the data a test needs is set up through
// /api/v1 and the behaviour under test is driven through the UI.

type Person = { page: Page; acc: Account; username: string; close: () => Promise<void> };

async function person(browser: Browser, baseURL: string | undefined, name: string, visibility = 'instance'): Promise<Person> {
  const ctx = await browser.newContext({ baseURL, ignoreHTTPSErrors: true });
  const page = await ctx.newPage();
  const acc = await account(page, name);
  const username = `${name}_${Math.random().toString(36).slice(2, 8)}`;
  const res = await acc.api.patch('/api/v1/me/profile', { headers: { 'X-CSRF-Token': acc.csrf }, data: { username, display_name: name[0].toUpperCase() + name.slice(1), visibility } });
  expect(res.status(), await res.text()).toBe(200);
  return { page, acc, username, close: () => ctx.close() };
}

async function api(p: Person, method: 'post' | 'patch' | 'put' | 'delete', path: string, data?: object) {
  const res = await p.acc.api[method](path, { headers: { 'X-CSRF-Token': p.acc.csrf }, data });
  expect(res.status(), await res.text()).toBeLessThan(300);
  return res.json();
}

test('follow someone from People, then see what they play on Home', async ({ browser, baseURL }) => {
  const alex = await person(browser, baseURL, 'alex');
  const bea = await person(browser, baseURL, 'bea');
  try {
    await gotoApp(bea.page, '#you/people');
    await bea.page.getByPlaceholder('Find people on this server').fill(alex.username);
    await bea.page.getByRole('link', { name: 'Alex' }).click();
    await expect(bea.page.getByRole('heading', { level: 1, name: 'Alex' })).toBeVisible();
    await bea.page.getByRole('button', { name: 'Follow' }).click();
    // Alex is visible to everyone here, so the follow is accepted at once.
    await expect(bea.page.getByRole('button', { name: 'Following' })).toBeVisible();

    await put(alex.acc, G.hades, { status: 'playing' });
    await session(alex.acc, G.hades, 90, localDay());
    await session(alex.acc, G.celeste, 30, localDay()).catch(() => {});
    await put(alex.acc, G.balatro, { status: 'completed', rating: 90 });

    await gotoApp(bea.page, '#home');
    const feed = bea.page.getByRole('region', { name: 'Friends' });
    await expect(feed).toContainText('Alex finished Balatro');
    await expect(feed).toContainText('Alex started Hades');
    await expect(feed).toContainText(/Alex (played Hades for|logged)/);

    // The profile shows their games.
    await gotoApp(bea.page, `#u/${alex.username}`);
    await expect(bea.page.getByRole('heading', { name: 'Playing', exact: true })).toBeVisible();
    await expect(bea.page.getByRole('heading', { name: 'Recently finished' })).toBeVisible();
  } finally {
    await alex.close();
    await bea.close();
  }
});

test('a private profile needs an approved follow', async ({ browser, baseURL }) => {
  const cam = await person(browser, baseURL, 'cam', 'followers');
  const dee = await person(browser, baseURL, 'dee');
  try {
    await put(cam.acc, G.hollow, { status: 'playing' });
    await gotoApp(dee.page, `#u/${cam.username}`);
    await expect(dee.page.getByRole('heading', { name: 'This profile is private' })).toBeVisible();
    await expect(dee.page.getByRole('heading', { name: 'Playing', exact: true })).toHaveCount(0);
    await dee.page.getByRole('button', { name: 'Follow' }).click();
    await expect(dee.page.getByRole('button', { name: 'Requested' })).toBeVisible();

    // Cam sees the request in the inbox badge and accepts it from People.
    await gotoApp(cam.page, '#you/people');
    await expect(cam.page.locator('.nav-item[data-nav="you"] .badge')).toHaveText('1');
    await cam.page.getByRole('button', { name: 'Accept' }).click();
    await expect(cam.page.getByRole('button', { name: 'Accept' })).toHaveCount(0);

    await dee.page.reload();
    await expect(dee.page.getByRole('heading', { name: 'Playing', exact: true })).toBeVisible();
    await expect(dee.page.locator('.upnext-card', { hasText: 'Hollow Knight' }).first()).toBeVisible();
  } finally {
    await cam.close();
    await dee.close();
  }
});

test('reviews: write one with a spoiler, a friend reveals and likes it, the like reaches the inbox', async ({ browser, baseURL }) => {
  const eve = await person(browser, baseURL, 'eve');
  const fin = await person(browser, baseURL, 'fin');
  try {
    await api(fin, 'post', `/api/v1/users/${eve.username}/follow`);
    await put(eve.acc, G.outer, { status: 'completed', rating: 100 });
    await gotoApp(eve.page, `#game/${G.outer}`);
    await eve.page.getByRole('button', { name: 'Write a review' }).click();
    const sheet = eve.page.locator('dialog.sheet');
    await sheet.getByLabel('Your review').fill('The ending is a quiet goodbye to the universe.');
    await sheet.getByLabel(/Contains spoilers/).check();
    await sheet.getByRole('button', { name: 'Post review' }).click();
    // (Earlier projects' Eves are on this server too: Eve's own review is
    // the one she sees in full.)
    await expect(eve.page.locator('.review', { hasText: 'quiet goodbye' })).toHaveCount(1);

    await gotoApp(fin.page, `#game/${G.outer}`);
    const review = fin.page.locator('.review', { has: fin.page.getByText('Friend', { exact: true }) });
    await expect(review.getByText('Contains spoilers — show')).toBeVisible();
    await expect(review).not.toContainText('quiet goodbye');
    await review.getByRole('button', { name: /show/ }).click();
    await expect(review).toContainText('quiet goodbye');
    await review.getByRole('button', { name: /Like \(0\)/ }).click();
    await expect(review.getByRole('button', { name: /Unlike \(1\)/ })).toBeVisible();
    await expect(fin.page.getByRole('heading', { name: /1 friend has this/ })).toBeVisible();

    await gotoApp(eve.page, '#you/inbox');
    await expect(eve.page.locator('main')).toContainText('Fin liked your review of Outer Wilds');
  } finally {
    await eve.close();
    await fin.close();
  }
});

test('lists: start from a suggestion, add from a game page, reorder', async ({ page }) => {
  const acc = await account(page, 'lists');
  await put(acc, G.tunic, { status: 'completed' });
  await put(acc, G.spire, { status: 'backlog' });
  await gotoApp(page, '#you/lists');
  await page.getByRole('button', { name: 'Comfort games' }).click();
  const sheet = page.locator('dialog.sheet');
  await expect(sheet.getByLabel('Title')).toHaveValue('Comfort games');
  await sheet.getByLabel(/Ranked/).check();
  await sheet.getByRole('button', { name: 'Create' }).click();
  await expect(page.getByRole('heading', { level: 1, name: 'Comfort games' })).toBeVisible();
  const listURL = page.url();

  for (const id of [G.tunic, G.spire]) {
    await gotoApp(page, `#game/${id}`);
    await page.getByRole('button', { name: 'Add to list' }).click();
    await page.locator('dialog.sheet').getByLabel('Comfort games').check();
    await page.keyboard.press('Escape');
  }
  await page.goto(listURL);
  const rows = page.locator('.list-items > li');
  await expect(rows).toHaveCount(2);
  await expect(rows.first()).toContainText('Tunic');
  await page.getByRole('button', { name: 'Move Slay the Spire up' }).click();
  await expect(rows.first()).toContainText('Slay the Spire');
  await page.reload();
  await expect(rows.first()).toContainText('Slay the Spire');
});

test('an invite link signs a friend up already connected', async ({ browser, baseURL }) => {
  const gus = await person(browser, baseURL, 'gus');
  const ctx = await browser.newContext({ baseURL, ignoreHTTPSErrors: true });
  try {
    await gotoApp(gus.page, '#you/people');
    await gus.page.getByRole('button', { name: 'Invite' }).click();
    await gus.page.locator('dialog.sheet').getByLabel(/Who's it for/).fill('Hana');
    await gus.page.getByRole('button', { name: 'Create link' }).click();
    const link = await gus.page.locator('dialog.sheet input[aria-label="Link"]').inputValue();
    expect(link).toContain('/login?invite=');
    await gus.page.getByRole('button', { name: 'Copy link' }).click();
    await expect(gus.page.locator('.toast-region')).toContainText('Link copied');

    const newcomer = await ctx.newPage();
    await newcomer.goto(new URL(link).pathname + new URL(link).search);
    await expect(newcomer.locator('#pitch')).toHaveText('Gus invited you to Cato.');
    await expect(newcomer.locator('#formTitle')).toHaveText('Create your account');
    await newcomer.fill('#email', `hana-${crypto.randomUUID()}@example.com`);
    await newcomer.fill('#password', PASSWORD);
    await newcomer.click('#submitBtn');
    await expect(newcomer.locator('#app[data-rendered] nav.nav')).toBeVisible();
    await newcomer.goto('/#you/people');
    await expect(newcomer.locator('main')).toContainText('Gus');

    await gus.page.keyboard.press('Escape');
    await gus.page.reload();
    await expect(gus.page.locator('main')).toContainText(/Used by/);
  } finally {
    await gus.close();
    await ctx.close();
  }
});

test('admin: people, reports, sign-up mode and the audit log', async ({ browser, baseURL }) => {
  const ctx = await browser.newContext({ baseURL, ignoreHTTPSErrors: true });
  const page = await ctx.newPage();
  const ivy = await person(browser, baseURL, 'ivy');
  try {
    // Ivy reports a review so there's something to moderate.
    await put(ivy.acc, G.dead, { status: 'completed', rating: 40 });
    const { review } = await api(ivy, 'post', '/api/v1/reviews', { game_id: G.dead, body: 'Report me', visibility: 'instance' });
    await api(ivy, 'post', '/api/v1/reports', { kind: 'review', target: String(review.id), reason: 'e2e' });

    const login = await page.request.post('/api/auth/login', { data: { email: 'admin@e2e.test', password: PASSWORD } });
    expect(login.status()).toBe(200);
    await gotoApp(page, '#you');
    await page.getByRole('link', { name: /Admin/ }).click();
    await expect(page.getByRole('cell', { name: new RegExp(ivy.username) })).toBeVisible();

    await page.getByRole('tab', { name: 'Reports' }).click();
    const report = page.locator('.list-row', { hasText: 'Report me' });
    await report.getByRole('button', { name: 'Hide it' }).click();
    await expect(page.getByText('No open reports')).toBeVisible();
    const gone = await ivy.acc.api.get(`/api/v1/games/${G.dead}/social`);
    expect((await gone.json()).reviews.find((r: { id: number; hidden: boolean }) => r.id === review.id)?.hidden).toBe(true);

    await page.getByRole('tab', { name: 'Sign-up' }).click();
    const csrf = (await (await page.request.get('/api/v1/me')).json()).csrf_token;
    try {
      await page.getByRole('radio', { name: 'Invite only' }).click();
      await expect(page.getByText('This overrides CATO_SIGNUP=open')).toBeVisible();
      const cfg = await (await page.request.get('/api/config')).json();
      expect(cfg.signup).toBe('invite');
    } finally {
      // Other specs sign up freely: put the environment's mode back.
      await page.request.patch('/api/v1/admin/settings', { headers: { 'X-CSRF-Token': csrf }, data: { signup: '' } });
    }

    await page.getByRole('tab', { name: 'System' }).click();
    await expect(page.getByText('Background jobs')).toBeVisible();
    await page.getByRole('tab', { name: 'Audit log' }).click();
    await expect(page.getByRole('cell', { name: 'report.resolve' }).first()).toBeVisible();
    await expect(page.getByRole('cell', { name: 'settings.signup' }).first()).toBeVisible();

    // Not for everyone.
    const denied = await ivy.acc.api.get('/api/v1/admin/users');
    expect(denied.status()).toBe(403);
  } finally {
    await ivy.close();
    await ctx.close();
  }
});
