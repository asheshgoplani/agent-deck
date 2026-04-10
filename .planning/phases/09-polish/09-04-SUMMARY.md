---
phase: 09-polish
plan: 04
subsystem: web-ui
tags: [pol-6, a11y, color-contrast, axe-core, light-theme, luminance, wcag-aa, tailwind-v4, playwright, tdd]

# Dependency graph
requires:
  - phase: 09-polish/01
    provides: "sessionsLoadedSignal + SessionList skeleton gate + GroupRow fade/density — final v1.5.0 sidebar layout for the audit to run against"
  - phase: 09-polish/02
    provides: "ProfileDropdown _*-filter + max-h-[300px] listbox scroll + CostDashboard Intl.NumberFormat(navigator.language) memoized formatter — final v1.5.0 profile dropdown and cost dashboard layout"
  - phase: 09-polish/03
    provides: "POL-7 regression guard on Toast.js + ToastHistoryDrawer.js — the guards fire if POL-6 accidentally breaks a POL-7 invariant"
  - phase: 08-performance/05
    provides: "PERF-H esbuild bundled dist/main.<hash>.js — drove the real-UI test pattern discovery (see Deviations)"
  - phase: 06-critical-p0-bugs/03
    provides: "deferred-items.md #5 (session list badges) — the load-bearing POL-6 target list"
  - phase: 06-critical-p0-bugs/04
    provides: "deferred-items.md #8 (drawer-axe underlying badges) + existing ToastHistoryDrawer timestamp fix (text-gray-400 → text-gray-600)"
provides:
  - "POL-6 light theme audit spec (axe-core + forced light theme via colorScheme: 'light' + localStorage theme seed) — 11 tests across every v1.5.0 surface"
  - "POL-6 targeted luminance spec (canvas-based getImageData color parsing — survives axe-core version bumps and handles Tailwind v4 OKLCH output) — 7 targeted checks"
  - "Fix batch: text-gray-400 → text-gray-600 and text-green-600 → text-green-700 across 8 component files; dark-theme variants preserved on every line"
  - "Resolution of deferred-items.md #5 and #8 with commit-trail annotations"
affects: [10-automated-testing/TEST-A visual baselines, 11-release/REL-1]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Real-UI driven test pattern: never mutate bundled signals via import('/static/app/state.js') — drive the bundled app via actual button clicks, keyboard presses, and localStorage seeds. PERF-H bundles state.js into closed-over vars; un-bundled imports create a parallel module instance that the DOM never sees."
    - "Forced-light theme dual mechanism: colorScheme: 'light' context option AND addInitScript setting localStorage.setItem('theme', 'light'). Defense in depth — if a future refactor switches theme storage, one layer still forces light."
    - "SSE /events/menu route aborted so Playwright waitForSelector('header') + 150ms settle can release — `networkidle` is unreachable with long-lived EventSource connections."
    - "Dialog scope narrowed to `.fixed.inset-0.z-50.bg-black\\/50` because Create/Confirm/GroupName render as plain divs without role='dialog' (pre-existing a11y gap outside POL-6's scope)."
    - "Canvas-based color parser via ctx.fillStyle + getImageData — converts any valid CSS color string (rgb, rgba, oklch, hsl, hex, named) to sRGB bytes in one line. Tailwind v4 emits oklch() which the initial regex-based parser rejected."
    - "Axe-core `runOnly: ['color-contrast']` scoped to specific includes (`#preact-session-list`, `[data-testid=\"empty-state-dashboard\"]`, `[role=\"dialog\"]`, etc.) to avoid catching pre-existing non-contrast violations."

key-files:
  created:
    - tests/e2e/pw-p9-plan4.config.mjs
    - tests/e2e/visual/p9-pol6-light-theme-audit.spec.ts
    - tests/e2e/visual/p9-pol6-light-theme-contrast.spec.ts
    - .planning/phases/09-polish/09-04-SUMMARY.md
  modified:
    - internal/web/static/app/SessionRow.js
    - internal/web/static/app/GroupRow.js
    - internal/web/static/app/SessionList.js
    - internal/web/static/app/EmptyStateDashboard.js
    - internal/web/static/app/CostDashboard.js
    - internal/web/static/app/ProfileDropdown.js
    - internal/web/static/app/SearchFilter.js
    - internal/web/static/app/SettingsPanel.js
    - internal/web/static/styles.css
    - .planning/phases/06-critical-p0-bugs/deferred-items.md

key-decisions:
  - "Bumped default gray from text-gray-400 → text-gray-600 (not text-gray-500) because v4 text-gray-500 on bg-white is 4.83:1 which passes axe but feels borderline; text-gray-600 at 7.5:1 gives comfortable headroom for any future background-color tweak."
  - "GroupRow header text: text-gray-500 → text-gray-700 rather than text-gray-600 because the background is bg-gray-50/50 (translucent), which composites to an effective #fcfdfd rather than pure white, lowering the contrast ratio slightly. text-gray-700 gives 7.72:1 on the composited bg."
  - "Cost badge (SessionRow): text-green-600 → text-green-700 was a bonus finding — text-green-600 is #00a63e which hits only 3.22:1 on pure white. text-green-700 gives 5.6:1 and still reads as green."
  - "Real-UI driven test pattern (not signal injection): when I discovered PERF-H bundling isolates state.js module instances, I rewrote T4/T6/T7/T8/T9/T10/T11/L2/L5/L6 to use actual button clicks, keyboard presses, and localStorage seeds. Zero test-only hooks in production code — every test flows through real user interactions."
  - "Dialog axe scope narrowed to `.fixed.inset-0.z-50.bg-black\\/50` instead of `[role=\"dialog\"]` because Create/Confirm/GroupName render as plain divs. Fixing the missing role is a11y refactor scope, not POL-6 color-contrast scope."
  - "Luminance spec uses canvas getImageData instead of regex parsing because Tailwind v4 emits oklch() in compiled CSS — a parser that only handled rgb() would silently fail on any v4-colored element."
  - "Kept computeContrastInPage as a stringified function passed to page.evaluate rather than a shared helper to avoid the Playwright serialization boundary. It's duplicated literally from the test file header, which is fine for a regression guard."

patterns-established:
  - "PERF-H signal isolation workaround: any Phase 9+ test that needs to drive state must use real UI, not import-and-mutate. Document this in Phase 10 TEST-A's test-infra guide so future authors don't rediscover it."
  - "Two-layer contrast audit: axe-core primary (rule coverage) + luminance secondary (targeted regression-resistant). Axe catches new violations; luminance locks in specific fixed elements so a future refactor can't silently regress them even if axe heuristics change."
  - "Dark-mode invariance rule: every POL-6 fix is a pure-light change (`text-gray-400` → `text-gray-600` without touching the `dark:text-tn-muted` sibling). Never add or remove dark variants unless the original class was unconditional."

requirements-completed: [POL-6]

# Metrics
duration: ~95min
completed: 2026-04-09
---

# Phase 9 Plan 04: POL-6 Light Theme Audit Summary

**Axe-core + luminance-based WCAG AA audit of every v1.5.0 light-theme surface, 8 component files fixed via `text-gray-400` → `text-gray-600` + one `text-green-600` → `text-green-700` bonus finding, zero dark-mode regressions**

## Performance

- **Duration:** ~95 min (heavy debug time on PERF-H signal-isolation discovery + OKLCH parser fix)
- **Started:** 2026-04-09T19:55:00Z
- **Completed:** 2026-04-09T21:30:00Z
- **Tasks:** 3 (discovery spec → per-file fixes → deferred-items close-out)
- **Tests added:** 18 (11 axe-core + 7 luminance), all green in ~17 seconds on final run
- **Commits:** 11 atomic (1 test baseline + 1 test revision + 7 fix + 1 chore + 1 docs)

## Discovery Pass (RED)

Initial run against tip of Wave 1 (commit `220de49`) with 18 fresh spec tests:

- **17 failed / 1 passed (3.1 minutes)** — effective RED state.
- **Primary fg color in violations:** `#99a1af` (Tailwind v4 `text-gray-400`) at 2.6:1 on `bg-white`, 2.55:1 on `bg-gray-50/50`, 2.48:1 on `bg-gray-50`.
- **Unique flagged class strings** extracted from axe violation output:
  1. `dark:text-tn-muted/60 text-gray-400 font-normal` — GroupRow count chip
  2. `px-sp-12 py-sp-16 dark:text-tn-muted text-gray-400 text-sm` — SessionList "No sessions"
  3. `text-sm dark:text-tn-muted/70 text-gray-400` — EmptyStateDashboard "No sessions yet"
  4. `text-xs dark:text-tn-muted text-gray-400 flex-shrink-0` — SessionRow tool label + EmptyStateDashboard recent status
  5. `text-xs dark:text-tn-muted text-gray-400 text-center` — EmptyStateDashboard keyboard hints

Also: SearchFilter placeholder button, CostDashboard summary card subtitles (`text-gray-400 mt-1` × 4), ProfileDropdown `(active)` label.

## GREEN Progression

| Run | Passed | Failed | Notes |
|---|---|---|---|
| 1 (discovery) | 1 | 17 | Baseline RED against current main (commit f0928dd) |
| 2 (after batch 1) | 5 | 13 | 8 source files edited (SessionRow/GroupRow/SessionList/EmptyState/Cost/Profile/SearchFilter/SettingsPanel), `make css` + `go generate` + rebuild. Remaining failures were test-infrastructure issues, not contrast. |
| 3 (after spec rewrite) | 14 | 4 | Rewrote audit specs to use real UI instead of signal injection. Discovered PERF-H bundling isolates module instances. |
| 4 (after selector + exact-match fixes) | 16 | 2 | Scoped sidebar rows to `#preact-session-list`, added `exact: true` to Confirm dialog button locator. |
| 5 (after bonus finding) | **18** | **0** | Fixed text-green-600 → text-green-700 on SessionRow cost badge after L2 luminance spec caught a 3.22:1 violation. |

Final run: **18 passed in 16.9s** against the production-equivalent build (`go1.24.0`, vcs.modified=false, PERF-H bundle embedded).

## Fix Batch (Source Files)

Every fix below is a **light-mode-only** change; `dark:*` variants are preserved untouched.

### SessionRow.js (commit `7f34792`)
- Line 114: `text-xs dark:text-tn-muted text-gray-400 flex-shrink-0` (tool label) → `text-gray-600` (2.6:1 → 7.5:1)
- Line 118: `text-xs dark:text-tn-green text-green-600 flex-shrink-0 font-mono` (cost badge) → `text-green-700` (**bonus finding**, 3.22:1 → 5.6:1)

### GroupRow.js (commit `2e5f152`)
- Line 98: `dark:text-tn-muted text-gray-500` (header text) → `text-gray-700` (effective 4.57:1 → 7.72:1 on composited `bg-gray-50/50`)
- Line 101: `hover:text-gray-700` → `hover:text-gray-900` (keeps hover state distinguishable now that resting state is already 700)
- Line 107: `dark:text-tn-muted/60 text-gray-400 font-normal` (count chip) → `text-gray-600` (2.55:1 → 6.85:1)

### SessionList.js (commit `d059c6e`)
- Line 161: `px-sp-12 py-sp-16 dark:text-tn-muted text-gray-400 text-sm` (No sessions placeholder) → `text-gray-600`

### EmptyStateDashboard.js (commit `13a68d8`)
- Line 106: `text-xs dark:text-tn-muted text-gray-400 flex-shrink-0` (recent list status) → `text-gray-600`
- Line 130: `text-xs dark:text-tn-muted text-gray-400 text-center` (keyboard hints paragraph) → `text-gray-600`
- Line 135: `text-sm dark:text-tn-muted/70 text-gray-400` (no sessions yet paragraph) → `text-gray-600`

### CostDashboard.js (commit `3bfa517`)
- Lines 225, 230, 235, 240: four `<div class="text-xs dark:text-tn-muted text-gray-400 mt-1">` summary card subtitles → `text-gray-600`
- Uppercase card headers (`text-gray-500`) and currency values (`text-teal-600` = 7.8:1) pass unchanged.

### ProfileDropdown.js (commit `f9970b3`)
- Line 116: `dark:text-tn-muted text-gray-400 ml-1` (the `(active)` marker next to current profile in listbox) → `text-gray-600`
- Non-active option rows (`text-gray-500` = 4.83:1) pass unchanged.
- Help text footer (`text-gray-500 italic` = 4.83:1) passes unchanged.
- WEB-P0-2 Option B invariants preserved: `role="status"`, `aria-haspopup="listbox"`, `HELP_TEXT` references, POL-3 `_*` filter, POL-3 `max-h-[300px]` listbox all untouched.

### SearchFilter.js + SettingsPanel.js (commit `fdc8bfa`)
- SearchFilter.js line 49: `text-gray-400 hover:text-gray-600` (collapsed-state placeholder button) → `text-gray-600 hover:text-gray-800` (resting state ≥ 4.5 now, hover state escalates to maintain visual distinction)
- SettingsPanel.js line 32: `text-xs dark:text-tn-muted text-gray-400` (transient Loading... state) → `text-gray-600`

### styles.css regeneration (commit `5380436`)
Tailwind v4 picked up the new `text-gray-600`, `text-gray-700`, `text-gray-800`, `text-gray-900`, `hover:text-gray-900`, and `text-green-700` utility class references. `make css` output is deterministic against the same source tree.

## Dark-Theme Regression Check

**All Wave 1 Phase 9 regression specs re-run after the fixes and remain green:**

- `pw-p9-plan1.config.mjs` (POL-1/POL-2/POL-4 sidebar + skeleton + density): **13 passed, 1 skipped** (same baseline as before).
- `pw-p9-plan2.config.mjs` (POL-3/POL-5 profile filter + currency locale): **21 passed, 3 skipped** (same baseline — skipped are intentional locale-scoped `test.skip()` calls).
- `pw-p9-plan3.config.mjs` (POL-7 structural regression guard): **10 passed** (all 10 assertions — Toast.js + ToastHistoryDrawer.js + state.js invariants intact).

**Dark-mode classes audited line-by-line:** zero `dark:*` classes modified, zero `dark:*` classes removed, zero new orphan light-mode classes introduced. Every fix is a pure sibling-class swap on the light variant only.

**Visual sanity check (light theme):** The tool label on session rows is now readable (gray-600 on white is 7.5:1, well above 4.5:1). The group count chips `(2)` are readable. The "No sessions" placeholder is readable. The empty-state keyboard hints are readable. The cost dashboard card subtitles ("5 events", "based on 7-day avg") are readable.

**Visual sanity check (dark theme):** Unchanged — every edit preserved the `dark:text-tn-muted` / `dark:text-tn-muted/60` / `dark:text-tn-muted/70` / `dark:text-tn-green` variant exactly as it was before.

## Bonus Findings

**Not pre-flagged in deferred-items.md:**

1. **SessionRow cost badge `text-green-600`** (fixed in `7f34792`). `#00a63e` on pure white only hits 3.22:1 — below WCAG AA. The luminance spec L2 caught this automatically. deferred-items.md #5 only called out the gray-400 badges; the green one slipped through because no earlier plan flagged it. Now fixed.
2. **SearchFilter collapsed-state button `text-gray-400`** (fixed in `fdc8bfa`). The sidebar's search placeholder button was at 2.6:1. Not pre-flagged but axe caught it in the main shell sweep.
3. **SettingsPanel Loading state `text-gray-400`** (fixed in `fdc8bfa`). Only visible during the brief /api/settings fetch window but still flagged. Pro-actively fixed since the file was already open.
4. **GroupRow header text `text-gray-500`** on composited `bg-gray-50/50`. Actually lands at 4.57:1 which axe 4.11 accepts as borderline pass, but I still escalated to `text-gray-700` for comfortable headroom since the effective luminance depends on compositing. Pre-emptive fix.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Playwright `networkidle` unreachable with SSE**
- **Found during:** Task 1 first discovery run (timed out after 3 minutes).
- **Issue:** `page.waitForLoadState('networkidle')` never resolves because the app opens `EventSource('/events/menu')` which keeps the network perpetually busy. Every audit test timed out at the 45-second timeout.
- **Fix:** Replaced with a two-step wait — `page.waitForSelector('header', { state: 'attached' })` + 150 ms settle, combined with `await page.route('**/events/menu*', r => r.abort())` in the mock endpoints helper. The SSE connection is killed before it can keep the network busy; the app's `EventSource` onerror handler gracefully marks the connection as disconnected and moves on.
- **Files modified:** `tests/e2e/visual/p9-pol6-light-theme-audit.spec.ts`, `tests/e2e/visual/p9-pol6-light-theme-contrast.spec.ts`
- **Committed in:** `2ac722c` (test revision commit, landed before the fix commits)

**2. [Rule 3 - Blocking] PERF-H bundled state.js module isolation**
- **Found during:** Task 2 (trying to drive `activeTabSignal = 'costs'` for the CostDashboard test — signal mutated but DOM never switched).
- **Root cause analysis:** Phase 8 plan 08-05 (PERF-H) ships a bundled `internal/web/static/dist/main.<hash>.js` where `state.js` is inlined into closed-over minified variables (`F = v("terminal")`). When Playwright calls `import('/static/app/state.js')` via `page.evaluate`, the browser loads the un-bundled source file, which creates a SECOND module instance with its own `activeTabSignal`. These two signal instances share the same `@preact/signals` constructor (via importmap) but are completely separate references — the bundled UI never sees mutations to the un-bundled signal.
- **Evidence:** A probe test showed `alert count: 0, unbundled signal length: 1` after pushing a toast via the un-bundled `addToast()`. The bundled app's `<div role="alert">` never materialized.
- **Fix:** Rewrote every test that needed to drive app state to use **real UI interactions** instead of signal injection:
  - T4 CostDashboard: click the real `button[title="Cost Dashboard"]` in the Topbar.
  - T6 CreateSessionDialog: click `New Session` button in EmptyStateDashboard (driven by an empty-menu fixture).
  - T7 ConfirmDialog: hover a session row, click its Delete icon.
  - T8 GroupNameDialog: hover a group, click its Rename icon.
  - T9 KeyboardShortcutsOverlay: focus body + press `Shift+Slash` (the `?` hotkey).
  - T10 ToastHistoryDrawer: seed `localStorage['agentdeck_toast_history']` in `addInitScript`, then click the toggle button — the drawer reads from localStorage on mount so this works without touching the bundled signal.
  - T11 error toast: mock `DELETE /api/sessions/*` to return 500, then flow through the real delete → confirm → mutation → error toast chain.
  - L2 cost badge: mock `POST /api/costs/batch` with the correct `{ costs: { s1: 1.23, ... } }` shape so the bundled SessionList renders the cost badge via its own render path.
  - L5, L6 mirrored T4 and T10.
- **Impact:** Test implementation changed significantly, but production code is untouched. The test pattern is now the recommended approach for all Phase 9+ specs that need to drive app state. Noted in the SUMMARY's "patterns-established" section.
- **Committed in:** `2ac722c`

**3. [Rule 3 - Blocking] Luminance parser rejected Tailwind v4 oklch()**
- **Found during:** Task 2 (L7 empty-state body text test reported `cannot parse fg color: oklch(0.446 0.03 256.802)`).
- **Issue:** My initial `parseRgb()` helper only matched `rgba?\((\d+)...)`. Tailwind v4 compiles gray utilities to OKLCH (`oklch(44.6% .03 256.802)` for `text-gray-600`), which Chromium's `getComputedStyle().color` preserves as-is rather than normalizing to RGB.
- **Fix:** Replaced the regex-based parser with a canvas-based approach: `ctx.fillStyle = cssColor; ctx.fillRect(0, 0, 1, 1); ctx.getImageData(0, 0, 1, 1).data` — the browser's native color parser accepts any valid CSS color (oklch, rgb, rgba, hsl, hex, named) and the `getImageData` readback always returns sRGB bytes. This is one of the cleanest tricks for CSS color normalization in browser-side JavaScript.
- **Files modified:** `tests/e2e/visual/p9-pol6-light-theme-contrast.spec.ts` (the `computeContrastInPage` string)
- **Committed in:** `2ac722c`

**4. [Rule 2 - Missing critical functionality] Dialog role="dialog" missing on 3 modals**
- **Found during:** Task 2 re-running audit after spec rewrite.
- **Issue:** My T6/T7/T8 tests initially waited for `[role="dialog"]` but `CreateSessionDialog.js`, `ConfirmDialog.js`, and `GroupNameDialog.js` render as plain `<div class="fixed inset-0 z-50 bg-black/50">` containers with **no ARIA role**. This is a pre-existing a11y gap that predates Phase 6 — none of the 2025-era plans added `role="dialog" aria-modal="true"` to these three components (only `ToastHistoryDrawer.js` and `KeyboardShortcutsOverlay.js` got the role attribute).
- **Fix:** For THIS plan's POL-6 color-contrast scope, narrow the test selector to `.fixed.inset-0.z-50.bg-black\\/50` (matches all three plain-div dialogs) and pass to AxeBuilder. The missing `role="dialog"` is an a11y refactor — out of scope for color-contrast. Documented as such in the spec's inline comment and in the "Issues Encountered" section below.
- **Committed in:** `2ac722c` (the spec revision). No production code touched.

**Total deviations:** 4 auto-fixed (3× Rule 3 Blocking + 1× Rule 2 Missing). Zero architectural (Rule 4) decisions; zero scope creep.

## Issues Encountered

### 1. Pre-existing `make ci` failures persist (carry-forward from deferred-items.md #1, #3, #7, #9)

Plan 09-04 touches zero Go files. All previously deferred lint + test failures reproduce on the current commit. No new failures introduced.

### 2. `role="dialog"` missing on Create/Confirm/GroupName dialogs (a11y refactor, out of scope for POL-6)

Three modal dialogs render as plain `<div class="fixed inset-0 z-50 bg-black/50">` without `role="dialog"` or `aria-modal="true"`. Screen readers cannot announce them as modals. This is **pre-existing structural a11y debt**, not introduced or fixable by plan 09-04 (which is scoped to color-contrast only per POL-6's charter).

**Recommended owner:** a dedicated a11y refactor plan in Phase 10 or later. The fix is small (add `role="dialog" aria-modal="true" aria-labelledby="..."` to each container + hook Esc-to-close if not already wired), but it needs its own regression spec + a11y verification that screen readers announce the dialogs correctly.

### 3. p6-bug4-a11y.spec.ts partial failures on the current bundled build

The Phase 6 plan 04 a11y spec uses the `import('/static/app/Toast.js')` + `mod.addToast(...)` pattern that I discovered doesn't work with PERF-H bundling. 5 of 6 tests in that spec fail with DOM-not-found errors. This is pre-existing — the spec was written before PERF-H bundled the app (commit `5539ce3` on 2026-04-09 bundled the assets; the p6-bug4 spec was written earlier), so the test itself needs updating to use real UI interactions. Plan 09-04 does not touch that spec (out of scope), but the POL-7 regression guard from 09-03 (which uses readFileSync + regex — a different, bundle-agnostic pattern) continues to pass 10/10.

**Recommended owner:** Phase 10 TEST-A test infrastructure work — update all a11y specs that use the signal-import pattern to use real UI interactions instead.

### 4. p6-bug2-a11y.spec.ts mobile touch target failure

Pre-existing: `profile-indicator must have a bounding box`. On mobile (375x667), the right-side Topbar controls (including ProfileDropdown) are collapsed into the WEB-P1-5 overflow `⋯` menu and are not visible until the menu opens. The p6-bug2 spec was written against the Phase 6 layout before WEB-P1-5 shipped the overflow menu in Phase 7. The `boundingBox()` returns null because the element is not rendered.

**Recommended owner:** same as #3 — a Phase 10 test infrastructure refresh should update this spec to open the overflow menu before checking the indicator's touch target. Not caused by plan 09-04.

## Verification

- [x] `cd tests/e2e && npx playwright test --config=pw-p9-plan4.config.mjs` — **18 passed / 0 failed** in 16.9s on final run (11 axe + 7 luminance)
- [x] `cd tests/e2e && npx playwright test --config=pw-p9-plan1.config.mjs` — **13 passed / 1 skipped** (09-01 regression suite unchanged)
- [x] `cd tests/e2e && npx playwright test --config=pw-p9-plan2.config.mjs` — **21 passed / 3 skipped** (09-02 regression suite unchanged)
- [x] `cd tests/e2e && npx playwright test --config=pw-p9-plan3.config.mjs` — **10 passed** (09-03 POL-7 guard still green)
- [x] `make build` — succeeds, `go version -m build/agent-deck | head -1` shows `go1.24.0`, vcs.modified unset (clean build)
- [x] `git diff --exit-code internal/web/static/styles.css` — clean after fresh `make css` (source-of-truth determinism)
- [x] `go vet ./...` — clean exit 0
- [x] `go test -race ./internal/web/...` — all tests pass (4.46s)
- [x] Dark theme visual sanity check — every `dark:*` variant preserved; no dark-mode contrast regressions
- [x] POL-6 regression guarded by 18 tests across 2 spec files (axe-core + luminance, two independent detection mechanisms)
- [x] deferred-items.md #5 and #8 annotated with RESOLVED markers
- [x] Commit log TDD order: `test(09-04)` → `test(09-04)` revision → series of `fix(09-04)` → `chore(09-04)` styles regen → `docs(09-04)` deferred-items update
- [x] No Claude attribution: `git log --format=%B 220de49..HEAD | grep -ciE 'claude|co-authored-by'` → **0**
- [x] No push, no tag, no PR, no merge — all 11 commits stay local on main per HARD RULES
- [x] No `rm` — used `trash` for probe cleanup

## Commits

1. `f0928dd` **test(09-04): add POL-6 light theme audit spec (discovery pass)**  —  3 files created: `pw-p9-plan4.config.mjs` (forced-light Playwright config), `p9-pol6-light-theme-audit.spec.ts` (11 axe-core tests), `p9-pol6-light-theme-contrast.spec.ts` (7 luminance tests). Initial RED: 1/18 passed.
2. `2ac722c` **test(09-04): drive POL-6 audit specs via real UI, not isolated signals**  —  rewrote T4/T6/T7/T8/T9/T10/T11/L2/L5/L6 to use real button clicks, keyboard presses, localStorage seeds, and mocked failing mutations instead of `import('/static/app/state.js')` signal injection (PERF-H bundling breaks the latter). Also switched `computeContrastInPage` to canvas getImageData-based CSS color parsing to handle Tailwind v4 OKLCH output.
3. `7f34792` **fix(09-04): POL-6 SessionRow tool label + cost badge contrast**  —  `text-gray-400` → `text-gray-600` on tool label span (line 114); `text-green-600` → `text-green-700` on cost badge (line 118, bonus finding from luminance spec L2).
4. `2e5f152` **fix(09-04): POL-6 GroupRow header + count chip contrast**  —  `text-gray-500` → `text-gray-700` on header text, `hover:text-gray-700` → `hover:text-gray-900`, `text-gray-400` → `text-gray-600` on count chip span.
5. `d059c6e` **fix(09-04): POL-6 SessionList "No sessions" empty state contrast**  —  `text-gray-400` → `text-gray-600` on the empty-state placeholder text (line 161, post skeleton gate).
6. `13a68d8` **fix(09-04): POL-6 EmptyStateDashboard body text contrast**  —  three `text-gray-400` → `text-gray-600` swaps: recent-list status label, keyboard hints paragraph, "No sessions yet" paragraph (preserves `dark:text-tn-muted/70` variant).
7. `3bfa517` **fix(09-04): POL-6 CostDashboard summary card subtitle contrast**  —  four `text-gray-400 mt-1` → `text-gray-600 mt-1` swaps on Today / This Week / This Month / Projected card subtitles.
8. `f9970b3` **fix(09-04): POL-6 ProfileDropdown (active) label contrast**  —  `text-gray-400 ml-1` → `text-gray-600 ml-1` on the inline `(active)` marker span. WEB-P0-2 Option B + POL-3 invariants preserved.
9. `fdc8bfa` **fix(09-04): POL-6 SearchFilter placeholder + SettingsPanel loading text**  —  SearchFilter collapsed-state button `text-gray-400 hover:text-gray-600` → `text-gray-600 hover:text-gray-800`; SettingsPanel `Loading...` transient text → `text-gray-600`.
10. `5380436` **chore(09-04): regenerate styles.css after POL-6 fixes**  —  `make css` + `go generate ./internal/web/` regenerate the minified Tailwind output with new utility references.
11. `46aac79` **docs(09-04): close deferred items #5 and #8 after POL-6 audit**  —  force-added `.planning/phases/06-critical-p0-bugs/deferred-items.md` with RESOLVED annotations on items #5 (session list badges) and #8 (drawer-axe underlying badges). Original entries preserved.

## Phase 9 Close-Out

Plan 09-04 is the final plan in Phase 9. All 7 POL requirements are now complete:

| Req | Description | Delivered |
|-----|-------------|-----------|
| POL-1 | Sidebar skeleton loader (replaces empty-state flicker) | Plan 09-01 ✓ |
| POL-2 | GroupRow action cluster 120ms opacity fade | Plan 09-01 ✓ |
| POL-3 | Profile dropdown `_*` filter + max-h-[300px] listbox scroll | Plan 09-02 ✓ |
| POL-4 | GroupRow density reduction (py-1 / min-h-[40px]) | Plan 09-01 ✓ |
| POL-5 | CostDashboard locale-aware Intl.NumberFormat(navigator.language) | Plan 09-02 ✓ |
| **POL-6** | **Light theme contrast audit — zero WCAG AA violations** | **Plan 09-04 ✓ (this plan)** |
| POL-7 | Toast stack cap + history drawer | Shipped in Phase 6 plan 04, traceability + regression guard in plan 09-03 ✓ |

**The final v1.5.0 layout is locked.** Any future layout change must re-run `pw-p9-plan4.config.mjs` to reconfirm contrast invariants.

## Next Phase Readiness

- **Phase 10 TEST-A (visual baselines):** Capture screenshots on the final, fully-polished light theme now that POL-6 fixes have landed. The regression-guarded elements (session row tool label, cost badge, group count chip, profile option, cost subtitle, drawer timestamp, empty-state body text) are all at known luminance ratios that TEST-A can snapshot as stable references.
- **Phase 10 TEST-A (a11y spec modernization):** The p6-bug4-a11y and p6-bug2-a11y specs need updating to work with PERF-H bundling (per Issues #3 and #4). Plan 09-04's spec rewrite (commit `2ac722c`) is a direct template for how to port them. Real-UI interactions, localStorage seeding, and failing-mutation mocks are all demonstrated.
- **Phase 11 REL-1 (v1.5.0 release):** Light theme is now WCAG AA compliant for color-contrast across all rendered surfaces. The release notes can honestly claim "v1.5.0 ships a fully polished light theme with zero WCAG AA color-contrast violations, guarded by 18 Playwright regression tests."

## Self-Check: PASSED

**Files created (all present):**
- `tests/e2e/pw-p9-plan4.config.mjs` — FOUND (42 lines, includes colorScheme: 'light' + serviceWorkers: 'block')
- `tests/e2e/visual/p9-pol6-light-theme-audit.spec.ts` — FOUND (11 axe-core tests)
- `tests/e2e/visual/p9-pol6-light-theme-contrast.spec.ts` — FOUND (7 luminance tests)
- `.planning/phases/09-polish/09-04-SUMMARY.md` — FOUND (this file)

**Files modified (all reflect the POL-6 fix):**
- `internal/web/static/app/SessionRow.js` — contains `text-gray-600` and `text-green-700`; no `text-gray-400` or `text-green-600` on text spans
- `internal/web/static/app/GroupRow.js` — contains `text-gray-700` (header) and `text-gray-600` (count)
- `internal/web/static/app/SessionList.js` — contains `text-gray-600 text-sm` on empty-state div
- `internal/web/static/app/EmptyStateDashboard.js` — contains `text-gray-600` on recent-status, keyboard hints, no-sessions-yet
- `internal/web/static/app/CostDashboard.js` — four `text-gray-600 mt-1` subtitles
- `internal/web/static/app/ProfileDropdown.js` — `(active)` label uses `text-gray-600`
- `internal/web/static/app/SearchFilter.js` — `text-gray-600` resting state
- `internal/web/static/app/SettingsPanel.js` — Loading state uses `text-gray-600`
- `internal/web/static/styles.css` — regenerated, contains `.text-gray-600`, `.text-gray-700`, `.text-gray-900`, `text-green-700` utilities
- `.planning/phases/06-critical-p0-bugs/deferred-items.md` — contains "RESOLVED 2026-04-09 (Phase 9 plan 04 POL-6 audit)" on items #5 and #8

**Commits verified (all 11 reachable from HEAD):**
- `f0928dd` test(09-04): add POL-6 light theme audit spec (discovery pass)
- `2ac722c` test(09-04): drive POL-6 audit specs via real UI, not isolated signals
- `7f34792` fix(09-04): POL-6 SessionRow tool label + cost badge contrast
- `2e5f152` fix(09-04): POL-6 GroupRow header + count chip contrast
- `d059c6e` fix(09-04): POL-6 SessionList "No sessions" empty state contrast
- `13a68d8` fix(09-04): POL-6 EmptyStateDashboard body text contrast
- `3bfa517` fix(09-04): POL-6 CostDashboard summary card subtitle contrast
- `f9970b3` fix(09-04): POL-6 ProfileDropdown (active) label contrast
- `fdc8bfa` fix(09-04): POL-6 SearchFilter placeholder + SettingsPanel loading text
- `5380436` chore(09-04): regenerate styles.css after POL-6 fixes
- `46aac79` docs(09-04): close deferred items #5 and #8 after POL-6 audit

**Claude attribution count:** 0 (verified `git log --format=%B 220de49..HEAD | grep -ciE 'claude|co-authored-by'` → 0)

**TDD order:** test commit (f0928dd) precedes all fix commits ✓

**Go toolchain:** go1.24.0 confirmed via `go version -m build/agent-deck` ✓

**Dark theme regressions:** zero (every fix preserves its `dark:*` sibling class unchanged) ✓

---

*Phase: 09-polish*
*Plan: 04*
*Completed: 2026-04-09*
*Phase 9 CLOSED — all 4 plans shipped, all 7 POL requirements complete*
