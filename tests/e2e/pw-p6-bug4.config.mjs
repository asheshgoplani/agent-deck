// Standalone Playwright config for Phase 6 / Plan 04 / WEB-P0-4 + POL-7
// regression spec (toast cap, error retention, history drawer).
//
// Points at the manually-managed test server on port 18420 (mirrors
// pw-p0-bug3.config.mjs). 60 second timeout because some DOM tests need
// up to 6 seconds of waitForTimeout for auto-dismiss verification.
import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './visual',
  testMatch: 'p6-bug4-toast-cap.spec.ts',
  timeout: 60000,
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
