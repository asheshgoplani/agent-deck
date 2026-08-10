# Task 09 — Follow emits one archived `gone` event

**tier: mid**
**Parallelism:** strictly serial after task 08.

## Approved design extract (verbatim)

> - Follow mode reports one `GONE` transition when an active child becomes archived.

## Change

In `cmd/agent-deck/session_children_follow.go` add:

```go
Reason string `json:"reason,omitempty"`
```

to `followEvent`. Modify `diffChildEvents(prev,curr)` so a child active in `prev` and archived in `curr` emits exactly one event with event/type `gone` according to the existing field naming, the child identity, and `Reason:"archived"`. Archived rows never emit snapshot, added, status, or done events. Remaining archived across polls emits nothing; later physical removal emits no second event.

Filter archived rows from `summarizeChildren` counts and from the set evaluated by `allChildrenTerminal`; an empty active set must follow the function's existing empty-set semantic rather than being made terminal merely by archived rows. Ensure first-poll snapshot omits already archived rows. Keep archived rows in internal previous/current maps long enough to detect the edge.

Tests first: active→archived exactly one gone; archived→archived none; archived→removed none; first snapshot suppression; no added/status/done for archived; mixed summary counts; until-done ignores archived without premature termination.

## Acceptance criteria

- Exactly one `gone`/`reason:"archived"` edge per active child archive.
- Archived children are absent from every other follow output and aggregate.

## Verification

```sh
go test ./cmd/agent-deck -run 'Child.*(Gone|Archived|Summary|Terminal)|ChildrenFollow.*Archived' -count=1 -v
```

Expected after red/green: selected tests `PASS`, exit 0.

## Interfaces

consumes:
- task 08 `childRow.Archived bool` and archived-retaining `loadChildRows`
- `diffChildEvents`, `summarizeChildren`, `allChildrenTerminal`, `runChildrenFollow`

produces:
- `followEvent.Reason string` JSON key `reason,omitempty`
- `gone` event contract with `reason:"archived"`
- active-only follow snapshot/aggregate/terminal semantics

## Record (append-only)
