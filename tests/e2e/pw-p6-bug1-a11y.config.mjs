// Standalone Playwright config for WEB-P0-1 a11y regression spec.
// Mirrors pw-p6-bug2-a11y.config.mjs and pw-p6-bug1.config.mjs.
// Mobile viewport (375x667) because the hamburger is only rendered at
// lg:hidden (<1024px).

import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './a11y',
  testMatch: 'p6-bug1-hamburger-a11y.spec.ts',
  timeout: 30000,
  retries: 0,
  use: {
    baseURL: 'http://127.0.0.1:18420',
    headless: true,
    viewport: { width: 375, height: 667 },
  },
  projects: [
    { name: 'chromium', use: { browserName: 'chromium' } },
  ],
});
