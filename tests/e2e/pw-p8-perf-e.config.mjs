// Standalone Playwright config for PERF-E regression spec.
//
// Phase 8 Plan 02 / Task 1: structural regression for the TerminalPanel.js
// event-listener leak fix (single AbortController pattern). The tests are
// readFileSync-based so no running server is strictly required, but the
// config mirrors the pw-p6-bug2 pattern (port 18420) so it composes with the
// shared test server lifecycle if future tests in this spec exercise the DOM.

import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './visual',
  testMatch: 'p8-perf-e-listener-cleanup.spec.ts',
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
