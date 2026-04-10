// Phase 10 / Plan 03: TEST-C functional E2E config.
//
// Runs session-lifecycle and group-crud specs against a test server on
// 127.0.0.1:18420. All API calls are intercepted via page.route() with
// fixture data from helpers/test-fixtures.ts, so no real tmux sessions
// are needed.
//
// Service workers are blocked so page.route() can intercept /api/* traffic.
//
// Test server (start manually):
//   env -u AGENTDECK_INSTANCE_ID -u TMUX -u TMUX_PANE -u TERM_PROGRAM \
//     AGENTDECK_PROFILE=_test ./build/agent-deck -p _test web \
//     --listen 127.0.0.1:18420 --token test > /tmp/p10-web.log 2>&1 &
import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: '.',
  testMatch: '{session-lifecycle,group-crud}.spec.ts',
  timeout: 30000,
  retries: 0,
  use: {
    baseURL: 'http://127.0.0.1:18420/?token=test',
    headless: true,
    viewport: { width: 1280, height: 800 },
    serviceWorkers: 'block',
  },
  projects: [
    {
      name: 'chromium-desktop',
      use: { browserName: 'chromium' },
    },
  ],
});
