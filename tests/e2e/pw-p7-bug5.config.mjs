// Standalone Playwright config for WEB-P1-5 / Phase 7 bug 5 regression spec.
// Default viewport is iPhone SE 375x667 because the overflow menu is a mobile-only
// concern. Specific tests override the viewport for the desktop no-regression check.
//
// Mirrors pw-p6-bug1.config.mjs and pw-p0-bug3.config.mjs: points at a
// manually-managed test server on 127.0.0.1:18420 (no webServer block, so
// nothing is spawned on test start).
//
// Start the test server (once per dev loop) with:
//   setsid env -u TMUX -u TMUX_PANE -u TERM_PROGRAM AGENTDECK_PROFILE=_test \
//     ./build/agent-deck -p _test web --listen 127.0.0.1:18420 \
//     < /dev/null > /tmp/web.log 2>&1 &

import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './visual',
  testMatch: 'p7-bug5-mobile-overflow-menu.spec.ts',
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
