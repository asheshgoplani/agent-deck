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

`tmux.output` is the hottest producer (fires on every pane write) and
deliberately skips `Flush` — only `Publish`'s bounded queue and drop-counter
keep it non-blocking. The other producers call `Flush(50ms)` after
`Publish` so a one-shot process (a hook-handler invocation) doesn't lose the
frame to an unflushed buffer before it exits; `Flush` is itself bounded, so
a stuck disk degrades to "returns false", never an indefinite block.

## On-disk layout

`<profile-data-dir>/bus/`, resolved through `internal/agentpaths`
(`EffectiveDataPath("bus", "bus")` — same XDG/legacy-dir and per-profile
rules as every other agent-deck data path). Typically:

- `~/.local/share/agent-deck/bus/` (XDG), or
- `~/.agent-deck/bus/` (legacy dir, if that's what's already in use).

| File | Meaning |
|---|---|
| `active.ndjson` | The segment currently being appended to. |
| `seg-<start>-<end>.ndjson` | A sealed, immutable segment (cursor range in the name). |

Rotation: the active segment seals (renamed to `seg-*`) and a fresh
`active.ndjson` starts once it passes 8 MiB or 50,000 frames. Compaction:
after each rotation, the oldest sealed segments beyond the last 32 are
removed. A `Subscribe(after)` older than every retained segment returns
`events.ErrCursorTooOld` instead of silently skipping frames.

## Durability / degrade behavior

| Condition | Behavior |
|---|---|
| Bus dir unwritable, or `AGENTDECK_EVENTS_BUS=0` | `Publish`/`Subscribe` become no-ops; one `slog.Warn` per process; nothing else in agent-deck depends on the bus. |
| Queue full (slow disk / producer burst) | Frame dropped, `Stats().Dropped` increments; producer never blocks. |
| Process crash between `Publish` and the next fsync | That frame may be lost (at most one flush interval, ~25ms, or one `Flush` window). Everything already synced is intact. |
| `events follow` killed and resumed with `--after <cursor>` | Zero lost, zero duplicated — this is the durability proof, asserted in `internal/events/bus_test.go`'s `TestResumeAfterKillLosesNothingAndDuplicatesNothing` and `TestResumeSurvivesProcessRestart`. |

## Go API (small, documented — slice 5's daemon streams this bus)

```go
Open(dir string) (*Bus, error)
Default() *Bus                                   // process-wide, lazily opened
(*Bus) Publish(kind, sessionID string, data any)  // never blocks
(*Bus) Subscribe(ctx, after Cursor) (*Subscription, error)
(*Bus) Cursor() Cursor
(*Bus) Stats() Stats
(*Bus) Flush(timeout time.Duration) bool
(*Bus) Close() error
```

## CLI

| Command | Output |
|---|---|
| `agent-deck events follow --json [--after <cursor>]` | NDJSON frames, oldest first, streams live until killed. |
| `agent-deck events stats --json` | `{enabled, dir, cursor, published, written, synced, dropped, queue_len, queue_cap}`. |

Registered as a plain command in `cmd/agent-deck/events_cmd.go`, not through
`internal/core`'s registry: the slice-1 registry bundle
(`core/registry-slice1-20260922`) does not apply on top of this branch (see
RESULTS.md) — `git bundle verify` reports a missing prerequisite commit the
bundle and this branch's `origin/main` don't share. If/when slice 1 lands on
a shared history, `events follow`/`events stats` are natural registry
commands to migrate.
