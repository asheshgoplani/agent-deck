// playwright.config.js -- e2e + screenshot regression for agent-deck web UI.
//
// Boots the agent-deck binary in `web` mode against a temporary state dir
// (see helpers/server.js), then runs e2e tests that exercise the full HTTP +
// WebSocket surface plus screenshot every major view.
//
// Screenshots live in tests/web/screenshots/ and are the visual contract:
// any pixel diff fails the build until the baseline is intentionally
// regenerated with `npm run test:e2e:update-snapshots`.

import { defineConfig, devices } from '@playwright/test'

const PORT = process.env.AGENT_DECK_WEB_PORT || '38291'
const baseURL = `http://127.0.0.1:${PORT}`

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: 1,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : [['list']],
  outputDir: './e2e-output',
  snapshotDir: './screenshots',
  snapshotPathTemplate:
    '{snapshotDir}/{testFilePath}/{arg}-{projectName}{ext}',
  expect: {
    toHaveScreenshot: {
      maxDiffPixelRatio: 0.01,
      animations: 'disabled',
      caret: 'hide',
    },
  },
  use: {
    baseURL,
    trace: 'retain-on-failure',
    video: 'retain-on-failure',
    screenshot: 'only-on-failure',
    actionTimeout: 5000,
    navigationTimeout: 10000,
    viewport: { width: 1280, height: 800 },
    colorScheme: 'dark',
    extraHTTPHeaders: {
      // Localhost without auth-disabled needs the dev token. helpers/server.js
      // configures the server with auth disabled for tests; this header is a
      // belt-and-braces fallback for any auth-enabled run.
      'X-Agent-Deck-Test': '1',
    },
  },
  // Tablet and phone projects use chromium with overridden viewports so we
  // don't need to install webkit just for layout coverage. This matches the
  // production reality: the agent-deck web UI is served as static assets and
  // any modern browser engine renders them; what matters for visual
  // regression is the viewport, not the engine. PR-B's responsive redesign
  // will lean on these projects to pin the breakpoints documented in the
  // chat transcript (mobile <720px → bottom tab bar; tablet ~820px).
  projects: [
    {
      name: 'chromium-desktop',
      use: { ...devices['Desktop Chrome'], viewport: { width: 1280, height: 800 } },
    },
    {
      name: 'chromium-tablet',
      use: { ...devices['Desktop Chrome'], viewport: { width: 820, height: 1180 } },
    },
    {
      name: 'chromium-phone',
      use: { ...devices['Desktop Chrome'], viewport: { width: 393, height: 851 } },
    },
  ],
  // The web server is booted by helpers/server.js inside the global setup,
  // so no `webServer:` block here.
  globalSetup: './helpers/global-setup.js',
  globalTeardown: './helpers/global-teardown.js',
})
