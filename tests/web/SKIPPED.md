# Skipped Phase 1 cells

Phase 1 of TEST-PLAN.md (regression-driven cells from v1.8 bugs) deferred
the following cells in this PR. Each has a concrete reason and a follow-up
action so they can be re-attempted without re-discovering the blocker.

## C3 — Multi-client tmux window-size (REGRESSION v1.8)

**Why skipped:** Requires `internal/testutil/multiclienttmux/` which lives
on `origin/main` (added by PR #916, commit `f704e477`) but is NOT on this
branch's base. The branch was forked from `0c283288 wip: capture pre-v1.9.0
tmux edits + Playwright web-parity suite snapshot` so the pre-v1.9 UI
remains the test target — merging `main` brings in a v1.9.x UI rewrite
that breaks every Phase 1 spec (verified locally, reverted).

**Follow-up:** Rebase or cherry-pick PR #916's `internal/testutil/`
contents onto this branch (or onto the Phase-2 branch), then author a
spec under `tests/e2e/` that uses `multiclienttmux.NewSession()` +
`multiclienttmux.AttachClient()` and asserts the aggregate
`#{window_width}x#{window_height}` via the fixture-only tmux info endpoint
(TEST-PLAN.md §6.1).

## C5 — Paste handler clipboard preservation (REGRESSION v1.8 WSL2+Chrome)

**Why skipped:** Building a self-contained Playwright spec required
stubbing the WebSocket (TerminalPanel.js gates the paste handler on
`ctx.ws.readyState === OPEN && ctx.terminalAttached`) and then
dispatching a synthetic `ClipboardEvent`. Despite waiting on a sentinel
that confirms the fake WebSocket open + `terminal_attached` status flush,
the capture-phase paste listener on the `containerRef` div never fired in
chromium-headless when the event was dispatched from `page.evaluate` (4
target selectors tried: `.h-full.w-full.overflow-hidden`, `.xterm`,
`.xterm-screen`, `.xterm-helper-textarea`). Reached the 30-minute skip
threshold per the worker prompt.

**Follow-up:** Either (a) drive paste via a real CDP `Input.dispatchKeyEvent`
sequence so chromium synthesizes the same event flow as a real Cmd+V, or
(b) cover the CRLF/CR→LF normalization with a Vitest unit test against a
freshly-instantiated `TerminalPanel` (requires resolving the
`preact/hooks` alias issue in `vitest.config.js` first — `require.resolve`
returns CJS, but Vite's import-analysis wants the `.mjs` from the package
`exports.import` map).

## J1 — CLI ↔ web parity field-by-field (REGRESSION v1.8)

**Why skipped:** Requires `internal/testutil/crossfixture/`
(`AttachWeb` + `AttachCLI` hooks per PR #916). Same branch-base reason as
C3.

**Follow-up:** Same — pull in PR #916's testutil packages, then build a
Go test under `tests/e2e/` (or `internal/web/`) that boots a single
fixture instance, spawns N sessions through both surfaces, and
deep-equals `agent-deck list --json` against `GET /api/sessions` after a
stable sort by id.

## J2 — TUI hookStatus ↔ web status (REGRESSION v1.8)

**Why skipped:** Requires `internal/testutil/fakeinotify/` (with
`DropAfter` and `SimulateOverflow`) plus the `Instance.UpdateStatus` /
`EventSource interface` plumbing the PR #916 message explicitly calls out
as out of scope for that PR — those land with the J2 test itself.

**Follow-up:** Wait for the inotify-overflow seam to land in
`internal/session/`, then drive a hook event through `fakeinotify` and
assert both a TUI tick (via `teatesthelper`) and `GET /api/sessions`
converge on the final status within the `pollFallbackTimeout` (~2s).

## J3 — Profile resolution across 5 surfaces (REGRESSION v1.8)

**Why skipped:** Requires `internal/testutil/profilefixture/`
(`Probe` + `AssertParity`) from PR #916. Same branch-base reason.

**Follow-up:** With profilefixture in place, set
`AGENTDECK_PROFILE` and a controlled `~/.agent-deck/config.json`,
run the 5-way probe (CLI list, `/api/settings`, `/api/profiles.current`,
`/healthz.profile`, TUI snapshot) and assert all equal.

---

Coverage delta unchanged for this branch: Phase 1 7-of-11 cells covered.
The remaining 4 cells are infrastructure-blocked, not logic-blocked.
