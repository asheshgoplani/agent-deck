// Standalone Playwright config for Phase 6 / Plan 03 / Task 3 a11y spec.
//
// Mirrors the pw-p6-bug2-a11y.config.mjs pattern — points at the manually
// managed test server on 127.0.0.1:18420 with no webServer block.
// Start the server with the same command as pw-p6-bug3.config.mjs.

import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './a11y',
  testMatch: 'p6-bug3-title-truncation-a11y.spec.ts',
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
