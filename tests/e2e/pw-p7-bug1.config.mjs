// Standalone Playwright config for WEB-P1-1 / Phase 7 bug 1 regression spec.
//
// Mirrors the pw-p0-bug3.config.mjs pattern. Manually-managed test server on
// 127.0.0.1:18420 (start with `setsid env -u TMUX -u TMUX_PANE -u TERM_PROGRAM
// AGENTDECK_PROFILE=_test ./build/agent-deck -p _test web --listen
// 127.0.0.1:18420 < /dev/null > /tmp/web.log 2>&1 &`).
import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './visual',
  testMatch: 'p7-bug1-terminal-fill.spec.ts',
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
