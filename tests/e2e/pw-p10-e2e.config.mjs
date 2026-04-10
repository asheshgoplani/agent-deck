// Phase 10 / Plan 03: Combined TEST-C + TEST-D E2E config.
//
// Runs all functional E2E (session-lifecycle + group-crud) on desktop viewport
// AND all mobile E2E (mobile-e2e) at three device viewports in a single
// invocation. Four projects total.
//
// Service workers are blocked so page.route() can intercept /api/* traffic.
//
// Test server (start manually):
//   env -u AGENTDECK_INSTANCE_ID -u TMUX -u TMUX_PANE -u TERM_PROGRAM \
//     AGENTDECK_PROFILE=_test ./build/agent-deck -p _test web \
//     --listen 127.0.0.1:18420 --token test > /tmp/p10-web.log 2>&1 &
//
// Run: cd tests/e2e && npx playwright test --config=pw-p10-e2e.config.mjs
import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: '.',
  timeout: 30000,
  retries: 0,
  use: {
    baseURL: 'http://127.0.0.1:18420/?token=test',
    headless: true,
    serviceWorkers: 'block',
  },
  projects: [
    {
      name: 'chromium-desktop',
      testMatch: '{session-lifecycle,group-crud}.spec.ts',
      use: {
        browserName: 'chromium',
        viewport: { width: 1280, height: 800 },
      },
    },
    {
      name: 'iphone-se',
      testMatch: 'mobile-e2e.spec.ts',
      use: {
        browserName: 'chromium',
        viewport: { width: 375, height: 667 },
      },
    },
    {
      name: 'iphone-14',
      testMatch: 'mobile-e2e.spec.ts',
      use: {
        browserName: 'chromium',
        viewport: { width: 390, height: 844 },
      },
    },
    {
      name: 'ipad',
      testMatch: 'mobile-e2e.spec.ts',
      use: {
        browserName: 'chromium',
        viewport: { width: 768, height: 1024 },
      },
    },
  ],
});
