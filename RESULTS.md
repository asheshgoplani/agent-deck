# PR #2080 review follow-up: hook-driven heartbeat gate

## What the HOLD verdict asked for

Independent review of PR #2080 (heartbeat interactive-state guard + bounded
consecutive skips) found the bounded-skip mechanism sound but the
interactive-state DETECTION still a pane-text guess: `_pane_has_open_picker`
infers an open AskUserQuestion picker from tmux glyphs, when a real
busy/interactive signal (hook status) already exists and should gate the
heartbeat instead, with pane text only as an "unknown"-confidence fallback.

Task: gate on the same hook-driven busy/interactive signal the send path uses
after PR #2273's queued-delivery fix (`hookDrivenBusy` /
`send.StatusIsBusy`), keep the bounded skips, and add a failing-first test
where the pane text is neutral but the hook state says interactive.

## Root cause

A fresh hook-driven status of `"running"`/`"starting"` already covers an open
AskUserQuestion picker: its `PreToolUse` event writes `"running"` and nothing
advances it to `Stop`/`PostToolUse` until the human answers. That signal was
never exposed outside the Go send path — `session show --json` had no field
for it — so the Python bridge had no choice but to re-derive "is this
interactive" from a raw pane capture, which is exactly the class of
false-positive/false-negative risk #1999 already documented for the picker
regex.

## Fix

1. **`cmd/agent-deck/session_cmd.go`** — `session show --json` now always
   reports `hook_status` and `hook_status_fresh` (the same
   `inst.GetHookStatus()` pair `--defer-if-busy`'s `fetchHookDrivenStatus`
   reads), so any caller — not just the Go send path — can gate on the
   authoritative busy/interactive signal instead of re-deriving it from pane
   text. Always present (never omitted when empty), matching this file's own
   `wrapper`/`channels` precedent: an absent key would be ambiguous with "this
   build predates the field".

2. **`internal/session/conductor_bridge.py`**
   - New `hook_driven_interactive(session, profile) -> (interactive, known)`:
     calls `session show --json`, reads `hook_status`/`hook_status_fresh`,
     and reports `known=False` (never a false "not interactive") on any CLI
     failure, JSON parse failure, or stale/absent hook sample. Interactive
     statuses mirror `internal/send/deferbusy.go`'s `StatusIsBusy`
     (`"running"`, `"starting"`).
   - `_pane_blocks_automated_send(pane_text, hook_known=False,
     hook_interactive=False)`: when the hook signal is known, it is
     authoritative — `hook_interactive=True` blocks with reason
     `"hook-busy-interactive"`, and pane-text picker detection is skipped
     entirely (no risk of a stale-glyph false positive, or a hook-confirmed
     idle target being blocked on a leftover pane shape). When the hook
     signal is unknown, pane-text picker detection runs as the fallback, but
     its verdict is now prefixed `"unknown:"` so it is never confused with
     confirmed hook evidence. The composer-unsent-draft check is unchanged
     and always runs off pane text (orthogonal to turn state).
   - `heartbeat_loop` now calls `hook_driven_interactive` before
     `capture_pane` and threads the result into `_pane_blocks_automated_send`.
     The bounded-skip/override machinery (`HEARTBEAT_SKIP_LIMIT`,
     `_heartbeat_skip_action`) is untouched — a persistently-busy hook signal
     still overrides after 3 consecutive cycles so a wedged hook file can
     never silence the conductor forever (#1999).

3. Updated `test_issue1981_heartbeat_send_guard.py`'s
   `test_open_picker_skips` for the (intentional) new `"unknown:"` prefix on
   the pane-only fallback call shape, and added a doc note pointing at the
   new test file.

## Backward compatibility

`_pane_blocks_automated_send`'s new parameters default to
`hook_known=False, hook_interactive=False` — a caller that never learned
about the hook signal gets exactly the pre-#2080 pane-only verdicts (proven
by `test_default_call_matches_pre_2080_pane_only_behavior`). `session show
--json` gains two new always-present keys; no existing key changed shape.

## Test evidence

### Red (failing-first)

Against the pre-fix code (`git stash` back to the pre-#2080-followup diff,
new test files left in place):

```
FAILED conductor/tests/test_issue2080_hook_gates_heartbeat.py::TestHookGatesOverPaneText::test_hook_interactive_blocks_even_on_neutral_pane_text
FAILED conductor/tests/test_issue2080_hook_gates_heartbeat.py::TestHookGatesOverPaneText::test_default_call_matches_pre_2080_pane_only_behavior
FAILED conductor/tests/test_issue2080_hook_gates_heartbeat.py::TestHookGatesOverPaneText::test_hook_known_not_interactive_ignores_stale_pane_picker_shape
FAILED conductor/tests/test_issue2080_hook_gates_heartbeat.py::TestHookGatesOverPaneText::test_hook_known_not_interactive_still_catches_unsent_draft
FAILED conductor/tests/test_issue2080_hook_gates_heartbeat.py::TestHookGatesOverPaneText::test_hook_unknown_and_pane_neutral_sends
FAILED conductor/tests/test_issue2080_hook_gates_heartbeat.py::TestHookGatesOverPaneText::test_hook_unknown_falls_back_to_pane_text_as_unknown_confidence
FAILED conductor/tests/test_issue2080_hook_gates_heartbeat.py::TestHookDrivenInteractive::test_cli_failure_is_unknown_not_not_interactive
FAILED conductor/tests/test_issue2080_hook_gates_heartbeat.py::TestHookDrivenInteractive::test_fresh_running_is_interactive_and_known
FAILED conductor/tests/test_issue2080_hook_gates_heartbeat.py::TestHookDrivenInteractive::test_fresh_waiting_is_not_interactive_but_known
FAILED conductor/tests/test_issue2080_hook_gates_heartbeat.py::TestHookDrivenInteractive::test_stale_hook_status_is_unknown
FAILED conductor/tests/test_issue2080_hook_gates_heartbeat.py::TestHookDrivenInteractive::test_unparseable_json_is_unknown
```

Go side (before `hook_status`/`hook_status_fresh` were added to
`session show --json`):

```
--- FAIL: TestIssue2080_SessionShowJSONIncludesHookStatus
    issue2080_hookstatus_show_test.go:61: session show --json omits the hook_status key entirely — callers cannot distinguish "hooks never fired" from "this build predates the field" (#2080)
```

### Green

```
$ python3 -m pytest conductor/tests/test_issue1981_heartbeat_send_guard.py conductor/tests/test_issue2080_hook_gates_heartbeat.py -q
26 passed in 0.05s

$ python3 -m pytest conductor/tests/ -q
13 failed, 92 passed, 1 skipped
```

The 13 residual failures (`test_bridge_paths.py`, `test_bridge_proxy.py`) are
pre-existing and environment-specific (macOS system Python 3.9's asyncio
event-loop policy, and this host's XDG/HOME layout) — confirmed by running
the identical unmodified tree (`git stash`) and getting the same 13 failures
plus the (then-red) new test file. Neither touched file this PR modifies.

Go:

```
$ go build ./...          # PASS (host)
$ go vet ./...            # PASS (host)
$ gofmt -l <touched files> # clean
```

Docker (`golang:1.25`, `--network none --cap-drop ALL`, module cache
pre-warmed once with network so the sandboxed run only compiles):

```
$ go test ./cmd/agent-deck/... -run TestIssue2080_SessionShowJSONIncludesHookStatus -v
--- PASS: TestIssue2080_SessionShowJSONIncludesHookStatus (2.29s)
PASS
```

Not run: `internal/session` / broader `cmd/agent-deck` full suites, and the
repository's CI race suite (host policy: no local `go test` outside a
container, and this task's scope is the one CLI field plus the Python bridge
guard). `go build`/`go vet` cover the whole module including the untouched
packages.

## Scope note

Contributor's original commits (`ff1ad56f`, `f04e685b`) are untouched; this
review-response work is two new commits on top, `carry/2080`, entirely local
(no push, no PR edit, no comment — per task constraints).
