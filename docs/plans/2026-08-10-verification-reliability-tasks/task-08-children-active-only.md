# Task 08 — Active-only child snapshots

**tier: mid**  
**Parallelism:** independent start; must finish before task 09 because both edit `session_children_follow.go`.

## Approved design extract (verbatim)

> - `session children` returns active children by default and gains `--include-archived` for explicit inspection.
> - Follow mode reports one `GONE` transition when an active child becomes archived.
> - The orchestration heartbeat filters archived children defensively, protecting users of older binaries as well as the current CLI.

## Change

Edit `cmd/agent-deck/session_children_follow.go`, `cmd/agent-deck/session_cmd.go`, `cmd/agent-deck/hook_children_context.go`, and tests.

Add field:

```go
Archived bool `json:"archived,omitempty"`
```

to `childRow`, populated by `buildChildRows`. Add:

```go
func activeChildren(kids []*session.Instance) []*session.Instance
```

which preserves order and returns only `!kid.IsArchived()`. Keep `childrenOf` unchanged. In one-shot `handleSessionChildren`, add boolean `--include-archived`; default applies `activeChildren`, explicit inclusion does not. Reject `--include-archived` with `--follow` as invalid usage. In `buildChildrenContextSummary`, always apply `activeChildren` before rows/formatting. Do not filter archived instances inside follow `loadChildRows`: task 09 needs `Archived:true` to detect transitions.

Test helper order/nil behavior, default JSON and text snapshots, explicit archived rows/field, conflict with follow, and fleet snapshot exclusion.

## Acceptance criteria

- One-shot and hook context are active-only by default.
- Explicit one-shot inclusion exposes archived rows with `archived:true`.
- Follow loader retains archived metadata for task 09.

## Verification

```sh
go test ./cmd/agent-deck -run 'Children.*(Archived|Active|Include)|ChildrenContext.*Archived' -count=1 -v
```

Expected after red/green: selected tests `PASS`, exit 0.

## Interfaces

consumes:
- `session.(*Instance).IsArchived() bool`
- `childrenOf(string, []*session.Instance) []*session.Instance`
- `buildChildRows`, `buildChildrenContextSummary`

produces:
- `childRow.Archived bool` JSON key `archived,omitempty`
- `activeChildren([]*session.Instance) []*session.Instance`
- `session children --include-archived` (one-shot only)
- follow rows retaining archived children for task 09

## Record (append-only)

