// Standalone Playwright config for the Phase 8 plan 03 PERF bundle regression.
//
// Structural spec covering the six PERF requirements shipped together
// (PERF-B Chart.js defer, PERF-C addon-canvas delete, PERF-D WebGL preload,
// PERF-F useDebounced, PERF-G SessionRow/GroupRow memo + local state,
// PERF-I POST /api/costs/batch). Mirrors the pw-p8-perf-e pattern.

import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './visual',
  testMatch: 'p8-perf-bundle.spec.ts',
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
