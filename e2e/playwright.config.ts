import { defineConfig, devices } from '@playwright/test';
import path from 'node:path';

// E2E for the cato PWA. webServer boots a disposable server (fresh SQLite DB)
// on port 7180 via scripts/e2e-server.sh — zero manual steps.
export default defineConfig({
  testDir: './tests',
  timeout: 30_000,
  expect: { timeout: 7_000 },
  fullyParallel: false, // shared server state (users/library) — keep order deterministic
  retries: process.env.CI ? 2 : 0,
  reporter: 'list',
  use: {
    baseURL: 'http://127.0.0.1:7180',
    trace: 'retain-on-failure',
  },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
  ],
  webServer: {
    command: 'bash scripts/e2e-server.sh',
    cwd: path.join(__dirname, '..'),
    url: 'http://127.0.0.1:7180/healthz',
    reuseExistingServer: !process.env.CI,
    timeout: 60_000,
  },
});
