// Phase 9 / Plan 02: Shared Playwright config for POL-3 + POL-5 regression specs.
//
// Two locale projects exercise the POL-5 Intl.NumberFormat contract:
//   - chromium-en-US renders `$1,234.56` (deterministic)
//   - chromium-de-DE renders `1.234,56 $` / `1.234,56 US$` / `1.234,56\u00a0$`
//     depending on ICU version (spec uses a loose regex)
//
// POL-3 behavior is locale-independent so both projects run the same DOM
// assertions (serves as a parallel-execution sanity check).
//
// Manually-managed test server on 127.0.0.1:18420 (start with:
//   env -u AGENTDECK_INSTANCE_ID -u AGENTDECK_PROFILE -u TMUX -u TMUX_PANE \
//       -u TERM_PROGRAM AGENTDECK_PROFILE=_test nohup ./build/agent-deck \
//       -p _test web --listen 127.0.0.1:18420 --token test \
//       < /dev/null > /tmp/web.log 2>&1 &
// )
import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './visual',
  testMatch: /p9-pol(3|5)-.*\.spec\.ts$/,
  timeout: 30000,
  retries: 0,
  use: {
    baseURL: 'http://127.0.0.1:18420/?token=test',
    headless: true,
    viewport: { width: 1280, height: 800 },
    // Block service workers so page.route() can intercept /api/* requests.
    // The production PWA service worker (static/sw.js) handles fetch events
    // from its own context, which Playwright cannot mock. Blocking SW
    // registration keeps all network traffic in the page context.
    serviceWorkers: 'block',
  },
  projects: [
    {
      name: 'chromium-en-US',
      use: { browserName: 'chromium', locale: 'en-US' },
    },
    {
      name: 'chromium-de-DE',
      use: { browserName: 'chromium', locale: 'de-DE' },
    },
  ],
});
