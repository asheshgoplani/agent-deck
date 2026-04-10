// Phase 10 / Plan 01: Visual regression Playwright config.
//
// Uses toMatchSnapshot with strict thresholds for pixel-level comparison
// against committed baselines. ALL baselines must be generated and tests
// must be run inside Docker (mcr.microsoft.com/playwright:v1.59.1-jammy)
// for deterministic font rendering.
//
// Launch args include --force-device-scale-factor=1 to eliminate HiDPI
// variance across different CI runners and developer machines.
//
// Test server on 127.0.0.1:18420 (start via:
//   env -u AGENTDECK_INSTANCE_ID -u TMUX -u TMUX_PANE -u TERM_PROGRAM \
//     AGENTDECK_PROFILE=_test ./build/agent-deck -p _test web \
//     --listen 127.0.0.1:18420 --token test
// )
import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './visual-regression',
  snapshotDir: './visual-regression/__screenshots__',
  snapshotPathTemplate: '{snapshotDir}/{testFileDir}/{testFileName}/{arg}{ext}',
  timeout: 60000,
  retries: 0,
  expect: {
    toMatchSnapshot: {
      maxDiffPixelRatio: 0.001,
      maxDiffPixels: 200,
      threshold: 0.2,
    },
  },
  use: {
    baseURL: 'http://127.0.0.1:18420/?token=test',
    headless: true,
    viewport: { width: 1280, height: 800 },
    colorScheme: 'dark',
    serviceWorkers: 'block',
    launchOptions: {
      args: [
        '--force-device-scale-factor=1',
        '--font-render-hinting=none',
        '--disable-font-subpixel-positioning',
      ],
    },
  },
  projects: [
    {
      name: 'chromium-visual',
      use: { browserName: 'chromium' },
    },
  ],
});
