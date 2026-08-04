# Codex stream-disconnect auto-resume

**Date:** 2026-08-04  
**Status:** Approved design  
**Scope:** Agent Deck TUI only

## Problem

A running Codex session can stop on this transport failure:

```
stream disconnected before completion: error sending request for url
(https://chatgpt.com/backend-api/codex/responses)
```

The saved Codex thread is resumable, but today the operator must notice the
pane, restart the Agent Deck session, and continue it manually. The existing
external watchdog can recover opt-in sessions, but it requires installation and
per-session setup. The requested behavior is automatic while the Agent Deck TUI
is open.

## Evidence

On 2026-08-04, a disposable live Codex session was tested through Agent Deck:

1. It answered `FIRST_OK`.
2. `agent-deck session restart <id> --force` restarted the session.
3. A second prompt returned `SECOND_OK FIRST_OK`.

This proves the current restart path retains the Codex conversation. It does
not prove automatic detection; that requires the implementation and regression
tests in this design.

## Decision

Add a narrow, TUI-owned recovery path for only the known Codex stream-disconnect
banner. It calls the existing `Instance.Restart()` method, which resumes the
persisted Codex session ID when its rollout exists. It never invokes
`RestartFresh()`.

The feature runs only while the Agent Deck TUI is running. A closed TUI makes
no recovery attempt. There is no launchd/systemd unit, external watchdog,
configuration flag, or per-session opt-in.

## Detection and recovery contract

The coordinator considers a session eligible only when all of the following
are true:

- It is an Agent Deck-owned Codex-compatible session.
- Its recent pane tail contains both `stream disconnected before completion`
  and `backend-api/codex/responses` in the same rendered error banner.
- Its pane is not showing active-work output.
- The same error fingerprint is observed again on the next 60-second recovery
  tick without pane-output movement.
- The session is not intentionally stopped and has a usable persisted Codex
  rollout/session binding.

On the first matching observation, the coordinator records only the fingerprint
and timestamp. On the second matching observation, it calls `Instance.Restart()`.
Any output change, a nonmatching pane, active work, a stopped session, or a
failed pane capture clears the pending confirmation.

Automatic attempts are capped at two per session in a rolling six-hour window.
A cap hit records a warning and requires an explicit human restart. Restart
success or failure is logged with the session ID, title, and decision reason;
logs must not copy full pane content.

## Architecture

`Home` owns one process-local Codex-disconnect coordinator, alongside the
existing 60-second `reviverTick`. On each tick it receives the same owned
instance snapshot as the reviver.

The coordinator has one responsibility: preserve confirmation and attempt
timestamps for an exact transport failure. It delegates all launch behavior to
`Instance.Restart()`, including its spawn lock, stale-rollout guard, native
`codex resume <session-id>` command construction, and duplicate-session sweep.

The detector is a small pure helper over a recent pane tail. It is Codex-only
and rejects quoted/user text and broad connection-error wording. This keeps the
new rule separate from the existing Claude-specific `auth-401` classifier,
where connection and credential failures intentionally share a coarse substate.

No general self-heal executor is enabled or expanded. Its broader action modes
are intentionally held and solve unrelated failure classes.

## Interface and visibility

There is no user-facing setting or new command. The automatic behavior is on
for eligible sessions while the TUI is open.

The existing status remains the source of truth. Operator visibility is through
structured logs with one of these outcomes:

- `first_observation`
- `recovered_before_confirm`
- `restart_attempted`
- `restart_failed`
- `attempt_cap_reached`

## Test contract

Focused tests must cover:

1. Exact Codex disconnect banner matches; a generic connection error, user
   prose, quoted tool output, another tool, and active-work output do not.
2. One matching observation never restarts; the unchanged second observation
   restarts exactly once.
3. Pane movement, a stopped session, a capture error, and a missing or stale
   Codex rollout cancel the pending recovery.
4. The third attempt in six hours is refused; an attempt after the window is
   allowed.
5. The recovery action calls `Restart()`, not `RestartFresh()`.

An end-to-end test uses a disposable fake Codex command and a real tmux-backed
Agent Deck instance. It writes the exact banner, runs two recovery scans, and
asserts that the replacement command is `codex resume <captured-id>`. A
nonmatching error-banner fixture asserts no restart. The existing live test
above remains manual evidence that a real Codex resumed thread retains context.

## Out of scope

- Recovering arbitrary Codex errors, usage limits, auth failures, or other
  tools.
- Continuing work when the TUI is closed.
- Resending a prompt after restart; Codex resumes its existing conversation and
  the operator or normal session workflow supplies the next turn.
- Replacing or installing the external watchdog.

## Principles pass

The design reuses the existing TUI tick and `Instance.Restart()` rather than
creating a daemon or generic recovery framework (KISS/YAGNI). The restart
command and rollout validation remain owned by the existing session layer
(DRY). Detection state is isolated from launch behavior, so neither component
gains unrelated responsibilities (SOLID).
