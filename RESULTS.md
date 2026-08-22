# Issue #1892 results

## Reproduction

Environment: current `origin/main` at `47bb2103`, Go 1.25 container, Debian tmux 3.5a. The regression test starts a real tmux pane whose process remains alive (`sleep 300`) but never produces an agent prompt or busy signal, then ages the production startup clock beyond its two-minute bound.

Before the fix:

```text
=== RUN   TestIssue1892_StartupWithoutAgentSignalTimesOutHonesty
    issue1892_startup_timeout_test.go:31: stuck startup status = "waiting", want error
--- FAIL: TestIssue1892_StartupWithoutAgentSignalTimesOutHonesty (0.42s)
```

This reproduces the reported state transition: an alive but non-interactive pane exhausts startup without reaching any explicit failure state.

## Root cause

[`internal/tmux/tmux.go:1576`](internal/tmux/tmux.go#L1576) previously exposed only `inStartupWindowLocked`: once `startupStateWindow` elapsed, every status path fell through to ordinary `waiting`. No transition owned the expired handover, no pane process consumed input, and no durable pane evidence let a later CLI/TUI process distinguish timeout from a healthy wait.

## Fix

[`internal/tmux/tmux.go:1580`](internal/tmux/tmux.go#L1580) now claims an expired startup exactly once per pane generation, marks it `error`, and replaces the unowned pane with an inert hold that disables terminal echo and prints both the timeout and the exact `agent-deck session restart <session>` recovery command. `Start` and `RespawnPane` clear the generation flag. [`internal/tmux/tmux.go:5084`](internal/tmux/tmux.go#L5084) recognizes the hold banner before tool-specific classification, so `status` remains `error` after a fresh process reload.

## Proof

- Regression: `go test ./internal/tmux -run TestIssue1892_StartupWithoutAgentSignalTimesOutHonesty -count=1 -v` — PASS.
- Targeted package: `go test ./internal/tmux -count=1` — PASS (`43.072s`).
- Required repository check: `docker run --rm -v $PWD:/src -w /src golang:1.25 sh -c "go build ./... && go vet ./..."` — PASS.
- Visual gauntlet: `screenshot.sh /tmp/iss-1892/gauntlet-frames` — PASS; settled frames are in `/tmp/iss-1892/gauntlet-frames/judge-frames/` (`list.png`, `filled.png`, `attached.png`, `back.png`, `help.png`).

The regression additionally constructs a fresh `Session` after the timeout and proves the pane evidence still classifies as `error`, covering the `status`/process-reload path rather than only the in-memory helper.

## Pull request

Pending creation.
