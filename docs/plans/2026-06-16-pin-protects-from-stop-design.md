# Pin protects sessions from automatic and bulk stops

**Date:** 2026-06-16
**Status:** Approved design — ready for implementation plan

## Problem

A "pinned" session (`Pin == top` or `bottom`) is one the user has deliberately
flagged as important — a conductor, a long-running worker, a session they want
kept visible at the top/bottom of its group. Today nothing protects such a
session from being killed:

- The **idle-timeout watcher** auto-kills any watchable session whose pane
  output hasn't changed for `IdleTimeoutSecs`. An important-but-quiet pinned
  session gets reaped.
- **Bulk "remove all errored"** (`session remove --all-errored`, and the TUI
  bulk-remove) sweeps every errored session at once. A pinned session that
  happens to be in an error state is collateral damage.

The user wants the pin to also mean "don't let automation or a bulk sweep stop
this." Explicit, deliberate per-session stops should continue to work
unchanged.

## Scope

Decided with the user:

| Stop path | Guarded? | Override |
| --- | --- | --- |
| Idle-timeout auto-stop | **Yes** — always skip pinned | None (it's automatic; unpin or stop explicitly) |
| Bulk remove-all-errored (CLI + TUI) | **Yes** — skip pinned by default | `--force` (CLI) / confirm (TUI) includes them |
| Explicit single stop (CLI `session stop`, TUI `Shift+D`, web stop/close/delete) | **No** — unchanged, always works | n/a |

**Pinned** is defined as `inst.Pin != session.PinNone` (covers both `PinTop` and
`PinBottom`). We reuse the existing pin signal — **no new field, no new config
flag**.

## Design

### Principle: guard at the decision point, not the kill primitive

All stop paths funnel through `Instance.killInternal(sync bool)`
(`internal/session/instance.go`). It is tempting to add the guard there, but
that primitive is shared by the explicit single-stop paths, which must keep
working. So the guard goes at the two *decision* sites that initiate automatic
or bulk kills — never in `Kill()` / `KillAndWait()` / `killInternal()`.

### 1. Idle-timeout watcher — always skip pinned

`internal/session/idle_timeout_watcher.go`, `IdleTimeoutWatcher.Tick`.

Add a pin check alongside the existing early-skip guards (`IdleTimeoutSecs <= 0`
and `!idleTimeoutWatchable(inst)`), using the same drop-from-tracking idiom so
the state is clean if the session is later unpinned:

```go
if inst.Pin != PinNone {
    delete(w.lastSeen, inst.ID)
    continue
}
```

Placement: after the `nil` check, before/after the `IdleTimeoutSecs <= 0`
check (order doesn't matter functionally; group it with the other skips).

Logging: the skip is a non-event, so it does **not** emit a
`ReasonIdleTimeoutExpired` lifecycle event. Emit a single debug line via
`idleLog.Debug("idle_timeout_skip_pinned", slog.String("instance_id", inst.ID),
slog.String("pin", string(inst.Pin)))` so the "why didn't / why won't this
session time out" question is answerable from the debug log (the standard
cross-ref method for these reports). Debug level keeps it out of normal output.

### 2. CLI bulk remove-all-errored — skip pinned unless `--force`

`cmd/agent-deck/session_remove_cmd.go`.

`--force` already exists in the flagset (`handleSessionRemove`, line ~24). It is
currently consumed only by the single-session path. Thread it into the bulk
path:

- Change `removeAllErrored(...)` signature to accept `force bool`.
- Update the call site (`if *allErrored { removeAllErrored(..., *force, ...) }`).
- In the loop, when `inst.Status == session.StatusError`, skip the kill+delete
  if `inst.Pin != session.PinNone && !force`. Collect skipped sessions into a
  `skipped` slice and leave them in `remaining` (so they are retained, not
  deleted).
- Report skipped count in the success output (both human and JSON):
  `Removed N errored session(s)` plus, when non-zero,
  `(skipped M pinned — use --force to include)`. Add a `skipped` field to the
  JSON payload.

`--force` semantics for `--all-errored` = "also include pinned errored
sessions." This intentionally overloads the existing flag (no new
`--include-pinned`) — its single-session meaning ("bypass the
removable-status gate") and this meaning are both "bypass a safety gate," which
is a coherent mental model.

### 3. TUI bulk remove errored — skip pinned

`internal/ui/home.go`, `bulkRemoveErrored`.

Skip pinned errored sessions (`inst.Pin != session.PinNone`); do not emit their
`sessionDeletedMsg`. Surface the skipped count in the resulting status line /
confirmation so the user knows why N sessions remained. (The TUI bulk path has
no force flag today; skipping is sufficient — an explicit `Shift+D` on the
specific session still works, matching the "explicit single stop always works"
rule.)

## Out of scope (YAGNI)

- No new DB field or schema bump — `Pin` already exists and round-trips.
- No new config flag — pin is the toggle.
- No change to `killInternal()` or any explicit single-stop path (CLI
  `session stop`, TUI `Shift+D`/close, web `/stop` `/close` `DELETE`).
- No web bulk-stop change — no web bulk endpoint exists.
- No new `--include-pinned` flag — `--force` covers it.

## Testing

### Idle watcher (`internal/session/idle_timeout_watcher_test.go`)
- **Pinned session is never stopped:** a pinned, watchable session whose pane
  hash is unchanged past `IdleTimeoutSecs` is NOT passed to `cfg.Stop`, and no
  `ReasonIdleTimeoutExpired` event is logged.
- **Unpinned still stops:** regression — an identical but unpinned session is
  still auto-stopped (guards against the pin check over-matching).
- **Unpin re-arms:** a session pinned then unpinned (Pin back to `PinNone`)
  resumes normal idle tracking and can be stopped.

### CLI bulk (`cmd/agent-deck/session_remove_cmd` test)
- `--all-errored` skips a pinned errored session and removes the unpinned
  errored ones; skipped count reported; pinned row still present afterward.
- `--all-errored --force` removes pinned errored sessions too.

### TUI bulk
- `bulkRemoveErrored` does not emit a `sessionDeletedMsg` for a pinned errored
  session; emits for unpinned ones.

## Files touched

| File | Change |
| --- | --- |
| `internal/session/idle_timeout_watcher.go` | pin skip + debug log in `Tick` |
| `cmd/agent-deck/session_remove_cmd.go` | thread `force` into `removeAllErrored`; skip+report pinned |
| `internal/ui/home.go` | skip pinned in `bulkRemoveErrored`; report count |
| `internal/session/idle_timeout_watcher_test.go` | pin-skip / unpinned-regression / unpin-rearm tests |
| `cmd/agent-deck/session_remove_cmd` test | bulk skip + `--force` include tests |
