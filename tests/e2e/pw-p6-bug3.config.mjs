// Standalone Playwright config for Phase 6 / Plan 03 / WEB-P0-3 regression spec.
//
// The project default playwright.config.ts auto-spawns its own webServer via
// `go run ../../cmd/agent-deck --web --port 19999`, which fails with a nested
// session error when the test runner is executed from inside an agent-deck
// tmux session. This config points at a manually-managed server on port 18420
// (start with: `setsid env -u TMUX -u TMUX_PANE -u TERM_PROGRAM
// AGENTDECK_PROFILE=_test ./build/agent-deck -p _test web --listen
// 127.0.0.1:18420 < /dev/null > /tmp/web.log 2>&1 &`) and has no webServer
// block so nothing is spawned.
//
// Mirrors the pw-p0-bug3.config.mjs / pw-p6-bug2.config.mjs pattern.

import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './visual',
  testMatch: 'p6-bug3-title-truncation.spec.ts',
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
