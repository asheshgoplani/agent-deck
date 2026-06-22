# Fleet fan-out CLI — design

**Date:** 2026-06-13
**Status:** Approved (design); implementation plan pending
**Branch:** `feat/fleet-fanout-cli`

## Problem

From inside a Claude session running under agent-deck, a user wants to fan out
several child sessions (e.g. "launch 5 sessions"), let them work independently,
and later — from any chat in the parent session, or from the TUI — check which
children are done and read their results. The check must be **non-blocking**:
inspecting progress from the parent must never freeze the parent's other work,
and must not consume/destroy events that another chat or the conductor relies on.

### What already works (verified)

- `agent-deck launch <path> -c claude -m "<task>"` creates a real, registered
  session in its own detached tmux process. It **auto-parents** the child to the
  launching session by default (`launch_cmd.go` — the `!*noParent` path), so
  children become sub-sessions of the parent and appear nested under it in the
  TUI session list with live status dots.
- Children run fully independently (separate tmux processes / context windows);
  closing the parent does not stop them.
- `agent-deck list --json` shows all sessions; `agent-deck session output <id>
  [--json]` returns a child's latest response.
- A completion sentinel exists: a worker ending its final turn with
  `===AGENTDECK_DONE=== status=<ok|fail> summary=<one line>` is detected on the
  `Stop` edge (`internal/session/done_sentinel.go`,
  `internal/session/hook_watcher.go`) and delivered to the parent as a `[DONE]`
  event carrying `done_status` / `done_summary`
  (`internal/session/taskworker.go:DeliverCompletion`).

### The gap

Completion (`done_status` / `done_summary`) currently lives **only** as a
transient event in the parent's durable outbox. The only way to read it is
`agent-deck inbox drain <session-id>`, which is **destructive** ("reading clears
the inbox") and last-wins per child. Consequences:

1. A parent cannot peek "which of my fleet is done" repeatedly without stealing
   those events from another chat / the conductor heartbeat.
2. Completion is not a queryable property of a session — `list --json` does not
   expose it, so the TUI / CLI cannot show it as state.
3. Children only emit the sentinel if the operator remembers to append the
   "assert completion" instruction to every launch message; otherwise completion
   is never trustworthy.

## Goals

- Fan out N children from the parent session; they appear in the TUI list nested
  under the parent and run independently.
- From the parent (any chat) or the TUI, read each child's status + last
  completion (ok/fail + summary) on demand, **non-blocking and non-destructive**.
- Make the completion signal reliable by default for Claude children.

## Non-goals

- **No blocking `session wait`.** Explicitly excluded — it would freeze the
  parent's other chats. The peek-and-collect loop replaces it.
- **No multi-spec / manifest `launch`.** Launch stays one-session-per-call;
  callers loop it. A manifest format is scope creep.
- The consumer-facing skill that documents the loop is a **follow-up**, not part
  of this spec.

## Design

Three changes, all within the existing CLI; no new top-level noun.

### 1. Persist last completion on the session record (core enabler)

When the Stop-hook completion sentinel is detected, in addition to delivering the
existing outbox event, persist onto the child's `Instance` record:

- `done_status` — `ok` | `fail`
- `done_summary` — the one-line summary (already capped via `capDoneSummary`)
- `done_at` — timestamp of detection

Semantics:

- **Last-wins per child**, mirroring current outbox behavior. A genuinely new
  completion (different summary) overwrites; re-reading the same sentinel does
  not change it.
- This makes completion a **queryable property**, independent of the destructive
  outbox. The outbox path is unchanged — conductor heartbeats still
  `inbox drain` as before.
- `list --json` gains `done_status` / `done_summary` / `done_at` fields
  (omitempty), so the TUI and any CLI consumer can render completion as state.

### 2. Non-destructive fleet view: `agent-deck session children [<id>] --json`

New read-only subcommand. `<id>` defaults to the current session (auto-detected,
same as `session current`). Lists the session's direct sub-sessions with, per
child:

- `id`, `title`
- live `status` (running / waiting / idle / error)
- persisted `done_status` / `done_summary` / `done_at` (from change 1)

Read-only — it **never** clears the inbox. This is the parent's on-demand "who's
done" call; safe to run from any chat, as often as wanted. To read a finished
child's full transcript, the existing `session output <id> --json` is used.

Human-readable default output; `--json` for programmatic use.

### 3. Reliable signaling: `launch --assert-done`

`launch` gains `--assert-done` (and `--no-assert-done`). When on, it appends the
standard completion-sentinel instruction to the child's initial `-m` message:

```
## Final step — assert completion
When the task is fully done, print exactly this as the last line of your final message:
  ===AGENTDECK_DONE=== status=ok summary=<what you accomplished, one line>
Use status=fail if you could not complete it; put the blocker in the summary.
```

**Default-on for `-c claude` children** (opt out with `--no-assert-done`).
Rationale: a completion signal nobody remembers to request is useless; the whole
non-blocking-check story depends on children reliably asserting done. Non-Claude
tools are unaffected unless explicitly enabled.

## End-to-end flow

```
# Parent session fans out 5 children (looped from inside the parent):
for i in 1..5: agent-deck launch <path_i> -c claude --assert-done -m "<task_i>"
#   → 5 detached sessions, auto-parented to this session, visible nested in TUI.

# Parent keeps working. Whenever convenient, from any chat:
agent-deck session children --json
#   → [{id, title, status, done_status, done_summary, done_at}, ...]  (non-destructive)

# For any child showing done_status, pull its full result:
agent-deck session output <child-id> --json
```

No step blocks the parent; nothing is consumed by checking.

## Components touched

- `internal/session/` — completion persistence on the `Instance` record at the
  sentinel-detection site (near `hook_watcher.go` / `taskworker.go`); JSON
  serialization fields; `list --json` surfacing.
- `cmd/agent-deck/session_cmd.go` — new `children` subcommand.
- `cmd/agent-deck/launch_cmd.go` — `--assert-done` / `--no-assert-done` flag and
  message-append logic; default-on gating for `-c claude`.
- Help text / examples for the above.

## Error handling

- `session children` on a session with no sub-sessions → empty list (not an
  error).
- `session children <id>` for an unknown/ambiguous id → existing
  `ResolveSession` error path.
- Malformed completion sentinels remain ignored (current behavior); persistence
  only happens for a valid `status`.
- `--assert-done` with no `-m` message → append nothing and warn (there is no
  initial message to attach the instruction to), or document that the instruction
  is only injected when an initial message is present. (Decide in plan.)

## Testing

- Unit: sentinel detection persists `done_status`/`done_summary`/`done_at` on the
  record; last-wins on re-detection; malformed sentinel does not persist.
- Unit: `launch --assert-done` appends the instruction to the message; default-on
  for `-c claude`, off for other tools; `--no-assert-done` suppresses it.
- CLI: `session children --json` returns parented children with completion fields
  and does **not** clear the inbox (assert outbox still drainable afterward).
- Regression: `inbox drain` behavior unchanged; non-parented (`--no-parent`)
  children do not appear under `session children`.

## Packaging (follow-up, out of scope here)

A thin skill in the user's claude-setup repo documenting the loop:
`launch --assert-done` ×N → keep working → `session children --json` →
`session output <id>`. Authored after the CLI changes land.
