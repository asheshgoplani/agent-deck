// Standalone Playwright config for WEB-P1-3 / Phase 7 bug 3 regression spec.
//
// Points at a manually-managed test server on port 18420. The default
// playwright.config.ts auto-spawns its own webServer via `go run
// ../../cmd/agent-deck --web --port 19999`, which fails when the runner
// executes from inside an agent-deck tmux session. Mirrors
// pw-p0-bug3.config.mjs (no webServer block so nothing is spawned).

import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './visual',
  testMatch: 'p7-bug3-row-density.spec.ts',
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
