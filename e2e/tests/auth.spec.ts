import { test, expect } from '@playwright/test';
import { PASSWORD } from './helpers';

test('the login page explains Cato and offers only what the server supports', async ({ page }) => {
  await page.goto('/login');
  await expect(page.locator('#pitch')).toContainText('what you\'re playing');
  // Google isn't configured in E2E.
  await expect(page.locator('#googleBtn')).toBeHidden();
  // The sign-up toggle is a real button, reachable by keyboard.
  const toggle = page.getByRole('button', { name: 'Create an account' });
  await expect(toggle).toBeVisible();
  await expect(page.locator('#password')).toHaveAttribute('autocomplete', 'current-password');
  await toggle.focus();
  await page.keyboard.press('Enter');
  await expect(page.getByRole('heading', { name: 'Create your account' })).toBeVisible();
  // Password managers offer to generate a password for new-password fields.
  await expect(page.locator('#password')).toHaveAttribute('autocomplete', 'new-password');
});

test('sign up lands on Home, sign out returns to the login page, sign in works', async ({ page }) => {
  const email = `auth-${crypto.randomUUID()}@example.com`;
  await page.goto('/login');
  await page.getByRole('button', { name: 'Create an account' }).click();
  await page.locator('#email').fill(email);
  await page.locator('#password').fill(PASSWORD);
  await page.locator('#submitBtn').click();
  await expect(page).toHaveURL(/\/(#home)?$/);
  await expect(page.getByRole('heading', { name: 'Now playing' })).toBeVisible();

  await page.goto('/#you/settings');
  await page.getByRole('button', { name: 'Sign out' }).click();
  await expect(page).toHaveURL(/\/login/);

  await page.locator('#email').fill(email);
  await page.locator('#password').fill('wrong-password-1');
  await page.locator('#submitBtn').click();
  await expect(page.locator('#errorMsg')).not.toBeEmpty();
  await page.locator('#password').fill(PASSWORD);
  await page.locator('#submitBtn').click();
  await expect(page.getByRole('heading', { name: 'Now playing' })).toBeVisible();
});

test('signed-out visitors are sent to the login page', async ({ page }) => {
  await page.goto('/#library');
  await expect(page).toHaveURL(/\/login#library$/);
});
