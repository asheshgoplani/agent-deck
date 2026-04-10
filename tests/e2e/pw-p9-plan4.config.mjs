// Phase 9 / Plan 04: POL-6 light theme audit Playwright config.
//
// This config forces the light theme via TWO mechanisms (defense in depth):
//   1. contextOptions.colorScheme: 'light' — OS-level preference that the
//      ThemeToggle 'system' default picks up on page load.
//   2. addInitScript in every beforeEach — sets
//      localStorage.setItem('theme', 'light') BEFORE the SPA bootstraps, so
//      the explicit user preference beats any system default.
//
// Service workers are blocked so page.route() can intercept /api/* traffic
// (matches the pw-p9-plan2.config.mjs pattern — the production PWA
// service worker otherwise handles fetch events in its own context).
//
// Manually-managed test server on 127.0.0.1:18420 (start via:
//   tmux new-session -d -s adeck-p9-plan4 'env -u AGENTDECK_INSTANCE_ID \
//     -u TMUX -u TMUX_PANE -u TERM_PROGRAM AGENTDECK_PROFILE=_test \
//     ./build/agent-deck -p _test web --listen 127.0.0.1:18420 \
//     --token test > /tmp/p9-plan4-web.log 2>&1'
// )
import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './visual',
  testMatch: /p9-pol6-.*\.spec\.ts$/,
  timeout: 45000,
  retries: 0,
  use: {
    baseURL: 'http://127.0.0.1:18420/?token=test',
    headless: true,
    viewport: { width: 1280, height: 800 },
    // OS-level color scheme forced to light (axe-core uses this to decide
    // which colors to sample when the app uses prefers-color-scheme media
    // queries).
    colorScheme: 'light',
    // Block service workers so page.route() can intercept /api/* requests.
    // Mirrors the pw-p9-plan2.config.mjs pattern.
    serviceWorkers: 'block',
  },
  projects: [
    {
      name: 'chromium-light',
      use: { browserName: 'chromium' },
    },
  ],
});
