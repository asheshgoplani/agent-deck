// Standalone Playwright config for the Phase 8 plan 04 PERF-K regression.
//
// Structural + pre-flight-gate spec for the virtualized SessionList hook.
// Mirrors the pw-p8-perf-bundle pattern (port 18420, readFileSync-based
// assertions). The DOM smoke test in the spec tolerates missing test
// fixtures by skipping cleanly.

import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './visual',
  testMatch: 'p8-perf-k-virtualization.spec.ts',
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
