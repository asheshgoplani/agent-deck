# Maintainer note: `internal/session` test-isolation regression from the XDG migration (#1294)

**Status:** rebased onto `main` as of 2026-06-07 and reconciled with the mainline
XDG isolation fix. The local macOS `internal/session` suite is green again
without re-pinning package-level XDG defaults.

**Author context:** surfaced while merging upstream `main` into a feature branch
(PR #1299). The contamination is **pre-existing on `main`** (reproduces on the
`v1.9.49` release commit `b555774a` with no feature-branch changes), not
introduced by that PR.

## Symptom

Before the mainline fix, running the full session package locally on macOS
failed ~66 tests:

```bash
GOTOOLCHAIN=go1.25.11 go test ./internal/session/... -count=1   # ~66 failures
```

Most failures passed in isolation (`-run TestX$`) and only appeared in a
full-package run, which made this look like ordinary test flake until the XDG
config path change was traced.

## Why CI is green (and this went unnoticed)

- The per-PR check (`.github/workflows/session-persistence.yml`) runs a
  **filtered** suite: `go test -run TestPersistence_ ./internal/session/...`.
  A filtered run doesn't trigger the polluters, so it stays green.
- The full `go test ./...` only runs at the **release gate**
  (`.github/workflows/release.yml`) on `ubuntu-latest`. On headless Linux the
  polluting tests (which need tmux/claude panes, gated by
  `skipIfClaudePaneUnreliable`) **skip**, so they never write the shared config
  and the gate passes — which is how `v1.9.49` shipped.
- The breakage therefore surfaces only in a **local full-package run on macOS**,
  where tmux/pane tests actually run. The local **pre-push hook**
  (`lefthook.yml` → `go test -race ./...`) hits it.

## Root cause

`#1294` moved user config from `$HOME`-relative paths to
`$XDG_CONFIG_HOME/agent-deck/config.toml` (`agentpaths.EffectiveConfigPath` /
`ConfigDir`). The first version of this branch was based on `v1.9.49`
(`b555774a`), where package-level HOME/XDG isolation still allowed a shared
XDG config location to leak between tests.

Most session tests predate the migration and isolate by overriding **only**
`HOME`. If `XDG_CONFIG_HOME` is pinned independently of that temp HOME, a test's
`SaveUserConfig` (or direct `os.WriteFile` to the legacy dir) and its config
**reads** can resolve against different config roots. So:

- A test that calls `SaveUserConfig` without overriding an inherited
  `XDG_CONFIG_HOME` can write to a foreign `config.toml` and leak into later
  tests (**polluter**).
- A test that writes config to its own legacy `$HOME/.agent-deck/config.toml`
  and reads it back can get a polluted or foreign XDG config instead, because
  `EffectiveConfigPath` prefers an existing XDG path (**victim**).

## What changed after rebase

- Current `main` now clears XDG base-dir env vars in `internal/testutil/homeenv.go`
  and in `internal/session/testmain_test.go`, so package-level defaults track
  the current temp `HOME` instead of pinning a shared XDG config root.
- This branch keeps that mainline strategy. It does **not** re-pin global
  `IsolateHome` behavior.
- The per-test `isolateConfigHomeXDG(t)` helper remains only for tests that
  write config through XDG-aware APIs (`SaveUserConfig`, `CreateExampleConfig`)
  and should make the scope explicit.
- The four legacy config-writing helpers still set `XDG_CONFIG_HOME` inside
  their temp HOME, so they remain isolated even if a caller or future test adds
  an XDG override.
- `TestCreateExampleConfigDocumentsCompatibleWith` now has the same explicit
  XDG isolation as its sibling config-writing tests.

## Additional macOS test-fixture fixes

After rebasing, the old XDG failure set was gone, but macOS still exposed
standalone test-fixture problems:

- JSONL resume fixtures wrote transcripts under hand-built project paths that
  could drift from production lookup rules. They now use a shared helper that
  mirrors production's Claude project-dir encoding.
- `TestSessionStop_ReapsMcpChildren_RegressionFor965` used `kill(pid, 0)` as
  the only death signal for a child owned by the Go test process. On macOS a
  killed child can remain waitable as a zombie until the parent calls `Wait`, so
  the test now waits on its owned fake child while keeping PID polling for the
  tmux-owned child in the wiring test.

## Remaining scope

No known local macOS `internal/session` failures remain on this branch after the
rebase and fixture fixes. The Linux+systemd end-to-end persistence harness is
still the separate host-specific gate for session lifecycle behavior.

## Repro / verification

```bash
GOTOOLCHAIN=go1.25.11 go test ./internal/session/... -count=1
```
