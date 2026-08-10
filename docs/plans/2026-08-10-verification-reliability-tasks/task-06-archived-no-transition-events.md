# Task 06 — Archived sessions emit no transition events

**tier: mid**
**Parallelism:** independent; safe with Groups A, C, D, E and task 07.

## Approved design extract (verbatim)

> - The centralized transition-notification predicate rejects archived sessions.
> - The transition daemon skips archived instances before hook and status probing.

## Change

In `internal/session/transition_notifier.go`, make `instanceAcceptsTransitionEvents(inst *Instance) bool` return false for nil, `NoTransitionNotify`, or `inst.IsArchived()`. In `internal/session/transition_daemon.go`, immediately after `storage.LoadWithGroups()` in `syncProfile`, filter `instances` to non-archived entries before both the hook-status loop and tmux status-probe loop. Consequently archived IDs must not reach statuses, `lastStatus`, `emitDoneSignals`, or self-heal inputs.

Add tests first: predicate table; archived instance with hook state never probed/emitted; archived instance with vanished tmux never probed/emitted; archive racing a transition yields no transition/done event. Use existing probe/notifier seams and race-safe synchronization, not sleeps.

## Acceptance criteria

- Central predicate rejects archived sessions.
- Daemon excludes archived sessions before any external probe or downstream event state.
- Active behavior is unchanged and race tests pass under `-race`.

## Verification

```sh
go test ./internal/session -run 'Archived.*Transition|Transition.*Archived|SyncProfile.*Archived' -race -count=1 -v
```

Expected after red/green: selected tests `PASS`, exit 0, no race report.

## Interfaces

consumes:
- `internal/session/archive.go`: `(*Instance).IsArchived() bool`
- `internal/session/transition_notifier.go`: `instanceAcceptsTransitionEvents(*Instance) bool`
- `internal/session/transition_daemon.go`: `syncProfile(string)` and existing probe seams

produces:
- archived-rejecting transition predicate
- `syncProfile` invariant: all downstream instances are active

## Record (append-only)
