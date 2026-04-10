# POL-7 Traceability

**Requirement:** POL-7 (REQUIREMENTS.md line 68)
**Phase where scheduled:** Phase 9 (Polish)
**Phase where shipped:** Phase 6 (Critical P0 Bugs), plan 04
**Ordering rationale:** STATE.md "Critical Ordering Constraints" #8 — POL-7 SHIPS WITH WEB-P0-4 in Phase 6 (same Toast.js refactor, same PR).
**Status:** Complete. REQUIREMENTS.md line 68 already marked [x].

## Requirement bullets and their shipping location

| POL-7 bullet | Shipped in | File | Commit |
|---|---|---|---|
| Toast stack capped at 3 visible | 06-04 Task 2 | `internal/web/static/app/Toast.js` (eviction `next.length > 3`) | `aa1c974` |
| 5s auto-dismiss for info/success | 06-04 Task 2 | `internal/web/static/app/Toast.js` (setTimeout branch) | `aa1c974` |
| Errors NOT auto-dismissed | 06-04 Task 2 | `internal/web/static/app/Toast.js` (error branch skips setTimeout) | `aa1c974` |
| Error-FIFO eviction only when all 3 visible are errors | 06-04 Task 2 | `internal/web/static/app/Toast.js` | `aa1c974` |
| History drawer for dismissed toasts | 06-04 Task 4 | `internal/web/static/app/ToastHistoryDrawer.js` (new component) | `a7f2548` |
| History drawer toggle in Topbar | 06-04 Task 4 | `internal/web/static/app/Topbar.js` + `AppShell.js` | `a7f2548` |
| Drawer persistence (50 entries via slice(-50)) | 06-04 Task 1 | `internal/web/static/app/state.js::toastHistorySignal` | `d3b4f35` |
| localStorage key `agentdeck_toast_history` | 06-04 Task 1 | `internal/web/static/app/state.js` | `d3b4f35` |
| ARIA live region split (role="alert" assertive for errors; role="status" polite for info/success) | 06-04 Task 2 | `internal/web/static/app/Toast.js` | `aa1c974` |
| Drawer has `role="dialog" aria-modal="true"` | 06-04 Task 4 | `internal/web/static/app/ToastHistoryDrawer.js` | `a7f2548` |
| Toggle has `data-testid="toast-history-toggle"` and 44x44 touch target | 06-04 Task 4 | `internal/web/static/app/ToastHistoryDrawer.js` | `a7f2548` |

## Commit chain (Phase 6 plan 04)

TDD ordering per STATE.md:
1. `80fea0d` — `test(06-04)` add failing regression spec
2. `d3b4f35` — `feat(06-04)` add state.js signals (toastHistorySignal, toastHistoryOpenSignal)
3. `aa1c974` — `fix(06-04)` Toast.js refactor (eviction + ARIA split)
4. `a7f2548` — `feat(06-04)` drawer + Topbar + AppShell wiring
5. `cf8322e` — `test(06-04)` a11y spec + inline contrast fix

Test artifacts committed in the same chain:
- `tests/e2e/visual/p6-bug4-toast-cap.spec.ts` — 13/13 regression tests
- `tests/e2e/visual/p6-bug4-a11y.spec.ts` — 6/6 a11y tests

## Why POL-7 appears in Phase 9 at all

POL-7 was listed in the v1.5.0 requirements as part of the Polish phase because it's thematically a polish item (quality-of-life for toast handling, not a bug fix). The ordering constraint emerged AFTER the roadmap was locked: both POL-7 and WEB-P0-4 extend the same `Toast.js` component, and shipping them in separate PRs would create a merge conflict and require double the a11y audit. Research pitfall #X (see `.planning/research/pitfalls.md`) documented that concurrent refactors on the same stateful component lose the second refactor's context — the first refactor "wins" and the second has to be rebased. Combining them in plan 06-04 was the safer choice.

The Phase 9 plan count stayed at 4 (not 3) because renumbering mid-roadmap breaks cross-references in STATE.md and REQUIREMENTS.md. Instead, this 09-03 plan documents the ship location and installs a regression guard, preserving the plan count and ensuring the shipped invariants remain testable.

## No implementation changes

This plan modifies zero files under `internal/`. The only new production-code file in this plan's `files_modified` list is under `tests/e2e/` (a regression-guard spec), and the only new documentation file is this traceability record.
