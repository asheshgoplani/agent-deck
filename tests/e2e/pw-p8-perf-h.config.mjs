// Standalone Playwright config for the Phase 8 plan 05 PERF-H bundling spec.
//
// Structural gates are readFileSync-based (no server required). The byte
// budget gate fetches assets from a live server at :18420 and skips
// cleanly if the server is not running. Go unit tests for LoadAssets /
// ResolveAsset / SubstitutePlaceholders live at internal/web/assets_test.go.

import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './visual',
  testMatch: 'p8-perf-h-bundle.spec.ts',
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
