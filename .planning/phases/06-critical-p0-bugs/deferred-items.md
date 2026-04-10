# Phase 6 Deferred Items

Out-of-scope discoveries logged during plan execution. These are NOT fixed as
part of their originating plan (per the execute-plan scope boundary rule) but
are captured here so a future maintenance plan can pick them up.

---

## Plan 06-01 (WEB-P0-2)

### 1. Pre-existing `internal/session` tmux SetEnvironment test failures

**Discovered during:** `make ci` verification (Task 3).
**Baseline commit verified:** `2e0520f` (tip of main before plan 06-01).
**Tests affected:**
- `TestSyncSessionIDsFromTmux_Claude`
- `TestSyncSessionIDsFromTmux_AllTools`
- `TestSyncSessionIDsFromTmux_OverwriteWithNew`
- `TestInstance_GetSessionIDFromTmux`
- `TestInstance_UpdateClaudeSession_TmuxFirst`
- `TestInstance_UpdateClaudeSession_RejectZombie`

**Symptom:** All six fail with `SetEnvironment failed: exit status 1` or
`set tmux env: exit status 1`. The underlying `tmux set-environment` command
is exiting non-zero, likely due to a session-scope or server-scope issue in
the test harness environment. Needs investigation in a session-subsystem
dedicated plan.

**Out of scope because:** plan 06-01 touches only
`internal/web/static/app/ProfileDropdown.js` and Playwright JS tests. It does
not modify the Go `internal/session` package. The failures reproduce on
the plan's baseline commit, so they are not regressions introduced by this
plan.

**Recommended owner:** session-subsystem maintenance plan in a future phase.

---

### 2. Pre-existing `TestSmoke_QuitExitsCleanly` TUI flake

**Discovered during:** `make ci` verification (Task 3).
**Baseline commit verified:** `2e0520f` (tip of main before plan 06-01).
**Test affected:** `internal/tuitest.TestSmoke_QuitExitsCleanly`

**Symptom:** `TUI did not exit after pressing 'q' then 'k' (tmux session
still exists)`. The TUI smoke test is a binary-level test that builds
agent-deck and drives it in a real tmux pane. This is likely timing/flake
related to the quit sequence in the test harness.

**Out of scope because:** plan 06-01 does not touch any TUI Go code. The
failure reproduces on the plan's baseline commit.

**Recommended owner:** a dedicated TUI smoke test stabilization plan.

---

## Plan 06-02 (WEB-P0-1)

### 3. Pre-existing `TestSmoke_TUIRenders` environment pollution

**Discovered during:** Task 3 `make ci` verification.
**Baseline commit verified:** `2e0520f` with `go clean -testcache`.
**Test affected:** `internal/tuitest.TestSmoke_TUIRenders`

**Symptom:** `expected 'conductor' group in TUI output`. The smoke test
launches `./build/agent-deck` against the `_test` profile and expects
to find a group named 'conductor' in the rendered TUI. The `_test`
profile's `state.db` has been touched by parallel executors / stale
test data, so the expected seed state is gone.

**Verified pre-existing:** Checked out `2e0520f` (plan 06-02's baseline),
ran `go clean -testcache && go test -race -v -run TestSmoke_TUIRenders
./internal/tuitest/...`. Result: same failure, same message. This
confirms the failure is NOT caused by plan 06-02's styles.src.css /
Topbar.js / Toast.js / ProfileDropdown.js edits (which do not touch Go
TUI code at all).

**Out of scope because:** plan 06-02 touches only web static assets
(CSS, JS). The failure is a TUI smoke-test profile-state pollution
issue unrelated to the hamburger z-index fix. Cleaning up the `_test`
profile's state.db would require coordination with parallel plans
06-03 and 06-04 that use the same test server.

**Recommended owner:** TUI smoke test stabilization plan (same owner
as deferred item #2), plus a one-time `_test` profile state reset.

### 4. `vcs.modified=true` warning during TUI smoke builds

**Discovered during:** Task 3 `TestSmoke_BuildVersion` run.
**Test affected:** `internal/tuitest.TestSmoke_BuildVersion` (PASSES but
warns).

**Symptom:** `WARNING: binary built from dirty worktree
(vcs.modified=true). Release builds must have vcs.modified=false.`

**Out of scope because:** the dirty worktree is a transient state during
parallel plan execution — plan 06-03 and 06-04 leave uncommitted
changes in the tree while plan 06-02 is still mid-flight. The test
still PASSES (it's a warning, not a failure). Release builds (tagged)
are built from clean worktrees per the Makefile release target. This
is not a plan 06-02 issue.

**Recommended owner:** no action needed — warning disappears once all
Wave 2 parallel plans commit their in-flight files.

---

## Plan 06-03 (WEB-P0-3)

### 5. Pre-existing axe-core violations in session list region (outside toolbar scope)

**Discovered during:** Plan 06-03 Task 3 a11y verification.
**Baseline commit verified:** Reproduces both before and after the plan 06-03 Task 2 fix (`278e136`). The violations are structural to the existing SessionRow.js layout, not introduced by the absolute-positioned toolbar conversion.
**Axe violations found** when scoping to the whole `#preact-session-list`:

1. **`color-contrast` (2 nodes, serious)** — `.dark:text-tn-muted/60` group count badge (e.g. `(1)`) at 2.55:1 and `.text-xs.dark:text-tn-muted.text-gray-400` tool badge (`shell`, `claude`, etc.) at 2.6:1 — both below the 4.5:1 WCAG AA minimum. These are light-theme contrast issues in the session list badges.
2. **`nested-interactive` (1 node, serious)** — the outer `<button data-session-id>` contains focusable `<button>` children in the action toolbar. Axe flags this as "Element has focusable descendants". This has been true structurally since SessionRow.js was originally written — the inner action buttons (Stop/Restart/Fork/Delete) have always lived inside the outer row button. Plan 06-03 only changed the container from `<span>` to `<div role="toolbar">` — the nesting itself is unchanged.

**Out of scope because:** plan 06-03 is scoped to "convert the action button `<span>` to an absolute-positioned `<div role='toolbar'>`". Neither violation is introduced by that change. `color-contrast` is about badge text colors in sibling spans (not the toolbar). `nested-interactive` is structural to the entire SessionRow design — fixing it would require a refactor of the outer button into `<div role="button" tabindex="0">` with keyboard handlers, affecting click semantics, keyboard navigation, and every SessionRow test across Phases 2-4.

Per the 06-01 pattern (whose axe spec narrows to `[data-testid="profile-indicator"]` rather than the whole header), plan 06-03's a11y spec narrows its axe scope to `[role="toolbar"][aria-label="Session actions"]` — the component this plan actually touches. The toolbar element itself has **zero axe violations**.

**Recommended owner:** `color-contrast` → Phase 9 POL-6 light theme audit (already planned per STATE.md ordering constraint #7). `nested-interactive` → a dedicated a11y refactor plan, probably in Phase 7 WEB-P1 work, since fixing it requires restructuring SessionRow's click/keyboard handling.

**RESOLVED 2026-04-09 (Phase 9 plan 04 POL-6 audit, commit `7f34792` + `2e5f152`):** session list color-contrast violations fixed. SessionRow.js tool label `text-gray-400` → `text-gray-600` (2.6:1 → 7.5:1), cost badge `text-green-600` → `text-green-700` (bonus finding, 3.22:1 → 5.6:1), GroupRow.js count chip `text-gray-400` → `text-gray-600` (2.55:1 → 6.85:1), GroupRow.js header text `text-gray-500` → `text-gray-700`. Regression guarded by:
- `tests/e2e/visual/p9-pol6-light-theme-audit.spec.ts` (T2 sidebar axe-core sweep with 4 fixture sessions across all statuses + 2 groups — zero color-contrast violations)
- `tests/e2e/visual/p9-pol6-light-theme-contrast.spec.ts` (L1 tool label, L2 cost badge, L3 group count chip — each asserted ≥4.5:1 via canvas-based luminance check, survives axe-core version bumps)

`nested-interactive` is still deferred — it's a11y refactor scope, not POL-6 color-contrast scope.

### 6. Related P2 DOM specs fail because their dedicated servers are not running

**Discovered during:** Plan 06-03 Task 3 cross-check of `p2-bug17-action-bubble.spec.ts`, `p2-bug12-action-overlap.spec.ts`, `p2-bug18-truncation-depth.spec.ts`.

Each of these three specs uses a dedicated port (18425 / 18428 / 18429) and expects a manually-started `agent-deck` test server on that port. None of those servers are running in the current environment — only port 18420 has a test server (used by `pw-p0-bug3.config.mjs`, `pw-p6-bug3.config.mjs`, `pw-p6-bug3-a11y.config.mjs`, `pw-p6-bug2*.config.mjs`, and `pw-p1.config.mjs`).

**Structural tests in all three specs PASS** (readFileSync-based assertions against SessionRow.js / GroupRow.js / SessionList.js). The DOM tests fail with `net::ERR_CONNECTION_REFUSED` against the unreserved ports:
- `p2-bug17`: 6/6 structural ✓, 1 DOM ✘ (ERR_CONNECTION_REFUSED to 18428)
- `p2-bug12`: 4/4 structural ✓, 2 DOM ✘ (ERR_CONNECTION_REFUSED to 18425)
- `p2-bug18`: 4/4 structural ✓, 2 DOM ✘ (ERR_CONNECTION_REFUSED to 18429)

Every SessionRow.js-related structural invariant the earlier plans enforced still holds after plan 06-03's Task 2 edit:
- `group w-full min-w-0` preservation check (P2-18): PASS
- `stopPropagation` count >= 5 (P2-17): PASS (6 now — 1 toolbar onClick + 1 toolbar onMouseDown + 4 inner buttons)

**Out of scope because:** starting / managing the dedicated servers on 18425 / 18428 / 18429 is a test-infra task unrelated to the WEB-P0-3 fix. Plan 06-03's load-bearing assertion is the structural tests, which all pass.

**Recommended owner:** a Phase 10 test infrastructure plan (TEST-A or TEST-B) should consolidate all Playwright configs onto a single shared port (mirroring how `pw-p1.config.mjs` uses one port for every P1 spec).

---

## Plan 06-04 (WEB-P0-4 + POL-7)

### 7. Pre-existing failures persist (carry-forward from items 1, 2, 3)

**Discovered during:** Plan 06-04 Task 5 `make ci` verification gate.
**Baseline commit verified:** `a7f2548` (final commit before Task 5 a11y artifacts) — all failures reproduce on baseline AFTER stashing the new a11y spec + ToastHistoryDrawer color contrast fix.

**Lint failures (5, all unrelated to plan 06-04 files):**
- `cmd/agent-deck/main.go:458 SA4006` (staticcheck) — `args` value never used
- `internal/tuitest/smoke_test.go:182,224 errcheck` — unchecked `os.MkdirAll`
- `internal/ui/branch_picker.go:18 unused` — `branchPickerResultMsg` unused type
- `internal/ui/home.go:63 unused` — `isCreatingPlaceholder` unused method

**Test failures (carry-forward from prior plans):**
- 6 `TestSyncSessionIDsFromTmux_*` / `TestInstance_*` SetEnvironment failures (same as deferred item #1 from plan 06-01)
- `TestSmoke_QuitExitsCleanly` — TUI did not exit after q+k (same as deferred item #2 from plan 06-01)

**Out of scope because:** plan 06-04 only touches `internal/web/static/app/Toast.js`, `state.js`, `Topbar.js`, `AppShell.js`, the new `ToastHistoryDrawer.js`, and Playwright JS specs. None of the failing files / tests are touched. All failures verified to reproduce on the plan's baseline commit `a7f2548` before the Task 5 a11y artifacts were added. Plan 06-04's scoped verification is the p6-bug4 regression spec (13/13 pass) and the a11y spec (6/6 pass). Both are green.

**Recommended owner:** session-subsystem maintenance plan (already tracked in items #1, #2). The 5 lint failures should ride the same plan or a small janitor commit from a future phase that owns those files.

### 8. Pre-existing axe-core color-contrast on session list badges (drawer-only check would surface it)

**Discovered during:** Plan 06-04 Task 5 a11y axe scan when scoping to the open ToastHistoryDrawer dialog.

The drawer's `<dialog>` overlay covers the page but does not occlude axe analysis of underlying DOM. When scoping `AxeBuilder.include('[role="dialog"]')`, axe traversed all of the dialog's descendants, which include the original page background. That surfaced the same `color-contrast` violations on session list badges (`text-tn-muted/60` group counts at 2.55:1, `text-gray-400` tool badges at 2.6:1) that deferred item #5 already documents.

**Resolution applied in plan 06-04:** narrowed the drawer axe scope from `[role="dialog"]` to `[role="dialog"][aria-label="Toast history"] ul` (the history list `<ul>` only). This:
1. Excludes the dialog's `<header>` so axe doesn't fire `landmark-no-duplicate-banner` against the top-level `<header>` (an HTML5 quirk of nesting `<header>` inside `<dialog>`, not a real bug).
2. Excludes any background page content that the dialog overlays.
3. Independently fixed `text-gray-400` to `text-gray-600` on the new history row timestamp label (the only color-contrast violation introduced by plan 06-04's own component) so the tightened scope still yields zero violations.

**Out of scope because:** the underlying badge contrast issues are POL-6 (Phase 9 light theme audit) territory, already tracked in deferred item #5.

**Recommended owner:** POL-6 in Phase 9.

**RESOLVED 2026-04-09 (Phase 9 plan 04 POL-6 audit):** the drawer-axe narrowing from 06-04 continues to apply (p6-bug4-a11y.spec.ts still scopes to `[role="dialog"][aria-label="Toast history"] ul`). In addition, POL-6 now independently fixes the underlying session list badges that the drawer scan would have surfaced — the broader drawer scan would now return zero violations even if widened. Plan 09-04's own ToastHistoryDrawer test (`p9-pol6-light-theme-audit.spec.ts` T10) scopes to `[role="dialog"][aria-label="Toast history"] ul` as well, mirroring 06-04's pattern, and the targeted luminance check L6 asserts the drawer timestamp contrast explicitly.

---

## Plan 06-05 (WEB-P0-4 prevention layer / mutations gating)

### 9. Pre-existing failures persist (carry-forward from items 1, 2, 3, 7)

**Discovered during:** Plan 06-05 Task 3 `make ci` verification gate.
**Baseline verification:** same lint failures + same test failures as deferred item #7 from plan 06-04. Plan 06-05 only touches `internal/web/static/app/{state,AppShell,SessionRow,CreateSessionDialog}.js` and Playwright JS specs. None of the failing Go files are in this plan's diff.

**Lint failures (5, all unrelated to plan 06-05 files):**
- `cmd/agent-deck/main.go:458 SA4006` (staticcheck) — `args` value never used
- `internal/tuitest/smoke_test.go:182,224 errcheck` — unchecked `os.MkdirAll`
- `internal/ui/branch_picker.go:18 unused` — `branchPickerResultMsg` unused type
- `internal/ui/home.go:63 unused` — `isCreatingPlaceholder` unused method

**Test failures (carry-forward from prior plans):**
- 6 `TestSyncSessionIDsFromTmux_*` / `TestInstance_*` SetEnvironment failures (same as deferred item #1 from plan 06-01)
- `TestSmoke_QuitExitsCleanly` — TUI did not exit after q+k (same as deferred item #2 from plan 06-01)

**Out of scope because:** plan 06-05 only modifies web static assets and test infrastructure. It touches zero Go files in `internal/session/`, `internal/tuitest/`, `internal/ui/`, or `cmd/agent-deck/`. Plan 06-05's scoped verification is the mutations-gating regression spec (11/11 pass including non-regression), the a11y spec (6/6 pass), and the cross-check of prior Phase-6 specs (p0-bug3 6/6, p6-bug1 9/9, p6-bug2 6/6, p6-bug3 11/11, p6-bug3-a11y 4/4, p6-bug4 toast-cap 13/13, p6-bug4-a11y 6/6). All green.

**Recommended owner:** session-subsystem maintenance plan (already tracked in items #1, #2, #7). The 5 lint failures should ride the same plan or a small janitor commit from a future phase that owns those files.

### 10. p6-bug3 and p6-bug3 a11y specs had to add mutationsEnabledSignal override

**Discovered during:** Plan 06-05 Task 3 cross-plan verification gate after running `make ci`.

When the test server is running with `webMutations=false` (which is the state plan 06-05 explicitly handles), the 06-03 specs for session-title truncation and toolbar a11y legitimately stop seeing `<div role="toolbar">` in the DOM — because plan 06-05's new gating removes it. These specs were asserting the 06-03 toolbar contract, not the 06-05 gating contract.

**Resolution applied in plan 06-05:** added a `await page.evaluate(() => { state.mutationsEnabledSignal.value = true })` preamble to every affected DOM test in:
- `tests/e2e/visual/p6-bug3-title-truncation.spec.ts` (2 DOM tests)
- `tests/e2e/a11y/p6-bug3-title-truncation-a11y.spec.ts` (4 DOM tests)

This isolates the 06-03 toolbar contract from the 06-05 gating contract: 06-03's structural and rendering assertions run against `mutationsEnabled=true`, and 06-05's gating assertions run against `mutationsEnabled=false`. Both specs now pass against a server with `webMutations=false`.

**Out of scope because:** the underlying bug is not in either plan. It is a test-infra consequence of running Phase 6 plans against a server that defaults to read-only mutations. No Go code changes are needed. This is an intentional update to test preambles, not a workaround for broken production code.

**Recommended owner:** none — the fix landed in plan 06-05's Task 3 commit (`515c318`).
