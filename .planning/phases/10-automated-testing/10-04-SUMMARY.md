---
phase: 10-automated-testing
plan: "04"
subsystem: testing
tags: [github-actions, playwright, lighthouse, shell, regression-testing, alerts]

# Dependency graph
requires:
  - phase: 10-01
    provides: pw-visual-regression.config.ts and visual baseline screenshots
  - phase: 10-02
    provides: .lighthouserc.json performance thresholds and Lighthouse CI config
provides:
  - Alert-only weekly regression workflow that fires every Sunday at midnight UTC
  - GitHub issue creation with idempotency (append comment vs create duplicate)
  - Issue body template with 11 placeholder tokens for dynamic substitution
  - Shell format validation script with 6 structural checks
  - Edge case test suite with 7 scenarios (4 expected failures, 3 expected successes)
affects: [11-release]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "TDD RED-GREEN for shell scripts: write failing validator first, then create the thing it validates"
    - "GitHub Actions idempotency: listForRepo with labels + title prefix check before creating new issue"
    - "continue-on-error: true on test steps so both suites run and ALL failures are collected before alerting"
    - "Inline fallback template in github-script when external file is missing"

key-files:
  created:
    - .github/workflows/weekly-regression.yml
    - .github/weekly-regression-issue-template.md
    - tests/ci/weekly-alert-format.test.sh
    - tests/ci/weekly-alert-format-edge-cases.sh
  modified: []

key-decisions:
  - "Used pw-visual-regression.config.ts (TypeScript) not .mjs — plan specified .mjs but actual Plan 10-01 artifact is .ts"
  - "grep -qP needs -- separator before patterns starting with '- ' to prevent flag misinterpretation"
  - "Alert-only per Pitfall 15: auto-fix is deferred to v1.6+, alert delivers 80% value with 0% risk"
  - "Artifacts uploaded unconditionally (even on success) for audit trail with 30-day retention"
  - "Assignees set to context.repo.owner for simplicity; documented as configurable in header comment"

patterns-established:
  - "Shell validators use set -euo pipefail with explicit ERRORS counter; exit 1 only at end after all checks"
  - "Edge case runners use expect_pass/expect_fail helpers that swap exit code semantics for readability"
  - "Weekly workflow uses AGENTDECK_PROFILE=_ci_weekly to isolate from other CI profiles"

requirements-completed: [TEST-E]

# Metrics
duration: 12min
completed: 2026-04-10
---

# Phase 10 Plan 04: Weekly Regression Alert Summary

**Alert-only GitHub Actions workflow fires every Sunday, builds the binary, runs visual regression (pw-visual-regression.config.ts) and Lighthouse CI (.lighthouserc.json), creates a labeled GitHub issue with idempotency on failure, and stays silent on success.**

## Performance

- **Duration:** ~12 min
- **Started:** 2026-04-10T02:53:00Z
- **Completed:** 2026-04-10T03:05:10Z
- **Tasks:** 3 (TDD RED + GREEN + edge cases/docs)
- **Files modified:** 0 existing, 4 created

## Accomplishments

- Shipped `.github/workflows/weekly-regression.yml`: cron `0 0 * * 0` + `workflow_dispatch`, single `regression-check` job, `continue-on-error: true` on both test steps, `actions/github-script@v7` for issue creation with open-issue deduplication, `actions/upload-artifact@v4` with 30-day retention, `GOTOOLCHAIN=go1.24.0` pinned
- Shipped `.github/weekly-regression-issue-template.md`: 11 placeholder tokens, 5 required sections, idempotency-ready structure
- Shipped `tests/ci/weekly-alert-format.test.sh`: 6 structural checks, exits 0/1, confirmed RED (empty input fails) then GREEN (well-formed body passes)
- Shipped `tests/ci/weekly-alert-format-edge-cases.sh`: 7 edge cases, 7/7 pass

## Task Commits

Each task was committed atomically:

1. **Task 1: Write issue format validation test (RED)** - `1ea4fb6` (test)
2. **Task 2: Create workflow and issue template (GREEN)** - `c212df8` (ci)
3. **Task 3: Edge case tests and documentation** - `5e14272` (docs)

**Plan metadata:** _(final docs commit — see below)_

## Files Created/Modified

- `.github/workflows/weekly-regression.yml` — scheduled weekly regression workflow (Sunday midnight UTC + manual dispatch)
- `.github/weekly-regression-issue-template.md` — issue body template with 11 `{{PLACEHOLDER}}` tokens
- `tests/ci/weekly-alert-format.test.sh` — shell format validator (6 checks, exit 0/1)
- `tests/ci/weekly-alert-format-edge-cases.sh` — 7-scenario edge case test runner

## Decisions Made

- **Config filename deviation:** Plan specified `pw-visual-regression.config.mjs` but Plan 10-01 actually created `pw-visual-regression.config.ts` (TypeScript). Updated workflow to use `.ts` to match the actual artifact.
- **grep -- separator:** `grep -qP '- \*\*...'` fails with `invalid option -- ' '` when the pattern starts with `- ` and the input is empty (grep treats it as a flag). Fixed by adding `--` before the pattern in the format validator.
- **Alert-only:** No auto-fix, no baseline updates, no AI agents per Pitfall 15 from Phase 10 research. Documented in header comment.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] grep -qP pattern misinterpreted as option flag**
- **Found during:** Task 1 (RED verification)
- **Issue:** `echo "" | grep -qP '- \*\*Visual regression:\*\* (PASS|FAIL)'` throws `invalid option -- ' '` because the pattern starting with `- ` is parsed as a flag when stdin is empty
- **Fix:** Added `--` end-of-options separator: `grep -qP -- '- \*\*...'`
- **Files modified:** tests/ci/weekly-alert-format.test.sh
- **Verification:** `echo "" | tests/ci/weekly-alert-format.test.sh` exits 1 (correct RED), full body exits 0 (correct GREEN)
- **Committed in:** 1ea4fb6 (Task 1 commit)

**2. [Rule 1 - Bug] Visual regression config filename mismatch**
- **Found during:** Task 2 (creating workflow)
- **Issue:** Plan specified `pw-visual-regression.config.mjs` but Plan 10-01 created `pw-visual-regression.config.ts`
- **Fix:** Used `pw-visual-regression.config.ts` in the workflow's visual regression step
- **Files modified:** .github/workflows/weekly-regression.yml
- **Verification:** File confirmed at `tests/e2e/pw-visual-regression.config.ts`
- **Committed in:** c212df8 (Task 2 commit)

---

**Total deviations:** 2 auto-fixed (2 Rule 1 bugs)
**Impact on plan:** Both fixes required for correctness. No scope creep.

## Issues Encountered

None beyond the two auto-fixed deviations above.

## User Setup Required

None — no external service configuration required. The workflow uses `${{ secrets.GITHUB_TOKEN }}` which is automatically provided by GitHub Actions.

## Next Phase Readiness

- Phase 10 (Automated Testing) is now COMPLETE: TEST-A (visual regression PR gate), TEST-B (Lighthouse CI PR gate), TEST-C (session lifecycle E2E), TEST-D (mobile E2E), and TEST-E (weekly regression alerting) are all shipped.
- Phase 11 (Release v1.5.0) can begin: all testing infrastructure is in place.

---
*Phase: 10-automated-testing*
*Completed: 2026-04-10*
