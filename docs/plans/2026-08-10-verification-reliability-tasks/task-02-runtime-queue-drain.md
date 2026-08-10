# Task 02 — Runtime queue two-phase drain

**tier: mid**
**Parallelism:** after task 01; unlocks tasks 03 and 04.

## Approved design extract (verbatim)

> - Drain messages in FIFO order after a trusted Stop or equivalent turn-finished edge.
> - Remove an entry only after verified submission. Persistence survives Agent Deck restarts.

## Change

Extend `internal/session/runtime_queue.go` and its tests. Mirror the inbox two-phase WAL in `internal/session/inbox_consumer.go`, with an independent runtime lock.

```go
func DrainRuntimeQueue(id string) ([]RuntimeQueuedMessage, error)
func FormatRuntimeMessagesForInjection(msgs []RuntimeQueuedMessage) string
```

Add an unexported stage function and test seam `RuntimeQueueDrainStagePhaseForCrashTest`. Under lock: recover inflight WAL, read active JSONL, durably write their FIFO union to `runtime-queue-inflight/<sanitized-id>.jsonl`, then remove/fsync active. Finalize by reading WAL and only then durably removing it. A stage-only crash must cause next-call redelivery. Do not add a consumed ledger.

Format one injection section with heading and numbered messages, preserving message text/order. Empty input returns `""`; do not serialize metadata as instructions. Test active+WAL FIFO, successful exactly-once removal, stage-crash redelivery, malformed WAL retention/error, and empty/single/multiple/multiline formatting.

## Acceptance criteria

- Staging is durable before active removal.
- Crash after stage safely redelivers; completed drain removes exactly once.
- FIFO survives recovery and formatting.

## Verification

```sh
go test ./internal/session -run '^TestRuntimeQueue(Drain|Crash|Format)' -race -count=1 -v
```

Expected after red/green: selected tests `PASS`, exit 0, no race report.

## Interfaces

consumes:
- task 01 `internal/session/runtime_queue.go`: `RuntimeQueuedMessage`, store/path APIs and runtime queue mutex
- `internal/session/inbox_consumer.go`: `stageInboxDrainLocked`/`finalizeInboxDrain` pattern

produces:
- `DrainRuntimeQueue(string) ([]RuntimeQueuedMessage, error)`
- `FormatRuntimeMessagesForInjection([]RuntimeQueuedMessage) string`
- test seam `RuntimeQueueDrainStagePhaseForCrashTest`

## Record (append-only)
