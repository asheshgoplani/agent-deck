# Visual Regression Tests

Pixel-level screenshot comparison tests that detect unintended visual
changes to the Agent Deck web app. Every PR is blocked from merging
if any screenshot differs from its committed baseline by more than
0.1% of pixels (maxDiffPixelRatio: 0.001) or 200 absolute pixels.

## Prerequisites

- Docker (for deterministic font rendering)
- Go 1.24.0 (for building the test server binary)
- Node.js + npm (for Playwright)

## Running Tests

Tests MUST be run inside Docker to produce pixel-identical results.
Running outside Docker will produce false positives due to OS-level
font rendering differences.

```bash
# 1. Build the binary
make build

# 2. Start the test server
tmux new-session -d -s adeck-visual-test \
  'env -u AGENTDECK_INSTANCE_ID -u TMUX -u TMUX_PANE -u TERM_PROGRAM \
  AGENTDECK_PROFILE=_test \
  ./build/agent-deck -p _test web --listen 127.0.0.1:18420 --token test'

# Wait for server readiness
for i in $(seq 1 15); do
  curl -sf http://127.0.0.1:18420/healthz && break
  sleep 1
done

# 3. Run tests in Docker
docker run --rm --network=host \
  -v $(pwd)/tests/e2e:/work \
  -w /work \
  mcr.microsoft.com/playwright:v1.59.1-jammy \
  npx playwright test --config=pw-visual-regression.config.ts
```

## Updating Baselines

When you make an intentional visual change (new component, layout
change, theme update), you must regenerate the affected baselines.

**CRITICAL: ALWAYS use the `-g` filter to scope updates.**

```bash
# Good: update only the affected spec
docker run --rm --network=host \
  -v $(pwd)/tests/e2e:/work \
  -w /work \
  mcr.microsoft.com/playwright:v1.59.1-jammy \
  npx playwright test --config=pw-visual-regression.config.ts \
    -g "empty state" --update-snapshots

# Another example: update only P0 regression baselines
docker run --rm --network=host \
  -v $(pwd)/tests/e2e:/work \
  -w /work \
  mcr.microsoft.com/playwright:v1.59.1-jammy \
  npx playwright test --config=pw-visual-regression.config.ts \
    -g "P0 bug" --update-snapshots
```

**DANGEROUS: NEVER do this without a `-g` filter.**

Running `--update-snapshots` without `-g` regenerates ALL baselines
at once. If any test is currently capturing a regression (a broken
state), the baseline will be updated to accept the regression as the
new "correct" state. Future runs will then silently pass even though
the UI is broken.

```bash
# NEVER run this without -g:
# npx playwright test --config=pw-visual-regression.config.ts --update-snapshots
```

After updating specific baselines, verify the full suite still passes:

```bash
docker run --rm --network=host \
  -v $(pwd)/tests/e2e:/work \
  -w /work \
  mcr.microsoft.com/playwright:v1.59.1-jammy \
  npx playwright test --config=pw-visual-regression.config.ts
```

**Important: Baseline PNGs must be force-added to git** because the
repository's `.git/info/exclude` contains a `*.png` rule. Use:

```bash
git add -f tests/e2e/visual-regression/__screenshots__/
```

## PR Requirements for Baseline Changes

When your PR updates baseline screenshots, you MUST:

1. Describe which baselines changed and why in the PR description
2. Include before/after comparison (the CI diff artifacts help)
3. Confirm the visual change is intentional

Reviewers will look for unexplained baseline changes as a signal that
a PR may have introduced a visual regression that was silently accepted.

## Adding New Tests

1. Create a new `.spec.ts` file in `tests/e2e/visual-regression/`
2. Import helpers from `./visual-helpers.js`
3. Call `freezeClock(page)` before `page.goto()`
4. Call `mockEndpoints(page)` before `page.goto()`
5. Call `prepareForScreenshot(page)` after page load
6. Call `getDynamicContentMasks(page)` for masking dynamic content
7. Call `expect(page).toHaveScreenshot('name.png', { mask })`
8. Generate the baseline in Docker with `-g "your test name" --update-snapshots`
9. Force-add the new baseline PNG via `git add -f`
10. Commit both the spec and the baseline in separate commits

### Example test

```typescript
import { test, expect } from '@playwright/test';
import {
  freezeClock, mockEndpoints, prepareForScreenshot,
  getDynamicContentMasks,
} from './visual-helpers.js';

test.describe('My feature baselines', () => {
  test('my feature at 1280x800', async ({ page }) => {
    await freezeClock(page);
    await mockEndpoints(page);
    await page.goto('/?token=test');
    await prepareForScreenshot(page);
    const masks = await getDynamicContentMasks(page);
    await expect(page).toHaveScreenshot('my-feature-1280x800.png', { mask: masks });
  });
});
```

## Thresholds

| Parameter | Value | Rationale |
|-----------|-------|-----------|
| maxDiffPixelRatio | 0.001 | 0.1% of pixels; catches layout shifts while allowing sub-pixel anti-aliasing variance |
| maxDiffPixels | 200 | Absolute cap; even on a 1920x1080 screenshot (2M pixels), 200 changed pixels is a meaningful regression |
| threshold | 0.2 | Per-pixel color distance; 0.2 on [0,1] scale allows minor sub-pixel color shifts |

These thresholds were calibrated so that:
- Sub-pixel font anti-aliasing differences do NOT trigger failures
- A single misplaced element (like a button shifting 1px) DOES trigger failures
- Color contrast changes (like a text color change) DO trigger failures

## CI Workflow

The `.github/workflows/visual-regression.yml` workflow:

1. Builds the Go binary (`make build` with `GOTOOLCHAIN=go1.24.0`)
2. Starts the test server on `127.0.0.1:18420`
3. Runs Playwright inside `mcr.microsoft.com/playwright:v1.59.1-jammy` Docker container
4. On failure: uploads `test-results/` as an artifact containing actual/expected/diff PNGs
5. Blocks PR merge until all tests pass

The Docker image tag `v1.59.1-jammy` is pinned to match the `@playwright/test` version
in `tests/e2e/package.json`. Do not upgrade one without the other.

## Baseline Directory Structure

```
visual-regression/__screenshots__/
  main-views.spec.ts/
    empty-state-dark-1280x800.png
    sidebar-sessions-dark-1280x800.png
    cost-dashboard-dark-1280x800.png
    mobile-sidebar-dark-375x812.png
    settings-panel-dark-1280x800.png
  p0-regressions.spec.ts/
    hamburger-clickable-375x667.png
    profile-switcher-readonly-1280x800.png
    title-no-truncation-1280x800.png
    toast-cap-3-1280x800.png
  p1-regressions.spec.ts/
    terminal-fill-1280x800.png
    fluid-sidebar-1920x1080.png
    row-density-40px-1280x800.png
    empty-state-card-grid-1920x1080.png
    mobile-overflow-menu-375x667.png
  polish-regressions.spec.ts/
    skeleton-loading-1280x800.png
    skeleton-to-loaded-1280x800.png
    group-density-tight-1280x800.png
    light-theme-sidebar-1280x800.png
```

## Troubleshooting

**Tests pass locally but fail in CI (or vice versa):**
You are probably running outside Docker. Font rendering differs between
macOS, Ubuntu host, and the Docker container. Always use Docker.

**False positive on a specific element:**
Add the element's selector to `getDynamicContentMasks()` in
`visual-helpers.ts` so it gets masked in all screenshots.

**Skeleton test is flaky:**
The skeleton-loading test deliberately delays the `/api/menu` response.
If the app renders faster than expected, the skeleton may not be visible.
Increase the route delay or add explicit skeleton element wait.

**Baseline PNGs not tracked by git:**
The `.git/info/exclude` file contains `*.png` which blocks PNGs from
git tracking. Use `git add -f` to force-add baseline PNGs. See the
"Updating Baselines" and "Adding New Tests" sections above.

**Docker image not available:**
Pull the image manually before running tests:
```bash
docker pull mcr.microsoft.com/playwright:v1.59.1-jammy
```
