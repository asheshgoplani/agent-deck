// Standalone Playwright config for WEB-P0-1 (Phase 6 Plan 02) regression spec.
//
// Mirrors pw-p6-bug2.config.mjs and pw-p0-bug3.config.mjs: points at a
// manually-managed test server on 127.0.0.1:18420 (no webServer block, so
// nothing is spawned on test start). Mobile viewport (375x667) because the
// hamburger is only rendered at lg:hidden (<1024px) and WEB-P0-1 specifically
// targets viewports <=768px.
//
// Start the test server (once per dev loop) with:
//   setsid env -u TMUX -u TMUX_PANE -u TERM_PROGRAM AGENTDECK_PROFILE=_test \
//     ./build/agent-deck -p _test web --listen 127.0.0.1:18420 \
//     < /dev/null > /tmp/web.log 2>&1 &

import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './visual',
  testMatch: 'p6-bug1-hamburger.spec.ts',
  timeout: 30000,
  retries: 0,
  use: {
    baseURL: 'http://127.0.0.1:18420',
    headless: true,
    viewport: { width: 375, height: 667 },
  },
  projects: [
    { name: 'chromium', use: { browserName: 'chromium' } },
  ],
});
