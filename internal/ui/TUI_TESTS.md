# TUI Evaluator Infrastructure

Agent-deck's TUI testing stack. Use this doc to decide which seam to
reach for when reproducing a bug report or guarding a new feature.

## The three seams

| Seam | Harness | Runtime | Speed | Catches | Misses |
|------|---------|---------|-------|---------|--------|
| **A** — model-level | plain Go test + `Home.Update` | synchronous, in-process | ~1 ms | logic, state mutation, resolver contracts, storage round-trips | real-runtime ordering, render, terminal layer |
| **B** — teatest | `charmbracelet/x/exp/teatest` | real `tea.Program` over buffers | ~30–200 ms | message routing, `tea.Batch` ordering, rendered output via `FinalOutput` | real terminal resize, signals, alt-screen, tmux integration |
| **C** — headless tmux | shell + `tmux send-keys` / `capture-pane` | real binary in tmux | ~2–5 s | real terminal, real signals, real tmux wrapping, first-run prompts | not a pure Go test — needs tmux in CI |

**Rule of thumb:** write the bug repro at the lowest seam that can still
observe the symptom. If a field change would catch it, Seam A. If only a
rendered View would catch it, Seam B. If only a real terminal would
catch it, Seam C.

## Canonical examples in this repo

- **Seam A** — `internal/ui/issue666_tui_test.go` + `internal/ui/tui_eval_seam_a_test.go`
  - `TestIssue666_ResolveNewSessionGroup_*` — resolver contract (method direct).
  - `TestIssue666_GlobalSearchImport_EndToEnd_PreservesGroupAcrossReload` — storage round-trip.
  - `TestSeamA_KeyDispatch_HelpOverlayToggle` — drive real `tea.KeyMsg` through `Home.Update`.
  - `TestSeamA_Issue666_KeyDispatch_DoesNotZeroGroupScope` — guard `groupScope` across key sequences.
- **Seam B** — `internal/ui/tui_eval_seam_b_test.go`
  - `TestSeamB_HelpOverlay_ViaTeatest` — teatest harness smoke.
  - `TestSeamB_Issue666_ResolverSurvivesFullRuntime` — resolver intact after real runtime key dispatch.
- **Seam C** — `scripts/verify-tui-eval-seam-c.sh`
  - Spawns `agent-deck` inside detached tmux, drives `?`, asserts help content rendered, dismisses.

## Writing a new TUI test

### Seam A — model-level (prefer this by default)

```go
func TestFeatureX_ModelLevel(t *testing.T) {
    h := newSeamATestHome()    // lightweight Home, no storage/workers
    h.flatItems = []session.Item{ /* set up the cursor context */ }
    h.cursor = 0

    newModel, _ := h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
    got := newModel.(*Home)

    if !got.helpOverlay.IsVisible() {
        t.Fatalf("...")
    }
}
```

The helper `newSeamATestHome` lives in `tui_eval_seam_a_test.go`. If your
test needs a field that panics under `View()`/`Update()`, add the default
to `seamBNewHome` (the richer initializer in `tui_eval_seam_b_test.go`)
and import it rather than duplicating.

### Seam B — teatest

```go
func TestFeatureX_Teatest(t *testing.T) {
    w := &seamBTestWrapper{home: seamBNewHome()}
    tm := teatest.NewTestModel(t, w, teatest.WithInitialTermSize(140, 50))
    tm.Send(tea.WindowSizeMsg{Width: 140, Height: 50})
    tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
    tm.Send(tea.QuitMsg{})
    _ = tm.Quit()

    final := tm.FinalModel(t, teatest.WithFinalTimeout(2*time.Second)).(*seamBTestWrapper)
    if !final.home.helpOverlay.IsVisible() {
        t.Fatalf("...")
    }
}
```

**Gotcha:** never hand `NewHome()` directly to teatest — `Home.Init()`
spawns storage watchers, status workers, and tickers. Wrap it in
`seamBTestWrapper` (no-op `Init`) to keep tests deterministic.

### Seam C — headless tmux

Copy `scripts/verify-tui-eval-seam-c.sh` as a template. The pattern:

1. Isolate: `HOME=$(mktemp -d)` so production state is untouched.
2. Write a minimal `config.toml` into `$HOME/.agent-deck/`.
3. `tmux new-session -d -s <name> -x 180 -y 50 "env ... agent-deck"`.
4. Poll `tmux capture-pane -p` until a startup string appears.
5. `tmux send-keys` to drive, `sleep` briefly, `capture-pane` to observe.
6. `grep` the captured text for signature strings. Tear down on EXIT.

**Signature strings are brittle by design.** Help overlay changed once
in this POC (the obvious `Help` header isn't in the pane — use
`any other key to close` instead). When you assert on rendered text,
pick a string that is semantically load-bearing for the feature, not
decorative.

## CI wiring

- **Seam A + B**: `go test ./internal/ui/... -race -count=1`. Already in the default `go test` path. Run on every PR.
- **Seam C**: `bash scripts/verify-tui-eval-seam-c.sh`. Needs tmux ≥ 3.0 installed on the CI runner. Add to `.github/workflows/` alongside the existing `verify-session-persistence.sh` and `verify-watcher-framework.sh` invocations. Skips cleanly on hosts without tmux.

## Discovery

A developer writing a new TUI test should:

1. Read this file.
2. Search `internal/ui/tui_eval_seam_*_test.go` for the closest analog.
3. Search `internal/ui/issue*_tui_test.go` for bug-specific regression pins as worked examples.

---

## Research notes — state of the art (April 2026)

See `internal/ui/TUI_TESTS_RESEARCH.md` for the full comparison of
alternatives (VHS, vt10x, go-expect, termtest, lazygit integration
pattern, internal RPC endpoints). Summary:

- **teatest** — primary Go-native harness; still `exp/` but widely used. Maintained. **Chosen for Seam B.**
- **VHS** — wrong layer for state bugs; rendered-diff only. Skip for bug repro; consider later for visual regression.
- **vt10x / go-expect / termtest** — fallback if teatest can't reproduce a terminal-layer bug. Currently: not needed.
- **lazygit's `pkg/integration`** — the closest architectural analog. They wrote their own harness on top of gocui with `Input`/`Assert.Eventually`. We can port this idea on top of teatest if Seam B tests grow in volume.
- **Anthropic Claude Code** — not open source, no public testing guidance.
- **Internal JSON test RPC** — not built. Consider a `-tags=testrpc` HTTP endpoint returning model state if Seam B ever proves too indirect.
