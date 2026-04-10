// Phase 10 / Plan 03: TEST-D mobile E2E config.
//
// Runs mobile-e2e.spec.ts at three viewports matching real-world devices:
//   - iPhone SE (375x667): smallest supported, tests overflow menu + tight layout
//   - iPhone 14 (390x844): mid-range phone, tests standard mobile flow
//   - iPad (768x1024): tablet breakpoint, tests sidebar behavior at md breakpoint
//
// All tests use page.route() with fixture data from helpers/test-fixtures.ts.
// Service workers are blocked so page.route() can intercept /api/* traffic.
//
// Test server (start manually):
//   env -u AGENTDECK_INSTANCE_ID -u TMUX -u TMUX_PANE -u TERM_PROGRAM \
//     AGENTDECK_PROFILE=_test ./build/agent-deck -p _test web \
//     --listen 127.0.0.1:18420 --token test > /tmp/p10-web.log 2>&1 &
import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: '.',
  testMatch: 'mobile-e2e.spec.ts',
  timeout: 30000,
  retries: 0,
  use: {
    baseURL: 'http://127.0.0.1:18420/?token=test',
    headless: true,
    serviceWorkers: 'block',
  },
  projects: [
    {
      name: 'iphone-se',
      use: {
        browserName: 'chromium',
        viewport: { width: 375, height: 667 },
      },
    },
    {
      name: 'iphone-14',
      use: {
        browserName: 'chromium',
        viewport: { width: 390, height: 844 },
      },
    },
    {
      name: 'ipad',
      use: {
        browserName: 'chromium',
        viewport: { width: 768, height: 1024 },
      },
    },
  ],
});
