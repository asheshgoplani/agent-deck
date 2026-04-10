// Standalone Playwright config for Phase 6 / Plan 05 / WEB-P0-4 prevention layer
// (mutations gating) regression spec.
//
// Points at the manually-managed test server on port 18420 (mirrors
// pw-p6-bug4.config.mjs). 30 second timeout is sufficient; no auto-dismiss
// waits needed for this spec.
import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './visual',
  testMatch: 'p6-bug4-mutations-gating.spec.ts',
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
