# Task 04 — Merge runtime queue into Stop-hook drain

**tier: mid**  
**Parallelism:** after task 02; safe in parallel with task 03.

## Approved design extract (verbatim)

> - Drain messages in FIFO order after a trusted Stop or equivalent turn-finished edge.
> - Remove an entry only after verified submission. Persistence survives Agent Deck restarts.

## Change

Edit only `internal/session/inbox_stophook.go` and tests; do not edit `cmd/agent-deck/hook_handler.go`. Extend `DrainForStopHook(instanceID string, stopHookActive bool) (StopHookDecision, bool, error)`.

Fast-path only when neither inbox nor runtime queue is pending. Under existing `stopBlockMu`, reserve one durable budget slot before draining. Drain both, then return exactly one block decision. Join non-empty `FormatCompletionsForInjection(events)` and `FormatRuntimeMessagesForInjection(messages)` with one blank line, inbox first. An error returns no block response while preserving recoverable WAL. One call consumes at most one `MaxStopHookBlocks` slot.

Test inbox-only, runtime-only, both (one decision/one budget increment/order), neither, inactive hook, exhausted budget, error recovery, and discarded runtime data producing no block.

## Acceptance criteria

- Stop emits at most one block decision containing both sources.
- One shared durable budget governs both.
- Existing inbox-only behavior remains compatible.

## Verification

```sh
go test ./internal/session -run '^TestDrainForStopHook' -race -count=1 -v
```

Expected after red/green: selected tests `PASS`, exit 0, no race report.

## Interfaces

consumes:
- task 02: `DrainRuntimeQueue(string) ([]RuntimeQueuedMessage,error)`, `FormatRuntimeMessagesForInjection([]RuntimeQueuedMessage) string`
- inbox APIs and `MaxStopHookBlocks`

produces:
- unchanged signature `DrainForStopHook(string,bool) (StopHookDecision,bool,error)` with merged semantics
- one-block contract consumed unchanged by `cmd/agent-deck/hook_handler.go`

## Record (append-only)

