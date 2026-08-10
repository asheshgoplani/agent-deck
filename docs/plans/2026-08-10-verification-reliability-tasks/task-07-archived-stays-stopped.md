# Task 07 — Archived status stays stopped

**tier: mid**  
**Parallelism:** independent; safe with task 06 and other groups.

## Approved design extract (verbatim)

> - An archived session retains the terminal `stopped` state instead of being reclassified as `error` because its tmux process is absent.

## Change

In `internal/session/instance.go` `UpdateStatus`, change both vanished-pane branches. When `i.tmuxSession == nil`, test `i.IsArchived()` first and set `i.Status = StatusStopped` before `neverStarted()` or terminated-pane classification. When `!i.tmuxSession.Exists()`, apply the same first-priority archive guard. Do not change pure `classifyTerminatedPane`.

Add table tests before code covering archived/no tmux object, archived/nonexistent tmux pane, active never-started, active clean exit, and active error exit. Assert archive cases remain stopped and do not refresh death/auth-hold state as an error.

## Acceptance criteria

- Both missing-pane paths preserve `StatusStopped` for archived instances.
- Non-archived classification behavior is unchanged.

## Verification

```sh
go test ./internal/session -run '^TestUpdateStatus.*Archived' -race -count=1 -v
```

Expected after red/green: selected tests `PASS`, exit 0.

## Interfaces

consumes:
- `internal/session/archive.go`: `(*Instance).IsArchived() bool`
- `internal/session/instance.go`: `(*Instance).UpdateStatus()`, `StatusStopped`

produces:
- `UpdateStatus` archive-first behavior in both vanished-pane branches

## Record (append-only)

