---
phase: 09-polish
verified: 2026-04-09T21:45:00Z
status: passed
score: 30/30 must-haves verified
plans_verified: 4
plans_expected: 4
commits_verified: 23
claude_attribution_count: 0
git_clean: true
playwright_results:
  pw-p9-plan1: "13 passed / 1 skipped / 0 failed"
  pw-p9-plan2: "21 passed / 3 skipped / 0 failed"
  pw-p9-plan3: "10 passed / 0 skipped / 0 failed"
  pw-p9-plan4: "18 passed / 0 skipped / 0 failed"
  total: "62 passed / 4 skipped / 0 failed"
requirements:
  POL-1: { plan: "09-01", status: satisfied, evidence: "sessionsLoadedSignal + SessionList skeleton gate" }
  POL-2: { plan: "09-01", status: satisfied, evidence: "GroupRow 120ms opacity fade + SessionRow regression guard" }
  POL-3: { plan: "09-02", status: satisfied, evidence: "ProfileDropdown _*-filter + max-h-[300px] scroll" }
  POL-4: { plan: "09-01", status: satisfied, evidence: "GroupRow py-1 / min-h-[40px]" }
  POL-5: { plan: "09-02", status: satisfied, evidence: "Intl.NumberFormat(navigator.language) memoized" }
  POL-6: { plan: "09-04", status: satisfied, evidence: "18 axe-core + luminance tests, 8 component files fixed" }
  POL-7: { plan: "09-03", status: satisfied, evidence: "Shipped in Phase 6 plan 04; traceability doc + 10-assertion regression guard" }
known_preexisting_failures:
  - spec: "tests/e2e/visual/p6-bug4-mutations-gating.spec.ts:129"
    status: "pre-existing (confirmed against baseline 5539ce3)"
    scope: "Phase 10 TEST-A"
  - spec: "tests/e2e/visual/p8-perf-e-listener-cleanup.spec.ts:73"
    status: "pre-existing (confirmed against baseline 5539ce3)"
    scope: "Phase 10 TEST-A"
  - category: "make ci Go lint + test failures"
    items:
      - "internal/tuitest/smoke_test.go:182,224 errcheck"
      - "internal/ui/branch_picker.go:18 unused branchPickerResultMsg"
      - "internal/ui/home.go:63 unused isCreatingPlaceholder"
      - "cmd/agent-deck/main.go:458 SA4006 args never used"
      - "TestSyncSessionIDsFromTmux_* (6 tests SetEnvironment failed)"
    scope: "deferred-items.md #1, #3, #7, #9 (session-subsystem + TUI refactor)"
human_verification:
  - test: "Light theme visual sanity on real displays"
    expected: "Sidebar, topbar, cost dashboard, dialogs, toasts all feel premium on physical macOS/iOS/Linux screens (not just axe-core compliant)"
    why_human: "Contrast math passes WCAG AA but subjective 'premium' feel requires real-eye judgement"
  - test: "Skeleton loader perceived-latency on slow connections"
    expected: "Sidebar skeleton appears instantly and never flashes 'No sessions' before real data arrives"
    why_human: "DOM tests use page.route stalling to simulate; real network timing characteristics may differ"
  - test: "GroupRow 120ms fade feel"
    expected: "Action buttons fade smoothly on hover, not 'snappy' or 'laggy'"
    why_human: "Subjective animation quality requires human hover testing"
  - test: "Locale formatting on real browsers"
    expected: "Cost dashboard shows locale-correct USD formatting (de-DE: `1.234,56 $`, en-US: `$1,234.56`) on real browsers with different navigator.language"
    why_human: "Playwright forces locale via chromium launch flags; real browser locale behavior may differ slightly"
  - test: "Profile dropdown scroll with 20+ real profiles"
    expected: "Listbox scrolls smoothly within 300px, does not push viewport"
    why_human: "Test uses mocked 12-profile response; real long lists should be exercised"
---

# Phase 9: Polish / Premium UX Verification Report

**Phase Goal:** Premium UX refinements that separate "works" from "premium." Skeleton loader matching final layout exactly (Linear/Vercel pattern), button transitions, profile dropdown filter, group divider gap, currency locale, light theme audit (LAST in phase). POL-7 ships WITH WEB-P0-4 in Phase 6 (same Toast.js refactor) but is listed here for traceability.

**Verified:** 2026-04-09T21:45:00Z
**Status:** PASSED
**Re-verification:** No — initial verification

## Goal Achievement Summary

All 5 success criteria from ROADMAP.md lines 191-195 have been implemented, shipped, and verified by 62 Playwright assertions across 4 per-plan configs (4 skipped are intentional `test.skip()` for locale-scoped / fixture-scoped tests). Zero failing assertions across any of the four pw-p9-planN configs.

### Observable Truths (Success Criteria)

| # | Truth | Status | Evidence |
|---|-------|--------|----------|
| 1 | User sees a skeleton loader matching the final sidebar layout EXACTLY during cold-load gap; uses Tailwind `animate-pulse`; respects `prefers-reduced-motion` | VERIFIED | `sessionsLoadedSignal` in state.js:144, SessionList.js:126-135 skeleton gate with `data-testid="sidebar-skeleton"` + `animate-pulse motion-reduce:animate-none`; main.js:63,93 dual-flip on /api/menu + SSE. pw-p9-plan1 plan 13 passed including DOM skeleton-visible + skeleton-clears tests. |
| 2 | User hovering over a session row sees action buttons fade in with 120ms opacity transition; respects `prefers-reduced-motion` | VERIFIED | GroupRow.js:117 `transition-opacity duration-[120ms] motion-reduce:transition-none`; `hidden group-hover:flex` removed. SessionRow 06-03 wiring preserved via structural regression guard. pw-p9-plan1 POL-2 DOM tests verify computed transition-property is `opacity` on hover and `none` under `emulateMedia({ reducedMotion: 'reduce' })`. |
| 3 | User opens profile dropdown and sees `_*` test profiles filtered out; max-height: 300px scrollable | VERIFIED | ProfileDropdown.js:38 `.filter(p => !p.startsWith('_'))`; line 103 `max-h-[300px] overflow-y-auto`. pw-p9-plan2 POL-3 tests verify 12-profile mock (2 `_*`) renders 10 visible options, listbox height ≤300px, scroll active, filtering to 1 profile falls through to single-profile status element. |
| 4 | User sees cost dashboard render currency respecting `navigator.language` via `Intl.NumberFormat` | VERIFIED | CostDashboard.js:14 module-level `currencyFormatter = new Intl.NumberFormat(navigator.language, { style: 'currency', currency: 'USD' })`; fmt() (line 20) and chart y-axis tick callback (line 132) both delegate. Exactly 1 construction (memoization guard enforced). pw-p9-plan2 en-US/de-DE/ja-JP DOM tests all pass locale-loose regex. |
| 5 | User sees light theme rendering consistently across sidebar, terminal, dialogs, tooltips, toasts, empty state, cost dashboard | VERIFIED | 8 component files fixed (`text-gray-400` → `text-gray-600`, `text-green-600` → `text-green-700`, `text-gray-500` → `text-gray-700`). All `dark:*` variants preserved. pw-p9-plan4 18 tests (11 axe-core color-contrast + 7 luminance canvas-based) all pass in 13.4s. Plans 1, 2, 3 regression suites re-run after POL-6 and all remain green. |

**Score: 5/5 observable truths verified**

## Per-Plan Summary

### Plan 09-01: POL-1 skeleton + POL-2 GroupRow fade + POL-4 density
- **Requirements claimed:** POL-1, POL-2, POL-4
- **Commits:** 4 (03b191f test, 7f91b26 feat, 44d38e2 fix, 220de49 docs)
- **Files touched:** state.js, main.js, SessionList.js, GroupRow.js, styles.css; 3 specs + 1 config created
- **Playwright:** 13 passed / 1 skipped (pw-p9-plan1.config.mjs in 1.6s)
- **Skipped test:** POL-4 DOM gap assertion needs 2 groups with intervening session in fixture — correctly skipped
- **SUMMARY.md:** Present, Self-Check PASSED

### Plan 09-02: POL-3 profile dropdown + POL-5 currency locale
- **Requirements claimed:** POL-3, POL-5
- **Commits:** 4 (39a0838 test, e5c4b42 fix, 23b4d04 feat, 0d39c60 docs)
- **Files touched:** ProfileDropdown.js, CostDashboard.js; 2 specs + 1 config created
- **Playwright:** 21 passed / 3 skipped (pw-p9-plan2.config.mjs in 3.0s)
- **Skipped tests:** 3 intentional locale-scoped `test.skip()` calls (en-US skip in de-DE project, de-DE skip in en-US project, ja-JP skip in de-DE project)
- **SUMMARY.md:** Present, Self-Check PASSED

### Plan 09-03: POL-7 traceability + regression guard
- **Requirements claimed:** POL-7 (implemented in Phase 6 plan 04)
- **Commits:** 3 (a83a6d5 docs traceability, 83e2d6e test, 3cbf3ab docs completion)
- **Files touched:** 1 traceability doc, 1 regression spec, 1 config — zero production files
- **Playwright:** 10 passed / 0 skipped (pw-p9-plan3.config.mjs in 770ms)
- **Special case:** POL-7 implementation shipped in Phase 6 plan 04 (commits 80fea0d, d3b4f35, aa1c974, a7f2548, cf8322e). Plan 09-03 is documentation + structural regression guard only. Per ROADMAP.md line 200: "P9-plan-3: POL-7 toast refinement (ships with Phase 6 P0-4; listed for traceability)."
- **SUMMARY.md:** Present, Self-Check PASSED

### Plan 09-04: POL-6 light theme audit (LAST in phase)
- **Requirements claimed:** POL-6
- **Commits:** 12 (f0928dd test discovery, 2ac722c test revision, 7f34792/2e5f152/d059c6e/13a68d8/3bfa517/f9970b3/fdc8bfa 7x fix, 5380436 chore styles regen, 46aac79 docs deferred-items close, e3170de docs completion)
- **Files touched:** 8 component files (SessionRow, GroupRow, SessionList, EmptyStateDashboard, CostDashboard, ProfileDropdown, SearchFilter, SettingsPanel), styles.css, deferred-items.md; 2 specs + 1 config created
- **Playwright:** 18 passed / 0 skipped (pw-p9-plan4.config.mjs in 13.4s). 11 axe-core + 7 luminance-canvas tests.
- **Ordering constraint satisfied:** POL-6 ran LAST in Phase 9, after all layout from plans 09-01, 09-02, 09-03 landed
- **Bonus findings:** SessionRow cost badge `text-green-600` → `text-green-700` (luminance spec L2 catch), SearchFilter + SettingsPanel pro-active fixes
- **SUMMARY.md:** Present, Self-Check PASSED

## Required Artifacts — All Verified

| Artifact | Expected | Status | Details |
|----------|----------|--------|---------|
| `.planning/phases/09-polish/09-01-SUMMARY.md` | Plan 1 summary | VERIFIED | Present, 20-min duration, Self-Check PASSED |
| `.planning/phases/09-polish/09-02-SUMMARY.md` | Plan 2 summary | VERIFIED | Present, ~55-min duration, Self-Check PASSED |
| `.planning/phases/09-polish/09-03-SUMMARY.md` | Plan 3 summary | VERIFIED | Present, ~4-min duration, Self-Check PASSED |
| `.planning/phases/09-polish/09-04-SUMMARY.md` | Plan 4 summary | VERIFIED | Present, ~95-min duration, Self-Check PASSED |
| `.planning/phases/09-polish/09-03-POL-7-TRACEABILITY.md` | POL-7 commit-to-bullet traceability | VERIFIED | Present |
| `tests/e2e/pw-p9-plan1.config.mjs` | Plan 1 config | VERIFIED | Present |
| `tests/e2e/pw-p9-plan2.config.mjs` | Plan 2 config (2 locale projects) | VERIFIED | Present, `serviceWorkers: 'block'` enabled |
| `tests/e2e/pw-p9-plan3.config.mjs` | Plan 3 config (structural only) | VERIFIED | Present |
| `tests/e2e/pw-p9-plan4.config.mjs` | Plan 4 config (forced light theme) | VERIFIED | Present |
| `tests/e2e/visual/p9-pol1-skeleton.spec.ts` | POL-1 regression | VERIFIED | 6 tests |
| `tests/e2e/visual/p9-pol2-transitions.spec.ts` | POL-2 regression | VERIFIED | 5 tests |
| `tests/e2e/visual/p9-pol3-profile-filter.spec.ts` | POL-3 regression | VERIFIED | 6 tests |
| `tests/e2e/visual/p9-pol4-group-density.spec.ts` | POL-4 regression | VERIFIED | 3 tests |
| `tests/e2e/visual/p9-pol5-currency-locale.spec.ts` | POL-5 regression | VERIFIED | 6 tests |
| `tests/e2e/visual/p9-pol6-light-theme-audit.spec.ts` | POL-6 axe-core | VERIFIED | 11 tests |
| `tests/e2e/visual/p9-pol6-light-theme-contrast.spec.ts` | POL-6 luminance | VERIFIED | 7 tests |
| `tests/e2e/visual/p9-pol7-regression-guard.spec.ts` | POL-7 structural guard | VERIFIED | 10 tests |
| `internal/web/static/app/state.js` | sessionsLoadedSignal at tail | VERIFIED | Line 144, after mutationsEnabledSignal |
| `internal/web/static/app/main.js` | Dual-flip sessionsLoadedSignal | VERIFIED | Lines 63 + 93 |
| `internal/web/static/app/SessionList.js` | Skeleton gate + data-testid | VERIFIED | Line 132 gate, line 135 data-testid |
| `internal/web/static/app/GroupRow.js` | py-1 min-h-[40px] + opacity fade | VERIFIED | Line 97 density, line 117 transition |
| `internal/web/static/app/ProfileDropdown.js` | _*-filter + max-h-[300px] | VERIFIED | Line 38 filter, line 103 max-h |
| `internal/web/static/app/CostDashboard.js` | Intl.NumberFormat formatter | VERIFIED | Line 14 construction, lines 20+132 delegation |

## Key Link Verification

| From | To | Via | Status |
|------|-----|-----|--------|
| state.js::sessionsLoadedSignal | main.js + SessionList.js | import + tail-append | WIRED |
| main.js::loadMenu | state.js::sessionsLoadedSignal.value = true | success branch (line 63) | WIRED |
| main.js::SSE menu handler | state.js::sessionsLoadedSignal.value = true | snapshot branch (line 93) | WIRED |
| SessionList.js | sessionsLoadedSignal | early branch render tri-state | WIRED |
| GroupRow.js action cluster | 120ms opacity fade | `transition-opacity duration-[120ms]` | WIRED |
| ProfileDropdown.js | `_*` filter | `startsWith('_')` before setProfiles | WIRED |
| ProfileDropdown.js listbox | max-h-[300px] | absolute-positioned container classes | WIRED |
| CostDashboard.js::fmt() | currencyFormatter | module-level memoized const | WIRED |
| CostDashboard.js::chart callback | currencyFormatter | same formatter (line 132) | WIRED |
| Toast.js / ToastHistoryDrawer.js / state.js | POL-7 invariants | readFileSync regression guard | WIRED (10/10 pass) |
| 8 component files | WCAG AA contrast | axe-core + canvas-luminance | WIRED (18/18 pass) |

## Requirements Traceability Matrix

| Req | Description | Source Plan | Description (REQUIREMENTS.md line) | Status | Evidence |
|-----|-------------|------------|-----------------------------------|--------|----------|
| **POL-1** | Skeleton loading state | 09-01 | Line 62 — Tailwind animate-pulse skeleton matching final sidebar layout | SATISFIED | pw-p9-plan1 6 POL-1 tests pass; code verified in state.js/main.js/SessionList.js |
| **POL-2** | 120ms opacity fade on action buttons | 09-01 | Line 63 — respects prefers-reduced-motion | SATISFIED | pw-p9-plan1 5 POL-2 tests pass; GroupRow.js:117 verified; SessionRow regression guard |
| **POL-3** | Profile dropdown `_*` filter + max-height 300px | 09-02 | Line 64 | SATISFIED | pw-p9-plan2 6 POL-3 tests pass (across 2 locale projects); ProfileDropdown.js:38,103 |
| **POL-4** | Group divider gap 48px → 12-16px | 09-01 | Line 65 — py-2.5/min-h-44 → py-1/min-h-40 | SATISFIED | pw-p9-plan1 3 POL-4 tests pass (1 DOM skipped for fixture reason, structural pass) |
| **POL-5** | Intl.NumberFormat(navigator.language) | 09-02 | Line 66 — currency stays USD, locale-aware formatting | SATISFIED | pw-p9-plan2 6 POL-5 tests pass (en-US + de-DE + ja-JP); CostDashboard.js:14 memoized |
| **POL-6** | Light theme audit across all surfaces | 09-04 | Line 67 — MUST ship LAST; fix contrast issues | SATISFIED | pw-p9-plan4 18 tests pass (11 axe + 7 luminance); 8 component files fixed; LAST in phase |
| **POL-7** | Toast stack cap 3 + 5s auto-dismiss + history drawer | 09-03 (impl in 06-04) | Line 68 — ships with WEB-P0-4 in same PR | SATISFIED | pw-p9-plan3 10/10 regression guard tests pass; traceability doc maps to 06-04 commits 80fea0d/d3b4f35/aa1c974/a7f2548/cf8322e |

**All 7 POL requirements: SATISFIED. Zero unmapped. Zero orphaned.**

**Note on REQUIREMENTS.md state:** REQUIREMENTS.md traceability table (lines 165-171) currently shows POL-1/2/4/7 as "Complete" and POL-3/5/6 as "Pending". This is stale documentation from before Phase 9 closed — the Phase 9 plans shipped all 7 POL items. The actual [ ]/[x] checkbox state in lines 62-68 also has POL-3/5/6 still unchecked. This is documentation drift to be closed in Phase 11 release notes, not a Phase 9 gap. All implementation evidence verifies the requirements are satisfied.

## Commit Chain Verification

**Baseline:** `5539ce3 feat(08-05): esbuild bundling via go generate + assets manifest (PERF-H)`
**Total commits since baseline:** 23 commits
**Claude attribution count:** `git log 5539ce3..HEAD --format='%B' | grep -ciE 'claude|co-authored-by'` → **0**
**Git status:** clean (0 tracked or untracked files)
**Go toolchain:** `go1.24.0` (preserved across all Phase 9 builds)

### Commit Log (chronological)

```
a83a6d5 docs(09-03): POL-7 traceability record — shipped in Phase 6 plan 04
83e2d6e test(09-03): POL-7 regression guard spec
03b191f test(09-01): add failing regression specs for POL-1/POL-2/POL-4
39a0838 test(09-02): add failing regression specs for POL-3/POL-5
3cbf3ab docs(09-03): complete POL-7 traceability + regression guard plan
7f91b26 feat(09-01): implement POL-1 skeleton loader
e5c4b42 fix(09-02): implement POL-3 profile dropdown filter + max-height
23b4d04 feat(09-02): implement POL-5 locale-aware currency formatting
44d38e2 fix(09-01): implement POL-2 GroupRow fade + POL-4 density reduction
0d39c60 docs(09-02): complete POL-3 profile filter + POL-5 currency locale plan
220de49 docs(09-01): complete POL-1 skeleton + POL-2 GroupRow fade + POL-4 density plan
f0928dd test(09-04): add POL-6 light theme audit spec (discovery pass)
2ac722c test(09-04): drive POL-6 audit specs via real UI, not isolated signals
7f34792 fix(09-04): POL-6 SessionRow tool label + cost badge contrast
2e5f152 fix(09-04): POL-6 GroupRow header + count chip contrast
d059c6e fix(09-04): POL-6 SessionList "No sessions" empty state contrast
13a68d8 fix(09-04): POL-6 EmptyStateDashboard body text contrast
3bfa517 fix(09-04): POL-6 CostDashboard summary card subtitle contrast
f9970b3 fix(09-04): POL-6 ProfileDropdown (active) label contrast
fdc8bfa fix(09-04): POL-6 SearchFilter placeholder + SettingsPanel loading text
5380436 chore(09-04): regenerate styles.css after POL-6 fixes
46aac79 docs(09-04): close deferred items #5 and #8 after POL-6 audit
e3170de docs(09-04): complete POL-6 light theme audit plan — Phase 9 CLOSED
```

### TDD Order Verification

- Plan 09-01: `test(03b191f)` → `feat(7f91b26)` → `fix(44d38e2)` → `docs(220de49)` — TDD order preserved
- Plan 09-02: `test(39a0838)` → `fix(e5c4b42)` → `feat(23b4d04)` → `docs(0d39c60)` — TDD order preserved
- Plan 09-03: `docs(a83a6d5)` → `test(83e2d6e)` → `docs(3cbf3ab)` — traceability only, no RED/GREEN cycle (POL-7 already shipped)
- Plan 09-04: `test(f0928dd)` → `test(2ac722c)` → 7x `fix` → `chore(5380436)` → `docs(46aac79)` → `docs(e3170de)` — TDD order preserved

## Playwright Regression Results (Final Run)

| Config | Passed | Skipped | Failed | Runtime |
|--------|-------:|--------:|-------:|---------|
| pw-p9-plan1.config.mjs | 13 | 1 | 0 | 1.6s |
| pw-p9-plan2.config.mjs | 21 | 3 | 0 | 3.0s |
| pw-p9-plan3.config.mjs | 10 | 0 | 0 | 770ms |
| pw-p9-plan4.config.mjs | 18 | 0 | 0 | 13.4s |
| **TOTAL** | **62** | **4** | **0** | **~18s** |

All 4 skipped tests are intentional `test.skip()` calls:
- 1 in plan 1: POL-4 DOM density needs 2 groups with session between them (fixture limitation, not a bug)
- 3 in plan 2: locale-scoped skips (en-US test skipped in de-DE project + de-DE test skipped in en-US project + ja-JP skipped in de-DE project)

## Regression Gate Summary (Prior Phase Tests)

Per orchestrator regression gate findings, two pre-existing test failures are confirmed NOT Phase 9 regressions:

1. **`tests/e2e/visual/p6-bug4-mutations-gating.spec.ts:129`** — Phase 6 WEB-P0-4 mutations gating test. Uses `import('/static/app/state.js')` + signal mutation pattern which the PERF-H esbuild bundler (shipped in baseline 5539ce3 as part of Phase 8 plan 08-05) broke by isolating bundled module instances from un-bundled imports. Confirmed pre-existing against baseline 5539ce3. Plan 09-04 SUMMARY Issue #3 documents the root cause and identifies it as Phase 10 TEST-A test-infra scope to port to real-UI driven pattern.

2. **`tests/e2e/visual/p8-perf-e-listener-cleanup.spec.ts:73`** — Phase 8 PERF-E AbortController listener cleanup test. Pre-existing against baseline 5539ce3. Not a Phase 9 regression. Phase 10 TEST-A scope.

**These MUST NOT be "fixed" in Phase 9** — they are out of scope per the HARD RULES. Phase 9 plans correctly left them untouched.

### make ci Pre-Existing Failures (Go-side)

Also confirmed as carry-forward from `deferred-items.md` items #1, #3, #7, #9:
- `internal/tuitest/smoke_test.go:182,224` errcheck on os.MkdirAll
- `internal/ui/branch_picker.go:18` unused branchPickerResultMsg
- `internal/ui/home.go:63` unused isCreatingPlaceholder
- `cmd/agent-deck/main.go:458` SA4006 args never used
- 6 tests: `TestSyncSessionIDsFromTmux_{Claude,AllTools,OverwriteWithNew}`, `TestInstance_{GetSessionIDFromTmux,UpdateClaudeSession_TmuxFirst,UpdateClaudeSession_RejectZombie}` all fail with `SetEnvironment failed: exit status 1`
- `TestSmoke_QuitExitsCleanly` + `TestSmoke_TUIRenders` (TUI flakes)

Plan 09-04 touches zero Go files. All Phase 9 plans confirmed these failures reproduce on baseline. Not Phase 9 regressions.

## Anti-Patterns Scanned (Files Modified)

| File | Lines Changed | Anti-patterns |
|------|---------------|---------------|
| state.js | 144 | None — single signal append at tail per 06-05 rule |
| main.js | 63, 93 | None — dual flip with explicit catch-branch non-flip (intentional offline behavior) |
| SessionList.js | 126-138 | None — skeleton gate above existing empty-state check; 8 placeholder rows |
| GroupRow.js | 97, 117 | None — pure class list swap, dark variants preserved |
| ProfileDropdown.js | 38, 103 | None — filter runs before single-vs-multi branch; WEB-P0-2 Option B scaffolding preserved |
| CostDashboard.js | 14, 20, 132 | None — single formatter construction enforced by test |
| SessionRow.js | 114, 118 | None — text-gray-600, text-green-700; dark variants preserved |
| EmptyStateDashboard.js | 106, 130, 135 | None — 3x text-gray-600; dark variants preserved |
| SearchFilter.js | 49 | None — text-gray-600 resting, hover:text-gray-800 escalation |
| SettingsPanel.js | 32 | None — Loading state text-gray-600 |

**TODO/FIXME/HACK scan:** Zero new TODOs or FIXMEs introduced by Phase 9 plans. No `placeholder` string literals. No console.log-only implementations. No `return null` stubs.

## Human Verification Required

While all automated assertions pass, five items benefit from human-eye verification:

### 1. Light theme visual sanity on real displays

**Test:** Open agent-deck web on a real macOS Retina display, iPhone, iPad, and Linux monitor. Toggle light theme. Visually scan sidebar, topbar, cost dashboard, all dialog types (create/confirm/group-name/settings/keyboard-shortcuts/toast-history), and toast variants.

**Expected:** Premium feel — no washed-out text, no missing borders, no contrast fatigue. The WCAG AA math is correct, but the final "does it look premium" judgment is subjective.

**Why human:** axe-core + luminance math pass by construction, but the subjective "premium" feel (Linear/Vercel polish) requires human-eye review on physical displays with varying gamma curves.

### 2. Skeleton loader perceived latency

**Test:** Open agent-deck web on a throttled network (Chrome DevTools → Slow 3G) and reload. Watch the cold-load sequence.

**Expected:** Skeleton pulse appears immediately (≤16ms). No "No sessions" flash before real data arrives. Skeleton → real sidebar transition is visually smooth, not jarring.

**Why human:** Playwright DOM tests stall `/api/menu` with `page.route` to simulate latency. Real network characteristics, SSE reconnect behavior, and SW fetch interception may differ.

### 3. GroupRow 120ms fade feel

**Test:** Hover over a group row in the sidebar. Observe action cluster fade in. Unhover. Observe fade out. Repeat with a focused inner button (keyboard focus).

**Expected:** Smooth 120ms fade, not snappy, not laggy. `prefers-reduced-motion: reduce` (macOS System Preferences → Accessibility → Display → Reduce motion) makes it instant.

**Why human:** DOM tests verify computed `transition-property: opacity` and `transition-duration: 0.12s` but cannot judge subjective animation quality on real GPU compositing.

### 4. Locale formatting on real browsers

**Test:** Open agent-deck web in a Chrome/Firefox/Safari instance configured for en-US. Navigate to Cost dashboard. Repeat with de-DE locale (browser settings → language → Deutsch). Repeat with ja-JP.

**Expected:**
- en-US: `$1,234.56`
- de-DE: `1.234,56 $` (or `1.234,56 US$` depending on ICU version)
- ja-JP: contains digits + `$` + Japanese-appropriate separators

**Why human:** Playwright forces locale via Chromium launch flags; real browser `navigator.language` resolution and ICU data may produce slightly different output. Visual inspection confirms the chart y-axis tick labels also render in the same locale.

### 5. Profile dropdown scroll with 20+ real profiles

**Test:** Configure a dev machine with 20+ agent-deck profiles (some with `_*` prefix). Open the profile dropdown in the topbar.

**Expected:** `_*` profiles hidden. Listbox scrolls within 300px max-height. Listbox does not push the viewport off-screen on short displays (e.g., iPhone SE 375×667).

**Why human:** Playwright test uses a mocked 12-profile response. Real profile lists with 20-40 entries + interspersed `_*` profiles exercise the filter AND scroll simultaneously.

## Gaps Summary

**None.** All must-haves verified. All 4 plans shipped. All 7 POL requirements satisfied. All 62 Playwright assertions pass. All 23 commits clean (zero Claude attribution). Git status clean. Regression gate does not block (pre-existing failures confirmed against baseline).

## Verdict

**PASSED.** Phase 9 polish plans 09-01 through 09-04 have achieved the phase goal. All 5 success criteria from ROADMAP.md are verified. All 7 POL-N requirements are satisfied. The final v1.5.0 layout is locked. Ready to hand off to Phase 10 (Automated Testing) for TEST-A visual baseline capture against the now-final Phase 9 surfaces.

---

*Verified: 2026-04-09T21:45:00Z*
*Verifier: Claude (gsd-verifier, Opus 4.6 1M)*
*Baseline commit: 5539ce3*
*Head commit: e3170de*
