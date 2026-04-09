// Standalone Playwright config for Phase 6 / Plan 05 / WEB-P0-4 prevention
// layer a11y spec. Mirrors pw-p6-bug4-a11y.config.mjs but targets the
// mutations-gating a11y file.
import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './a11y',
  testMatch: 'p6-bug4-mutations-gating-a11y.spec.ts',
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
