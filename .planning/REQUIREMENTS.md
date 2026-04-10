# Requirements: Agent Deck v1.5.0 Premium Web App

**Defined:** 2026-04-08
**Core Value:** Users can create, monitor, and control many concurrent AI coding agent sessions from any device (desktop terminal, mobile browser, web) without losing work or context.
**Milestone target:** v1.5.0
**Starting point:** v1.4.1
**Source spec:** `docs/WEB-APP-V15-SPEC.md`
**Research:** `.planning/research/SUMMARY.md` (synthesized from 4 dimensions — Stack, Features, Architecture, Pitfalls)

## v1.5.0 Requirements

Requirements for this milestone. Each maps to exactly one phase.

### Critical Regressions (Phase 5 — SHIPPED in v1.4.1)

Emergency patch that fixed 6 regressions introduced in v1.4.0. All already shipped.

- [x] **REG-01**: Shift+letter keys no longer dropped — CSI u reader wired into `tea.NewProgram` input pipeline (issue #535, PR #537/#536)
- [x] **REG-02**: tmux history-limit respected, scrollback no longer cleared on restart (issue #533, PR #533)
- [x] **REG-03**: Mousewheel no longer shows [0/0] — scrolling works in tmux sessions (issue #531, PR #532)
- [x] **REG-04**: Conductor heartbeat works on Linux — our `grep -o` fix no longer breaks it (issue #522, PR #524/#523)
- [x] **REG-05**: tmux detected from well-known paths when not in PATH (issue #525, PR #527)
- [x] **REG-06**: bash -c quoting bug fixed — session commands always wrapped regardless of content (issue #526, PR shipped)

### Critical P0 Bugs (Phase 6)

Remaining from v1.3.4 audit (not actually fixed in v1.4.0 despite "21 bugs fixed" claim).

- [x] **WEB-P0-1**: Mobile hamburger is clickable on all viewports ≤768px. Costs button (or any topbar element) no longer intercepts pointer events over the hamburger hit target. Systematic z-index scale via CSS custom properties in `styles.css` eliminates future pointer-events fights.
- [x] **WEB-P0-2**: Profile switcher either (a) reloads the page with `?profile=X` and the SPA bootstraps against the selected profile, OR (b) is removed and replaced with a read-only label showing the current profile. **Decision gate**: Phase 6 Task 1 investigates whether backend supports per-request profile override via header. If yes → option (a); if no → option (b). Out of scope either way: runtime profile switching without reload.
- [x] **WEB-P0-3**: Session title truncation is fixed — action buttons (stop/restart/fork/delete) are `position: absolute` with hover-reveal via opacity, no longer reserving 90px of horizontal space when hidden. After fix, title truncation shows only on titles that actually exceed the sidebar width (measured: <10% of typical titles, down from 76%).
- [x] **WEB-P0-4**: When `mutationsEnabled=false`, write buttons (stop/start/restart/fork/delete) are hidden in the UI so users cannot click them. If a 403 somehow still occurs, toast auto-dismisses after 5 seconds and the toast stack is capped at 3 visible at once. (POL-7 also extends the toast component; WEB-P0-4 and POL-7 ship together in the same PR per research.) **Shipped across 06-04 (mitigation: toast cap + history drawer) and 06-05 (prevention: mutationsEnabledSignal hides SessionRow toolbar + CreateSessionDialog).**

### P1 Layout Bugs (Phase 7)

Layout issues making the desktop experience feel broken on large monitors and the mobile experience feel cramped.

- [ ] **WEB-P1-1**: Terminal panel fills its container on attach — no huge empty gray space below the terminal. tmux pane size matches browser viewport cols×rows, OR xterm fit addon triggers properly on container resize events, OR a flex-based `flex: 1` on the terminal container forces fill.
- [ ] **WEB-P1-2**: Sidebar width is fluid via `clamp(260px, 22vw, 380px)` on screens ≥1280px. On 1920px monitors, the main panel no longer wastes 1640px. Drag-to-resize handle is **out of scope** (deferred to v1.6; `clamp()` ships the quick win).
- [ ] **WEB-P1-3**: Sidebar row density is increased to 40px per row (from ~52px). Session rows use `py-1.5 leading-tight`. Target: 20+ sessions visible in sidebar at 1080p instead of 12. Row height is **stable/fixed** (prerequisite for PERF-K virtualization).
- [ ] **WEB-P1-4**: Empty-state dashboard uses a card layout with `max-width: 1024px` centered on big screens. No more "sea of gray" floating text on 1920px monitors. Optionally includes 2-3 quick-start widgets (session count, recent sessions, create button).
- [ ] **WEB-P1-5**: Mobile topbar (viewport <600px) collapses right-side controls (Costs, Info, Settings, Profile) into an overflow `⋯` menu. Header no longer wraps or clips on iPhone SE (375×667). **Blocked by WEB-P0-1** (hamburger must be clickable first).

### Performance (Phase 8 — Premium Feel)

12 performance bottlenecks surfaced in post-v1.4.1 performance audit. First-load wire size drops from 668 KB → <150 KB gzipped. FCP <500ms, LCP <1s, TBT <100ms.

- [ ] **PERF-A**: gzip compression wraps the static file handler ONLY (not the full mux — SSE and WebSocket routes excluded). Uses `github.com/klauspost/compress/gzhttp` v1.18.4 (maintained successor to NYTimes/gziphandler). Biggest single win: ~518 KB saved on wire per cold load. New file: `internal/web/middleware.go`.
- [ ] **PERF-B**: Chart.js 206 KB script tag gets `defer` attribute in `index.html` so it no longer blocks the HTML parser. Dynamic import (`lazy-load`) deferred to v1.6 due to UMD→ESM migration risk identified in pitfalls research.
- [ ] **PERF-C**: xterm canvas fallback chain is cleaned up — either `addon-canvas.js` is deleted from `vendor/` (preferred, since xterm v6 removed canvas renderer entirely per stack research) OR the dead fallback code in `TerminalPanel.js` is removed. Fallback chain is now WebGL → DOM only.
- [ ] **PERF-D**: WebGL addon (126 KB) is lazy-loaded via `await import('@xterm/addon-webgl')` inside `TerminalPanel.js` mount, desktop only. Mobile skips the import entirely and uses DOM renderer. Saves 126 KB on mobile cold load.
- [ ] **PERF-E**: Event listener leak in `TerminalPanel.js` is fixed via `AbortController` pattern — single `controller.abort()` in cleanup replaces 4 bare `ws.addEventListener` calls and the anonymous arrow `touchstart` listener at line 204. Listener count at rest drops from 290→~50 and no longer grows over a session. Confirmed bug, not speculative.
- [ ] **PERF-F**: Search input typing is debounced (250ms) OR the filter result is memoized via `useMemo`. Current lag: 33ms (2 animation frames). Target: <8ms (half a frame).
- [ ] **PERF-G**: `SessionRow` components memoized via `memo()` so that collapsing a group no longer rerenders 152 unrelated buttons. Collapse logic moves into `GroupRow.js`.
- [ ] **PERF-H**: ES modules in `static/app/` are bundled via `esbuild/pkg/api` (Go library, not npm CLI) with `Splitting: true, Format: FormatESModule`. Bundle output lands in `static/dist/` with cache-busted filenames (`main.a1b2c3.js`). `index.html` uses `{{ASSET:app/main.js}}` placeholder substitution at request time via new `internal/web/assets.go`. Source-module fallback preserved for dev mode (manifest missing → serve `/static/app/*` directly). **Ships LAST in Phase 8** (per pitfalls research: minification obscures pre-existing bugs, baselines captured pre-bundle become invalid).
- [ ] **PERF-I**: `/api/costs/batch?ids=...` converts from GET with query string to POST with JSON body. Prevents 414 URI Too Long when many sessions are queried. Frontend updated to use POST.
- [ ] **PERF-J**: `Cache-Control: public, max-age=31536000, immutable` is set on hashed assets (`static/dist/*.[hash].js|css`). `index.html` gets `Cache-Control: no-cache` (must revalidate). Middleware detects hashed vs unhashed via filename pattern. Ships with PERF-A as single middleware PR.
- [ ] **PERF-K**: `SessionList` is virtualized via hand-rolled `useVirtualList` hook (new file: `internal/web/static/app/hooks/useVirtualList.js`, ~100 lines). Binary search on offsets array. Feature-flagged via localStorage (`agentdeck_virtualize=1`). Gated at count > 50 sessions (below threshold, non-virtualized renders fine). Group headers (variable height) and session rows (fixed 40px) both handled via `estimateSize(item)` callback. **Blocked by WEB-P1-3** (row height must be stable) and **WEB-P0-3** (action button fix first).

### Polish (Phase 9 — Premium UX)

- [x] **POL-1**: Skeleton loading state replaces 126ms of blank UI before sidebar renders. CSS-only pattern via Tailwind `animate-pulse` on skeleton boxes that match the final sidebar layout EXACTLY (per Linear/Vercel pattern). No library needed.
- [x] **POL-2**: Action button transitions use 120ms opacity fade instead of snap-show/hide. Respects `prefers-reduced-motion`.
- [ ] **POL-3**: Profile dropdown filters out `_*` test profiles (e.g., `_test`, `_dev`) and becomes scrollable with `max-height: 300px` when profile list is long. Search filter input optional.
- [x] **POL-4**: Group divider gap reduces from 48px to 12-16px for tighter grouping. Tightens sidebar information density.
- [ ] **POL-5**: Cost dashboard currency symbol respects `Intl.NumberFormat(navigator.language, { style: 'currency', currency: 'USD' })` — shows `$` for en-US, `US$` for de-DE, etc. No currency conversion, just locale-aware formatting.
- [ ] **POL-6**: Light theme re-audited across all surfaces — sidebar, terminal, dialogs, tooltips, toasts, empty state, cost dashboard. Fix any contrast issues, missing borders, or washed-out colors. **MUST ship LAST** in Phase 9 (per architecture research: audit after all layout is final).
- [x] **POL-7**: Toast stack cap (3 visible) + 5s auto-dismiss + optional history drawer for dismissed toasts (so critical errors aren't silently lost per pitfalls research). Extends existing `Toast.js` with `@preact/signals` top-level state. **Ships with WEB-P0-4** in the same PR.

### Automated Testing (Phase 10 — No More Regressions)

- [x] **TEST-A**: Playwright visual regression tests with committed baselines for every bug fixed in this milestone. Baselines live in `tests/e2e/visual/__screenshots__/`. Runs in Docker (`mcr.microsoft.com/playwright:v1.59.1-jammy` pinned by SHA) for stable font rendering. Thresholds: `maxDiffPixelRatio: 0.001, maxDiffPixels: 200, threshold: 0.2`. Dynamic content (timestamps, costs, session IDs) masked. CI workflow `.github/workflows/visual-regression.yml` blocks merge on diff. **Baselines captured at end of Phase 9**, not during (per pitfalls research).
- [x] **TEST-B**: Lighthouse CI runs on every PR via `@lhci/cli@0.15.1` + `treosh/lighthouse-ci-action@v12` with `numberOfRuns: 5` (median) to fight runner variance. Upload target: `temporary-public-storage` (no LHCI server). Byte-weight assertions are HARD gates (deterministic). FCP/LCP/TBT thresholds are soft warnings. `.lighthouserc.json` in repo root. CI workflow `.github/workflows/lighthouse-ci.yml`.
- [x] **TEST-C**: Functional E2E covering (a) session lifecycle (create → attach → send input → verify output → stop → delete) and (b) group CRUD via web (create group → add session → reorder → delete). New file: `tests/e2e/session-lifecycle.spec.ts`, `tests/e2e/group-crud.spec.ts`.
- [x] **TEST-D**: Mobile E2E at 3 viewports (iPhone SE 375×667, iPhone 14 390×844, iPad 768×1024) via Playwright `projects` config. Covers hamburger tap, overflow menu, sidebar drawer, terminal attach, form input.
- [x] **TEST-E**: Auto-fix loop on scheduled weekly workflow. **Scoped DOWN to alert-only** per pitfalls research (high-risk, low-value as a full auto-fix). Workflow runs visual regression + Lighthouse, on failure posts an issue with diff images and failed metrics, tags the on-call dev. Does NOT automatically create fix PRs. Agent-driven auto-fix deferred to v1.6 or later.

### Release (Phase 11)

- [ ] **REL-1**: v1.5.0 is tagged with clean build (`vcs.modified=false` via `go version -m ./build/agent-deck`), Go 1.24.0 toolchain verified, and shipped via GoReleaser with updated Homebrew tap.
- [ ] **REL-2**: Visual verification (`scripts/visual-verify.sh`) passes for all 5 TUI states (main screen, new session dialog, settings panel, session running, help overlay) before tagging.
- [ ] **REL-3**: Manual macOS smoke test passes — session create, restart, stop with existing state.db from a prior version. Required per CLAUDE.md release workflow.
- [ ] **REL-4**: v1.5.0 release notes document Phase 5 regressions (shipped in v1.4.1), Phase 6 P0 fixes, Phase 7 P1 layout fixes, Phase 8 performance improvements with before/after byte counts, Phase 9 polish items, Phase 10 testing infrastructure additions, and any breaking changes.
- [ ] **REL-5**: Web app verified on real iPhone and iPad over Tailscale. Terminal input, scrolling, profile switcher, mobile overflow menu, visual theme all working.

## Future Requirements (deferred to v1.6+)

### Web App Features

- **V16-WEB-01**: Drag-to-resize sidebar handle with localStorage persistence (v1.5.0 uses `clamp()` as the quick win)
- **V16-WEB-02**: Chart.js UMD → ESM migration for true lazy-loading (v1.5.0 ships `defer` attribute as simpler win)
- **V16-WEB-03**: Sonner physics port for toast animations (v1.5.0 ships simple opacity fade)
- **V16-WEB-04**: Per-session terminal color customization (issue #391)
- **V16-WEB-05**: Ctrl+Q handling improvements (issue #434)
- **V16-WEB-06**: Group reorganization UI in web (issue #447)
- **V16-WEB-07**: Web-native drag-to-reorder for sessions and groups
- **V16-WEB-08**: Service worker caching for offline-capable PWA (needs cache versioning discipline first)
- **V16-WEB-09**: Auto-fix agent loop for visual regression failures (v1.5.0 ships alert-only)

### Performance

- **V16-PERF-01**: Brotli compression via CAFxX/httpcompression (v1.5.0 ships gzip; brotli is ~20% smaller but adds cgo dependency)
- **V16-PERF-02**: Virtual scrolling infinite loading pattern if session counts exceed ~1000

### Architecture

- **V16-ARCH-01**: gorilla/websocket → coder/websocket migration (v1.4.0 deferred, still deferred — isolated phase with high risk, not premium polish)
- **V16-ARCH-02**: Go 1.22+ enhanced mux router refactor (v1.4.0 deferred, still deferred — pure refactor, no new deps)
- **V16-ARCH-03**: gorilla → coder/websocket + router refactor as combined v1.6 phase (both are Go web layer refactors that should ship together)

## Out of Scope (v1.5.0)

Explicitly excluded. Documented to prevent scope creep.

| Feature | Reason |
|---------|--------|
| Tech stack change (React/Svelte/Next.js) | v1.4.0 research validated Preact + HTM + Tailwind + xterm.js as best-in-class |
| SQLite schema changes | Explicit risk avoidance — PR #385 incident. localStorage for any new persistence |
| Windows native support | Tailscale from Mac/iPhone covers remote access; no validated demand |
| Complete component rewrites | Polish milestone — any rewrite needs isolated justification |
| Runtime profile switching (no reload) | Would require re-architecting profile isolation; too invasive for polish milestone |
| WebSocket migration (gorilla → coder) | Isolated high-risk change; deferred to v1.6 |
| Router refactor (Go 1.22 mux) | Pure refactor, no user-facing value; deferred to v1.6 |
| Drag-to-resize sidebar | `clamp()` ships the quick win; drag handle is v1.6 |
| Chart.js dynamic import | UMD→ESM migration adds race conditions; `defer` ships the same benefit; v1.6 |
| Sonner physics toast | 80% of feel for 10% of work via simple opacity fade |
| Brotli compression | gzip is "good enough" for v1.5.0; brotli adds cgo dep |
| Per-component screenshot tests | Requires JS build step maturity first (v1.6) |
| Auto-fix PR creation | TEST-E scoped to alert-only; actual auto-fix is v1.6 or later |
| iOS/Android native apps | PWA remains the mobile path |
| New features beyond spec | Scope locked to `docs/WEB-APP-V15-SPEC.md` |

## Traceability

Which phases cover which requirements. Filled by roadmapper; `pending` until mapped.

| Requirement | Phase | Status |
|-------------|-------|--------|
| REG-01 | Phase 5: Critical Regressions | Complete (v1.4.1) |
| REG-02 | Phase 5: Critical Regressions | Complete (v1.4.1) |
| REG-03 | Phase 5: Critical Regressions | Complete (v1.4.1) |
| REG-04 | Phase 5: Critical Regressions | Complete (v1.4.1) |
| REG-05 | Phase 5: Critical Regressions | Complete (v1.4.1) |
| REG-06 | Phase 5: Critical Regressions | Complete (v1.4.1) |
| WEB-P0-1 | Phase 6: Critical P0 Bugs | Complete |
| WEB-P0-2 | Phase 6: Critical P0 Bugs | Complete |
| WEB-P0-3 | Phase 6: Critical P0 Bugs | Complete |
| WEB-P0-4 | Phase 6: Critical P0 Bugs | Complete |
| WEB-P1-1 | Phase 7: P1 Layout Bugs | Pending |
| WEB-P1-2 | Phase 7: P1 Layout Bugs | Pending |
| WEB-P1-3 | Phase 7: P1 Layout Bugs | Pending |
| WEB-P1-4 | Phase 7: P1 Layout Bugs | Pending |
| WEB-P1-5 | Phase 7: P1 Layout Bugs | Pending |
| PERF-A | Phase 8: Performance | Pending |
| PERF-B | Phase 8: Performance | Pending |
| PERF-C | Phase 8: Performance | Pending |
| PERF-D | Phase 8: Performance | Pending |
| PERF-E | Phase 8: Performance | Pending |
| PERF-F | Phase 8: Performance | Pending |
| PERF-G | Phase 8: Performance | Pending |
| PERF-H | Phase 8: Performance | Pending |
| PERF-I | Phase 8: Performance | Pending |
| PERF-J | Phase 8: Performance | Pending |
| PERF-K | Phase 8: Performance | Pending |
| POL-1 | Phase 9: Polish | Complete |
| POL-2 | Phase 9: Polish | Complete |
| POL-3 | Phase 9: Polish | Pending |
| POL-4 | Phase 9: Polish | Complete |
| POL-5 | Phase 9: Polish | Pending |
| POL-6 | Phase 9: Polish | Pending |
| POL-7 | Phase 9: Polish | Complete |
| TEST-A | Phase 10: Automated Testing | Complete |
| TEST-B | Phase 10: Automated Testing | Complete |
| TEST-C | Phase 10: Automated Testing | Complete |
| TEST-D | Phase 10: Automated Testing | Complete |
| TEST-E | Phase 10: Automated Testing | Complete |
| REL-1 | Phase 11: Release v1.5.0 | Pending |
| REL-2 | Phase 11: Release v1.5.0 | Pending |
| REL-3 | Phase 11: Release v1.5.0 | Pending |
| REL-4 | Phase 11: Release v1.5.0 | Pending |
| REL-5 | Phase 11: Release v1.5.0 | Pending |

**Coverage:**
- v1.5.0 requirements: 43 total
- Pre-complete (v1.4.1): 6 (REG-01..06)
- Active: 37
- Mapped to phases: 43 (100%)
- Unmapped: 0
- Total phases: 7 (5 active, Phase 5 pre-complete)

---
*Requirements defined: 2026-04-08 from `docs/WEB-APP-V15-SPEC.md` with research from `.planning/research/SUMMARY.md`*
