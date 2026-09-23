# Daemon protocol (`agent-deck daemon`)

Slice 5 of the one-core plan. `agent-deck daemon serve` exposes the same
command registry the CLI runs (`docs/core-registry.md`) and the event bus
(`docs/events.md`) on a Unix socket. It is an additional executor, not a
second store: every call runs the same `internal/core` Def and the same
`internal/session` functions the CLI would.

## Commands

| Command | Does | Exit |
|---|---|---|
| `agent-deck daemon serve` | Runs in the foreground until SIGINT/SIGTERM or `daemon stop` | 1 if another daemon owns the profile |
| `agent-deck daemon status [--json]` | `running`, `stale` (socket left by a dead owner) or `absent` | 0 only when running |
| `agent-deck daemon stop` | Sends `shutdown`, waits up to 5s | 0, also when nothing runs |

`[core] daemon = false` (the default) keeps the CLI in direct mode: it never
dials the socket. With `true`, a `--json=envelope` request of a registry
command goes to the daemon when one answers the dial and runs in process
otherwise; text and legacy `--json` output always run in process. A call that
was sent is never retried in process.

## Socket, lock, peer

| Item | Rule |
|---|---|
| Directory | `<data dir>/runtime/profiles/<profile>/` (`agentpaths.ProfileRuntimeDir`), mode 0700 |
| Socket | `daemon.sock`, mode 0600 |
| Owner lock | `daemon.lock`: exclusive `flock` plus the owner pid. A second `serve` fails with `daemon already running (pid N)` and touches nothing |
| Mutation lock | `mutation.lock`: an exclusive per-profile `flock` shared by daemon calls and direct registry CLI mutations. It covers load, decision, external effects and save |
| Stale socket | After a SIGKILL, the next `serve` removes the socket only if the recorded pid is dead and no listener answers a hello. A live pid, responsive socket, missing pid or non-socket file is never replaced |
| Peer | Only a Unix socket peer whose uid equals the daemon's (`SO_PEERCRED` on Linux, `LOCAL_PEERCRED` on macOS). Others get `PEER_REJECTED` before any hello |

## Frames

One JSON object per line (NDJSON), at most 1 MiB per line. The first frame on
a connection is the server's `hello`, which carries a fresh random 32-hex
`token`. Every client frame must carry `v`, `type`, `id` and that `token`;
the server echoes `id` on its reply.

The server allows up to 64 concurrent clients. A partial client frame must
complete within 2 seconds, including the newline; an idle subscription has
60 seconds without an event or client frame before it is closed. Each event
resets that deadline. Writes are
bounded to 2 seconds. The client waits 2 seconds for control replies and 8
seconds for ordinary command replies. `session restart --all` has a five-minute
reply deadline because its paced boot sweep can take longer than eight seconds.
The server's seven-second request context bounds waiting for the mutation lock;
the boot executor does not use it to interrupt a sweep. A hello-only peer cannot hold a CLI call
indefinitely. A timed-out call is not retried in process because it may have
already executed.

| Client `type` | Extra fields | Server reply |
|---|---|---|
| `call` | `cmd` (command id), `input` (the command's input object) | `result` with `envelope` |
| `catalog` | | `catalog` with `commands: [{id, cli, summary, class, remote}]` |
| `status` | | `status` with `status: {pid, profile, socket, version, started_at, calls, connections}` |
| `subscribe` | `after` (cursor, 0 = retained log start) | `subscribed`, then one `event` frame per bus frame |
| `shutdown` | | `status`, then the daemon stops |

- `envelope` is the same response envelope `--json=envelope` prints
  (`{schema, request_id, ok, data, warnings, revision, error?}`), with
  `request_id` set to the frame `id`. `input` is decoded strictly (unknown
  command-input fields are `INVALID_INPUT`); an empty `profile` is the daemon's profile,
  another profile is `INVALID_INPUT`.
- Unknown or wrong-type client-frame fields, a second JSON value on the
  line, and an empty or absent `id` are `BAD_FRAME`. The `input` value must
  be one JSON object when present; an omitted `input` is `{}`. Live status cost
  counters remain available through the direct CLI's `--stats` diagnostic;
  they are omitted from canonical envelopes because elapsed time and
  process-local tmux call counts vary between executors.
- `event` carries `event`: the bus frame as the exact canonical JSON line
  `events follow --json` prints. Resume after a drop by subscribing again
  with the last `cursor` seen: no frame is lost or repeated. One
  subscription per connection; use a second connection for calls.
- Mutating commands share the profile's cross-process lock with direct CLI
  registry commands; queries run concurrently.

## Error codes

Protocol errors arrive as `{"type":"error","id":…,"error":{"code","message"}}`.
Command failures are not protocol errors: they are envelopes with
`ok:false` and a core code (`NOT_FOUND`, `UNKNOWN_COMMAND`, `INVALID_INPUT`,
…, see `docs/core-registry.md`), plus `REMOTE_DENIED` for a Def marked
local-only.

| Code | When | Connection |
|---|---|---|
| `PEER_REJECTED` | peer uid is not the owner, or not a Unix socket | closed |
| `AUTH_FAILED` | frame without the connection's token | closed |
| `BAD_FRAME` | line is not JSON | closed |
| `FRAME_TOO_LARGE` | line over 1 MiB | closed |
| `READ_TIMEOUT` | partial or idle client frame exceeded its read deadline | closed |
| `SERVER_BUSY` | 64 clients are already connected | closed |
| `UNSUPPORTED_VERSION` | `v` is not 1 | kept |
| `UNKNOWN_TYPE` | unknown `type` | kept |
| `ALREADY_SUBSCRIBED` | second `subscribe` on one connection | kept |
| `EVENTS_UNAVAILABLE` | the bus is disabled (`AGENTDECK_EVENTS_BUS=0`) | kept |
| `CURSOR_TOO_OLD` | `after` predates the retained log; resync from 0 | kept |

## Versioning

`v` is the frame layout version, 1 today. A new frame type or an added field
keeps `v`; clients ignore fields they do not know. Removing or changing the
meaning of a field bumps `v`, and a daemon answers a frame with another `v`
with `UNSUPPORTED_VERSION` instead of guessing. Command payloads are
versioned separately by the envelope `schema` (`agent-deck/<id>/v1`).

## TCP later

The framing is transport-agnostic (`frameConn` reads and writes any byte
stream), so a phone client over Tailscale or an ssh forward gets the same
frames. The server admits only Unix socket peers today: the uid check is the
authentication and the per-connection token binds frames to the admitted
connection. A TCP listener needs its own admission step (a pre-shared key or
the ssh forward itself) in front of the same `hello`; it is not part of this
slice.
