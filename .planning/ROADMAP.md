# Agent Deck v1.5.0 Roadmap

**Milestone:** v1.5.0 — Premium Web App
**Starting point:** v1.4.1 (2026-04-08)
**Created:** 2026-04-08
**Granularity:** Standard (7 phases, 6 active; Phase 5 pre-complete)
**Parallelization:** Enabled (Phases 6, 7, 8, 9 have internal parallelization with strict ordering constraints)

---

## Executive Summary

v1.5.0 is a polish milestone, not a feature milestone. v1.4.0 shipped the web redesign but four P0 bugs (mobile hamburger, profile switcher, title truncation, infinite toast spam) survived manual review, five P1 layout bugs were never fixed, eleven performance bottlenecks remain (gzip alone leaks ~518 KB per cold load), and the codebase has a confirmed listener leak in `TerminalPanel.js`. The job is to make the embedded web app feel **premium** — instant cold load (<150 KB gzipped, FCP <500 ms), zero bugs, dense desktop layout, fully functional mobile, terminal that fills its pane — and lock those gains in with visual regression + Lighthouse CI so v1.4.0's "manual review missed everything" pattern cannot recur.

Three cross-cutting themes shape the phase order:

1. **Layout stabilizes before performance optimizes before tests freeze.** Phase 6 (P0) and Phase 7 (P1) stabilize layout. Phase 8 (perf) optimizes on a stable layout. Phase 9 (polish) refines on optimized layout. Phase 10 (tests) captures baselines on the final render. Any inversion wastes baselines.
2. **PERF-H (esbuild bundling) ships LAST in Phase 8.** Bundling reorders module load and minification obscures errors. Pre-existing bugs become harder to diagnose post-bundle. All other perf items ship first; PERF-H is the single highest-risk PR in the milestone.
3. **Every bug needs a regression test BEFORE the fix** (TDD, woven through every phase). Visual regression baselines captured at the END of Phase 9, not during Phase 10 start.

Two release-safety anchors are non-negotiable (from the 2026-03-26 and PR #385 incidents):
- **Go 1.24.0 toolchain pinned** at every layer. Go 1.25 silently breaks macOS TUI.
- **No SQLite schema changes this milestone.** localStorage for any new persistence.

Merge policy: 3-5 PRs per batch with `make ci` + macOS TUI smoke test between batches. Never 15+ PRs at once (the v0.27.0 anti-pattern).

---

## Phases

- [x] **Phase 5: Critical Regressions** — 6 regressions from v1.4.0 fixed in emergency v1.4.1 patch (COMPLETE)
- [x] **Phase 6: Critical P0 Bugs** — Fix 4 P0 web bugs that survived v1.4.0 (hamburger, profile switcher, title truncation, toast spam) (COMPLETE 2026-04-08; 5/5 plans)
- [x] **Phase 7: P1 Layout Bugs** — Fix 5 layout bugs (terminal fill, sidebar width, row density, empty state, mobile topbar overflow) (COMPLETE 2026-04-09; 4/4 plans)
- [x] **Phase 8: Performance** — Ship 11 perf wins to hit <150 KB gzipped first-load, FCP<500ms, LCP<1s, TBT<100ms (COMPLETE 2026-04-09; 5/5 plans)
- [x] **Phase 9: Polish** — 7 premium UX refinements (skeleton loader, transitions, profile dropdown, light theme audit) (COMPLETE 2026-04-09; 4/4 plans)
- [x] **Phase 10: Automated Testing** — Visual regression + Lighthouse CI + E2E coverage blocking future regressions (completed 2026-04-10)
- [ ] **Phase 11: Release v1.5.0** — Tag, visual verification, macOS smoke test, changelog, real-device mobile verification

---

## Phase Overview

| # | Phase | Requirements | Plans | Parallelizable? | Blocks |
|---|-------|--------------|-------|------------------|--------|
| 5 | Critical Regressions | 6 | 4/5 | In Progress|  |
| 6 | Critical P0 Bugs | 4 | 5 | Partial (3 waves; Wave 1 serial P0-2; Wave 2 parallel P0-1/P0-3/P0-4 mitigation; Wave 3 P0-4 prevention) | Phases 7, 8, 10 |
| 7 | P1 Layout Bugs | 5 | 4 | Partial (P1-1, P1-2, P1-4 parallel; P1-3 + P1-5 have deps) | Phases 8 (PERF-K), 9, 10 |
| 8 | Performance | 11 | 5 | Partial (strict internal ordering; PERF-H last) | Phases 9 (POL-1), 10 (TEST-A, TEST-B) |
| 9 | Polish | 7 | 4 | Partial (POL-1..5 parallel; POL-6 last; POL-7 with P0-4) | Phase 10 (TEST-A baselines) |
| 10 | 4/4 | Complete   | 2026-04-10 | Partial (TEST-A first, TEST-B after PERF-H, TEST-C/D parallel) | Phase 11 |
| 11 | Release v1.5.0 | 5 | 3 | No (sequential release gate) | — |

**Total requirements mapped:** 43 / 43 (100%)
**Total plans across active phases:** ~25 (Phase 5 excluded; plan counts refined in plan-phase stage)

---

## Phase Details

### Phase 5: Critical Regressions

**Status:** COMPLETE (shipped in v1.4.1)
**Goal:** Fix the 6 regressions introduced in v1.4.0 that shipped as an emergency patch.
**Depends on:** v1.4.0 ship
**Requirements:** REG-01, REG-02, REG-03, REG-04, REG-05, REG-06

**Success Criteria (verified in v1.4.1):**
1. Shift+letter keys no longer dropped in any session (CSI u reader wired into tea.NewProgram input pipeline)
2. tmux scrollback preserved across session restart, history-limit user setting respected
3. Mousewheel scrolling works in all tmux sessions (no more [0/0] cursor display)
4. Conductor heartbeat works on Linux (grep -o fix no longer breaks it)
5. tmux detected from well-known paths when not in PATH (Homebrew, Nix, MacPorts)
6. bash -c quoting bug fixed — session commands always wrapped regardless of content

**Plans:** N/A (already shipped in v1.4.1: PRs #533, #535/#537, #532, #524/#523, #527, #526)

---

### Phase 6: Critical P0 Bugs

**Goal:** Fix the 4 P0 web bugs that survived v1.4.0's manual review. These bugs block every downstream phase — session title truncation must be fixed before row density (Phase 7) or virtualization (Phase 8), hamburger z-index must be fixed before mobile overflow menu (Phase 7), and the profile switcher decision gate must be resolved before any downstream planning can proceed.
**Depends on:** Phase 5 (shipped)
**Requirements:** WEB-P0-1, WEB-P0-2, WEB-P0-3, WEB-P0-4

**Success Criteria (what must be TRUE for users):**
1. User can tap the mobile hamburger on all viewports ≤768px and the sidebar drawer opens — no topbar element intercepts the pointer anymore
2. User either sees the profile switcher reload the page into the selected profile (option A, `?profile=X`) OR sees a read-only label showing the current profile (option B — if backend cannot support per-request profile override)
3. User sees full session titles in the sidebar (truncation rate drops from 76% to <10%) because action buttons are `position: absolute` with hover-reveal and no longer reserve 90px of horizontal space
4. User never sees more than 3 stacked toasts at once; info/success toasts auto-dismiss after 5s; error toasts require explicit dismiss; when `mutationsEnabled=false`, write buttons are hidden so users cannot generate 403 error spam
5. All four fixes pass a11y verification (keyboard Tab navigation, screen reader, mobile touch targets) — not just visual pass

**Plans:** 5/5 plans executed — Phase 6 COMPLETE

Plans:
- [x] 06-01-PLAN.md — WEB-P0-2 profile switcher (Option B read-only label; decision gate resolved — backend `server.go:79` binds `cfg.Profile` once at `NewServer()` time; Wave 1 serial) — shipped 2026-04-08 (commits e68eeef / 7b39232 / 285a9bd)
- [x] 06-02-PLAN.md — WEB-P0-1 mobile hamburger z-index (systematic 7-token Tailwind v4 `@theme` scale via `--z-index-*` namespace, which is what Tailwind v4 actually consumes — 06-CONTEXT.md specified `--z-*` but empirical test showed the utility is driven by `--z-index-*`; mirrored `--z-*` aliases kept for structural-test anchors; BLOCKS WEB-P1-5 — now unblocked; Wave 2 parallel) — shipped 2026-04-08 (commits 914a9ff / 8f466c8 / 432ea9d)
- [x] 06-03-PLAN.md — WEB-P0-3 absolute-positioned action toolbar (flex→absolute overlay with 120ms opacity reveal, `role="toolbar"`, `focus-visible` keyboard reveal; BLOCKS WEB-P1-3 + PERF-K — now unblocked; Wave 2 parallel) — shipped 2026-04-08 (commits 526d711 / 278e136 / 0840d88)
- [x] 06-04-PLAN.md — WEB-P0-4 mitigation + POL-7: toast cap-3 + error preservation + history drawer (depends on 06-01 and 06-02 for z-index utilities; Wave 2 parallel) — shipped 2026-04-08 (commits 80fea0d / d3b4f35 / aa1c974 / a7f2548 / cf8322e). POL-7 satisfied early; Phase 9 POL-7 entry can be marked done.
- [x] 06-05-PLAN.md — WEB-P0-4 prevention: mutations-gating (hide write buttons + disable CreateSessionDialog when `webMutations=false`; depends on 06-01, 06-03, 06-04; Wave 3) — shipped 2026-04-08 (commits f582929 / 52497f3 / 34b88bd / 515c318). mutationsEnabledSignal seeded optimistically in state.js, AppShell fetches /api/settings on mount, SessionRow toolbar wrapped in a mutationsEnabled short-circuit plus a read-only lock indicator, CreateSessionDialog early-returns null after hooks plus disables its submit button as belt-and-braces. Cross-plan fix: 06-03 p6-bug3 specs now force mutationsEnabledSignal=true in 6 affected DOM tests to preserve their contract independent of server webMutations mode.

**Wave structure:**
- **Wave 1 (serial):** 06-01 — decision gate for P0-2
- **Wave 2 (parallel, after 06-01):** 06-02 (P0-1 z-index), 06-03 (P0-3 absolute toolbar), 06-04 (P0-4 mitigation + POL-7)
- **Wave 3 (after Wave 2):** 06-05 — P0-4 prevention layer (mutations-gating); depends on 06-03 (toolbar to gate) + 06-04 (state.js layout) + 06-01

**Ordering constraints:**
- P0-2 (plan 06-01) is SERIAL and ships FIRST (decision gate)
- P0-1 (plan 06-02) BLOCKS P1-5 — must ship before Phase 7 P1-5 work starts
- P0-3 (plan 06-03) BLOCKS P1-3 and PERF-K — same `SessionList.js` component, conflict avoidance
- P0-4 mitigation (plan 06-04) SHIPS WITH POL-7 (same Toast.js refactor, same PR) and depends on 06-02 (`z-toast`/`z-modal` Tailwind utilities) + 06-01
- P0-4 prevention (plan 06-05) ships in Wave 3 and depends on 06-03 (toolbar to gate) + 06-04 (state.js additive ordering) + 06-01

---

### Phase 7: P1 Layout Bugs

**Goal:** Fix the 5 layout bugs that make desktop feel broken on large monitors and mobile feel cramped. Layout must be stable before performance work (Phase 8) and skeleton loader (POL-1) so the skeleton matches the final layout exactly. Baselines (TEST-A) captured on a layout that's still moving is wasted work.
**Depends on:** Phase 6 (WEB-P0-1 must ship before WEB-P1-5; WEB-P0-3 must ship before WEB-P1-3)
**Requirements:** WEB-P1-1, WEB-P1-2, WEB-P1-3, WEB-P1-4, WEB-P1-5

**Success Criteria (what must be TRUE for users):**
1. User sees the terminal panel fill its container on attach — no huge empty gray gap below the terminal (xterm fit addon triggers on resize AND tmux pane_resize matches browser viewport cols×rows)
2. User on a 1920px monitor sees the sidebar at ~22vw (fluid `clamp(260px, 22vw, 380px)`) — main panel no longer wastes 1640px of real estate
3. User sees 20+ sessions in the sidebar at 1080p (row height drops from ~52px to 40-44px via `py-1.5 leading-tight`) — row height is stable and fixed (prerequisite for PERF-K virtualization)
4. User on a large monitor sees a card-grid empty-state dashboard with `max-w-4xl` centered layout — no more floating "nothing selected" message in a gray void
5. User on iPhone SE (<600px) sees a topbar with hamburger left + title center + ONE primary action right + `⋯` overflow menu — never 4+ buttons cramming the header

**Plans:** 4 plans across 2 waves (see `.planning/phases/07-p1-layout-bugs/`)

Plans:
- [ ] 07-01-PLAN.md — WEB-P1-1 terminal panel fill: xterm FitAddon + window-resize listener via AbortController, flex chain min-h-0 propagation in AppShell.js (Wave 1)
- [ ] 07-02-PLAN.md — WEB-P1-2 fluid sidebar + WEB-P1-4 empty-state card grid: `.sidebar-fluid` Tailwind utility (`clamp(260px, 22vw, 380px)`), drag handle removed, EmptyStateDashboard `max-w-4xl` card grid (Wave 2; depends on 07-01 because both touch AppShell.js)
- [ ] 07-03-PLAN.md — WEB-P1-3 sidebar row density: `py-1.5 leading-tight min-h-[40px]` on SessionRow outer button; cross-phase BLOCKED BY Phase 6 plan 06-03 (Wave 1; pre-flight check enforces Phase 6 dep)
- [ ] 07-04-PLAN.md — WEB-P1-5 mobile topbar overflow menu: `max-[599px]` Tailwind breakpoint, `⋯` popover with role=menu using `z-topbar-primary` from Phase 6 06-02; cross-phase BLOCKED BY Phase 6 plan 06-02 (Wave 1; pre-flight check enforces Phase 6 dep)

**Wave structure:**
- **Wave 1 (parallel, file-disjoint):** 07-01 (TerminalPanel.js + AppShell.js main panel), 07-03 (SessionRow.js, gated on Phase 6 06-03), 07-04 (Topbar.js, gated on Phase 6 06-02)
- **Wave 2 (after 07-01):** 07-02 (AppShell.js aside + EmptyStateDashboard.js + styles.src.css) — depends on 07-01 because both touch AppShell.js

**Ordering constraints:**
- P1-3 (plan 3) BLOCKED BY WEB-P0-3 — action button absolute overlay must ship before row density can be verified
- P1-5 (plan 4) BLOCKED BY WEB-P0-1 — hamburger z-index fix is prerequisite
- P1-1, P1-2, P1-4 are independent and can run in parallel

---

### Phase 8: Performance

**Goal:** Hit premium perf budgets — first-load wire size <150 KB gzipped (from 668 KB), FCP <500ms, LCP <1s, TBT <100ms, zero listener leaks, zero JS errors on load. Eleven internal items with strict ordering constraints: PERF-A+J ship together (same middleware PR), PERF-E before PERF-D (listener cleanup before lazy import), PERF-K depends on WEB-P0-3 + WEB-P1-3 (stable row height), and PERF-H ships LAST (minification obscures pre-existing bugs).
**Depends on:** Phase 6 (WEB-P0-3), Phase 7 (WEB-P1-3)
**Requirements:** PERF-A, PERF-B, PERF-C, PERF-D, PERF-E, PERF-F, PERF-G, PERF-H, PERF-I, PERF-J, PERF-K

**Success Criteria (what must be TRUE for users):**
1. User on cold load sees first contentful paint in <500ms and largest contentful paint in <1s (down from the current ~1.2s/~2s baseline); byte-weight assertions show first-load <150 KB gzipped
2. User in a long-running session no longer accumulates listener leaks — listener count at rest stays ~50 (down from 290→625 growth) because `AbortController` pattern replaces manual `removeEventListener` in `TerminalPanel.js`
3. User typing in the search input sees <8ms response (half a frame, down from 33ms / 2 frames) — debounced or memoized filter
4. User expanding a group no longer triggers 152 unrelated `SessionRow` rerenders — buttons memoized via `memo()`; collapse state isolated in `GroupRow.js`
5. User with 100+ sessions sees the sidebar render via virtualized windowing (feature-flagged via `agentdeck_virtualize=1`; gated at count >50); scroll anchor preserved on collapse/expand; keyboard + screen reader nav preserved

**Plans:** 5 plans across 4 waves (see `.planning/phases/08-performance/`)

Plans:
- [ ] 08-01-PLAN.md — PERF-A + PERF-J static-file middleware (Wave 1): `github.com/klauspost/compress/gzhttp` v1.18.4 + Cache-Control headers via new file `internal/web/middleware.go`; wrap ONLY `/static/` prefix (SSE and WebSocket routes bypass structurally); content-type allowlist excludes `text/event-stream`; hashed assets get `public, max-age=31536000, immutable`, non-hashed and index.html get `no-cache, must-revalidate`. Single atomic PR per ordering constraint "PERF-A + PERF-J same PR".
- [ ] 08-02-PLAN.md — PERF-E listener cleanup via AbortController (Wave 1, parallel with 08-01): single `new AbortController()` in TerminalPanel.js useEffect; every addEventListener (9 total: 4 touch on container, 1 window resize, 1 anonymous touchstart, 4 on WebSocket) carries `{ signal: controller.signal }`; cleanup replaces all manual removeEventListener with a single `controller.abort()`. Blocks plan 08-03 per ordering constraint "PERF-E BEFORE PERF-D".
- [ ] 08-03-PLAN.md — PERF-B defer + PERF-C canvas delete + PERF-D WebGL preload + PERF-F debounce + PERF-G memo + PERF-I costs POST (Wave 2; depends on 08-02): Chart.js gets `defer` attribute (NOT lazy import per Pitfall 4); delete dead `internal/web/static/vendor/addon-canvas.js`; `<link rel="preload" as="script">` for WebGL addon on desktop only (NOT dynamic import per Pitfall 5); `useDebounced` 250ms hook on search filter; `memo(SessionRowImpl, areEqual)` wrap; verify local `isOpen` state in GroupRow.js; `/api/costs/batch` converted GET→POST (backend handler + frontend fetch).
- [ ] 08-04-PLAN.md — PERF-K SessionList virtualization (Wave 3; depends on 08-03 for PERF-G): hand-rolled `useVirtualList` hook in `internal/web/static/app/useVirtualList.js` (binary search on offsets, ResizeObserver for variable group headers, overscan 6); feature-flagged via `localStorage.getItem('agentdeck_virtualize') === '1'`; gated at `sessions.length > 50`; keyboard ArrowUp/ArrowDown uses `scrollIntoView({ block: 'nearest' })`; ARIA `role="list"` + `aria-rowcount` = total + `aria-rowindex` = 1-based real index; scroll anchor preserved across collapse/expand. Pre-flight task ABORTS if Phase 6 WEB-P0-3 + Phase 7 WEB-P1-3 + plan 08-03 PERF-G invariants are missing.
- [ ] 08-05-PLAN.md — PERF-H esbuild bundling (Wave 4; depends on 08-01, 08-02, 08-03, 08-04): `github.com/evanw/esbuild/pkg/api` v0.28.0 via `go generate ./internal/web/...`; new `internal/web/bundle.go` with `//go:build ignore`; new `internal/web/assets.go` with `LoadAssets` + `ResolveAsset` + `SubstitutePlaceholders`; `Splitting: true, Format: FormatESModule`; hashed filenames in `internal/web/static/dist/`; `{{ASSET:app/main.js}}` placeholder substitution in hand-written index.html at request time (Pitfall 3 mitigation); `AGENTDECK_WEB_BUNDLE=0` env var rollback to dev mode; byte budget gate `< 150 KB gzipped first-party`. Ships ABSOLUTELY LAST per ordering constraint "PERF-H LAST in Phase 8".

**Wave structure:**
- **Wave 1 (parallel, file-disjoint):** 08-01 (middleware.go + server.go + go.mod), 08-02 (TerminalPanel.js)
- **Wave 2 (after 08-02):** 08-03 (index.html + TerminalPanel.js + SessionList.js + SessionRow.js + GroupRow.js + handlers_costs.go + useDebounced.js)
- **Wave 3 (after 08-03):** 08-04 (useVirtualList.js + SessionList.js)
- **Wave 4 (after 08-01, 08-02, 08-03, 08-04):** 08-05 (assets.go + bundle.go + server.go + index.html + go.mod)

**Ordering constraints:**
- PERF-A + PERF-J ship in SAME PR (same middleware file) — plan 08-01
- PERF-E BEFORE PERF-D (listener cleanup must be safe before adding async lazy import) — plan 08-02 before plan 08-03
- PERF-K BLOCKED BY WEB-P0-3 + WEB-P1-3 (stable row height) AND PERF-G (memoized rows) — plan 08-04 pre-flight gate enforces all three
- PERF-H ships LAST — plan 08-05 depends on all prior Phase 8 plans; minification obscures pre-existing bugs
- PERF-C is a deletion (dead code removal), lives in plan 08-03 alongside the other front-end quick wins

---

### Phase 9: Polish

**Goal:** Premium UX refinements that separate "works" from "premium." Skeleton loader matching final layout exactly (Linear/Vercel pattern), button transitions, profile dropdown filter, group divider gap, currency locale, light theme audit (LAST in phase). POL-7 ships WITH WEB-P0-4 in Phase 6 (same Toast.js refactor) but is listed here for traceability.
**Depends on:** Phase 8 (especially PERF-H — skeleton must match bundled layout; POL-6 audit must run on final layout)
**Requirements:** POL-1, POL-2, POL-3, POL-4, POL-5, POL-6, POL-7

**Success Criteria (what must be TRUE for users):**
1. User sees a skeleton loader matching the final sidebar layout EXACTLY during the 126ms cold load gap — no more blank UI flash; uses Tailwind `animate-pulse` (no library); respects `prefers-reduced-motion`
2. User hovering over a session row sees action buttons fade in with 120ms opacity transition — no more snap show/hide; respects `prefers-reduced-motion`
3. User opens the profile dropdown and sees `_*` test profiles filtered out and the dropdown scrollable with `max-height: 300px` when the profile list is long
4. User sees the cost dashboard render currency respecting `navigator.language` via `Intl.NumberFormat` (shows `$` for en-US, `US$` for de-DE, etc.)
5. User sees the light theme rendering consistently across sidebar, terminal, dialogs, tooltips, toasts, empty state, and cost dashboard — no contrast failures, missing borders, or washed-out colors

**Plans (4, partial parallelization):**
1. **P9-plan-1: POL-1 skeleton loader + POL-2 action button transitions + POL-4 group divider gap** (parallel) — Tailwind `animate-pulse` skeleton matching final layout; 120ms opacity fade on action buttons; divider gap 48px→12-16px
2. **P9-plan-2: POL-3 profile dropdown filter + POL-5 currency locale** (parallel with plan 1) — filter `_*` test profiles; `max-height: 300px` scroll; `Intl.NumberFormat(navigator.language, ...)` in CostDashboard
3. **P9-plan-3: POL-7 toast refinement** (ships with Phase 6 P0-4; listed for traceability) — already covered by Phase 6 plan 4
4. **P9-plan-4: POL-6 light theme audit** (SERIAL; LAST in phase) — audit across all surfaces; fix contrast issues, missing borders, washed-out colors; must run AFTER all layout/component work is final

**Ordering constraints:**
- POL-1 depends on PERF-H shipping (skeleton must match bundled layout)
- POL-6 MUST ship LAST in Phase 9 — audit after all layout is final
- POL-7 SHIPS WITH WEB-P0-4 in Phase 6 (same Toast.js refactor, same PR)
- POL-1..POL-5 can run in parallel

---

### Phase 10: Automated Testing

**Goal:** Lock in the gains from Phases 6-9 so they cannot silently regress. Visual regression with committed baselines blocks merge on >0.1% diff. Lighthouse CI enforces perf budgets. Functional E2E covers session lifecycle + group CRUD. Mobile E2E covers 3 viewports. TEST-E scoped DOWN to alert-only per pitfalls research (auto-fix is v1.6+ experiment). Baselines captured at the END of Phase 9, not during Phase 10 start — baselines of a non-final render waste the baseline budget.
**Depends on:** Phase 9 complete (all visual work final)
**Requirements:** TEST-A, TEST-B, TEST-C, TEST-D, TEST-E

**Success Criteria (what must be TRUE for users):**
1. Contributor sees CI block merge on any PR with >0.1% visual diff against committed baselines — baselines captured in Docker (`mcr.microsoft.com/playwright:v1.59.1-jammy`) for stable font rendering
2. Contributor sees CI block merge on any PR that exceeds first-load byte budget (hard gate); FCP/LCP/TBT regressions surface as warnings (median of 5 runs via `@lhci/cli 0.15.1` + `treosh/lighthouse-ci-action@v12`)
3. Contributor sees functional E2E tests covering the full session lifecycle (create→attach→send input→verify output→stop→delete) and group CRUD (create→add session→reorder→delete) via web
4. Contributor sees mobile E2E tests running at iPhone SE (375×667), iPhone 14 (390×844), and iPad (768×1024) viewports — hamburger, overflow menu, sidebar drawer, terminal attach, form input all covered
5. On scheduled weekly workflow, visual regression + Lighthouse failures post an issue with diff images and failed metrics (alert-only; no auto-fix PR creation — deferred to v1.6+)

**Plans (4, strict ordering):**
1. **P10-plan-1: TEST-A visual regression infrastructure** (FIRST; only after Phase 9 complete) — Docker runner, animation kill via `addStyleTag`, `page.clock.install` clock freeze, dynamic content masking, `maxDiffPixelRatio: 0.001`, baselines in `tests/e2e/visual/__screenshots__/`; `.github/workflows/visual-regression.yml`
2. **P10-plan-2: TEST-B Lighthouse CI** (depends on PERF-H shipped + 10 main-branch runs for p95 calibration) — `@lhci/cli 0.15.1`, `numberOfRuns: 5` (median), `temporary-public-storage` upload, byte-weight as HARD gates, FCP/LCP/TBT as soft warnings initially; `.lighthouserc.json` in repo root
3. **P10-plan-3: TEST-C functional E2E + TEST-D mobile E2E** (parallel) — extend `tests/e2e/` with `session-lifecycle.spec.ts` + `group-crud.spec.ts` + mobile projects config at 3 viewports
4. **P10-plan-4: TEST-E alert-only weekly workflow** (independent) — `.github/workflows/auto-fix-weekly.yml`; visual regression + Lighthouse on schedule; on failure post issue with diff images + failed metrics; NO auto-fix PR creation

**Ordering constraints:**
- TEST-A MUST be first test written (the "hello world" baseline is the gate); only after Phase 9 complete
- TEST-B depends on PERF-H shipped + 10 baseline runs on main for p95 threshold calibration
- TEST-C, TEST-D can run in parallel after TEST-A infrastructure is solid
- TEST-E scoped to alert-only per Pitfall 15 (auto-fix loops are v1.6+)

---

### Phase 11: Release v1.5.0

**Goal:** Ship v1.5.0 with all gates green — clean build, Go 1.24.0 verified, visual verification pass, macOS smoke test, real-device mobile test, comprehensive changelog. Locked by CLAUDE.md release rules.
**Depends on:** Phase 10 complete (all tests green)
**Requirements:** REL-1, REL-2, REL-3, REL-4, REL-5

**Success Criteria (what must be TRUE for users):**
1. User `brew upgrade agent-deck` pulls v1.5.0 — tagged with clean build (`vcs.modified=false`), Go 1.24.0 verified via `go version -m ./build/agent-deck`
2. Maintainer runs `scripts/visual-verify.sh` and all 5 TUI states pass (main screen, new session dialog, settings panel, session running, help overlay)
3. Maintainer runs manual macOS smoke test and session create/restart/stop all work with an existing state.db from a prior version
4. User reading the v1.5.0 changelog sees regressions + P0 fixes + P1 fixes + perf wins with before/after byte counts + polish items + testing infrastructure documented
5. User on iPhone + iPad over Tailscale can use the web app — terminal input, scrolling, profile switcher, mobile overflow menu, visual theme all working

**Plans (3, sequential release gate):**
1. **P11-plan-1: REL-2 + REL-3 pre-release verification** (FIRST) — `scripts/visual-verify.sh` across all 5 TUI states + manual macOS smoke test with existing state.db
2. **P11-plan-2: REL-1 tag + ship + REL-4 changelog** (after plan 1 passes) — bump `const Version` in `cmd/agent-deck/main.go`; `make ci`; `make build`; verify Go 1.24.0 + `vcs.modified=false`; tag + push; `make release-local`; write changelog
3. **P11-plan-3: REL-5 real-device mobile verification** (AFTER release ships) — real iPhone + iPad over Tailscale; terminal input, scrolling, profile switcher, overflow menu, theme — document findings in release notes addendum if any issues

**Ordering constraints:**
- REL-2 + REL-3 before REL-1 (cannot tag without verification)
- REL-4 with REL-1 (changelog part of the release)
- REL-5 after ship (real-device test on shipped binary)
- Sequential; no parallelization

---

## Progress Table

| Phase | Plans Complete | Status | Completed |
|-------|----------------|--------|-----------|
| 5. Critical Regressions | — | COMPLETE (v1.4.1) | 2026-04-08 |
| 6. Critical P0 Bugs | 5/5 | COMPLETE | 2026-04-08 |
| 7. P1 Layout Bugs | 4/4 | COMPLETE | 2026-04-09 |
| 8. Performance | 0/5 | Planned | — |
| 9. Polish | 0/4 | Not started | — |
| 10. Automated Testing | 0/4 | Not started | — |
| 11. Release v1.5.0 | 0/3 | Not started | — |

---

## Cross-Phase Ordering Constraints Summary

These are non-negotiable dependencies from research (SUMMARY.md + PITFALLS.md + ARCHITECTURE.md):

1. **WEB-P1-3 BLOCKED BY WEB-P0-3** — virtual list (PERF-K) requires stable row height; absolute action buttons must ship before row density
2. **WEB-P0-3 BLOCKS PERF-K** — same `SessionList.js` component; conflict avoidance
3. **WEB-P0-1 BLOCKS WEB-P1-5** — topbar z-index fix is prerequisite for mobile overflow menu
4. **PERF-E BEFORE PERF-D** — listener leak (AbortController) must ship before dynamic WebGL import (safe async cleanup)
5. **PERF-A + PERF-J SAME PR** — both are middleware, same file (`internal/web/middleware.go`)
6. **PERF-H LAST in Phase 8** — minification obscures pre-existing bugs; baselines captured pre-bundle are invalid
7. **POL-6 LAST in Phase 9** — light theme audit only after all layout is final
8. **POL-7 SHIPS WITH WEB-P0-4** — same Toast.js refactor, same PR (Phase 6 plan 06-04)
9. **TEST-A BASELINES captured at END of Phase 9**, not during Phase 10 start
10. **WEB-P0-2 DECISION GATE** — first task of Phase 6 investigates whether `server.go` supports per-request profile override; result determines option A (reload) vs option B (remove dropdown)
11. **PERF-K INVESTIGATE FIRST** — the 876-node empty DOM may be the culprit; virtualization solves nothing if so (per Open Question #5 in SUMMARY.md)

---

## Parallelization Opportunities

Per `.planning/config.json` parallelization=true:

- **Phase 6:** Wave 1 serial (06-01 P0-2 decision gate); Wave 2 parallel (06-02 P0-1, 06-03 P0-3, 06-04 P0-4 mitigation + POL-7); Wave 3 (06-05 P0-4 prevention after Wave 2)
- **Phase 7:** WEB-P1-1, WEB-P1-2, WEB-P1-4 can run in parallel; WEB-P1-3 and WEB-P1-5 have hard dependencies on Phase 6
- **Phase 8:** Wave 1 parallel (08-01 gzip + cache middleware, 08-02 listener cleanup); Wave 2 (08-03 front-end perf bundle after 08-02); Wave 3 (08-04 virtualization after 08-03); Wave 4 (08-05 esbuild bundling ships absolutely last after all prior Phase 8 plans)
- **Phase 9:** POL-1..POL-5 parallel; POL-6 SERIAL LAST; POL-7 ships with WEB-P0-4 in Phase 6
- **Phase 10:** TEST-A first (infrastructure), TEST-C + TEST-D parallel after, TEST-B after PERF-H, TEST-E independent
- **Phase 11:** Sequential only (release gate)

---

## Stack Additions (2 new Go dependencies only)

Per `.planning/research/STACK.md`:

- `github.com/klauspost/compress/gzhttp` v1.18.4 — gzip middleware (PERF-A); wraps ONLY `/static/`, NOT SSE/WS
- `github.com/evanw/esbuild/pkg/api` v0.28.0 — JS bundler via `go generate` (PERF-H); NOT the npm CLI

Everything else is deletion (`addon-canvas.js`) or hand-roll (virtualization, skeleton, toast). No npm dependencies. No runtime npm. Single-binary invariant preserved.

---

## Constraints (carried from PROJECT.md)

- **Go 1.24.0 toolchain pinned** — `GOTOOLCHAIN=go1.24.0` in Makefile and `.goreleaser.yml`
- **No SQLite schema changes** — localStorage for any new persistence
- **No runtime profile switching** — reload with `?profile=X` only (if WEB-P0-2 option A)
- **3-5 PR batches** — `make ci` + macOS TUI test between each batch
- **Clean builds only** — `vcs.modified=false` via `go version -m ./build/agent-deck`
- **Visual verification mandatory** — `scripts/visual-verify.sh` before every release
- **Performance targets** — <150 KB gzipped first-load, FCP<500ms, LCP<1s, TBT<100ms
- **Mobile support** — iPhone SE (375px) and up
- **TDD** — regression test BEFORE fix; test fails without fix
- **Visual regression gate** — CI blocks merge when diff >0.1%
- **Lighthouse gate** — CI blocks merge on byte budget regression (FCP/LCP/TBT as warnings initially)

---

*Roadmap created: 2026-04-08 from REQUIREMENTS.md + research/SUMMARY.md. Replaces archived v1.4.0 roadmap at `.planning/archive/v1.4.0/ROADMAP.md`.*
