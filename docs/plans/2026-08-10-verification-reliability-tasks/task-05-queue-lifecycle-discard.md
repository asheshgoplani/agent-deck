# Task 05 — Discard runtime queue on removal/archive

**tier: cheap**  
**Parallelism:** after task 01; disjoint from tasks 02–04 except consumed API.

## Approved design extract (verbatim)

> - Removing or archiving a session discards its queued runtime messages so unarchive or identifier reuse cannot replay stale work.

## Change

Call `DiscardRuntimeQueue(id)` at exactly:

- `internal/session/rm_sweep.go`: inside `sweepParentSideArtifacts(id)`.
- `cmd/agent-deck/session_cmd.go`: successful `handleSessionArchive`.
- `internal/ui/home.go`: successful `archiveSession`.
- `internal/ui/web_mutator.go`: successful `WebMutator.ArchiveSession`.
- `internal/ui/context_budget_ui.go`: successful source archive during rotation.

Propagate errors where possible; otherwise use the existing UI error path. Never discard before archive persistence succeeds. Add tests seeding active+inflight files, performing remove/archive, and asserting both absent; a failed archive must retain them.

## Acceptance criteria

- All remove/archive entrances clear active and inflight runtime artifacts after success.
- Failed lifecycle transition does not destroy queued work.

## Verification

```sh
go test ./internal/session ./cmd/agent-deck ./internal/ui -run 'RuntimeQueue.*(Remove|Archive|Rotation)|Archive.*RuntimeQueue' -count=1 -v
```

Expected after red/green: selected tests `PASS`, exit 0.

## Interfaces

consumes:
- task 01: `internal/session.DiscardRuntimeQueue(string) error`
- `sweepParentSideArtifacts(string)` and four archive functions above

produces:
- lifecycle guarantee that no active/inflight runtime queue survives successful remove/archive

## Record (append-only)

