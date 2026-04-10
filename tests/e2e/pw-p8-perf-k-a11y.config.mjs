// Standalone Playwright config for the Phase 8 plan 04 PERF-K a11y spec.
//
// Runs against a live server at :18420; tests skip cleanly when no list
// is rendered (empty state) or when the test server does not expose a
// 200-session mock fixture. The structural regression is covered by
// pw-p8-perf-k.config.mjs; this config only runs the runtime a11y gates.

import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './a11y',
  testMatch: 'p8-perf-k-virtualization-a11y.spec.ts',
  timeout: 30000,
  retries: 0,
  use: {
    baseURL: 'http://127.0.0.1:18420',
    headless: true,
    viewport: { width: 1280, height: 800 },
  },
  projects: [
    { name: 'chromium', use: { browserName: 'chromium' } },
  ],
});
