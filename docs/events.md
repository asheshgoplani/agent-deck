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
| `internal/session/transition_daemon_turns.go` | a top-level conductor's own turn, dropped (`self_conductor`) before the notifier; one frame per turn or observed flip | `session.transition` |
| `internal/session/transition_notifier.go` | `NotifyFinished` | `session.finished` |
| `internal/tmux/pipemanager.go` | tmux `%output`, only while a follower demands it (see below) | `tmux.output` |
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

`tmux.output` is an on-demand kind: it has no payload, so it is written only
while a live follower asks for it. `events follow` asks when its `--kind`
filter includes `tmux.output` (or it has no filter); a daemon `subscribe`
stream always asks. A follower holds a lease file `want/<kind>.<pid>.<id>`
under the bus dir, touched every 5 s and removed when it stops; a lease older
than 15 s (a follower that died) no longer counts and is swept by the next
follower; a live follower whose late lease was swept (after a sleep or a
stop) recreates it on its next refresh. The producer checks the lease dir at most once per second, so a new
follower starts receiving ticks within about a second. With no follower,
nothing is written (#2481).

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
OpenAt(dir string, opts Options) (*Bus, error)     // Options{RetainSegments, RetentionDays, MaxSegmentBytes, ReadOnly}
(*Bus) Commit(kind, sessionID string, data any) (Frame, error) // synchronous: fsynced under writer.lock before it returns
(*Bus) ReadOnly() bool
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

`OpenAt` is `Open` with knobs a second log needs. The status bus itself is
unchanged: its recovery still truncates from the first malformed line, its
checkpoint and drops files are still rename-only, its followers tail the
active file without the lock, and `Publish` keeps its contract.
`Options.KeepCorrupt` (recovery leaves a malformed line in place and
readers skip it; a malformed line after the last parseable frame, or a
file of only malformed lines after a rotation, spends its cursor number on
top of the sealed history; a `Commit` rolled back after a failed write or
fsync records its cursor in `spent.cursor` so the number is never reused;
followers read the active file's new bytes under the writer lock, so they
see a frame only after its `Commit` finished; `(*Bus).Ends` reports the
cursor and the newest parseable frame so a reader stops there instead of
waiting for a spent cursor), `Options.Private` (0600 files, 0700 directory, fsynced
checkpoints and directory writes) and `Options.MaxBytes` (`ErrQuota`) are
taken only by the comms ledger. `Commit` is the
synchronous primitive for a record whose loss a consumer could not detect
(the comms ledger, docs/comms.md): it appends and fsyncs under the
cross-process lock and returns the cursor it was assigned; `Publish` keeps
its never-blocks, may-drop contract. `Options.RetentionDays` removes a
sealed segment once its mtime is older than the window, independently of
the segment-count bound. `Options.ReadOnly` opens a follower: no writer
goroutine, no tail repair, `Commit` returns `ErrReadOnly`, `Publish` counts
a drop; the directory must already exist (`ErrNoBus` otherwise).

## CLI

| Command | Output |
|---|---|
| `agent-deck events follow --json [--after <cursor>] [--kind <prefix,...>] [--session <id>] [--bus events\|comms]` | NDJSON frames, oldest first, streams live until killed. `--jsonl` and `--since` are aliases of `--json` and `--after`; `agent-deck remote <name> events follow --jsonl --since <cursor>` forwards the same stream from a registered remote (docs/remote-recall.md). `--kind session` matches `session.*`; `--kind macapp.` matches the namespace; filters never change cursors. `--bus comms` follows the comms ledger (read-only; docs/comms.md). |
| `agent-deck events stats --json [--bus events\|comms]` | `{enabled, dir, cursor, published, written, synced, dropped, queue_len, queue_cap, kinds: {kind: retained count}}`. |
| `agent-deck events publish --kind macapp.<name> [--session <id>] [--data <json> \| --data-file <path\|->] [--json]` | Publishes one frame and waits (≤ 2 s) until it is committed; prints `{ok, kind, session_id, cursor, profile}`. Only the `macapp.*` namespace, only with `[macapp] plugins = true` (exit 2 otherwise). `--session` defaults to `$AGENTDECK_INSTANCE_ID`. |

`cursor` and `dropped` reflect the profile across processes. `published`,
`written`, `synced` and queue occupancy describe the process running the
command.

Registered as a plain CLI command on this independent slice-4 branch.
The slice-1 registry bundle and this branch now share `origin/main` at
`3b41e36d`, and both bundles verify. Slice 1 remains a separate branch; its
registry is absent from this branch. Integration belongs to the later branch
that combines the two slices.

## Claude statusline metadata

`usage ingest claude` and the installed `usage statusline-wrap` publish
`usage.statusline` without the Mac app plugins gate. The frame's `session_id`
is the owning agent-deck session ID when the native Claude ID resolves uniquely,
or an empty string when it cannot be resolved. The data object contains only
`claude_session_id`, `captured_at` (UTC RFC3339 with milliseconds), `model`
(`id`, `display_name`), `cwd`, `context_window` (`used_percentage`,
`context_window_size`, `total_input_tokens`, `total_output_tokens`) and
`rate_limits` (`five_hour`, `seven_day`, each with `used_percentage` and epoch
seconds `resets_at`). Unreported objects and numeric fields are null. Two
string fields appear only when known: `account`, the configured Claude account
slot whose config dir the status line ran under (`$CLAUDE_CONFIG_DIR`, else
`~/.claude`), and `permission_mode`, the payload's `permission_mode` or else the
newest `permissionMode` in the last 256 KiB of the transcript. Prompt text,
transcript paths, cost data and additional payload keys are not retained.

Use `usage statusline --session <id|title> --json` to read the current native
conversation's last record. Missing or ambiguous records return
`{"error":"no statusline record"}` with exit 1. Records are replaced per native
ID; a known session retains only its latest native conversation. Registry
removal prunes linked records. At most 128 unlinked records remain per
profile. An unlinked record is kept in the feed profile when that profile
already has a store, otherwise in the configured default profile when it has
one; if neither exists, only the quota cache is written and no event is
published, so a statusline update never creates a profile. The event log keeps
its existing bounded retention.

`events follow --json --kind usage.statusline` streams these records from the
host that ran the ingest. Account-named hook feeds keep their original quota
cache while statusline events go to the owning session's profile bus. The
wrapper is not installed when `[claude] statusline_feed = false`.
