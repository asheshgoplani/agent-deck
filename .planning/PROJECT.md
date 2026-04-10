# Agent Deck

## What This Is

Terminal session manager for AI coding agents. Go + Bubble Tea TUI that manages tmux sessions for Claude Code, Gemini CLI, Codex, OpenCode, and other AI tools. Ships as a single static binary with CLI, TUI, and an embedded web server (Preact SPA + WebSocket terminal bridge) for remote access over localhost, LAN, or Tailscale.

## Core Value

Reliable session management for AI coding agents: users can create, monitor, and control many concurrent agent sessions from anywhere (desktop terminal, mobile browser, web) without losing work or context.

## Current Milestone: v1.5.0 — Premium Web App

**Starting point:** v1.4.1 (2026-04-08). v1.4.0 shipped the web app redesign with 4 executed phases (Tailwind precompile, critical bug fixes, UX/cosmetic polish). v1.4.1 was an emergency patch that fixed 6 regressions shipped with v1.4.0 (REG-01 through REG-06: CSI u reader, tmux scrollback, mousewheel, conductor heartbeat, tmux PATH detection, bash -c quoting).

**User testing verdict (post-v1.4.1):**
- **4 P0 bugs remain** from the v1.3.4 audit that were NOT actually fixed in v1.4.0 (profile switcher broken, mobile topbar hamburger unclickable, session title truncation STILL broken, mutations-disabled toast spam)
- **5 P1 layout bugs** (sidebar density, terminal panel sizing, fixed sidebar width, empty-state void, mobile topbar overflow)
- **12 performance bottlenecks** (no gzip = 518 KB extra wire per cold load, Chart.js blocking, WebGL broken fallback, listener leaks, etc.)
- **7 polish items** that separate "works" from "premium"

**Goal:** Ship a **premium-quality** v1.5.0 where the web app feels instant and snappy, all remaining bugs are fixed, the desktop uses screen real estate properly, mobile is fully functional, the terminal fills its pane, and automated visual regression tests prevent future regressions. The app should represent what agent-deck deserves: premium, polished, production-ready.

**Source spec:** `docs/WEB-APP-V15-SPEC.md`

**Target features:**
- All 4 P0 bugs fixed and visually verified
- All 5 P1 layout bugs fixed
- First-load wire size < 150 KB gzipped (from 668 KB today)
- FCP < 500ms, LCP < 1s, TBT < 100ms
- Zero JS errors on load, zero listener leaks
- Profile switching actually works (or is removed if out-of-scope)
- Mobile fully functional on iPhone SE (375×667), iPhone 14 (390×844), iPad (768×1024)
- Visual regression tests in CI blocking merge on >0.1% diff
- Release v1.5.0 published with comprehensive changelog

## Requirements

### Validated

<!-- Shipped and confirmed valuable across v0.x-v1.4.x -->

- ✓ TUI session management (create, start, stop, restart, fork, delete) — v0.x
- ✓ Group hierarchy (path-based, drag-to-reorder) — v0.x
- ✓ MCP attach/detach per session with LOCAL vs GLOBAL scope — v0.x
- ✓ Cost tracking for Claude Code sessions — v0.x
- ✓ Multi-profile support (isolated state.db per profile) — v0.x
- ✓ Conductor workflow (multi-agent orchestration) — v0.x
- ✓ Git worktree integration — v0.x
- ✓ Global search across conversations — v0.x
- ✓ Web app with Preact + HTM + Tailwind + xterm.js — v1.3.x
- ✓ Session CRUD from web — v1.3.x
- ✓ SSE menu snapshots + WebSocket terminal bridge — v1.3.x
- ✓ Responsive layout framework (desktop/tablet/mobile) — v1.3.4
- ✓ Tokyo Night dark/light/auto theme toggle — v1.3.4
- ✓ Playwright E2E suite (25 specs across desktop + mobile) — v1.4.0
- ✓ Tailwind v4 precompile via `go generate` — v1.4.0 Phase 1
- ✓ Critical web bug fixes (CONDUCTOR vanish, JS errors, 0-width names) — v1.4.0 Phase 2
- ✓ UX polish (button overlap, keyboard hints, search placeholder, chart colors) — v1.4.0 Phase 4
- ✓ Cosmetic fixes (focus flicker, full-page screenshots, clean-build suffix) — v1.4.0 Phase 4
- ✓ 6 critical regressions fixed (CSI u, tmux scrollback, mousewheel, heartbeat, PATH, bash -c) — v1.4.1
- ✓ WEB-P0-1: mobile hamburger clickable across all viewports via systematic 7-level z-index scale (Tailwind v4 `--z-index-*` namespace) — v1.5.0 Phase 6
- ✓ WEB-P0-2: profile switcher shipped as Option B read-only label (single-profile `role="status"`, multi-profile `aria-disabled` listbox) — decision gate resolved: `server.go:79` binds `cfg.Profile` once at `NewServer()`, per-request override out of scope — v1.5.0 Phase 6
- ✓ WEB-P0-3: session title truncation eliminated (action toolbar converted from in-flow flex to `absolute right-2 top-1/2` overlay, title width 82px → 184px at 1280x800; row height stable at 44px for PERF-K) — v1.5.0 Phase 6
- ✓ WEB-P0-4 + POL-7: toast stack capped at 3, errors sticky, `ToastHistoryDrawer` persists last 50 to localStorage; prevention layer hides write buttons + `CreateSessionDialog` when `webMutations=false` — v1.5.0 Phase 6

### Active (v1.5.0 scope)

**Phase 5 — Critical Regressions (SHIPPED in v1.4.1):**
- [x] REG-01: CSI u reader wired, Shift+letter no longer dropped (PR #535)
- [x] REG-02: tmux scrollback preserved, history-limit respected (PR #533)
- [x] REG-03: Mousewheel no longer shows [0/0] (PR #531)
- [x] REG-04: Conductor heartbeat works on Linux (PR #522)
- [x] REG-05: tmux detected from well-known paths when not in PATH (PR #525)
- [x] REG-06: bash -c quoting bug fixed (PR #526)

**Phase 6 — Web App Critical P0 Bugs (COMPLETE 2026-04-08, 5/5 plans):**
- [x] WEB-P0-1: Mobile hamburger clickable (7-level z-index scale, hamburger `z-topbar-primary:45`, right-side controls `z-topbar:40`)
- [x] WEB-P0-2: Profile switcher shipped as Option B read-only label (Option A ruled out: `server.go:79` binds profile once at NewServer())
- [x] WEB-P0-3: Session title truncation fixed (action toolbar absolute-positioned, 186px reservation eliminated, row height stable)
- [x] WEB-P0-4: Mitigation (cap-3 toast + sticky errors + history drawer) + prevention (mutationsEnabledSignal hides write UI when webMutations=false)

**Phase 7 — Web App P1 Layout Bugs:**
- [ ] WEB-P1-1: Terminal panel fills its container (tmux pane resize matches browser viewport, or xterm fit addon triggers correctly)
- [ ] WEB-P1-2: Sidebar fluid width via `clamp(260px, 22vw, 380px)` OR drag handle to resize
- [ ] WEB-P1-3: Sidebar row density increased to 40-44px (20+ sessions visible instead of 12)
- [ ] WEB-P1-4: Empty-state dashboard uses card layout with max-width on big screens
- [ ] WEB-P1-5: Mobile topbar collapses right-side controls into overflow menu when viewport < 600px

**Phase 8 — Performance (Premium Feel):**
- [ ] PERF-A: gzip compression on static files (~518 KB saved on wire per cold load)
- [ ] PERF-B: Chart.js deferred or lazy-imported (206 KB no longer blocks parser)
- [ ] PERF-C: xterm canvas fallback fixed or dead fallback removed
- [ ] PERF-D: WebGL addon (126 KB) lazy-loaded via dynamic import
- [ ] PERF-E: Event listener leak fixed (no more 290→625 growth over session)
- [ ] PERF-F: Search typing debounced (no more 33ms / 2-frame lag)
- [ ] PERF-G: Sidebar buttons memoized (no full-tree rerender on collapse toggle)
- [ ] PERF-H: ES module bundling via esbuild --bundle --splitting --format=esm
- [ ] PERF-I: `/api/costs/batch` converted from GET query to POST body (avoids 414)
- [ ] PERF-J: Cache-Control: public, max-age=31536000, immutable on hashed assets
- [ ] PERF-K: SessionList virtualized to lower DOM baseline (876 nodes + 290 listeners)

**Phase 9 — Polish (Premium UX):**
- [ ] POL-1: Skeleton loading state eliminates 126ms of blank UI before sidebar renders
- [ ] POL-2: Action button transitions (opacity 120ms instead of snap)
- [ ] POL-3: Profile dropdown filters `_*` test profiles + scrollable when long
- [ ] POL-4: Group divider gap reduced from 48px to 12-16px
- [ ] POL-5: Cost dashboard respects user locale for currency symbol
- [ ] POL-6: Light theme re-audited for all surfaces
- [x] POL-7: Stacked toast auto-dismiss + stack cap (shipped early with WEB-P0-4 in Phase 6, same Toast.js refactor)

**Phase 10 — Automated Testing (No More Regressions):**
- [ ] TEST-A: Visual regression in CI with committed baselines blocking merge on >0.1% diff
- [ ] TEST-B: Lighthouse CI with FCP/LCP/TBT thresholds blocking merge
- [ ] TEST-C: Functional E2E covering session lifecycle and group CRUD via web
- [ ] TEST-D: Mobile E2E at 375×667, 390×844, 768×1024 viewports
- [ ] TEST-E: Auto-fix loop on scheduled weekly workflow (visual fail → agent creates fix PR)

**Phase 11 — Release v1.5.0:**
- [ ] REL-1: v1.5.0 tagged with clean build (`vcs.modified=false`), Go 1.24.0 toolchain verified
- [ ] REL-2: Visual verification (`scripts/visual-verify.sh`) passes for all 5 TUI states
- [ ] REL-3: Manual macOS smoke test passes (session create, restart, stop with existing state.db)
- [ ] REL-4: v1.5.0 changelog documents regressions + P0/P1 fixes + perf + polish + testing
- [ ] REL-5: Mobile verified on real device over Tailscale (iPhone + iPad)

### Out of Scope

- **Tech stack change** — Preact + HTM + Tailwind + xterm.js is locked (v1.4.0 research verdict). No framework swap.
- **SQLite schema changes** — v1.5.0 explicitly avoids schema changes to prevent recurrence of PR #385 ALTER TABLE incident. Any persistence goes to localStorage.
- **Windows native support** — Tailscale from Mac/iPhone covers remote access; Windows demand not yet validated.
- **Complete rewrites** — This is a polish milestone. Any component that "needs rewriting" must be isolated in its own plan with explicit justification.
- **New features beyond the spec** — Scope is locked to the spec document. Feature requests defer to v1.6+.
- **iOS/Android native apps** — PWA via web app remains the mobile path.
- **Runtime profile switching via API** — If WEB-P0-2 is implemented, it's via page reload with `?profile=X`; no backend runtime switch (would require re-architecting the profile isolation model).

## Context

**Brownfield:** Mature codebase at v1.4.1. Codebase map is in `.planning/codebase/`. Architecture is a layered Go monolith: `cmd/agent-deck` → `internal/ui` (Bubble Tea TUI, ~12K lines) + `internal/web` (HTTP/WS/SSE server) + `internal/session` (data model) + `internal/tmux` (tmux abstraction) + `internal/statedb` (SQLite via `modernc.org/sqlite`, no CGO).

**Frontend architecture:** `internal/web/static/app/` holds Preact components. v1.4.0 introduced `go generate` for Tailwind compilation producing `internal/web/static/styles.css`. v1.5.0 introduces a JavaScript bundling step via esbuild to consolidate 24 separate ES module fetches into a split bundle (PERF-H).

**Testing landscape:** Playwright E2E in `tests/e2e/` (25 specs covering desktop + mobile). v1.5.0 adds visual regression with committed baselines, Lighthouse CI for perf budgets, and auto-fix weekly workflow.

**Performance baseline (v1.4.1):** 668 KB first-load wire (uncompressed), Chart.js blocks parser, WebGL addon eagerly loaded, 290 listeners at rest, 876 DOM nodes before any session, no gzip, no cache headers. v1.5.0 target: <150 KB gzipped, FCP<500ms, LCP<1s, TBT<100ms.

**Key files to modify (from spec):**
- Frontend: `app/Sidebar.js` (P0-1, P1-2, P1-3, PERF-G, PERF-K), `app/SessionList.js` (P0-3, PERF-K), `app/TerminalPanel.js` (P1-1, PERF-E), `app/CostDashboard.js` (PERF-B), `app/SearchFilter.js` (PERF-F), `app/AppShell.js` (P1-5 mobile topbar, POL-3 profile), `app/Toast.js` (P0-4, POL-7), `app/EmptyStateDashboard.js` (P1-4)
- Backend: `server.go` (gzip handler, cache headers), `handlers_costs.go` (PERF-I POST conversion), `static_files.go` (gzip + cache)
- Build: `Makefile` (esbuild integration), new `esbuild.config.mjs` or `go generate` for JS bundling
- Tests: new `tests/e2e/visual/` specs, `tests/e2e/mobile/` specs, `.github/workflows/visual-regression.yml`, `.github/workflows/lighthouse-ci.yml`, `.github/workflows/auto-fix-weekly.yml`

**GitHub issues still tracked for future:** #391 (per-session colors), #434 (Ctrl+Q), #447 (reorg groups) — deferred to v1.6+.

## Constraints

- **Tech stack**: Preact + HTM + Tailwind + xterm.js — locked from v1.4.0 research. No framework swap.
- **Go toolchain**: Pinned to 1.24.0 via `GOTOOLCHAIN=go1.24.0` in `Makefile` and `.goreleaser.yml`. Go 1.25 silently breaks macOS TUI (2026-03-26 incident). Non-negotiable.
- **No SQLite schema changes**: v1.5.0 explicitly avoids schema changes. localStorage for any new persistence.
- **No backend runtime profile switching**: If WEB-P0-2 is solved, it's via page reload with `?profile=X`. Profile isolation model stays intact.
- **Batch sizing**: 3-5 PRs per batch with `make ci` + macOS TUI test between each batch. Never merge 15+ PRs at once (the v0.27.0 anti-pattern).
- **Release builds**: Must verify `vcs.modified=false` via `go version -m ./build/agent-deck`. Dirty builds never ship.
- **Visual verification**: Mandatory before every release. `scripts/visual-verify.sh` must pass for all 5 TUI states.
- **Performance target**: First-load < 150 KB gzipped (from 668 KB). FCP < 500ms, LCP < 1s, TBT < 100ms.
- **Mobile**: Must work on 375px viewport (iPhone SE) and up.
- **Testing philosophy**: Every shipped bug is a missing test. Regression test must be written *before* the fix, and must fail without the fix.
- **Visual regression gate**: CI blocks merge when visual diff > 0.1%. Baselines committed per bug.
- **Lighthouse gate**: CI blocks merge when FCP > 500ms, LCP > 1s, or TBT > 100ms.

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|---------|
| Keep Preact + HTM + Tailwind + xterm.js stack | v1.4.0 research validated against ttyd, PocketBase, syncthing, code-server | ✓ Locked (carried from v1.4.0) |
| Introduce esbuild JS bundling (PERF-H) | 24 separate ES module fetches on cold load — bundling cuts request count and enables effective caching | — Pending |
| Enable gzip on static files (PERF-A) | Biggest single win — ~518 KB saved on wire per cold load. Simple `gziphandler.GzipHandler` wrap. | — Pending |
| Profile switcher: fix via reload OR remove | Current dropdown lies (clicks do nothing). Runtime profile switch would require re-architecting profile isolation; page reload with `?profile=X` is the pragmatic path. If that's too invasive, remove dropdown and show read-only label. | ✓ Resolved Phase 6 plan 06-01 (Option B — read-only multi-profile listbox, single-profile `role="status"`, always-visible HELP_TEXT). POL-3 (Phase 9 plan 09-02) extended with `_*` filter and `max-h-[300px] overflow-y-auto`. |
| Session title truncation fix via absolute-positioned action buttons | v1.4.0 Phase 3's `min-w-0` propagation did not solve it — 76% still truncated. Root cause is buttons reserving 90px even when hidden. Absolute overlay fixes reservation. | ✓ Resolved Phase 6 plan 06-03 (title span ratio 0.30 → 0.66). |
| Toast auto-dismiss + stack cap | 403 errors when mutations disabled currently spawn infinite undismissable toasts. Auto-dismiss 5s + cap 3 is the pragmatic fix. | ✓ Resolved Phase 6 plan 06-04 (eviction-with-history pattern, visible stack cap 3, error-FIFO, info/success 5s auto-dismiss, errors preserved + ToastHistoryDrawer). POL-7 shipped early with WEB-P0-4. Phase 9 plan 09-03 locked a regression guard. |
| Visual regression in CI with committed baselines | v1.4.0 user testing revealed 4 P0 bugs slipped through manual review. Automated visual diff prevents regression recurrence. | — Pending (Phase 10 TEST-A) |
| Lighthouse CI perf budgets | Premium feel is a binary outcome — either the budgets hold or they don't. CI enforcement prevents slow drift. | — Pending (Phase 10 TEST-B) |
| SessionList virtualization (PERF-K) | 876 DOM nodes before any session is too high. Virtual scrolling via @tanstack/virtual OR hand-rolled is the industry standard answer. | ✓ Resolved Phase 8 plan 08-04 (useVirtualList hook + feature-flagged SessionList integration). |
| esbuild JS bundling (PERF-H) | 24 separate ES module fetches on cold load — bundling cuts request count and enables effective caching. | ✓ Resolved Phase 8 plan 08-05 (esbuild via `go generate` + assets manifest). |
| Light theme WCAG AA audit (POL-6) | Plans 06-03 and 06-04 flagged `text-gray-400` (2.6:1) and translucent backgrounds as "POL-6 territory" — needed a single pass across all surfaces after the final layout landed. | ✓ Resolved Phase 9 plan 09-04 (18 Playwright tests green; 11 axe-core + 7 luminance; `text-gray-400` → `text-gray-600` across 8 components; 17 → 0 violations). |
| Locale-aware currency formatting (POL-5) | `'$' + v.toFixed(2)` ignores user locale; premium apps format per `navigator.language`. | ✓ Resolved Phase 9 plan 09-02 (module-level `Intl.NumberFormat(navigator.language, {style: 'currency', currency: 'USD'})` memoized; both `fmt()` and chart y-axis tick callback delegate to the same instance). |
| Skeleton loader matching final layout (POL-1) | Users see "No sessions" flicker during the cold-load gap before `/api/menu` returns. Linear/Vercel pattern: render a layout-matched skeleton stack. | ✓ Resolved Phase 9 plan 09-01 (new `sessionsLoadedSignal`, tri-state render in SessionList.js, `animate-pulse motion-reduce:animate-none`). |
| auto_advance disabled in GSD config | User explicitly requested each stage in a separate session for context hygiene | ✓ Enforced |

---
*Last updated: 2026-04-09 after Phase 9 (Polish / Premium UX) completion — 4/4 plans shipped, all 7 POL-* requirements validated (POL-1 skeleton loader, POL-2 GroupRow fade, POL-3 profile filter, POL-4 group density, POL-5 locale currency, POL-6 WCAG AA light theme audit, POL-7 toast cap + history drawer). Phases 6, 7, 8, 9 complete (18/24 plans). Phase 10 (Automated Testing — TEST-A/B/C/D/E) next.*
