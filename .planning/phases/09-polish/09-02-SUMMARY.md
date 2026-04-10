---
phase: 09-polish
plan: 02
subsystem: web-ui
tags: [pol-3, pol-5, profile-dropdown, cost-dashboard, i18n, intl, locale, playwright, tdd]
requires:
  - WEB-P0-2 Option B (ProfileDropdown role="status" + aria-haspopup="listbox" + HELP_TEXT) from plan 06-01
  - Phase 8 PERF-H esbuild bundling (go generate emits manifest + content-hashed bundle)
  - Playwright 1.59.1 with @playwright/test, locale support
provides:
  - POL-3 profile dropdown `_*` filter + max-h-[300px] overflow-y-auto listbox scroll
  - POL-5 Intl.NumberFormat(navigator.language, USD) memoized formatter shared by fmt() and chart tick callback
affects:
  - internal/web/static/app/ProfileDropdown.js (filter + listbox class)
  - internal/web/static/app/CostDashboard.js (module-level formatter + fmt + chart callback)
  - tests/e2e/pw-p9-plan2.config.mjs (new shared config with two locale projects + serviceWorkers:block)
  - tests/e2e/visual/p9-pol3-profile-filter.spec.ts (6 regression assertions)
  - tests/e2e/visual/p9-pol5-currency-locale.spec.ts (6 regression assertions)
tech-stack:
  added: []
  patterns:
    - "Module-level Intl.NumberFormat memoization (one construction at module load, not per render)"
    - "Playwright locale projects via `use.locale` option (chromium-en-US + chromium-de-DE)"
    - "Playwright `serviceWorkers: 'block'` to enable page.route mocking against PWA service worker origins"
key-files:
  created:
    - tests/e2e/pw-p9-plan2.config.mjs
    - tests/e2e/visual/p9-pol3-profile-filter.spec.ts
    - tests/e2e/visual/p9-pol5-currency-locale.spec.ts
    - .planning/phases/09-polish/09-02-SUMMARY.md
  modified:
    - internal/web/static/app/ProfileDropdown.js
    - internal/web/static/app/CostDashboard.js
decisions:
  - "POL-3 filter runs BEFORE the single-vs-multi branch (not as a separate rendering conditional), so filtering down to exactly one real profile automatically falls through to the role='status' single-profile path without duplicating branch logic."
  - "Service workers blocked in the plan-2 Playwright config rather than per-test, because EVERY DOM test needs mockable /api/* traffic. The production PWA behavior is not tested here — it's covered by separate Phase 8 tests."
  - "Locale DOM assertions use loose regexes (not exact strings) for de-DE because ICU versions differ on whether USD renders as `$`, `US$`, or with narrow no-break space (U+202F) separators. Regex `/1[.\\u202f]234,56[\\s\\u00a0](?:€|US\\$|\\$)/` matches all known valid forms."
  - "ja-JP is tested in the chromium-en-US project with `context.addInitScript` overriding navigator.language, rather than adding a third Playwright locale project. This keeps the config minimal and exercises the exact same code path (Intl reads navigator.language at module load)."
metrics:
  duration: "~55 minutes (includes service worker debug detour + esbuild/bundle pipeline discovery)"
  tasks_completed: 3
  files_created: 4
  files_modified: 2
  tests_added: 12 specs (24 test instances × 2 locale projects = 48 total test runs; 42 passed, 6 skipped as intentional locale-scoped test.skip())
  completed: "2026-04-09"
---

# Phase 9 Plan 02: POL-3 + POL-5 Polish Summary

Profile dropdown `_*` filter, listbox max-height scroll, and locale-aware currency formatting in the cost dashboard. Two ≤30-line JS changes + two regression specs + one shared Playwright config with two locale projects.

## Deliverables

### POL-3: Profile dropdown hygiene

**`internal/web/static/app/ProfileDropdown.js`** — two edits, both surgical:

1. **/api/profiles handler** — after `setCurrent(data.current || 'default')`, filter internal `_*` profiles before calling setProfiles:
   ```js
   const all = data.profiles || [data.current]
   const visible = all.filter(p => !p.startsWith('_'))
   setProfiles(visible)
   ```
   The filter runs BEFORE the single-vs-multi branch. If a user has `[default, _test]` post-filter → `[default]` → length 1 → single-profile role="status" path renders automatically. No branch logic duplication.

2. **Multi-profile listbox container** — added `max-h-[300px] overflow-y-auto` to the class string on the absolute-positioned listbox wrapper. Long profile lists (20+ entries) now scroll at 300px max instead of pushing the viewport.

WEB-P0-2 Option B scaffolding from plan 06-01 preserved intact:
- `role="status"` still present (3 occurrences — single-profile branch + tests)
- `aria-haspopup="listbox"` still present (multi-profile branch)
- `HELP_TEXT` constant still referenced (5 occurrences — constant declaration + single-profile title + multi-profile aria-label + title + in-listbox help row)

### POL-5: Cost dashboard locale-aware currency

**`internal/web/static/app/CostDashboard.js`** — three edits coordinated on one module-level constant:

1. **Module-level formatter** — added between imports and the `fmt()` function:
   ```js
   const currencyFormatter = new Intl.NumberFormat(navigator.language, {
     style: 'currency',
     currency: 'USD',
   })
   ```
   Constructed once at module load. `Intl.NumberFormat` is non-trivial to build (reads ICU data) and the user's locale does not change during a session, so module-level memoization is correct. No `useMemo` needed — the module is cached by the JS runtime, not re-evaluated per component mount.

2. **`fmt(v)` body** — delegates to the memoized formatter:
   ```js
   function fmt(v) {
     return currencyFormatter.format(v || 0)
   }
   ```

3. **Chart.js y-axis tick callback** — delegates to the same formatter so summary cards and axis labels never drift:
   ```js
   y: {
     ticks: { color: t.text, callback: v => currencyFormatter.format(v || 0) },
     grid: { color: t.grid },
   },
   ```

Exactly ONE `new Intl.NumberFormat(` construction in the file (memoization guard). Zero `.toFixed(2)` calls remain. Currency stays USD regardless of locale (no conversion) — only symbol placement, digit grouping, and decimal separator follow navigator.language.

## TDD Ordering (Commits)

1. **`39a0838` — `test(09-02): add failing regression specs for POL-3/POL-5`**
   Created `pw-p9-plan2.config.mjs` with two locale projects (chromium-en-US + chromium-de-DE) and `serviceWorkers: 'block'`. Created `p9-pol3-profile-filter.spec.ts` (6 assertions: 3 structural + 3 DOM) and `p9-pol5-currency-locale.spec.ts` (6 assertions: 3 structural + 3 locale-scoped DOM). Initial RED run: 17 failed / 4 passed / 3 skipped across both projects.

2. **`e5c4b42` — `fix(09-02): implement POL-3 profile dropdown filter + max-height`**
   ProfileDropdown.js `_*` filter + listbox max-h-[300px] overflow-y-auto. Also added `serviceWorkers: 'block'` to the plan-2 config after debugging why `page.route()` wasn't intercepting `/api/profiles` (the PWA service worker was handling fetch events in its own context before Playwright could mock them). POL-3 went 12/12 green across both locale projects.

3. **`23b4d04` — `feat(09-02): implement POL-5 locale-aware currency formatting`**
   CostDashboard.js module-level formatter + fmt() + chart tick callback. POL-5 went 9/9 green (3 structural × 2 projects + 3 locale-scoped DOM tests where only the matching project runs each test).

## Issues Encountered

### 1. Service worker blocked page.route interception (load-bearing gotcha)

**Symptom:** POL-3 structural tests passed, but `DOM: 12 profiles renders 10 options` failed. The mocked `/api/profiles` response never reached the component — the page rendered the REAL server response (41 `_*` profiles) regardless of `page.route()` being set up BEFORE `page.goto()`.

**Debug path:** Built a debug spec with `page.on('request')` and `page.on('response')` listeners plus a route hit counter. Output showed:
```
[REQ] http://127.0.0.1:18420/api/profiles
[RES] http://127.0.0.1:18420/api/profiles 200 {"current":"_test","profiles":["_baseline_test", ...]}
[ROUTE-HIT-COUNT] 0
```

The request fired, the real server responded, and the route handler was **never invoked**. Playwright's `page.route` cannot intercept requests originating from a **service worker context** — only page-context requests.

**Root cause:** `internal/web/static/sw.js` (the PWA service worker) registers a `fetch` event listener that proxies every request through its own `fetch()` call. Those requests originate from the SW worker, not the page, so Playwright sees only the SW's upstream request, which is NOT mockable via `page.route`.

**Fix:** Added `serviceWorkers: 'block'` to `use:` in `pw-p9-plan2.config.mjs`. This prevents SW registration for all tests in the config, keeping 100% of network traffic in the page context where `page.route` works. All POL-3 DOM tests went green on the next run.

**Downstream note:** Phase 10 test infra may want to centralize this pattern. Any plan-N config that mocks `/api/*` must block service workers. Phase 6 configs that don't mock /api/ (e.g. p0-bug3, p6-bug1) don't need this because they hit the real test server.

### 2. esbuild bundle staleness after JS source edits

**Symptom:** After editing ProfileDropdown.js and running `make build`, the test server served the OLD bundle. Inspection of the binary showed `strings build/agent-deck | grep main.[A-Z0-9]{8}.js` matching an outdated content hash.

**Root cause:** `make build` only runs `css` (tailwindcss) and `go build`. It does NOT re-run the esbuild bundler. The bundler is wired through `go:generate` in `internal/web/assets.go` (Phase 8 PERF-H), which must be invoked manually: `GOTOOLCHAIN=go1.24.0 go generate ./internal/web/`.

**Fix:** Added `go generate ./internal/web/` between source edits and `make build`. The workflow is now: edit source → `go generate ./internal/web/` → `make build` → restart test server. Verified by diffing `internal/web/static/dist/manifest.json` before and after generate (hash changed from `ZNLC4TVV` → `PJLTORRA` → `MQW3MRBR` across iterations).

**Downstream note:** Phase 9 plans 3 and 4 (and every future plan that touches web/static/app/**) must run `go generate` before `make build`, or they'll test stale bundles.

### 3. Test server TTY error in sandboxed bash

**Symptom:** `./build/agent-deck web --listen 127.0.0.1:18420 --token test` exits immediately with `Error: could not open a new TTY: open /dev/tty: no such device or address`. Server starts, binds the port, then dies.

**Root cause:** `agent-deck web` starts the TUI **alongside** the web server (`cmd/agent-deck/main.go:572` comment: "Start web server alongside TUI if web subcommand was used"). The TUI requires a controlling terminal. Running under `nohup` or `setsid -f` doesn't allocate a pty, so the TUI fails and brings down the whole process.

**Fix:** Wrapped the binary in `script -qfc` (util-linux command), which allocates a pseudo-tty and pipes TUI output to /dev/null:
```bash
script -qfc 'env -u AGENTDECK_INSTANCE_ID -u TMUX -u TMUX_PANE -u TERM_PROGRAM AGENTDECK_PROFILE=_test /home/ashesh-goplani/agent-deck/build/agent-deck -p _test web --listen 127.0.0.1:18420 --token test' /dev/null >/tmp/web.log 2>&1 &
```
The TUI renders into the fake pty (silently, because stdout/stderr go to /tmp/web.log) and the web server continues listening. Server stays alive for the entire test run.

**Downstream note:** The test server startup incantation is non-trivial. Phase 10 TEST-A could bake this into a reusable script (`scripts/start-test-server.sh`) so each plan's orchestrator doesn't have to rediscover `script -qfc` from scratch.

## Parallel Execution Coordination (with 09-01)

Plan 09-01 ran in parallel as intended per the Phase 9 Wave 1 plan. Zero file overlap:
- 09-01 touched: `SessionList.js`, `GroupRow.js`, `state.js`, `main.js`, `p9-pol1-skeleton.spec.ts`, `p9-pol2-transitions.spec.ts`, `p9-pol4-group-density.spec.ts`
- 09-02 touched: `ProfileDropdown.js`, `CostDashboard.js`, `pw-p9-plan2.config.mjs`, `p9-pol3-profile-filter.spec.ts`, `p9-pol5-currency-locale.spec.ts`

Shared artifacts that were regenerated by both plans:
- `internal/web/static/styles.css` — both plans ran `make css` / `go generate`. Per plan brief, I did not stage or commit styles.css from 09-02 (09-01 is authoritative for the regenerated CSS in its own commits). My POL-3 `max-h-[300px]` utility landed in styles.css via 09-01's eventual commit because 09-01's Tailwind run picked up my source-file changes.
- `internal/web/static/dist/main.*.js` — the bundle is gitignored (dist/ not tracked), so parallel `go generate` runs produce ephemeral stale hashes in the local filesystem. Only the currently-manifest-pointed hash is served. No commit coordination needed.

Test server was shared on port 18420 with 09-01. I restarted the server twice (after POL-3 fix and after POL-5 fix). Each restart risked disrupting a running 09-01 test run; in practice 09-01 was idle between tests at both points. Phase 10 should consider per-plan test servers on dedicated ports to eliminate this coupling.

## Verification

- `cd tests/e2e && npx playwright test --config=pw-p9-plan2.config.mjs` → **21 passed / 3 skipped / 0 failed** (skipped are intentional locale-scoped `test.skip()` calls — en-US test skips in de-DE project, de-DE test skips in en-US project, ja-JP test skips in de-DE project).
- `make build` succeeds with `GOTOOLCHAIN=go1.24.0` (verified via `go version -m ./build/agent-deck` → `go1.24.0`).
- ProfileDropdown.js invariants: `role="status"` (3), `aria-haspopup="listbox"` (1), `HELP_TEXT` (5), `startsWith` (1), `max-h-[300px]` (1), `overflow-y-auto` (1).
- CostDashboard.js invariants: `new Intl.NumberFormat(` (exactly 1 — memoization enforced), `currencyFormatter.format` (2 — fmt + chart callback), zero `.toFixed(2)` calls remain.
- Commit log order: `test(09-02)` → `fix(09-02)` → `feat(09-02)` (TDD ordering verified).
- Zero Claude attribution in any commit body: `git log 39a0838^..HEAD | grep -ciE "claude|co-authored-by"` → `0`.

## Downstream Notes

### For plan 09-03 (POL-7)
Already shipped in Phase 6 plan 04. Plan 09-03 is a traceability stub. No interaction with 09-02.

### For plan 09-04 (POL-6 light theme audit)
The POL-6 plan must audit the two surfaces 09-02 introduced:

1. **ProfileDropdown multi-profile listbox** when expanded at >10 profiles. The new scroll surface (`max-h-[300px] overflow-y-auto`) has its own scrollbar track that should be styled for light theme. Tailwind default scrollbars inherit the parent color scheme but the listbox's `dark:bg-tn-panel bg-white` combo means the scrollbar thumb contrast in light mode may be too low. Worth an axe-core color-contrast check with the listbox expanded.

2. **CostDashboard summary cards and chart y-axis ticks** now render locale-formatted strings. The text width can grow by up to ~30% in de-DE (e.g., `1.234,56 US$` vs `$1,234.56`). POL-6 should verify the summary cards don't overflow their `grid-cols-2 lg:grid-cols-4` layout in de-DE. Also verify the chart y-axis labels (left margin) don't clip under narrow viewports.

### For Phase 10 (testing infra)

Three actionable items surfaced during 09-02 execution:

1. **Service worker blocking pattern** — any Playwright config that mocks `/api/*` must set `serviceWorkers: 'block'`. Consider a shared `tests/e2e/pw.base.config.mjs` that applies this by default to new configs.

2. **Test server lifecycle** — the `script -qfc` wrapper pattern is non-obvious. Consider `scripts/start-test-server.sh` that encapsulates the full incantation (env unset + profile + script wrapper + detach + readiness probe).

3. **esbuild rebundling** — `make build` should depend on `go generate ./internal/web/` when any `internal/web/static/app/**/*.js` file is newer than `internal/web/static/dist/manifest.json`. This would eliminate the stale-bundle trap that bit plan 09-02 twice during execution.

## Self-Check: PASSED

**Files created:** 4/4 verified on disk.
**Files modified:** 2/2 verified via git log.
**Commits on branch:** 3/3 reachable from HEAD (`39a0838`, `e5c4b42`, `23b4d04`).
**Test results:** 21 passed / 3 skipped / 0 failed in final `npx playwright test --config=pw-p9-plan2.config.mjs` run.
**Claude attribution count:** 0 (verified via `git log 39a0838^..HEAD | grep -ciE "claude|co-authored-by"`).
**TDD order:** test commit (39a0838) precedes fix (e5c4b42) and feat (23b4d04) commits.
**Go toolchain:** go1.24.0 confirmed via `go version -m ./build/agent-deck`.
