// Standalone Playwright config for WEB-P0-2 a11y regression spec.
//
// Phase 6 Plan 01 / Task 3: axe-core + keyboard Tab + 44px touch target
// verification for the Option B ProfileDropdown fix (see
// internal/web/static/app/ProfileDropdown.js). Points at the same manually
// managed test server as pw-p6-bug2.config.mjs so the runner does not spawn
// a nested agent-deck instance.

import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './a11y',
  testMatch: 'p6-bug2-profile-switcher-a11y.spec.ts',
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
