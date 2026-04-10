// Standalone Playwright config for Phase 9 Plan 03 (POL-7 regression guard).
// No web server boot required — this spec is pure readFileSync structural.
// Mirrored from pw-p7-bug4.config.mjs for consistency.
import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: './visual',
  testMatch: 'p9-pol7-regression-guard.spec.ts',
  timeout: 30000,
  retries: 0,
  use: {
    baseURL: 'http://127.0.0.1:18420/?token=test',
    headless: true,
    viewport: { width: 1280, height: 800 },
  },
  projects: [{ name: 'chromium', use: { browserName: 'chromium' } }],
})
