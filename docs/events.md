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
| `internal/session/event_writer.go` | No live bus tap (the events/ file format is unchanged). | — |
| `internal/statedb` `WriteStatus` via `internal/session/status_bus.go` (only with `[macapp] status_events = true`) | every status row transition, from whichever process owns the status (TUI poller, notify daemon); one frame per edge, even when two owners write the same transition | `session.status` |
| same | entering `running` / leaving `running` | `session.turn` |
| `internal/session/transition_daemon.go` (only with `[macapp] transcript_events = true`) | a live session's native transcript grew | `session.transcript` |
| `agent-deck events publish` (only with `[macapp] plugins = true`) | a client/plugin frame | `macapp.*` |
| `internal/session/transition_notifier.go` | `NotifyTransition` | `session.transition` |
| `internal/session/transition_notifier.go` | `NotifyFinished` | `session.finished` |
| `internal/tmux/pipemanager.go` | tmux `%output` | `tmux.output` |
| `internal/watcher/engine.go` | `writerLoop` (new persisted event) | `watcher.event` |
| `internal/watcher/engine.go` | `healthLoop` (health snapshot) | `watcher.health` |

Frame data for the session kinds (`session_id` is the agent-deck session id):

| Kind | `data` |
|---|---|
| `session.status` | `{from, to, tool, tmux_session, substate, changed_at}` (`changed_at` RFC 3339, the frame `ts` is the same instant in ms) |
| `session.turn` | `{phase: "started"}` or `{phase: "ended", to, duration_ms}` (duration measured by the publishing process; omitted when it did not see the start) |
| `session.transcript` | `{path, bytes_appended, size}` |
| `tmux.output` | none: `session_id` is the tmux session name; the frame has no `data` key (never `"data": null`) |

A producer that passes nil data publishes a frame without `data`; a stored
`data: null` from an older writer is also rendered without it.

The tmux producer calls `PublishDefault` into a bounded in-memory queue.
Transitions call `PublishProfile` with the event's owning profile, including
when one notify daemon walks several profiles. Watcher producers call
`Publish` on the Engine-owned bus for the TUI's effective profile. The writer
groups up to 256 frames under one cross-process file lock and syncs at most
once per second or at close. A normal shutdown drains accepted taps for up to
two seconds; a held disk lock cannot hold the TUI exit path indefinitely.
`CloseDefault` returns `ErrCloseTimeout` when that deadline expires and counts
accepted unwritten frames as drops where the bus handle is available.

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

Rotation: the active segment seals (renamed to `seg-*`) on the next sync pass
after it passes 8 MiB or 50,000 frames, and a fresh `active.ndjson` starts.
This keeps file data durable before the sealed cursor range is published. Compaction:
after each rotation, the oldest sealed segments beyond the last 32 are
removed. A `Subscribe(after)` older than every retained segment returns
`events.ErrCursorTooOld` instead of silently skipping frames.

## Durability / degrade behavior

| Condition | Behavior |
|---|---|
| Bus dir unwritable, or `AGENTDECK_EVENTS_BUS=0` | Producer taps and `Subscribe` become no-ops; one `slog.Warn` per process; nothing else in agent-deck depends on the bus. |
| Either queue full (slow disk / producer burst) | Frame dropped; the owning process persists its count asynchronously, and `events stats --json` reads the profile total. |
| Append or sync fails after open | One warning, bus disabled, and the failed append does not advance the cursor. Existing producer writes continue. |
| Process crash before the queued frame is synced | That frame can be lost. A normal CLI or Engine shutdown drains accepted frames; shutdown under a held disk lock stops after two seconds. |
| `events follow` killed and resumed with `--after <cursor>` | Zero lost, zero duplicated — this is the durability proof, asserted in `internal/events/bus_test.go`'s `TestResumeAfterKillLosesNothingAndDuplicatesNothing` and `TestResumeSurvivesProcessRestart`. |

## Go API (small, documented — slice 5's daemon streams this bus)

```go
Open(dir string) (*Bus, error)
Default() *Bus                                   // process-wide, lazily opened
PublishDefault(kind, sessionID string, data any)  // bounded, no disk on producer path
PublishProfile(profile, kind, sessionID string, data any) // per-profile transition tap
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
| `agent-deck events follow --json [--after <cursor>] [--kind <prefix,...>] [--session <id>]` | NDJSON frames, oldest first, streams live until killed. `--kind session` matches `session.*`; `--kind macapp.` matches the namespace; filters never change cursors. |
| `agent-deck events stats --json` | `{enabled, dir, cursor, published, written, synced, dropped, queue_len, queue_cap, kinds: {kind: retained count}}`. |
| `agent-deck events publish --kind macapp.<name> [--session <id>] [--data <json> \| --data-file <path\|->] [--json]` | Publishes one frame and waits (≤ 2 s) until it is committed; prints `{ok, kind, session_id, cursor, profile}`. Only the `macapp.*` namespace, only with `[macapp] plugins = true` (exit 2 otherwise). `--session` defaults to `$AGENTDECK_INSTANCE_ID`. |

`cursor` and `dropped` reflect the profile across processes. `published`,
`written`, `synced` and queue occupancy describe the process running the
command.

Registered as a plain CLI command on this independent slice-4 branch.
The slice-1 registry bundle and this branch now share `origin/main` at
`3b41e36d`, and both bundles verify. Slice 1 remains a separate branch; its
registry is absent from this branch. Integration belongs to the later branch
that combines the two slices.
