---
gsd_state_version: 1.0
milestone: v1.5
milestone_name: milestone
status: unknown
stopped_at: Phase 10 plan 10-04 COMPLETE — TEST-E weekly regression alerting (alert-only workflow, issue template, format validator, 7 edge case tests); 3 atomic commits + 1 metadata commit
last_updated: "2026-04-10T03:07:00.000Z"
progress:
  total_phases: 7
  completed_phases: 4
  total_plans: 22
  completed_plans: 17
---

# Project State

## Project Reference

**Project:** Agent Deck
**Repository:** /home/ashesh-goplani/agent-deck
**Branch:** main
**Current version:** v1.4.1
**Target version:** v1.5.0

See `/home/ashesh-goplani/agent-deck/.planning/PROJECT.md` for full project context.
See `/home/ashesh-goplani/agent-deck/.planning/ROADMAP.md` for the full v1.5.0 roadmap.
See `/home/ashesh-goplani/agent-deck/.planning/REQUIREMENTS.md` for the 43 requirements and their phase mappings.

## Milestone: v1.5.0 — Premium Web App

**Goal:** Ship a premium-quality v1.5.0 where the web app feels instant and snappy, all 4 remaining P0 bugs + 5 P1 bugs are fixed, first-load wire size drops from 668 KB to <150 KB gzipped, FCP<500ms / LCP<1s / TBT<100ms, automated visual regression tests block merge on >0.1% diff, and mobile is fully functional. The app should represent what agent-deck deserves: premium, polished, production-ready.

**Source spec:** `docs/WEB-APP-V15-SPEC.md`
**Starting point:** v1.4.1 (emergency patch that fixed 6 regressions shipped in v1.4.0)

## Current Position

Phase: 10 (automated-testing) — COMPLETE
Plan: 4 of 4 DONE

## Phase Progress

| # | Phase | Status | Requirements | Plans |
|---|-------|--------|--------------|-------|
| 5 | Critical Regressions | COMPLETE (shipped in v1.4.1) | 6 (REG-01..06 ✓) | — |
| 6 | Web App Critical P0 Bugs | COMPLETE 2026-04-08 (5/5 plans, WEB-P0-1 ✓ WEB-P0-2 ✓ WEB-P0-3 ✓ WEB-P0-4 mitigation ✓ WEB-P0-4 prevention ✓ + POL-7 ✓) | 4 (WEB-P0-1..4 all ✓) | 5 |
| 7 | Web App P1 Layout Bugs | COMPLETE 2026-04-09 (4/4 plans, WEB-P1-1 ✓ WEB-P1-2 ✓ WEB-P1-3 ✓ WEB-P1-4 ✓ WEB-P1-5 ✓) | 5 (WEB-P1-1..5 all ✓) | 4 |
| 8 | Performance (Premium Feel) | COMPLETE 2026-04-09 (5/5 plans, PERF-A..K all ✓) | 11 (PERF-A..K) | 5 |
| 9 | Polish (Premium UX) | COMPLETE 2026-04-09 (4/4 plans, POL-1 ✓ POL-2 ✓ POL-3 ✓ POL-4 ✓ POL-5 ✓ POL-6 ✓ POL-7 ✓) | 7 (POL-1..7 all ✓) | 4 |
| 10 | Automated Testing | COMPLETE 2026-04-10 (4/4 plans, TEST-A ✓ TEST-B ✓ TEST-C ✓ TEST-D ✓ TEST-E ✓) | 5 (TEST-A..E all ✓) | 4 |
| 11 | Release v1.5.0 | Not started | 5 (REL-1..5) | 3 |

**Total active plans across Phases 6-11:** 24
**Total active requirements:** 37

## Accumulated Context

### v1.5.0 Milestone Init (2026-04-08)

- Previous milestone (v1.4.0) shipped with 4 phases executed. v1.4.1 was an emergency patch for 6 regressions. Post-v1.4.1 user testing surfaced that 4 P0 bugs from v1.3.4 audit were NOT actually fixed in v1.4.0.
- v1.5.0 scope: 43 requirements total — 6 pre-complete (Phase 5, v1.4.1), 37 active (4 P0, 5 P1, 11 perf, 7 polish, 5 testing, 5 release).
- Research (`/.planning/research/SUMMARY.md`) completed with HIGH confidence overall. Stack locked: 2 new Go dependencies only (`klauspost/compress/gzhttp`, `esbuild/pkg/api`). Everything else is hand-roll or deletion.
- Roadmap (`/.planning/ROADMAP.md`) created 2026-04-08 with 7 phases, 24 plans, 100% requirement coverage.

### Plan 06-01 Complete (2026-04-08)

- **WEB-P0-2 decision gate closed: Option B ships.** Verified that `server.go:79` binds `cfg.Profile` once at `NewServer()` time with no per-request profile resolver, so Option A (query-string reload) is infeasible without re-architecting profile isolation (explicitly OUT OF SCOPE per REQUIREMENTS.md line 121).
- **Fix shipped:** `internal/web/static/app/ProfileDropdown.js` rewritten — single profile renders `<div role="status">` (screen-reader-correct), multi profile renders a non-interactive listbox with always-visible "Switch profiles by restarting agent-deck with -p <name>" help text.
- **TDD ordering verified:** `test(06-01)` commit `e68eeef` → `fix(06-01)` commit `7b39232` → `test(06-01) a11y` commit `285a9bd`. Regression spec + a11y spec (axe-core, keyboard, 44px touch target) both green.
- **Phase 6 Wave 2 unblocked.** Plans 06-02 (hamburger z-index), 06-03 (action toolbar), 06-04 (toast cap + POL-7) can now run in parallel. Plan 06-05 (mutations-gating) stays in Wave 3.
- **Deferred items logged** for pre-existing `internal/session` tmux test failures, `TestSmoke_QuitExitsCleanly`, and 5 golangci-lint errors in unrelated files — see `.planning/phases/06-critical-p0-bugs/deferred-items.md`.

### Plan 06-02 Complete (2026-04-08)

- **WEB-P0-1 fixed:** mobile hamburger now clickable at 375x667 and all sub-1024px viewports. Runtime proof at RED (Playwright click timeout message: "Costs button from right-side subtree intercepts pointer events") and GREEN (`elementFromPoint` at hamburger center resolves to the hamburger, click flips `aria-expanded`).
- **Systematic 7-level z-index scale shipped.** Named tokens in `styles.src.css` @theme block: `--z-index-base/sticky/dropdown/topbar/topbar-primary/modal/toast` drive Tailwind v4 utility generation. Mirrored `--z-*` aliases serve as structural-test anchors and human-readable documentation.
- **Tailwind v4 theme namespace discovery:** the z-index utility is driven by `--z-index-*`, NOT `--z-*` (the latter produces zero utilities). The plan's 06-CONTEXT.md locked in `--z-*` tokens verbatim — I verified empirically by building a minimal test fixture and shipped both namespaces so Tailwind generates utilities AND the structural tests pass as written. Documented in the @theme block comment for future maintainers.
- **Class migrations:** `Topbar.js` hamburger `<button>` gains `relative z-topbar-primary pointer-events-auto`; right-side controls wrapper gains `relative z-topbar`; `<header>` demoted from `z-50` to `z-sticky`. `Toast.js` `z-[100]` → `z-toast`. `ProfileDropdown.js` multi-profile listbox adds `z-dropdown`.
- **TDD ordering verified:** `test(06-02)` commit `914a9ff` → `fix(06-02)` commit `8f466c8` → `test(06-02) a11y` commit `432ea9d`. 9/9 regression tests + 4/4 a11y tests pass.
- **Workflow note:** test server MUST be rebuilt (`make build`) AND restarted after web static edits — `internal/web/static/*` is embedded into the Go binary via `embed.FS`, so a running server serves a frozen asset snapshot until it's replaced.
- **Phase 7 WEB-P1-5 unblocked** per STATE.md ordering constraint #3 (topbar z-index fix is prerequisite for mobile overflow menu).
- **Deferred items appended:** `TestSmoke_TUIRenders` pre-existing profile-state pollution (verified on baseline `2e0520f` with `go clean -testcache`) and `vcs.modified=true` warning from parallel Wave 2 dirty worktree. Neither caused by plan 06-02 edits.
- **Parallel execution note:** plan 06-03's commit `278e136` picked up my in-flight regenerated `styles.css` because 06-03 ran `make css` while my `styles.src.css` edits were live on disk. The source of truth (`styles.src.css`) still ships in my commit `8f466c8`; `make css && git diff --exit-code internal/web/static/styles.css` is clean.

### Plan 06-05 Complete (2026-04-08) — Phase 6 CLOSED

- **WEB-P0-4 prevention layer shipped.** mutationsEnabledSignal seeded optimistically in `state.js` (appended after `toastHistoryOpenSignal`), `AppShell.js` fetches `/api/settings` on mount and assigns `mutationsEnabledSignal.value` from `webMutations`. Network failures keep the optimistic default so the UI is not locked out on transient errors. Confirmed end-to-end against the test server running with `webMutations=false` — the write UI correctly disappears.
- **SessionRow toolbar gated + lock indicator.** The entire `<div role="toolbar">` from 06-03 is wrapped in `${mutationsEnabled && html`...`}` (short-circuit removes it from the DOM, no orphan ARIA). A small read-only lock icon (`aria-label="Read-only"`) renders between the cost label and the (hidden) toolbar when `!mutationsEnabledSignal.value`.
- **CreateSessionDialog dual-gated.** Early-returns `null` after the useState hooks (rules-of-hooks compliance; matches PushControls.js / SystemStats.js pattern), AND the submit button's `disabled` prop becomes `${submitting || !mutationsEnabledSignal.value}` as belt-and-braces.
- **TDD ordering verified:** `test(06-05)` commit `f582929` → `fix(06-05)` commit `52497f3` → `test(06-05)` non-regression `34b88bd` → `test(06-05)` a11y + cross-plan fix `515c318`. 11/11 mutations-gating tests (10 gating + 1 non-regression) + 6/6 a11y tests pass.
- **Cross-plan test isolation fix.** 06-03's p6-bug3 DOM specs (2 tests) and p6-bug3 a11y specs (4 tests) broke against the `webMutations=false` test server because they assumed the 06-03 toolbar was unconditionally in the DOM. Rather than reconfiguring the server, added an `await page.evaluate(() => state.mutationsEnabledSignal.value = true)` preamble to each affected test. Both 06-03 specs plus p6-bug1 (9/9), p6-bug1-a11y (4/4), p6-bug2 (6/6), p6-bug2-a11y (3/3), p6-bug4 toast-cap (13/13), p6-bug4-a11y (6/6), p0-bug3 (6/6) all remain green.
- **`make ci` failures are all pre-existing** (carry-forward from items #1, #2, #3, #7 in deferred-items.md). Same 5 lint failures + 6 session test failures + 1 TUI smoke test failure. None touch plan 06-05's files. Logged as item #9 in deferred-items.md; item #10 documents the cross-plan test isolation decision.
- **Phase 6 CLOSED.** All 4 P0 bugs (WEB-P0-1 hamburger ✓, WEB-P0-2 profile switcher ✓, WEB-P0-3 title truncation ✓, WEB-P0-4 toast spam ✓ with BOTH mitigation and prevention layers) shipped across 5 plans. POL-7 shipped early in 06-04 per ordering constraint #8.
- **State.js interface final form for Phase 7+:** `toastsSignal`, `shortcutsOverlaySignal`, `toastHistorySignal`, `toastHistoryOpenSignal`, `mutationsEnabledSignal` — all additive-only tail appends from plans 06-04 and 06-05. Any future plan that needs a new signal should append after `mutationsEnabledSignal`.

### Plan 06-04 Complete (2026-04-08)

- **WEB-P0-4 mitigation layer + POL-7 shipped in a single PR.** `Toast.js` rewritten with eviction-with-history pattern: visible stack capped at 3 (literal `next.length > 3` per Task 1 regex), oldest non-error evicted first, errors evicted FIFO only when all 3 visible are errors, errors no longer auto-dismiss, info / success keep 5s `setTimeout`. Dismissed toasts push into `toastHistorySignal` (capped at 50 via `slice(-50)`) persisted to `localStorage` key `agentdeck_toast_history`.
- **New `ToastHistoryDrawer.js` component** exports `ToastHistoryDrawer` (`role="dialog" aria-modal="true"` panel listing history newest-first, errors highlighted) AND `ToastHistoryDrawerToggle` (clock icon + entry count, 44x44 touch target, `data-testid="toast-history-toggle"`). Mounted by `AppShell.js` next to `ToastContainer`. Toggle button slotted into `Topbar.js` right-side `z-topbar` wrapper between Info and PushControls (does NOT touch the hamburger stacking classes from 06-02).
- **ARIA live region split** by severity: errors get `role="alert" aria-live="assertive"` (interrupts screen readers), info / success get `role="status" aria-live="polite"` (queued). Verified by Playwright a11y test.
- **Tailwind v4 token-vs-utility distinction discovered.** The `--z-index-modal: 50` token existed in `styles.src.css` after 06-02, but `.z-modal` was NOT in compiled `styles.css` because no source consumed it. Tailwind v4 only emits utilities for tokens actually USED in JS. Once `ToastHistoryDrawer.js` referenced `z-modal`, `make css` regenerated styles.css with the rule. Documented in summary's "Issues Encountered".
- **TDD ordering verified:** `test(06-04)` commit `80fea0d` → `feat(06-04)` `d3b4f35` (state.js signals) → `fix(06-04)` `aa1c974` (Toast.js refactor) → `feat(06-04)` `a7f2548` (drawer + Topbar + AppShell) → `test(06-04)` `cf8322e` (a11y spec + inline contrast fix). 13/13 regression tests + 6/6 a11y tests pass.
- **Inline a11y fix on the brand-new component:** `text-gray-400` → `text-gray-600` on the drawer history row timestamp (~2.6:1 → ~5.7:1 contrast). Pre-existing badge contrast violations elsewhere remain POL-6 territory in Phase 9.
- **Embedded asset reload friction worth documenting:** the test server on port 18420 serves a frozen `embed.FS` snapshot from the Go binary; required `make build` + kill PID + restart server twice during this plan (after Task 3 and Task 4) so Playwright DOM tests reflected new code.
- **State.js interface for 06-05:** all edits are PURELY ADDITIVE — `toastHistorySignal` and `toastHistoryOpenSignal` appended after `shortcutsOverlaySignal`. Plan 06-05 should append `mutationsEnabledSignal` after `toastHistoryOpenSignal` for clean merging. SessionRow.js was NOT touched (per scope boundary), so 06-05's plan to gate the absolute-positioned toolbar from 06-03 is unblocked.
- **POL-7 marked complete in this phase.** Phase 9 POL-7 entry can be marked done without re-implementation work.
- **Deferred items appended (item #7):** the same 5 lint failures + 6 + 1 test failures from items #1, #2, #3 reproduce on plan 06-04's baseline `a7f2548` (verified by stashing and re-running `make ci`). All in files plan 06-04 doesn't touch. Item #8 documents the axe drawer scope narrowing decision.

### Plan 06-03 Complete (2026-04-08)

- **WEB-P0-3 fixed:** session title truncation resolved by converting the SessionRow.js action button container from an in-flow `<span>` (that reserved 186px of flex space even when hidden via `opacity-0 pointer-events-none`) to an absolute-positioned `<div role="toolbar" aria-label="Session actions">` overlay. Measured title span ratio at 1280x800: **0.30 → 0.66 (82.3px → 184.3px of 279px row width, +124%)**.
- **Fix details:** outer button gains `relative` between `min-w-0` and `flex items-center`; toolbar uses `absolute right-2 top-1/2 -translate-y-1/2 flex items-center gap-0.5 transition-opacity duration-[120ms] motion-reduce:transition-none`; each of the 4 inner action buttons gains `focus-visible:opacity-100 focus-visible:pointer-events-auto` as a secondary defense. Title span class (`flex-1 truncate min-w-0`) is unchanged — now occupies full row width because the toolbar no longer participates in the flex layout.
- **Row height stability (PERF-K prereq satisfied):** row height stays at 44px regardless of toolbar visibility (measured via DOM). Since `position: absolute` removes the toolbar from the flex flow, the row only honors its own `min-h-[44px]` from the dot/title/badge baseline. Phase 8 virtualization can safely assume a stable row height.
- **Tailwind v4 cascade quirk discovered:** the pure-CSS `group-focus-within:opacity-100` utility loses the cascade to a plain `.opacity-0` sibling despite the expected higher specificity of `:is(:where(.group):focus-within *)`. Empirically verified via a cssRules walk + `matches()` check — both rules match, yet `.opacity-0` wins. Workaround: added a Preact `hasFocusWithin` state + `onFocus`/`onBlur` bubble handlers on the outer button; focus events bubble from inner buttons so a single handler pair catches all cases. The `group-focus-within:*` class is retained in source as a documentation anchor, but the state-based approach is the load-bearing mechanism.
- **TDD ordering verified:** `test(06-03)` commit `526d711` → `fix(06-03)` commit `278e136` → `test(06-03) a11y` commit `0840d88`. 11/11 regression tests (8 structural + 3 DOM) + 4/4 a11y tests pass.
- **Cross-plan regex relaxations:** `p0-bug3-session-name-width.spec.ts` and `p1-bug7-selected-indicator.spec.ts` had structural regexes requiring `min-w-0` to be immediately followed by `flex items-center`. My insertion of `relative` between them required relaxing both regexes to `/group w-full\s+min-w-0(?:\s+[-\w\[\]/:.]+)*\s+flex items-center/`. The BUG #3 / CRIT-03 and LAYT-04 invariants (both require `min-w-0` presence on the outer button) are still enforced. Both specs still pass 6/6.
- **Phase 8 PERF-K + Phase 7 WEB-P1-3 unblocked.** Row height stability was the prerequisite. Plan 06-05 (mutations-gating) depends on this plan's toolbar structure and can land cleanly because all 4 inner action buttons + their click handlers + 44px touch targets are preserved unchanged.
- **Deferred items appended (deferred-items.md #5 and #6):** pre-existing axe violations in the session list region (color-contrast on badges for POL-6 in Phase 9, nested-interactive on the outer button tree for a future a11y refactor) and pre-existing P2 DOM-test servers not running on ports 18425/18428/18429 (structural tests all pass; DOM tests fail with ERR_CONNECTION_REFUSED). Both categories verified to reproduce on the plan's baseline commit.

### Plan 09-03 Complete (2026-04-09)

- **POL-7 traceability + regression guard shipped (zero production code).** Plan 09-03 is a paperwork + guard plan: POL-7 itself already landed in Phase 6 plan 04 per ordering constraint #8, so this plan's only job is to document the ship location and install a forward-looking regression guard. Zero files under `internal/`, `cmd/`, or `pkg/` were touched.
- **Traceability document:** `.planning/phases/09-polish/09-03-POL-7-TRACEABILITY.md` maps every POL-7 requirement bullet (REQUIREMENTS.md line 68) to the specific commit SHA and file in Phase 6 plan 04 that satisfied it — `80fea0d` (failing test), `d3b4f35` (state.js signals), `aa1c974` (Toast.js refactor + ARIA split), `a7f2548` (ToastHistoryDrawer.js + Topbar + AppShell), `cf8322e` (a11y spec + contrast fix). Committed via `git add -f` because `.planning/` is in `.git/info/exclude`; local-only, never pushed.
- **Regression guard spec:** `tests/e2e/visual/p9-pol7-regression-guard.spec.ts` holds 10 structural assertions grouped into three describe blocks (Toast eviction / History drawer / state.js signals). All assertions are `readFileSync` + `toMatch` on `Toast.js`, `ToastHistoryDrawer.js`, and `state.js` — no server boot, no DOM navigation, runs in 827ms. A future refactor that removes `next.length > 3`, `role="alert"`, `aria-live="assertive"`, `role="status"`, `aria-live="polite"`, `export function ToastHistoryDrawer`, `export function ToastHistoryDrawerToggle`, `role="dialog"`, `aria-modal="true"`, `data-testid="toast-history-toggle"`, the 44x44 touch target, `toastHistorySignal`, `toastHistoryOpenSignal`, or the `agentdeck_toast_history` localStorage key will fail this guard loudly before it can silently break POL-7.
- **All 10 assertions passed on current main (baseline `5539ce3`) in 827ms.** Zero regressions — POL-7 shipped in 06-04 is fully intact in the current source tree. The plan's premise is confirmed.
- **Minimal per-plan config:** `tests/e2e/pw-p9-plan3.config.mjs` points only at the regression-guard spec, mirroring the `pw-p7-bugN` / `pw-p6-bugN` per-bug config pattern (plan 09-01's `pw-p9-plan1.config.mjs` did not yet exist when Task 2 was drafted because 09-01 is a parallel Wave 1 sibling).
- **TDD note:** Task 2 is a forward-looking guard, not a driving test, so there is no RED → GREEN pair. All 10 assertions passed on the first run against current main (`5539ce3`).
- **Commits (local only, no push):** `a83a6d5` `docs(09-03): POL-7 traceability record — shipped in Phase 6 plan 04` → `83e2d6e` `test(09-03): POL-7 regression guard spec`. Both are atomic; only plan 09-03's own files were staged (parallel plans 09-01 and 09-02 had untracked `tests/e2e/` files which were deliberately left alone).
- **Phase 9 plan count of 4 preserved** without re-implementing shipped code. Plan 09-04 (POL-6 light theme audit) can assume `Toast.js` and `ToastHistoryDrawer.js` surfaces are stable inputs to its audit — the regression guard will fail loudly if a POL-6 contrast fix accidentally breaks a POL-7 invariant.
- **POL-7 remains `[x]` in REQUIREMENTS.md line 68.** This plan verified the mark is still justified without modifying the file.

### Plan 09-01 Complete (2026-04-09)

- **POL-1 + POL-2 + POL-4 shipped in one atomic plan** because all three touch `SessionList.js` + `GroupRow.js` + `state.js` and splitting would cross-race a parallel wave. ~20 min execution.
- **POL-1 skeleton loader:** `sessionsLoadedSignal = signal(false)` appended at state.js tail (honors the 06-05 additive-tail interface rule). `main.js` imports it and flips it to `true` in BOTH `loadMenu()` success branch AND the SSE `menu` event handler; catch branch intentionally left unflipped so skeleton stays during offline. `SessionList.js` renders `<ul data-testid="sidebar-skeleton">` with 8 placeholder rows + `animate-pulse motion-reduce:animate-none` before the existing empty-state branch. Active search bypasses the skeleton (pulsing while a user types would feel broken).
- **POL-2 GroupRow fade + SessionRow regression guard:** GroupRow.js action cluster switched from `hidden group-hover:flex` (display-based snap, not transitionable) to `opacity-0 pointer-events-none group-hover:opacity-100 group-hover:pointer-events-auto group-focus-within:opacity-100 group-focus-within:pointer-events-auto transition-opacity duration-[120ms] motion-reduce:transition-none`. No Preact state needed (unlike SessionRow's 06-03 hasFocusWithin workaround) because GroupRow has no competing `.opacity-0` sibling that could shadow the group-hover variant. SessionRow.js 06-03 toolbar wiring is now asserted by a dedicated readFileSync + regex regression guard in `p9-pol2-transitions.spec.ts`.
- **POL-4 group density:** GroupRow.js outer button `py-2.5 min-h-[44px]` → `py-1 min-h-[40px]`. The 40 px min-h is still a reasonable tap target for a passive expand/collapse; inner action cluster buttons retain their 36 px targets. Plan DOM density assertion skipped on current fixture (needs session-then-group adjacency; `_test` has two groups at level 0 with no session between).
- **TDD ordering verified:** `test(09-01)` commit `03b191f` → `feat(09-01)` commit `7f91b26` (POL-1) → `fix(09-01)` commit `44d38e2` (POL-2 + POL-4). Initial RED run: 9 failed / 2 passed / 3 skipped. Final GREEN run: 13 passed / 1 skipped / 0 failed.
- **Playwright baseURL query-string gotcha:** `baseURL: 'http://127.0.0.1:18420/?token=test'` + `page.goto('/')` clobbers the query string — Playwright replaces path AND clears query, so /api/menu returned 401. Fix: all `page.goto` calls use `'/?token=test'` explicitly. Applied to all three p9-pol*.spec.ts files.
- **Stale embed.FS bundle trap (same as 09-02's discovery):** `make build` only runs `css` + `go build`, not `go generate`. After editing GroupRow.js the embedded PERF-H bundle `dist/main.<hash>.js` kept serving the old classes until I ran `GOTOOLCHAIN=go1.24.0 go generate ./internal/web/... && make build && restart server`. The correct recipe for any plan touching `internal/web/static/app/**/*.js` is: edit → `make css` → `go generate ./internal/web/...` → `make build` → kill all port 18420 processes → restart server.
- **Test server lifecycle via tmux:** `script -qc` pty wrapper + `nohup` both failed (`could not open a new TTY`) because the nested agent-deck environment lacks a real pty. Fix: spawn inside `tmux new-session -d -s adeck-p9-test '...agent-deck web...'`. tmux provides a real pty, TUI's `/dev/tty` open succeeds, server stays alive indefinitely. Cleaner than 09-02's `script -qfc` approach.
- **Parallel coordination with 09-02 and 09-03:** zero file overlap. 09-01 owned state.js/main.js/SessionList.js/GroupRow.js; 09-02 owned ProfileDropdown.js/CostDashboard.js/etc; 09-03 owned docs + SessionRow regression spec only. My git log shows interleaved commits (`03b191f 09-01 test → a83a6d5 09-03 docs → 83e2d6e 09-03 test → 39a0838 09-02 test → 3cbf3ab 09-03 docs → 7f91b26 09-01 feat → e5c4b42 09-02 fix → 23b4d04 09-02 feat → 44d38e2 09-01 fix`). No merge conflicts. State.js additive-tail interface held: 09-01 and 09-02 both append and the appends are orthogonal.
- **Hard rules honored:** no push, no tag, no PR, no merge. Zero Claude attribution in any plan-01 commit (verified `git log --format=%B 5539ce3..HEAD | grep -c 'Claude\|Co-Authored-By'` → 0 across all 3 plan-01 commits). No `rm` — used `trash` for stale dist bundles.
- **State.js interface at end of plan 09-01:** tail is now `mutationsEnabledSignal` → `sessionsLoadedSignal`. Any future plan adding a signal MUST append AFTER `sessionsLoadedSignal`.
- **`make ci` failures are all carry-forward** from deferred-items.md items #1, #3, #7, #9: same 5 lint errors + same 6 `TestSyncSessionIDsFromTmux_*` / `TestInstance_*` SetEnvironment failures. Zero new failures. `internal/web` tests pass (4.5s). TUI smoke tests pass.

### Plan 09-02 Complete (2026-04-09)

- **POL-3 and POL-5 shipped in a single Wave 1 plan** running parallel to 09-01 with zero file overlap. 09-01 owned SessionList/GroupRow/state; 09-02 owned ProfileDropdown/CostDashboard.
- **POL-3 profile dropdown hygiene:** `ProfileDropdown.js` `/api/profiles` handler now filters out internal `_*` test profiles (`_test`, `_dev`, `_baseline_test`, etc.) before calling `setProfiles`. The filter runs BEFORE the single-vs-multi branch, so a user with `[default, _test]` after filter → `[default]` → length 1 → single-profile `role="status"` path renders automatically (no branch duplication). Multi-profile listbox container gained `max-h-[300px] overflow-y-auto` so long profile lists scroll at 300px instead of pushing the viewport. WEB-P0-2 Option B scaffolding from plan 06-01 preserved intact (3 `role="status"`, 1 `aria-haspopup="listbox"`, 5 `HELP_TEXT` occurrences).
- **POL-5 locale-aware currency:** `CostDashboard.js` module-level `currencyFormatter = new Intl.NumberFormat(navigator.language, { style: 'currency', currency: 'USD' })` memoized at module load. Both `fmt(v)` (summary cards) and the Chart.js y-axis tick callback delegate to the same formatter so summary and axis labels never drift. Exactly ONE `new Intl.NumberFormat(` construction in the file (memoization guard); zero `.toFixed(2)` calls remain. Currency stays USD; only symbol placement / digit grouping / decimal separator follow `navigator.language`.
- **TDD ordering verified:** `test(09-02)` commit `39a0838` → `fix(09-02)` commit `e5c4b42` (POL-3) → `feat(09-02)` commit `23b4d04` (POL-5). Initial RED run: 17 failed / 4 passed / 3 skipped. Final GREEN run: 21 passed / 3 skipped (intentional locale-scoped `test.skip()` calls) / 0 failed across both `chromium-en-US` and `chromium-de-DE` projects.
- **Service worker interception gotcha (load-bearing):** `page.route()` silently failed to intercept `/api/profiles` because the PWA service worker (`internal/web/static/sw.js`) handles fetch events in its own context, which Playwright cannot mock. Fix: added `serviceWorkers: 'block'` to `pw-p9-plan2.config.mjs`. Any future plan-N config that mocks `/api/*` must set this option.
- **esbuild bundle staleness discovery:** `make build` only runs `css` + `go build`, NOT the esbuild bundler. The bundler is wired through `go:generate` in `internal/web/assets.go` (Phase 8 PERF-H) and must be invoked manually: `GOTOOLCHAIN=go1.24.0 go generate ./internal/web/`. The workflow for any plan touching `internal/web/static/app/**/*.js` is now: edit source → `go generate ./internal/web/` → `make build` → restart test server. Verified by iterating through three bundle content hashes during execution (`ZNLC4TVV` → `PJLTORRA` → `MQW3MRBR`).
- **Test server `script -qfc` wrapper:** `./build/agent-deck web` starts the TUI alongside the web server, and the TUI dies without a controlling TTY. `nohup` and `setsid -f` both failed; `script -qfc` (util-linux) allocates a pseudo-TTY that keeps the binary alive. Full incantation: `script -qfc 'env -u AGENTDECK_INSTANCE_ID -u TMUX -u TMUX_PANE -u TERM_PROGRAM AGENTDECK_PROFILE=_test ./build/agent-deck -p _test web --listen 127.0.0.1:18420 --token test' /dev/null > /tmp/web.log 2>&1 &`.
- **Parallel coordination with 09-01:** zero file overlap. Shared artifact `internal/web/static/styles.css` regenerated by both plans' `make css` / `go generate` runs; per brief, 09-02 did not stage styles.css (09-01 is authoritative). My POL-3 `max-h-[300px]` utility landed in 09-01's eventual styles.css commit because Tailwind scans both plans' source-file edits. Test server on port 18420 shared; restarted twice (after POL-3 fix, after POL-5 fix) without disrupting 09-01.
- **Downstream note for plan 09-04 (POL-6 light theme audit):** two new surfaces to audit — (1) ProfileDropdown multi-profile listbox scroll track (new `max-h-[300px] overflow-y-auto` may need scrollbar styling in light mode), (2) CostDashboard summary cards + chart y-axis ticks in de-DE (text width can grow ~30% e.g. `1.234,56 US$` vs `$1,234.56`; verify no grid overflow at narrow viewports).
- **Downstream notes for Phase 10 test infra:** (1) Shared base Playwright config should default to `serviceWorkers: 'block'`. (2) `scripts/start-test-server.sh` should encapsulate the `script -qfc` + env-unset + detach + probe incantation. (3) `make build` should depend on `go generate ./internal/web/` when app JS is newer than `dist/manifest.json`.
- **Hard rules honored:** no push, no tag, no PR, no merge, zero Claude attribution in any commit body (verified via `git log 39a0838^..HEAD | grep -ciE "claude|co-authored-by"` → 0), no `rm` usage (used `mv`/`trash` for temp file cleanup), no pre-existing failures from `deferred-items.md` touched.

### Plan 09-04 Complete (2026-04-09) — Phase 9 CLOSED

- **POL-6 light theme audit shipped as the final Phase 9 plan** per STATE.md Critical Ordering Constraint #7 (POL-6 LAST in Phase 9 after all layout is final). Wave 1 (09-01/02/03) locked the final v1.5.0 sidebar / profile dropdown / cost dashboard / toast surfaces before the audit ran.
- **Two-layer contrast audit:** (1) `@axe-core/playwright` sweep with `runOnly: ['color-contrast']` across 11 light-theme surfaces (main shell, sidebar with fixture sessions, multi-profile dropdown open, CostDashboard tab, EmptyStateDashboard, Create/Confirm/GroupName dialogs, KeyboardShortcutsOverlay, ToastHistoryDrawer, error toast variant); (2) targeted luminance check with canvas `getImageData`-based color parser (handles Tailwind v4 OKLCH) on 7 pre-flagged elements (tool label, cost badge, group count chip, profile option, cost subtitle, drawer timestamp, empty-state body).
- **Discovery pass (commit `f0928dd`):** 1 passed / 17 failed in 3.1 minutes against current main. Primary fg color in violations: `#99a1af` (Tailwind v4 `text-gray-400`) at 2.6:1 on `bg-white`, 2.55:1 on `bg-gray-50/50`, 2.48:1 on `bg-gray-50`. Unique flagged class strings extracted via grep: GroupRow count chip, SessionList "No sessions", EmptyStateDashboard recent-status + keyboard hints + "No sessions yet", SessionRow tool label, CostDashboard subtitles, SearchFilter placeholder, ProfileDropdown `(active)` label, SettingsPanel Loading text.
- **Fix batch:** 8 source files edited, 14 light-mode-only class swaps (all preserving their `dark:*` siblings unchanged). Primary substitution: `text-gray-400` → `text-gray-600` (2.6:1 → 7.5:1 on bg-white). GroupRow header went `text-gray-500` → `text-gray-700` because the `bg-gray-50/50` translucent background composited to a lower effective luminance. Bonus finding: SessionRow cost badge `text-green-600` → `text-green-700` (3.22:1 → 5.6:1) — Tailwind v4 `text-green-600` is `#00a63e` which fails AA on pure white; caught by the luminance spec L2, not pre-flagged.
- **Real-UI driven test pattern discovery (commit `2ac722c`):** The Phase 8 PERF-H bundle (`dist/main.<hash>.js`) closes `state.js` over its own minified variables (`F = v("terminal")` is `activeTabSignal`). When Playwright calls `import('/static/app/state.js')` via `page.evaluate`, the browser loads the un-bundled source file which creates a SECOND module instance with its own `activeTabSignal` closure. Mutations to the un-bundled signal never reach the bundled app's render tree. I discovered this while debugging why `activeTabSignal.value = 'costs'` wasn't switching tabs. Evidence: a probe showed `alert count: 0, unbundled signal length: 1` after pushing a toast via un-bundled `addToast()`. **Fix:** rewrote T4/T6/T7/T8/T9/T10/T11/L2/L5/L6 to drive state through real UI interactions (button clicks, keyboard presses, localStorage seeds, failing mutation mocks). This pattern is now the recommended approach for all Phase 9+ specs that need to drive app state. **p6-bug4-a11y.spec.ts and p6-bug2-a11y.spec.ts from Phase 6 need updating to use this pattern** — logged as out-of-scope deferred items for Phase 10 TEST-A.
- **OKLCH luminance parser fix:** initial regex-based `parseRgb` rejected Tailwind v4's `oklch(44.6% .03 256.802)` output with "cannot parse fg color". Replaced with a canvas-based approach: `ctx.fillStyle = cssColor; ctx.fillRect(0,0,1,1); ctx.getImageData(0,0,1,1).data` — the browser's native color parser accepts any valid CSS color and `getImageData` always returns sRGB bytes. Bundle-agnostic and handles every CSS color format including OKLCH, named colors, hex, hsl, and rgb.
- **Dialog container selector narrowing:** CreateSessionDialog / ConfirmDialog / GroupNameDialog render as plain `<div class="fixed inset-0 z-50 bg-black/50">` without `role="dialog"` — pre-existing structural a11y debt outside POL-6's color-contrast scope. Tests scope to `.fixed.inset-0.z-50.bg-black\\/50` instead. Recommended as a dedicated a11y refactor plan for Phase 10+.
- **TDD ordering verified:** `test(09-04)` commit `f0928dd` (discovery spec) → `test(09-04)` commit `2ac722c` (real-UI revision) → 7× `fix(09-04)` per-file commits (`7f34792`, `2e5f152`, `d059c6e`, `13a68d8`, `3bfa517`, `f9970b3`, `fdc8bfa`) → `chore(09-04)` `5380436` (styles.css regen) → `docs(09-04)` `46aac79` (deferred-items close-out).
- **Final GREEN run:** 18/18 POL-6 tests pass in 16.9s. All Wave 1 regression specs remain green: 09-01 (13 passed, 1 skipped), 09-02 (21 passed, 3 skipped), 09-03 (10 passed). Zero new Go test failures. Binary built with go1.24.0, vcs.modified=false.
- **Pre-existing failures preserved per scope boundary:** `make ci` lint + 6 `TestSyncSessionIDsFromTmux_*` + 1 `TestSmoke_QuitExitsCleanly` (carry-forward from deferred-items #1, #3, #7, #9). Plan 09-04 touches zero Go files. Also documented 2 NEW out-of-scope deferred items: p6-bug4-a11y signal-pattern breakage + p6-bug2-a11y mobile overflow-menu breakage (both caused by earlier phases, not by 09-04).
- **Deferred items #5 and #8 closed (commit `46aac79`):** session list color-contrast (item #5 from plan 06-03) and drawer-axe underlying badges (item #8 from plan 06-04) both RESOLVED by the POL-6 fix batch. Original entries preserved in deferred-items.md; RESOLVED annotations are additive for incident-trail transparency.
- **Hard rules honored:** no push, no tag, no PR, no merge, zero Claude attribution (verified `git log 220de49..HEAD | grep -ciE "claude|co-authored-by"` → 0), no `rm` (used `trash` for probe cleanup), no pre-existing failures touched.
- **Phase 9 CLOSED.** All 4 plans shipped, all 7 POL requirements complete (POL-1 ✓ POL-2 ✓ POL-3 ✓ POL-4 ✓ POL-5 ✓ POL-6 ✓ POL-7 ✓). The final v1.5.0 light theme is WCAG AA compliant for color-contrast across all rendered surfaces, guarded by 18 Playwright regression tests. Phase 10 TEST-A can now capture visual baselines on the final, fully-polished theme.

### Plan 10-01 Complete (2026-04-10)

- **TEST-A shipped:** 18-test Playwright visual regression suite with Docker-only pixel-diff baselines. The infrastructure uses `mcr.microsoft.com/playwright:v1.59.1-jammy` for deterministic font rendering. All baselines committed via `git add -f` (workaround for `*.png` in `.git/info/exclude`).
- **Key selector discoveries:** hamburger button aria-label is `"Open sidebar"` (dynamic: `"Close sidebar"` when open) per Topbar.js line — plan spec had wrong label `"Toggle sidebar"`. SettingsPanel lives inside the info drawer; opened via `"Open info panel"` button. No standalone settings button exists.
- **18 baselines:** 5 main-views (empty-state, sidebar-sessions, cost-dashboard, mobile-sidebar, settings-panel) + 4 P0 (hamburger, profile-switcher, title-no-truncation, toast-cap-3) + 5 P1 (terminal-fill, fluid-sidebar, row-density-40px, empty-state-card-grid, mobile-overflow-menu) + 4 Polish (skeleton-loading, skeleton-to-loaded, group-density-tight, light-theme-sidebar). All 18 pass in Docker.
- **CI workflow:** `.github/workflows/visual-regression.yml` triggers on every PR to main. Builds binary, starts server, runs Playwright in Docker with `--network=host`, uploads diff artifacts on failure, cleans up server. Uses `pw-visual-regression.config.ts` (no `--update-snapshots`).
- **Thresholds:** maxDiffPixelRatio: 0.001 + maxDiffPixels: 200 + threshold: 0.2. Catches layout shifts and color changes while allowing sub-pixel anti-aliasing variance.
- **Scoped baseline update pattern established:** always use `-g` filter with `--update-snapshots`. Never run blanket `--update-snapshots` (would silently accept regressions). Documented in `tests/e2e/visual-regression/README.md`.
- **TDD ordering compressed:** plan 10-01 ran `--update-snapshots` once per spec group during Docker generation (Tasks 2 and 4). The Docker container wrote baselines immediately — no separate RED run without server was achievable since Docker always connects to live server. Net result: all 18 tests pass, all baselines committed. Acceptance criteria met.

### Plan 10-02 Complete (2026-04-10)

- **TEST-B shipped:** Lighthouse CI merge-blocking performance gate with two-tier assertion model. Hard gates (`error`): `total-byte-weight` (180KB), `resource-summary:script:size` (120KB), `cumulative-layout-shift` (0.1). Soft warnings (`warn`): FCP (800ms), LCP (1500ms), TBT (200ms), speed-index (1500ms).
- **GitHub Actions workflow:** `.github/workflows/lighthouse-ci.yml` triggers on PRs touching `internal/web/**` or `.lighthouserc.json`. Builds binary with `GOTOOLCHAIN=go1.24.0`, uses `treosh/lighthouse-ci-action@v12`, `numberOfRuns:5` median, `temporary-public-storage` upload. `cancel-in-progress:true` concurrency group.
- **Local tooling:** `tests/lighthouse/budget-check.sh` (pre-push verification, port 19999) and `tests/lighthouse/calibrate.sh` (10-run p95+10%/20% threshold calibration, port 19998). Both scripts include pre-flight checks, server lifecycle management, healthz polling, pre-warm.
- **Pre-calibration fallback used:** `./build/agent-deck web` cannot start inside agent-deck sessions (nested-session detection exits immediately). Conservative Phase 8 spec + CI noise buffers used as documented in the plan. Run `calibrate.sh` from a plain terminal to replace with real calibrated values.
- **Documentation:** `tests/lighthouse/README.md` covers threshold tiers, CI flow, local verification, recalibration process, troubleshooting (including nested-session limitation), and design decisions (JSON vs CJS, temporary-public-storage, 5 runs, desktop preset).

### Plan 10-04 Complete (2026-04-10) — Phase 10 CLOSED

- **TEST-E shipped:** Alert-only weekly regression workflow (Sunday midnight UTC + `workflow_dispatch`) that runs visual regression + Lighthouse CI, creates a labeled GitHub issue with idempotency on failure, and stays silent on success. Three files: `.github/workflows/weekly-regression.yml`, `.github/weekly-regression-issue-template.md` (11 placeholder tokens), and the shell validator `tests/ci/weekly-alert-format.test.sh` (6 structural checks).
- **grep -- separator discovery:** `grep -qP '- \*\*...'` with empty stdin throws `invalid option -- ' '` because the pattern starting with `- ` is parsed as a flag. Fix: add `--` end-of-options separator before the pattern.
- **Config filename correction:** Plan specified `pw-visual-regression.config.mjs` but Plan 10-01 created `pw-visual-regression.config.ts`. Updated workflow to use the actual artifact filename.
- **7 edge case tests** in `tests/ci/weekly-alert-format-edge-cases.sh` all pass: empty input, title-only, missing section, wrong title format, both-PASS, standard valid body, large body with 55 results.
- **Phase 10 CLOSED.** All 4 plans shipped: TEST-A (visual regression PR gate, 18 tests) ✓, TEST-B (Lighthouse CI performance gate) ✓, TEST-C (session lifecycle + group CRUD E2E, 9 tests) ✓, TEST-D (mobile E2E, 3 viewports, 20 pass + 1 skip) ✓, TEST-E (weekly regression alerting) ✓.
- **Phase 11 unblocked.** All testing infrastructure is in place. Release v1.5.0 can begin.
- **TEST-E scope resolved:** alert-only as recommended by Pitfall 15. Decision #2 from Top Decisions Open is now CLOSED.

### Critical Ordering Constraints (from research)

These are non-negotiable dependencies — enforce during planning:

1. **WEB-P1-3 BLOCKED BY WEB-P0-3** — virtual list (PERF-K) requires stable row height
2. **WEB-P0-3 BLOCKS PERF-K** — same SessionList.js component
3. **WEB-P0-1 BLOCKS WEB-P1-5** — topbar z-index fix is prerequisite for mobile overflow menu
4. **PERF-E BEFORE PERF-D** — listener cleanup before dynamic WebGL import
5. **PERF-A + PERF-J SAME PR** — both are middleware
6. **PERF-H LAST in Phase 8** — minification obscures pre-existing bugs
7. **POL-6 LAST in Phase 9** — light theme audit after all layout is final
8. **POL-7 SHIPS WITH WEB-P0-4** — same Toast.js refactor, in Phase 6
9. **TEST-A BASELINES captured at END of Phase 9**, not Phase 10 start
10. **WEB-P0-2 DECISION GATE** — first task of Phase 6 investigates backend per-request profile support

### Top Decisions Open (from research)

1. ~~**WEB-P0-2 backend support**~~ — RESOLVED 2026-04-08. Backend does NOT support per-request profile override (`server.go:79` binds `cfg.Profile` once at NewServer() time). Option B shipped in plan 06-01 (commit `7b39232`).
2. **TEST-E scope** — alert-only (strongly recommended) or auto-fix? Default: alert-only per Pitfall 15.
3. ~~**Toast history drawer**~~ — RESOLVED 2026-04-08. Shipped in plan 06-04 alongside WEB-P0-4 mitigation (POL-7 satisfied early; commits `a7f2548` and `cf8322e`). Phase 9 POL-7 entry can be marked done.
4. **Service worker cache versioning** (DURING Phase 8) — bundle into PERF-J, not separate.
5. **PERF-K root cause investigation** (BEFORE PERF-K) — why 876 DOM nodes for 0 sessions? Simpler fix may exist.
6. **Lighthouse CI threshold calibration** (DURING Phase 10) — 10 baseline runs on main before setting thresholds.

### Archive Reference

Previous milestone (v1.4.0) state archived to `.planning/archive/v1.4.0/`:

- `phases/` — 4 executed phase directories with plans and verification reports
- `ROADMAP.md` — v1.4.0 9-phase roadmap (5 phases abandoned)
- `REQUIREMENTS.md` — v1.4.0 44 requirements
- `STATE.md` — final v1.4.0 state
- `research/` — v1.4.0 research (Stack, Features, Architecture, Pitfalls, Summary)

### Incident-Driven Rules (Non-Negotiable)

Carried forward from v1.4.0 constraints:

- **Go 1.24.0 toolchain pinned.** Go 1.25 silently breaks macOS TUI (2026-03-26 incident).
- **No SQLite schema changes this milestone.** localStorage for any new persistence. PR #385 broke all existing users without ALTER TABLE migration.
- **Small merge batches (3-5 PRs).** Never 15+ at once (the v0.27.0 anti-pattern).
- **Visual verification mandatory before release.** `scripts/visual-verify.sh` must pass for all 5 TUI states.
- **Every bug is a missing test.** Regression test written BEFORE fix; test fails without fix.
- **macOS TUI smoke test after every merge.** Session create, restart, stop with existing state.db.

## Workflow Configuration

From `.planning/config.json`:

- Mode: yolo (auto-approve)
- Granularity: standard
- Parallelization: enabled
- Model profile: balanced (Sonnet)
- Research: enabled (per-phase)
- Plan check: enabled (per-phase)
- Verifier: enabled (per-phase)
- Auto-advance: **disabled** (user explicitly wants each stage in fresh session for context hygiene)

## Next Action

Phase 7 is COMPLETE. 10 commits live on local main, NOT pushed (per HARD RULES — user owns push/PR/merge/tag). Next: Phase 8 (Performance / Premium Feel — 11 PERF requirements across 5 plans). Phase 8 PERF-K virtualization is now unblocked because Phase 7 plan 03 stabilized SessionRow height at exactly 40 px. Recommended next command in a fresh session: `/gsd:plan-phase 8`.

## Last session

- **Stopped at:** Phase 10 plan 10-03 COMPLETE — TEST-C (session lifecycle + group CRUD E2E, 9 tests) and TEST-D (mobile E2E at 3 viewports, 20 pass + 1 skip); 6 atomic commits; combined pw-p10-e2e.config.mjs covers all 30 runs
- **Timestamp:** 2026-04-09T17:42:25Z
- **Duration:** ~20 min (tmux-hosted test server avoided 09-02's `script -qfc` debug detour)
- **Commits:** 03b191f (test(09-01) failing specs + pw-p9-plan1 config), 7f91b26 (feat(09-01) POL-1 skeleton loader), 44d38e2 (fix(09-01) POL-2 GroupRow fade + POL-4 density)
- **Key issues handled in-flight:** (1) Playwright `baseURL: '/?token=test'` + `page.goto('/')` clobbers the query string — each test must use `page.goto('/?token=test')` explicitly; (2) stale embed.FS bundle after source edit — `make build` alone is insufficient, must run `go generate ./internal/web/...` first to refresh `dist/main.<hash>.js`; (3) test server TTY requirement — wrapped in `tmux new-session -d -s adeck-p9-test` which provides a real pty, cleaner than `script -qfc` from 09-02.

## Performance Metrics

| Phase | Plan | Duration | Tasks | Files Modified | Completed |
|-------|------|----------|-------|----------------|-----------|
| 06 | 01 | 10 min | 3 | 4 (+ 5 created) | 2026-04-08 |
| 06 | 02 | 14 min | 3 | 5 (+ 4 created) | 2026-04-08 |
| 06 | 03 | 20 min | 3 | 4 (+ 4 created) | 2026-04-08 |
| 06 | 04 | 32 min | 5 | 5 (+ 5 created) | 2026-04-08 |
| 06 | 05 | 14 min | 3 | 6 (+ 4 created) | 2026-04-08 |
| 07 | 01 | (prior) | 3 | 2 (+ 2 created) | 2026-04-09 |
| 07 | 03 | ~25 min (incl. WIP triage) | 3 | 1 (+ 2 created) | 2026-04-09 |
| 07 | 04 | ~35 min (incl. WIP triage + parse error fix + test patch) | 3 | 1 (+ 2 created) | 2026-04-09 |
| 07 | 02 | ~45 min (incl. Tailwind v4 @utility discovery) | 4 | 4 (+ 4 created) | 2026-04-09 |
| 09 | 03 | ~4 min | 2 | 0 (+ 3 created, 0 internal/) | 2026-04-09 |
| 09 | 02 | ~55 min (incl. service worker debug + esbuild pipeline discovery) | 3 | 2 (+ 4 created) | 2026-04-09 |
| 09 | 01 | ~20 min | 3 | 4 (+ 4 created) | 2026-04-09 |
| Phase 10-automated-testing P02 | 5 | 4 tasks | 5 files |
| Phase 10-automated-testing P03 | 17min | 7 tasks | 7 files |
