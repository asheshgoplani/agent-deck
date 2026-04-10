---
phase: 10-automated-testing
plan: 01
subsystem: testing
tags: [playwright, visual-regression, docker, github-actions, pixel-diff, snapshots]

# Dependency graph
requires:
  - phase: 09-polish
    provides: all v1.5.0 visual polish changes (POL-1..7) that are captured as baselines
  - phase: 07-web-p1-layout-bugs
    provides: P1 layout fixes (WEB-P1-1..5) captured as regression baselines
  - phase: 06-web-critical-p0-bugs
    provides: P0 bug fixes (WEB-P0-1..4) captured as regression baselines
provides:
  - Playwright visual regression test infrastructure (pw-visual-regression.config.ts)
  - Shared screenshot helpers (visual-helpers.ts) for deterministic captures
  - 5 hello-world baseline screenshots (main views: empty-state, sidebar-sessions, cost-dashboard, mobile-sidebar, settings-panel)
  - 13 per-bug regression baselines (4 P0 + 5 P1 + 4 Polish)
  - GitHub Actions CI workflow blocking PR merge on >0.1% pixel diff
  - Contributor documentation for updating baselines safely
affects:
  - Phase 11: release — visual regression gate must pass before v1.5.0 ships

# Tech tracking
tech-stack:
  added: [mcr.microsoft.com/playwright:v1.59.1-jammy (Docker image)]
  patterns:
    - Docker-only visual regression (pixel-identical font rendering via playwright:jammy image)
    - Deterministic screenshot prep (freezeClock before goto, mockEndpoints before goto, prepareForScreenshot after load)
    - Dynamic content masking via getDynamicContentMasks (timestamps, costs, connection status)
    - Scoped baseline update via -g filter (never blanket --update-snapshots)
    - force-add PNG baselines via git add -f (*.png in .git/info/exclude blocks all PNGs)

key-files:
  created:
    - tests/e2e/pw-visual-regression.config.ts
    - tests/e2e/visual-regression/visual-helpers.ts
    - tests/e2e/visual-regression/main-views.spec.ts
    - tests/e2e/visual-regression/p0-regressions.spec.ts
    - tests/e2e/visual-regression/p1-regressions.spec.ts
    - tests/e2e/visual-regression/polish-regressions.spec.ts
    - tests/e2e/visual-regression/__screenshots__/main-views.spec.ts/ (5 PNGs)
    - tests/e2e/visual-regression/__screenshots__/p0-regressions.spec.ts/ (4 PNGs)
    - tests/e2e/visual-regression/__screenshots__/p1-regressions.spec.ts/ (5 PNGs)
    - tests/e2e/visual-regression/__screenshots__/polish-regressions.spec.ts/ (4 PNGs)
    - .github/workflows/visual-regression.yml
    - tests/e2e/visual-regression/README.md
  modified: []

key-decisions:
  - "Docker image tag v1.59.1-jammy pinned to match @playwright/test version in package.json — must upgrade together"
  - "Correct hamburger selector is aria-label='Open sidebar' (dynamic: 'Close sidebar' when open) — NOT 'Toggle sidebar'"
  - "SettingsPanel is inside the info drawer — opened via aria-label='Open info panel', not a dedicated settings button"
  - "Baseline PNGs must be force-added via git add -f because .git/info/exclude contains *.png global rule"
  - "Docker --network=host connects container to host test server on 127.0.0.1:18420"
  - "toMatchSnapshot thresholds: maxDiffPixelRatio 0.001 + maxDiffPixels 200 + threshold 0.2 for sub-pixel tolerance"

patterns-established:
  - "Visual regression prep: freezeClock (before goto) + mockEndpoints (before goto) + prepareForScreenshot (after goto)"
  - "Dynamic content masking: getDynamicContentMasks() returns locators for timestamps, costs, connection status, version strings"
  - "Scoped baseline update: always use -g filter, never blanket --update-snapshots"
  - "Docker-only rule: baselines generated and tests run inside mcr.microsoft.com/playwright:v1.59.1-jammy only"

requirements-completed: [TEST-A]

# Metrics
duration: 12min
completed: 2026-04-10
---

# Phase 10 Plan 01: Visual Regression Testing Infrastructure Summary

**18-test Playwright visual regression suite with Docker-only pixel-diff baseline generation, per-bug regression coverage for all P0/P1/Polish fixes, and GitHub Actions CI gate blocking merge on >0.1% visual diff**

## Performance

- **Duration:** ~12 min
- **Started:** 2026-04-10T02:20:29Z
- **Completed:** 2026-04-10T02:32:00Z
- **Tasks:** 6
- **Files modified:** 14 (7 spec/config, 1 workflow, 1 README, 18 PNG baselines)

## Accomplishments

- Visual regression infrastructure created: Playwright config with strict toMatchSnapshot thresholds, shared helpers (killAnimations, freezeClock, getDynamicContentMasks, waitForStable, prepareForScreenshot, mockEndpoints), and fixture constants for deterministic API mocking
- 5 hello-world main-view baselines: empty-state, sidebar-sessions, cost-dashboard, mobile-sidebar, settings-panel (all at correct viewports and dark theme)
- 13 per-bug regression baselines: 4 P0 (hamburger, profile switcher, title truncation, toast cap), 5 P1 (terminal fill, fluid sidebar, row density, empty state grid, mobile overflow), 4 Polish (skeleton loading, skeleton-to-loaded, group density, light theme)
- GitHub Actions workflow: triggers on every PR to main, builds binary, starts test server, runs Playwright in Docker, uploads diff artifacts on failure, cleans up server
- Contributor documentation: Docker-only workflow, scoped -g update process, PR requirements for baseline changes, troubleshooting guide

## Task Commits

1. **Task 1: Visual regression infrastructure + main-view specs (RED)** - `2ea724c` (test)
2. **Task 2: Generate main-view baselines in Docker (GREEN)** - `39a32e3` (test)
3. **Task 3: Per-bug regression specs P0/P1/Polish (RED)** - `da72261` (test)
4. **Task 4: Generate per-bug baselines in Docker (GREEN)** - `ffc87e4` (test)
5. **Task 5: GitHub Actions CI workflow** - `9542443` (ci)
6. **Task 6: Baseline update documentation** - `37453f4` (docs)

## Files Created/Modified

- `tests/e2e/pw-visual-regression.config.ts` - Playwright config with toMatchSnapshot thresholds, Docker launch args
- `tests/e2e/visual-regression/visual-helpers.ts` - Shared helpers: killAnimations, freezeClock, getDynamicContentMasks, waitForStable, prepareForScreenshot, mockEndpoints + fixture constants
- `tests/e2e/visual-regression/main-views.spec.ts` - 5 hello-world baseline specs
- `tests/e2e/visual-regression/p0-regressions.spec.ts` - 4 P0 bug regression specs
- `tests/e2e/visual-regression/p1-regressions.spec.ts` - 5 P1 bug regression specs
- `tests/e2e/visual-regression/polish-regressions.spec.ts` - 4 Polish regression specs
- `tests/e2e/visual-regression/__screenshots__/` - 18 PNG baselines (5 main-views + 4 P0 + 5 P1 + 4 Polish)
- `.github/workflows/visual-regression.yml` - CI workflow
- `tests/e2e/visual-regression/README.md` - Contributor documentation

## Decisions Made

- **Docker-only baseline generation:** baselines generated inside `mcr.microsoft.com/playwright:v1.59.1-jammy` for deterministic font rendering across all environments (Linux, macOS, CI)
- **`--force-device-scale-factor=1` launch arg:** eliminates HiDPI pixel density variance across different CI runner hardware
- **Thresholds:** maxDiffPixelRatio 0.001 + maxDiffPixels 200 + threshold 0.2 — catches layout shifts and color changes while allowing sub-pixel anti-aliasing variance
- **`--network=host` Docker flag:** test server runs on CI host, Docker container reaches it via host network

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Corrected hamburger button selector**
- **Found during:** Task 2 (Docker baseline generation for mobile-sidebar test)
- **Issue:** Plan specified `aria-label="Toggle sidebar"` but the actual Topbar.js uses a dynamic label: `aria-label=${sidebarOpen ? 'Close sidebar' : 'Open sidebar'}`. The `waitFor` timed out.
- **Fix:** Changed selector to `aria-label="Open sidebar"` (the initial state when sidebar is closed on mobile)
- **Files modified:** tests/e2e/visual-regression/main-views.spec.ts, tests/e2e/visual-regression/p0-regressions.spec.ts
- **Verification:** Both mobile-sidebar and hamburger-clickable tests now pass in Docker
- **Committed in:** `39a32e3` (part of Task 2 baseline commit)

**2. [Rule 1 - Bug] Corrected settings panel opener selector**
- **Found during:** Task 2 (Docker baseline generation for settings-panel test)
- **Issue:** Plan specified `button[title="Settings"]` but the app has no dedicated settings button. The SettingsPanel component is always rendered inside the info drawer, opened via `button[aria-label="Open info panel"]`
- **Fix:** Changed selector to `button[aria-label="Open info panel"]`
- **Files modified:** tests/e2e/visual-regression/main-views.spec.ts
- **Verification:** settings-panel test passes in Docker, screenshot captures the info drawer with settings content
- **Committed in:** `39a32e3` (part of Task 2 baseline commit)

**3. [Rule 3 - Blocking] Force-add PNG baselines via git add -f**
- **Found during:** Task 2 (attempting to commit baseline PNGs)
- **Issue:** `.git/info/exclude` contains `*.png` global rule which prevents any PNG from being tracked by git. This blocks the baseline PNGs from being committed.
- **Fix:** Used `git add -f` to force-add the PNG files, same pattern documented in plan 09-03 STATE.md context
- **Files modified:** All 18 baseline PNGs
- **Verification:** `git log --stat` shows PNGs committed successfully
- **Committed in:** `39a32e3` and `ffc87e4`

---

**Total deviations:** 3 auto-fixed (2 wrong selector bugs, 1 blocking git exclude issue)
**Impact on plan:** All three fixes were necessary for the plan to complete. No scope creep. The selector fixes were due to incorrect assumptions in the plan about button labels that required reading the actual Topbar.js source.

## Issues Encountered

- TDD RED-then-GREEN sequence was collapsed: since Docker `--update-snapshots` was run during Task 2 for main-views while p0/p1/polish specs already existed from Task 3 work-in-progress, Docker wrote baselines for those specs too. The net result (18 passing tests with committed baselines) is correct per the acceptance criteria even though the strict RED-GREEN order was not perfectly preserved.

## User Setup Required

None - no external service configuration required. The CI workflow runs automatically on every PR.

## Next Phase Readiness

- TEST-A complete: visual regression gate is operational
- 18 baseline PNGs committed and all tests passing in Docker (18/18)
- CI workflow in `.github/workflows/visual-regression.yml` will block any PR that introduces >0.1% visual diff
- Phase 10 Plan 02 (TEST-B) can proceed

---
*Phase: 10-automated-testing*
*Completed: 2026-04-10*

## Self-Check: PASSED

All files and commits verified:
- 9 key files created (specs, config, workflow, README, SUMMARY)
- 18 PNG baselines committed (5 main-views + 4 P0 + 5 P1 + 4 Polish)
- 6 task commits all present (2ea724c, 39a32e3, da72261, ffc87e4, 9542443, 37453f4)
