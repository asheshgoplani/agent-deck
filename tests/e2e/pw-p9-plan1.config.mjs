// Standalone Playwright config for Phase 9 Plan 01 (POL-1 + POL-2 + POL-4).
// Mirrors the pw-p7-bug1.config.mjs pattern verbatim. Manually-managed
// test server on 127.0.0.1:18420 (start with `nohup script -qc 'env -u TMUX
// -u TMUX_PANE -u TERM_PROGRAM AGENTDECK_PROFILE=_test ./build/agent-deck
// -p _test web --listen 127.0.0.1:18420 --token test' /dev/null
// < /dev/null > /tmp/web.log 2>&1 & disown`).
import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: './visual',
  testMatch: /p9-pol(1|2|4)-.*\.spec\.ts$/,
  timeout: 30000,
  retries: 0,
  use: {
    baseURL: 'http://127.0.0.1:18420/?token=test',
    headless: true,
    viewport: { width: 1280, height: 800 },
  },
  projects: [{ name: 'chromium', use: { browserName: 'chromium' } }],
})
