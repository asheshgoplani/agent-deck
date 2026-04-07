// Standalone Playwright config for visual/p2-bug16-group-count.spec.ts.
// Used by Phase 4 / Plan 06 to reproduce and verify the BUG #16 / UX-05
// group count from rendered children fix.
//
// Connects to a manually-started agent-deck web server on port 18426 so the
// spec does not race the default playwright.config.ts webServer (port 19999)
// or other phase 4 standalone configs (port 18420). The user-supplied port
// 18426 is dedicated to this plan to avoid contention with concurrent plans
// in the same worktree.
import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: '.',
  testMatch: 'visual/p2-bug16-group-count.spec.ts',
  timeout: 60000,
  retries: 0,
  use: {
    baseURL: 'http://127.0.0.1:18426',
    headless: true,
    viewport: { width: 1280, height: 800 },
    extraHTTPHeaders: {
      Authorization: 'Bearer test',
    },
  },
  projects: [
    { name: 'chromium', use: { browserName: 'chromium' } },
  ],
  // No webServer block — server is started manually before this spec runs.
})
