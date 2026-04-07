// Standalone Playwright config for visual/p2-bug14-shortcuts-overlay.spec.ts.
// Used by Phase 4 / Plan 03 to reproduce and verify the BUG #14 / UX-03
// keyboard shortcuts overlay fix.
//
// Port note: the canonical Phase 2 test server port is 18420. Sibling
// worktrees (gsd-phase3-exec and gsd-phase4-polish) and plans 04-01 / 04-02
// already claim ports 18420 / 18421 / 18422, so plan 04-03 takes 18423 to
// avoid colliding with parallel worktrees. This is the same Rule 3
// blocking-deviation pattern documented in plan 04-01 (chore(04-01): switch
// bug15 test config to port 18421 to avoid parallel worktree collision)
// and plan 04-02 (port 18422).
import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: '.',
  testMatch: 'visual/p2-bug14-shortcuts-overlay.spec.ts',
  timeout: 60000,
  retries: 0,
  use: {
    baseURL: 'http://127.0.0.1:18423',
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
