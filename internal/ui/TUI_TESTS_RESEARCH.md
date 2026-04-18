# TUI Testing — State of the Art (April 2026)

Research companion to `TUI_TESTS.md`. Snapshot of the landscape at the
time the evaluator infrastructure was built. Re-run the research when
one of the candidates moves to v1 / stable or when a new entrant lands
in the Charm ecosystem.

Target scenario for agent-deck: *cursor on Window item → open
new-session dialog → pick recent session → submit → assert SQLite
GroupPath*. The scenario is a model+state assertion, not a pixel diff.

## 1. teatest — `github.com/charmbracelet/x/exp/teatest`

- **Status**: maintained, still `exp/`. `v2/` subdir exists for Bubble Tea v2. Lives in `charmbracelet/x` experimental module. Charm blog posts (2024) and Carlos Becker's writeup still current.
- **CI fit**: deterministic when driving via `tm.Send(...)` and asserting on `FinalModel` / `FinalOutput`. Golden files via `RequireEqualOutput`. Flaky if relying on `WaitFor` with timing-sensitive View output.
- **Target fit**: good. Send key msgs, get `FinalModel`, cast to root model, assert on any exported field. SQLite assertion happens in the same test after `tm.Quit()`.
- **Gotchas**: renders to a raw byte buffer, not a real vt100 — ANSI cursor moves look ugly in golden files. `exp` → breaking changes possible.
- **Verdict: recommended** (primary harness = **Seam B**).
- Links:
  - <https://github.com/charmbracelet/x/tree/main/exp/teatest>
  - <https://charm.land/blog/teatest/>
  - <https://carlosbecker.com/posts/teatest/>
  - <https://github.com/charmbracelet/bubbletea/discussions/1528>

## 2. VHS — `github.com/charmbracelet/vhs`

- **Status**: actively maintained; `.tape` → gif/ascii/txt. Can emit `Output foo.txt` for golden diffing.
- **CI fit**: slow (spawns ttyd + headless chrome for gif; txt mode faster). Known flaky on timing — needs explicit `Sleep` between steps.
- **Target fit**: poor for state bugs. VHS tests the *rendered screen*, not the DB. Still need a separate Go test for SQLite. Good complement for visual regressions only.
- **Verdict: skip for bug repro; consider later for visual regression of list/dialog.**
- Link: <https://github.com/charmbracelet/vhs>

## 3. In-process terminal emulators

- **hinshun/vt10x** — low commit activity (stable). Used by `Netflix/go-expect` as screen backend. Solid, small, embeddable.
- **taigrr/bubbleterm** — newer (2024), embeddable terminal *inside* a Bubble Tea app; not a test tool.
- **ActiveState/termtest** — wraps go-expect + vt10x with scrollback, cross-platform (incl. Windows ConPTY). Used by ActiveState's state-tool CI.
- **Verdict**: **consider vt10x** as screen backend if teatest's raw buffer stops being enough. **consider termtest** for Windows coverage; otherwise skip.
- Links:
  - <https://github.com/hinshun/vt10x>
  - <https://github.com/ActiveState/termtest>
  - <https://pkg.go.dev/github.com/taigrr/bubbleterm>

## 4. PTY + expect

- **Netflix/go-expect** — de-facto standard, low churn, widely imported. Pairs with vt10x for screen state.
- **creack/pty** — lower level; fine for pure-Go scripting.
- **Verdict**: **consider (go-expect+vt10x)** as fallback if teatest can't reproduce a bug because it bypasses the real tea runtime (e.g., input routing, alt-screen, signal handling).
- Link: <https://github.com/Netflix/go-expect>

## 5. Industry examples

- **lazygit** — `pkg/integration/tests/**/*.go`: Cypress-style. Each test = `setup(Shell)` + `run(Shell, Input, Assert)`. `Input.PressKeys`, `Assert.MatchesRegexp` with exponential backoff. Runs via `cmd/integration_test/main.go`. Didn't use teatest — wrote custom harness on top of gocui. **Closest analog to agent-deck's needs.**
  - <https://github.com/jesseduffield/lazygit/blob/master/pkg/integration/README.md>
  - <https://jesseduffield.com/IntegrationTests/>
  - <https://jesseduffield.com/More-Lazygit-Integration-Testing/>
  - <https://github.com/jesseduffield/lazygit/pull/2094>
- **glow / gum / soft-serve** — mostly unit tests + CI lint; no heavy E2E TUI tests.
- **k9s** — unit + lint only in CI; no TUI E2E harness.
- **Claude Code (Anthropic)** — not open-source; no published testing guidance.
- **Verdict**: copy lazygit's `Input`/`Assert.Eventually` pattern on top of teatest if Seam B tests grow in volume. **Architectural reference.**

## 6. tmux-as-test-driver

- No mainstream published pattern for Bubble Tea. Agent-deck's own `scripts/verify-session-persistence.sh` and `verify-watcher-framework.sh` are the prior art. Slow but catches real lifecycle bugs.
- **Verdict**: **keep** for systemd/tmux-interaction bugs (Seam C). Don't use for model-state assertions.

## 7. Internal test RPC endpoint

- No established pattern in public Bubble Tea projects. Custom `-tags=testrpc` HTTP handler returning model JSON would be cheap to add, high value for deterministic state inspection.
- **Verdict**: **consider** as a lightweight escape hatch for flaky scenarios. Not built yet.

## 8. Anthropic guidance

- None published. Treat as absent.

---

## Recommendation for agent-deck

- **Primary**: teatest (fast, Go-native, gives `FinalModel` for cross-checks). **Seam B.**
- **Architecture**: copy lazygit's `Input` + `Assert.Eventually` wrappers on top of teatest if test volume grows, so each bug repro reads like a Cypress test.
- **Escape hatch**: keep the existing tmux scripts for lifecycle bugs (Seam C); add a `-tags=testrpc` JSON state endpoint for the flaky cases if they appear.
- **Skip**: VHS (wrong layer), ActiveState/termtest (Windows-only value for now).

Model-level tests (Seam A) remain the default first stop — fastest
feedback, zero flakiness, no new dependencies. Seam B and C exist to
cover what Seam A cannot observe.
