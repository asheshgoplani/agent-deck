// Standalone Playwright config for WEB-P0-2 regression spec.
//
// Phase 6 Plan 01 / Task 1: regression spec for the Option B (read-only)
// ProfileDropdown fix. The default playwright.config.ts auto-spawns its own
// webServer which fails inside an agent-deck tmux session. This config points
// at a manually-managed test server on port 18420 (start with:
// `setsid env -u TMUX -u TMUX_PANE -u TERM_PROGRAM AGENTDECK_PROFILE=_test
// ./build/agent-deck -p _test web --listen 127.0.0.1:18420 < /dev/null >
// /tmp/web.log 2>&1 &`) and has no webServer block so nothing is spawned.
//
// Mirrors pw-p0-bug3.config.mjs structurally so it composes with the same
// test server lifecycle.

import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './visual',
  testMatch: 'p6-bug2-profile-switcher.spec.ts',
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
