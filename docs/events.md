# Event bus (`internal/events`)

CORE-PLAN slice 4. Additive only: every existing producer keeps writing its
own on-disk format exactly as before (events/ directory, inbox jsonl,
per-parent outbox, state.db); the bus is a second, independent tap.

## Frame shape

`{cursor, event_id, ts, kind, session_id, data}`, one per NDJSON line,
canonical JSON (keys sorted lexicographically at every level, compact, no
trailing spaces).

| Field | Type | Meaning |
|---|---|---|
| `cursor` | uint64 | Durable, monotonic, per-profile. Never reused. 0 means "no frame". |
| `event_id` | string | Random, informational only. Not the ordering/identity key. |
| `ts` | int64 | Unix milliseconds. |
| `kind` | string | Producer-defined, dotted (`session.status`, `tmux.output`, ...). |
| `session_id` | string | The instance/session this event is about, when applicable. |
| `data` | object | Producer-defined payload. Omitted when empty. |

## Producers (Phase A: additive tap only)

| File | Publishes | Kind |
|---|---|---|
| `internal/session/event_writer.go` | `WriteStatusEvent` | `session.status` |
| `internal/session/transition_notifier.go` | `NotifyTransition` | `session.transition` |
| `internal/session/transition_notifier.go` | `NotifyFinished` | `session.finished` |
| `internal/tmux/pipemanager.go` | tmux `%output` | `tmux.output` |
| `internal/watcher/engine.go` | `writerLoop` (new persisted event) | `watcher.event` |
| `internal/watcher/engine.go` | `healthLoop` (health snapshot) | `watcher.health` |

Every producer only calls `Publish` into a bounded in-memory queue. The
background writer takes the cross-process file lock, appends and syncs. A
one-shot CLI process closes its bus when the handler returns, draining its
accepted taps before exit. A watcher Engine owns a separate bus and closes it
after its producers stop.

## On-disk layout

`<data-dir>/bus/<profile>/`, with the data root resolved through
`internal/agentpaths` and the selected profile appended as a validated local
name. Typically:

- `~/.local/share/agent-deck/bus/default/` (XDG), or
- `~/.agent-deck/bus/default/` (legacy root, if already in use).

| File | Meaning |
|---|---|
| `active.ndjson` | The segment currently being appended to. |
| `seg-<start>-<end>.ndjson` | A sealed, immutable segment (cursor range in the name). |
| `writer.lock` | Cross-process advisory lock for cursor assignment and rotation. |
| `cursor.state` | Last sealed cursor, retained even when all sealed segments are compacted. |
| `drops.count` | Cumulative drops from all producers for this profile. |

Rotation: the active segment seals (renamed to `seg-*`) and a fresh
`active.ndjson` starts once it passes 8 MiB or 50,000 frames. Compaction:
after each rotation, the oldest sealed segments beyond the last 32 are
removed. A `Subscribe(after)` older than every retained segment returns
`events.ErrCursorTooOld` instead of silently skipping frames.

## Durability / degrade behavior

| Condition | Behavior |
|---|---|
| Bus dir unwritable, or `AGENTDECK_EVENTS_BUS=0` | `Publish`/`Subscribe` become no-ops; one `slog.Warn` per process; nothing else in agent-deck depends on the bus. |
| Queue full (slow disk / producer burst) | Frame dropped; the owning process persists its count asynchronously, and `events stats --json` reads the profile total. |
| Append or sync fails after open | One warning, bus disabled, and the failed append does not advance the cursor. Existing producer writes continue. |
| Process crash before the queued frame is appended | That frame can be lost. A normal CLI or Engine shutdown drains accepted frames. |
| `events follow` killed and resumed with `--after <cursor>` | Zero lost, zero duplicated — this is the durability proof, asserted in `internal/events/bus_test.go`'s `TestResumeAfterKillLosesNothingAndDuplicatesNothing` and `TestResumeSurvivesProcessRestart`. |

## Go API (small, documented — slice 5's daemon streams this bus)

```go
Open(dir string) (*Bus, error)
Default() *Bus                                   // process-wide, lazily opened
OpenProfile(profile string) *Bus                  // component owned
(*Bus) Publish(kind, sessionID string, data any)  // never blocks
(*Bus) Subscribe(ctx, after Cursor) (*Subscription, error)
(*Bus) Cursor() Cursor
(*Bus) Stats() Stats
(*Bus) Flush(timeout time.Duration) bool
(*Bus) Close() error
CloseDefault() error                              // CLI/TUI shutdown
```

## CLI

| Command | Output |
|---|---|
| `agent-deck events follow --json [--after <cursor>]` | NDJSON frames, oldest first, streams live until killed. |
| `agent-deck events stats --json` | `{enabled, dir, cursor, published, written, synced, dropped, queue_len, queue_cap}`. |

`cursor` and `dropped` reflect the profile across processes. `published`,
`written`, `synced` and queue occupancy describe the process running the
command.

Registered as a plain CLI command on this independent slice-4 branch.
The slice-1 registry bundle and this branch now share `origin/main` at
`3b41e36d`, and both bundles verify. Slice 1 remains a separate branch; its
registry is absent from this branch. Integration belongs to the later branch
that combines the two slices.
