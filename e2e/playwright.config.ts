import { defineConfig, devices } from '@playwright/test';
import path from 'node:path';
import { E2E_HOST, E2E_PORT, spki, tlsDir } from './tls';

// Chromium runs against the non-loopback name cato.test (so loopback-only
// secure-context exemptions can't hide HTTPS bugs) with the dev certificate
// pinned by SPKI. WebKit has no equivalent switch; ignoreHTTPSErrors on
// https://127.0.0.1 still yields a secure context and a working service
// worker there. See e2e/tls.ts.
const chromiumHTTPS = {
  baseURL: `https://${E2E_HOST}:${E2E_PORT}`,
  ignoreHTTPSErrors: true, // Node-side request contexts only; the browser trusts the pin.
  launchOptions: {
    args: [
      `--host-resolver-rules=MAP ${E2E_HOST} 127.0.0.1`,
      `--ignore-certificate-errors-spki-list=${spki}`,
    ],
  },
};
const webkitHTTPS = { baseURL: `https://127.0.0.1:${E2E_PORT}`, ignoreHTTPSErrors: true };

// E2E for the cato PWA. webServer boots a disposable HTTPS server (fresh
// SQLite DB) on port 7180 via scripts/e2e-server.sh — zero manual steps.
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
    trace: 'retain-on-failure',
  },
  // Visual baselines are per platform (fonts render differently); a
  // platform without baselines records them instead of failing.
  snapshotPathTemplate: '{testDir}/__screenshots__/{testFilePath}/{arg}-{projectName}-{platform}{ext}',
  updateSnapshots: 'missing',
  projects: [
    // Desktop Chromium runs everything.
    { name: 'chromium', testIgnore: /gestures\.spec\.ts/, use: { ...devices['Desktop Chrome'], ...chromiumHTTPS } },
    // Offline SW emulation, CDP performance budgets and visual baselines
    // are Chromium-only; see e2e/README.md.
    { name: 'webkit', testIgnore: /(offline|perf|visual|gestures)\.spec\.ts/, use: { ...devices['Desktop Safari'], ...webkitHTTPS } },
    // Firefox, like WebKit, opens https://127.0.0.1 with ignoreHTTPSErrors
    // (a secure context, and its service worker registers there).
    { name: 'firefox', testIgnore: /(offline|perf|visual|gestures)\.spec\.ts/, use: { ...devices['Desktop Firefox'], ...webkitHTTPS } },
    { name: 'mobile-chromium', testMatch: /(journeys|home|game|offline|regressions|a11y|pwa|https|perf|visual|gestures)\.spec\.ts/, use: { ...devices['Pixel 7'], ...chromiumHTTPS } },
    { name: 'mobile-webkit', testMatch: /(journeys|home|game|regressions|a11y|pwa|https)\.spec\.ts/, use: { ...devices['iPhone 13'], ...webkitHTTPS } },
  ],
  webServer: {
    command: 'bash scripts/e2e-server.sh',
    cwd: path.join(__dirname, '..'),
    env: { E2E_TLS_DIR: tlsDir, E2E_ADDR: `127.0.0.1:${E2E_PORT}` },
    wait: { stdout: /CATO_E2E_READY/ },
    reuseExistingServer: false,
    gracefulShutdown: { signal: 'SIGTERM', timeout: 30_000 },
    timeout: 60_000,
  },
});
