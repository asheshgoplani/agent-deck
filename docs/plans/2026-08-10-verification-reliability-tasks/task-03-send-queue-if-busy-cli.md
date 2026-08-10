# Task 03 — `session send --queue-if-busy`

**tier: mid**  
**Parallelism:** after task 02; safe in parallel with task 04.

## Approved design extract (verbatim)

> Add `agent-deck session send --queue-if-busy`.
>
> - Resolve the complete message before queueing, including `--message-file` contents.
> - When the target is idle, use the existing verified send path immediately.
> - When a hook-capable target is busy, append the message atomically to a durable per-session FIFO and return promptly with a machine-readable queued receipt.
> - Reject queueing for missing, stopped, archived, or non-hook-capable targets.
> - Enforce fixed internal limits for message count and total bytes; a full queue returns a clear nonzero error.
> - Preserve the behavior of default sends and the existing `--no-wait`, `--wait`, and `--defer-if-busy` flags.

## Change

Edit `cmd/agent-deck/session_cmd.go`, `cmd/agent-deck/cli_utils.go`, and focused tests. Add `const ErrCodeQueueFull = "QUEUE_FULL"`. Register boolean `--queue-if-busy`; reject combinations with `--no-wait`, `--wait`, `--stream`, `--draft`, or `--defer-if-busy` via invalid-operation output. Resolve message/file input before branching. Reject missing/nonexistent, stopped, archived, or `!sessionstatus.IsHookEmittingTool(inst.Tool)` targets.

Call `fetchHookDrivenStatus`. If not busy per `send.StatusIsBusy`, continue through existing readiness/`executeSend` unchanged. If busy, call `session.EnqueueRuntimeMessage(inst.ID, message)` and return. Map full to `ErrCodeQueueFull`, other persistence errors to `ErrCodeDeliveryFailed`. Success `.Success` data contains `queued:true`, `session_id`, and integer `queue_depth`; do not call readiness/send on queued branch.

Write table tests for conflicts/target states, busy JSON receipt/depth including message-file resolution, queue-full code, idle sender invocation/no queue, and unchanged default sends.

## Acceptance criteria

- Busy eligible target promptly queues the fully resolved message.
- Idle target uses verified synchronous send.
- Invalid targets/flags and capacity yield stable machine-readable errors.

## Verification

```sh
go test ./cmd/agent-deck -run '^TestSessionSendQueueIfBusy' -count=1 -v
```

Expected after red/green: selected tests `PASS`, exit 0.

## Interfaces

consumes:
- task 01: `session.EnqueueRuntimeMessage(string,string) (int,error)`, `session.ErrRuntimeQueueFull`
- `fetchHookDrivenStatus`, `send.StatusIsBusy(string) bool`, `sessionstatus.IsHookEmittingTool(string) bool`

produces:
- `cmd/agent-deck/cli_utils.go`: `ErrCodeQueueFull = "QUEUE_FULL"`
- CLI `--queue-if-busy`; receipt keys `queued`, `session_id`, `queue_depth`

## Record (append-only)

