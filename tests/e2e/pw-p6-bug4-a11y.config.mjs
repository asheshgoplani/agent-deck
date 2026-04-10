// Standalone Playwright config for Phase 6 / Plan 04 / WEB-P0-4 + POL-7
// a11y regression spec (axe-core, ARIA live regions, drawer dialog
// semantics, 44px touch targets).
//
// Points at the manually-managed test server on port 18420 (mirrors
// pw-p6-bug4.config.mjs).
import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './a11y',
  testMatch: 'p6-bug4-a11y.spec.ts',
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
