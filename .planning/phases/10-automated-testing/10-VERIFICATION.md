---
phase: 10-automated-testing
verified: 2026-04-10T06:30:00Z
status: gaps_found
score: 4/5 success criteria verified
re_verification: null
gaps:
  - truth: "Functional E2E covers session lifecycle (create → attach → send input → verify output → stop → delete) and group CRUD (create → add session → reorder → delete)"
    status: partial
    reason: "The implementation covers create/select/stop/delete for sessions and create/rename/delete for groups, but does not cover 'send input to terminal and verify output' (session) or 'add session to group' and 'reorder' (group CRUD). The PLAN intentionally downscoped from REQUIREMENTS.md and the ROADMAP success criteria; the implementation matches the PLAN scope but does not satisfy the full TEST-C requirement text."
    artifacts:
      - path: "tests/e2e/session-lifecycle.spec.ts"
        issue: "Tests verify terminal panel area is visible but do not send keyboard input to the terminal or verify terminal output content"
      - path: "tests/e2e/group-crud.spec.ts"
        issue: "Tests cover create/rename/delete but do not cover 'add session to group' (moving a session into a group) or session reordering within a group"
    missing:
      - "session-lifecycle.spec.ts: test that types input into terminal and verifies it appears in terminal output (or documents why this is deferred)"
      - "group-crud.spec.ts: test that moves/adds a session into a group and verifies the session appears under that group"
      - "group-crud.spec.ts: test or explicit documentation that 'reorder' is deferred (web UI has no drag-to-reorder per v1.5.0 out-of-scope decisions, so this may be intentionally out of scope)"
human_verification:
  - test: "Run the visual regression suite in Docker against the current codebase"
    expected: "All 18 tests pass against committed baselines (0 pixel diff failures)"
    why_human: "Cannot run Docker-in-Docker to verify Playwright visual regression; requires a host with Docker and a running test server outside agent-deck"
  - test: "Run Lighthouse CI via ./tests/lighthouse/budget-check.sh from a plain terminal"
    expected: "total-byte-weight assertion passes (<180KB), script size assertion passes (<120KB), FCP/LCP/TBT surface as warnings if exceeded"
    why_human: "Test server cannot start inside agent-deck session due to nested-session detection; requires a plain terminal outside agent-deck"
  - test: "Run functional E2E: cd tests/e2e && npx playwright test --config=pw-p10-e2e.config.mjs"
    expected: "29 tests pass, 1 correctly skips (iPad hamburger test on large viewport)"
    why_human: "Requires a running test server on 127.0.0.1:18420 outside agent-deck"
---

# Phase 10: Automated Testing — Verification Report

**Phase Goal:** Lock in the gains from Phases 6-9 so they cannot silently regress. Visual regression with committed baselines blocks merge on >0.1% diff. Lighthouse CI enforces perf budgets. Functional E2E covers session lifecycle + group CRUD. Mobile E2E covers 3 viewports. TEST-E scoped DOWN to alert-only (no auto-fix).
**Verified:** 2026-04-10T06:30:00Z
**Status:** GAPS FOUND (4/5 success criteria verified; 1 partial)
**Re-verification:** No — initial verification

---

## Goal Achievement

### Observable Truths (from ROADMAP Success Criteria)

| # | Truth | Status | Evidence |
|---|-------|--------|----------|
| 1 | CI blocks merge on any PR with >0.1% visual diff against committed baselines — baselines captured in Docker | VERIFIED | `visual-regression.yml` triggers on `pull_request` to main, runs tests in `mcr.microsoft.com/playwright:v1.59.1-jammy`, config has `maxDiffPixelRatio: 0.001`, 18 PNGs committed and git-tracked |
| 2 | Lighthouse CI runs on every PR with `numberOfRuns: 5`, byte-weight HARD gates, timing soft warnings | VERIFIED | `.lighthouserc.json` has `numberOfRuns: 5`, `total-byte-weight` and `resource-summary:script:size` at `error` level, FCP/LCP/TBT at `warn` level; `lighthouse-ci.yml` uses `treosh/lighthouse-ci-action@v12` |
| 3 | Functional E2E covering session lifecycle (create → attach → send input → verify output → stop → delete) and group CRUD (create → add session → reorder → delete) | PARTIAL | session-lifecycle.spec.ts has create/select/stop/delete (5 tests); group-crud.spec.ts has create/rename/delete (4 tests). Missing: terminal input/output verification, add-session-to-group, and reorder |
| 4 | Mobile E2E at 3 viewports (iPhone SE 375x667, iPhone 14 390x844, iPad 768x1024) | VERIFIED | `pw-p10-mobile.config.mjs` and `pw-p10-e2e.config.mjs` configure all 3 viewports as separate Playwright projects; `mobile-e2e.spec.ts` covers hamburger, overflow menu, sidebar auto-close, terminal visibility, form input, no overflow |
| 5 | Weekly regression workflow runs visual + Lighthouse, creates issue on failure, does NOT auto-fix | VERIFIED | `weekly-regression.yml`: cron `0 0 * * 0` + `workflow_dispatch`, `continue-on-error: true` on both test steps, `actions/github-script@v7` creates/updates issue with idempotency check, no auto-fix or baseline-update steps present |

**Score:** 4/5 truths verified (1 partial)

---

## Required Artifacts

| Artifact | Expected | Status | Details |
|----------|----------|--------|---------|
| `tests/e2e/pw-visual-regression.config.ts` | Visual regression Playwright config | VERIFIED | Exists, `maxDiffPixelRatio: 0.001`, `maxDiffPixels: 200`, `threshold: 0.2`, Docker launch args, `snapshotDir: './visual-regression/__screenshots__'` |
| `tests/e2e/visual-regression/visual-helpers.ts` | Shared helpers for deterministic screenshots | VERIFIED | Exports `killAnimations`, `freezeClock`, `getDynamicContentMasks`, `waitForStable`, `prepareForScreenshot`, `mockEndpoints` + fixture constants |
| `tests/e2e/visual-regression/main-views.spec.ts` | 5 main-view baseline specs | VERIFIED | 5 tests: empty-state, sidebar-sessions, cost-dashboard, mobile-sidebar, settings-panel |
| `tests/e2e/visual-regression/p0-regressions.spec.ts` | 4 P0 bug regression specs | VERIFIED | 4 tests: hamburger-clickable, profile-switcher-readonly, title-no-truncation, toast-cap-3 |
| `tests/e2e/visual-regression/p1-regressions.spec.ts` | 5 P1 bug regression specs | VERIFIED | 5 tests: terminal-fill, fluid-sidebar, row-density-40px, empty-state-card-grid, mobile-overflow-menu |
| `tests/e2e/visual-regression/polish-regressions.spec.ts` | 4 Polish regression specs | VERIFIED | 4 tests: skeleton-loading, skeleton-to-loaded, group-density-tight, light-theme-sidebar |
| `tests/e2e/visual-regression/__screenshots__/` | 18 committed PNG baselines | VERIFIED | 18 PNGs committed and git-tracked (5 main-views + 4 P0 + 5 P1 + 4 Polish) |
| `.github/workflows/visual-regression.yml` | CI workflow blocking PR merge on pixel diff | VERIFIED | Triggers on `pull_request` to main, pulls Docker image, runs tests in container, uploads diffs on failure |
| `.lighthouserc.json` | Lighthouse CI config with hard/soft tiers | VERIFIED | `numberOfRuns: 5`, `total-byte-weight` + `resource-summary:script:size` as `error`, FCP/LCP/TBT/speed-index as `warn`, CLS as `error`, `temporary-public-storage` upload |
| `.github/workflows/lighthouse-ci.yml` | Lighthouse CI PR gate | VERIFIED | Triggers on `internal/web/**` + `.lighthouserc.json` changes, `treosh/lighthouse-ci-action@v12`, `GOTOOLCHAIN=go1.24.0` pinned |
| `tests/lighthouse/budget-check.sh` | Local Lighthouse budget verification | VERIFIED | Executable, server lifecycle, healthz wait, pre-warm, `lhci collect+assert`, trap cleanup |
| `tests/lighthouse/calibrate.sh` | Threshold calibration from 10 runs | VERIFIED | Executable, 10-run collection, Node.js p50/p95 parser, outputs recommended `.lighthouserc.json` assertions |
| `tests/e2e/helpers/test-fixtures.ts` | Shared E2E fixture helper | VERIFIED | Exports `mockAllEndpoints`, `mockSessionCRUD`, `mockGroupCRUD`, `createTestState`, `waitForAppReady`, fixture data constants |
| `tests/e2e/session-lifecycle.spec.ts` | Session lifecycle E2E (5 tests) | PARTIAL | Exists and substantive (5 tests: create, select, stop, delete, full-lifecycle); missing terminal input/output verification per REQUIREMENTS.md |
| `tests/e2e/group-crud.spec.ts` | Group CRUD E2E (4 tests) | PARTIAL | Exists and substantive (4 tests: create, rename, delete, full-lifecycle); missing add-session-to-group and reorder per REQUIREMENTS.md |
| `tests/e2e/mobile-e2e.spec.ts` | Mobile E2E at 3 viewports (8 tests) | VERIFIED | 8 tests covering hamburger, overflow menu, sidebar auto-close, terminal panel, form input, no overflow, topbar visibility; viewport-conditional skips handled correctly |
| `tests/e2e/pw-p10-functional.config.mjs` | Playwright config for TEST-C | VERIFIED | chromium-desktop 1280x800, service workers blocked, glob testMatch |
| `tests/e2e/pw-p10-mobile.config.mjs` | Playwright config for TEST-D | VERIFIED | 3 projects: iPhone SE (375x667), iPhone 14 (390x844), iPad (768x1024) |
| `tests/e2e/pw-p10-e2e.config.mjs` | Combined E2E config | VERIFIED | 4 projects in one invocation (chromium-desktop + 3 mobile viewports) |
| `.github/workflows/weekly-regression.yml` | Weekly alert-only workflow | VERIFIED | Cron `0 0 * * 0`, `continue-on-error: true`, issue creation with idempotency, 30-day artifact retention, no auto-fix |
| `.github/weekly-regression-issue-template.md` | Issue body template | VERIFIED | 11 placeholder tokens (`{{VISUAL_STATUS}}`, `{{LIGHTHOUSE_STATUS}}`, `{{FAILURE_COUNT}}`, `{{DATE}}`, `{{VISUAL_DETAILS}}`, `{{LIGHTHOUSE_DETAILS}}`, `{{ARTIFACTS_URL}}`, `{{RUN_URL}}`, `{{BRANCH}}`, `{{COMMIT_SHA}}`, `{{COMMIT_MSG}}`), 5 required sections |
| `tests/ci/weekly-alert-format.test.sh` | Shell format validator | VERIFIED | Executable, 6 structural checks, exits 0 on pass / 1 on fail, `set -euo pipefail` |
| `tests/ci/weekly-alert-format-edge-cases.sh` | 7 edge case tests | VERIFIED | Executable, expect_pass/expect_fail helpers, 7 scenarios (4 expected-fail, 3 expected-pass) |

---

## Key Link Verification

| From | To | Via | Status | Details |
|------|----|-----|--------|---------|
| `visual-regression.yml` | `pw-visual-regression.config.ts` | `--config=pw-visual-regression.config.ts` | WIRED | Workflow runs Docker container with this config |
| `visual-regression.yml` | Docker image `v1.59.1-jammy` | `docker pull mcr.microsoft.com/playwright:v1.59.1-jammy` | WIRED | Image pinned by tag, used in run step |
| `lighthouse-ci.yml` | `.lighthouserc.json` | `configPath: .lighthouserc.json` | WIRED | Action reads the config file |
| `weekly-regression.yml` | `pw-visual-regression.config.ts` | `--config=pw-visual-regression.config.ts` | WIRED | Corrected from plan's `.mjs` to actual `.ts` |
| `weekly-regression.yml` | `.lighthouserc.json` | `--config=.lighthouserc.json` | WIRED | `lhci autorun --config=.lighthouserc.json` |
| `weekly-regression.yml` | `weekly-regression-issue-template.md` | `fs.readFileSync('.github/weekly-regression-issue-template.md')` | WIRED | With inline fallback if file missing |
| `weekly-alert-format-edge-cases.sh` | `weekly-alert-format.test.sh` | `VALIDATOR="$SCRIPT_DIR/weekly-alert-format.test.sh"` | WIRED | Edge case runner calls the validator directly |
| `session-lifecycle.spec.ts` | `helpers/test-fixtures.ts` | `import { mockAllEndpoints, mockSessionCRUD, createTestState, waitForAppReady, resetIdCounter }` | WIRED | Imports used throughout spec |
| `group-crud.spec.ts` | `helpers/test-fixtures.ts` | `import { mockAllEndpoints, mockGroupCRUD, createTestState, waitForAppReady, resetIdCounter }` | WIRED | Imports used throughout spec |
| `mobile-e2e.spec.ts` | `helpers/test-fixtures.ts` | `import { mockAllEndpoints, waitForAppReady }` | WIRED | Imports used in `beforeEach` |
| `pw-p10-e2e.config.mjs` | `session-lifecycle.spec.ts` + `group-crud.spec.ts` | `testMatch: '{session-lifecycle,group-crud}.spec.ts'` | WIRED | Glob format correctly targets both files |
| `pw-p10-e2e.config.mjs` | `mobile-e2e.spec.ts` | `testMatch: 'mobile-e2e.spec.ts'` | WIRED | Each of 3 mobile projects targets this file |

---

## Requirements Coverage

| Requirement | Source Plan | Description | Status | Evidence |
|-------------|-------------|-------------|--------|----------|
| TEST-A | 10-01 | Visual regression with committed baselines, Docker-only, CI blocks merge on >0.1% diff | SATISFIED | 18 PNG baselines committed, workflow blocks PR merge, `maxDiffPixelRatio: 0.001` |
| TEST-B | 10-02 | Lighthouse CI with `numberOfRuns: 5`, byte-weight HARD gates, timing soft warnings | SATISFIED | `.lighthouserc.json` has 5 runs, `error`-level byte assertions, `warn`-level timing assertions |
| TEST-C | 10-03 | Session lifecycle (create→attach→send input→verify output→stop→delete) and group CRUD (create→add session→reorder→delete) | PARTIAL | Session lifecycle (5 tests) covers create/select/stop/delete but NOT send-input/verify-output. Group CRUD (4 tests) covers create/rename/delete but NOT add-session-to-group or reorder. The PLAN scoped these down from the REQUIREMENTS.md wording without documenting the deviation as an explicit deferral. |
| TEST-D | 10-03 | Mobile E2E at iPhone SE (375x667), iPhone 14 (390x844), iPad (768x1024) — hamburger, overflow menu, sidebar drawer, terminal attach, form input | SATISFIED | All 3 viewports configured, all named scenarios covered in mobile-e2e.spec.ts |
| TEST-E | 10-04 | Alert-only weekly workflow — runs visual + Lighthouse, creates issue on failure, does NOT auto-fix | SATISFIED | weekly-regression.yml is alert-only, no auto-fix code present, idempotent issue creation |

**Coverage note:** All 5 Phase 10 requirements are addressed by plans. TEST-C has a scope gap between the REQUIREMENTS.md definition and what was planned and implemented.

---

## Commits Verified

All 19 commits documented in SUMMARYs are present in git history:

| Plan | Commits |
|------|---------|
| 10-01 (TEST-A) | `2ea724c`, `39a32e3`, `da72261`, `ffc87e4`, `9542443`, `37453f4` — all present |
| 10-02 (TEST-B) | `7ca63f6`, `ccf0b6f`, `cd0fcfc`, `94f85e8` — all present |
| 10-03 (TEST-C/D) | `b9a7f2b`, `8111f8d`, `42886b1`, `1276c4e`, `b4526bf`, `fccf0a8` — all present |
| 10-04 (TEST-E) | `1ea4fb6`, `c212df8`, `5e14272` — all present |

---

## Anti-Patterns Found

| File | Pattern | Severity | Impact |
|------|---------|----------|--------|
| `.lighthouserc.json` | Pre-calibration thresholds used (Phase 8 spec + 20% CI buffer), not live-calibrated from 10 baseline runs | INFO | `calibrate.sh` is ready; thresholds are conservative (180KB vs 150KB actual). Not a blocker — values will pass the current codebase. Recalibration recommended before v1.5.0 release to set tight bounds. |
| `weekly-regression.yml` (line 107-109) | Visual regression step runs `npx playwright test` twice — once to capture JSON output (swallows exit code via `|| true`), once to capture exit code (discards output to `/dev/null`) | WARNING | The dual-run approach is fragile — if the second run flips from the first (e.g., flaky test), `steps.visual.outcome` reflects the second run while the JSON artifacts reflect the first. Could cause misleading issue bodies. |
| `tests/e2e/session-lifecycle.spec.ts` | No test for "send input to terminal" or "verify terminal output" | WARNING | TEST-C REQUIREMENTS.md explicitly requires this. The xterm terminal is rendered in the main panel but typing into it and reading output is not tested. If terminal input breaks, this suite would not catch it. |
| `tests/e2e/group-crud.spec.ts` | No test for "add session to group" or "reorder sessions within group" | WARNING | TEST-C REQUIREMENTS.md explicitly requires this. Moving a session into a group is a web mutation not covered by the existing 4 tests. |

---

## Human Verification Required

### 1. Visual Regression Suite Against Live Baselines

**Test:** From a machine with Docker installed and outside an agent-deck session, run:
```
make build
env -u AGENTDECK_INSTANCE_ID -u TMUX ./build/agent-deck -p _test web --listen 127.0.0.1:18420 --token test &
cd tests/e2e && docker run --rm --network=host -v "$(pwd):/work" -w /work \
  mcr.microsoft.com/playwright:v1.59.1-jammy \
  npx playwright test --config=pw-visual-regression.config.ts
```
**Expected:** 18/18 tests pass, 0 pixel diff failures against committed baselines
**Why human:** Cannot run Docker inside agent-deck session; nested-session detection blocks the test server

### 2. Lighthouse CI Budget Verification

**Test:** From a plain terminal outside agent-deck, run `make build && ./tests/lighthouse/budget-check.sh`
**Expected:** `total-byte-weight` assertion passes (<180KB), `resource-summary:script:size` passes (<120KB). FCP/LCP/TBT appear as warnings if exceeded but do not block.
**Why human:** Test server exits immediately inside agent-deck (nested-session detection)

### 3. Functional + Mobile E2E Suite

**Test:** Start test server outside agent-deck, then: `cd tests/e2e && npx playwright test --config=pw-p10-e2e.config.mjs`
**Expected:** 29 tests pass, 1 correctly skips (iPad hamburger at large viewport)
**Why human:** Requires test server running on 127.0.0.1:18420 outside agent-deck

---

## Gaps Summary

**1 gap blocking full TEST-C requirement satisfaction:**

The REQUIREMENTS.md and ROADMAP success criteria for TEST-C explicitly specify:
- Session lifecycle: "create → attach → **send input → verify output** → stop → delete"
- Group CRUD: "create group → **add session → reorder** → delete"

What was planned (10-03-PLAN.md `must_haves.truths`) and delivered:
- Session lifecycle: create → select → verify terminal panel visible → stop → delete (no input/output verification)
- Group CRUD: create → rename → delete (no add-session-to-group, no reorder)

The PLAN downscoped from the REQUIREMENTS silently (without documenting the items as explicitly deferred). The implementation matches the PLAN scope exactly — the gap is between REQUIREMENTS.md and the PLAN, not between PLAN and implementation.

**Why this matters:** If terminal input breaks (e.g., xterm.js update, WebSocket handler regression), the current test suite will not catch it. If the "move session to group" API breaks, the suite will not catch it. These are exactly the kinds of regressions Phase 10 was designed to prevent.

**Recommended resolution:** Either (a) add the missing test cases to `session-lifecycle.spec.ts` and `group-crud.spec.ts`, or (b) document in the PLAN/REQUIREMENTS that "send input", "verify output", "add session to group", and "reorder" are explicitly deferred to v1.6+ (noting that web-native session reorder is already listed as V16-WEB-07 out-of-scope).

**The dual-run fragility in `weekly-regression.yml` (lines 107-109) is a non-blocking warning** — it does not prevent the weekly alert from firing but could cause misleading issue body content in flaky test scenarios.

---

_Verified: 2026-04-10T06:30:00Z_
_Verifier: Claude (gsd-verifier)_
