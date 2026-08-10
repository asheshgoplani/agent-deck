# Task 01 — Runtime queue durable store

**tier: mid**
**Parallelism:** starts Group A; no prerequisite.

## Approved design extract (verbatim)

> Add `agent-deck session send --queue-if-busy`.
>
> - Resolve the complete message before queueing, including `--message-file` contents.
> - When the target is idle, use the existing verified send path immediately.
> - When a hook-capable target is busy, append the message atomically to a durable per-session FIFO and return promptly with a machine-readable queued receipt.
> - Drain messages in FIFO order after a trusted Stop or equivalent turn-finished edge.
> - Remove an entry only after verified submission. Persistence survives Agent Deck restarts.
> - Reject queueing for missing, stopped, archived, or non-hook-capable targets.
> - Enforce fixed internal limits for message count and total bytes; a full queue returns a clear nonzero error.
> - Removing or archiving a session discards its queued runtime messages so unarchive or identifier reuse cannot replay stale work.

## Change

Create `internal/session/runtime_queue.go` and `internal/session/runtime_queue_test.go`. Before implementation, add one failing test per behavior and run it for the intended assertion failure.

Implement:

```go
type RuntimeQueuedMessage struct {
    ID       string    `json:"id"`
    Message  string    `json:"message"`
    QueuedAt time.Time `json:"queued_at"`
    Source   string    `json:"source"`
}

var ErrRuntimeQueueFull = errors.New("runtime message queue is full")

const (
    MaxRuntimeQueueMessages = 100
    MaxRuntimeQueueBytes    = 16 << 20
)

func RuntimeQueueDir() string
func RuntimeQueuePathFor(id string) string
func EnqueueRuntimeMessage(id, msg string) (depth int, err error)
func RuntimeQueueHasPending(id string) bool
func PeekRuntimeQueue(id string) ([]RuntimeQueuedMessage, error)
func DiscardRuntimeQueue(id string) error
```

Use the same package-level mutex discipline as `inboxWriteMu`; reuse `sanitizeInboxName`, `dataPath`, `fileHasContent`, `writeFileDurable`, and `fsyncDir`. Store one JSON object per line under a dedicated `runtime-queues` directory. Generate a unique non-empty ID, UTC `QueuedAt`, and `Source: "session-send"`. Under the lock, parse existing data, enforce both limits against each encoded line including newline, append the complete line, `Sync`, and close. A rejected enqueue leaves bytes unchanged. `Peek` returns FIFO without mutation; missing means empty. `Discard` durably removes active and task-02 inflight paths; missing is success.

Tests cover path sanitization, persistence/FIFO, populated metadata, count and byte boundaries, no mutation on rejection, process-local restart simulation, discard, and malformed/overlong input.

## Acceptance criteria

- Durable append is fsynced and preserves FIFO across re-read.
- Both limits return `errors.Is(err, ErrRuntimeQueueFull)` independently.
- Pending/peek and idempotent discard cover missing and populated queues.

## Verification

```sh
go test ./internal/session -run '^TestRuntimeQueue(Store|Capacity|Restart|Discard|Malformed)' -race -count=1 -v
```

Expected after observed red failures: selected tests `PASS`, exit 0, no race report.

```sh
gofmt -w internal/session/runtime_queue.go internal/session/runtime_queue_test.go
git diff --check
```

Expected: `git diff --check` prints nothing and exits 0.

## Interfaces

consumes:
- `internal/session/inbox.go`: `sanitizeInboxName(string) string`, `writeFileDurable(string, []byte, fs.FileMode) error`, `fsyncDir(string) error`
- `internal/session/inbox_consumer.go`: `fileHasContent(string) bool`
- `internal/session/data_paths.go`: `dataPath(name string, markers ...string) string`

produces:
- `internal/session/runtime_queue.go`: `RuntimeQueuedMessage`, `ErrRuntimeQueueFull`, `MaxRuntimeQueueMessages`, `MaxRuntimeQueueBytes`
- `RuntimeQueueDir() string`, `RuntimeQueuePathFor(string) string`, `EnqueueRuntimeMessage(string, string) (int, error)`, `RuntimeQueueHasPending(string) bool`, `PeekRuntimeQueue(string) ([]RuntimeQueuedMessage, error)`, `DiscardRuntimeQueue(string) error`

## Record (append-only)
