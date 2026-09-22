import { defineConfig, devices } from '@playwright/test';
import path from 'node:path';

// E2E for the cato PWA. webServer boots a disposable server (fresh SQLite DB)
// on port 7180 via scripts/e2e-server.sh — zero manual steps.
export default defineConfig({
  testDir: './tests',
  timeout: 30_000,
  expect: { timeout: 7_000 },
  fullyParallel: false, // shared server state (users/library) — keep order deterministic
  workers: 1,
  retries: 0, // A flaky release gate should fail visibly.
  reporter: 'list',
  outputDir: process.env.CATO_E2E_RESULTS || '/tmp/cato-e2e-results',
  use: {
    baseURL: 'http://127.0.0.1:7180',
    trace: 'retain-on-failure',
  },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
    { name: 'mobile-chromium', testMatch: /release\.spec\.ts/, use: { ...devices['Pixel 7'] } },
  ],
  webServer: {
    command: 'bash scripts/e2e-server.sh',
    cwd: path.join(__dirname, '..'),
    wait: { stdout: /CATO_E2E_READY/ },
    reuseExistingServer: false,
    gracefulShutdown: { signal: 'SIGTERM', timeout: 30_000 },
    timeout: 60_000,
  },
});
