# Open a page in AgentDeck

`agent-deck open report.html` shows an HTML report in the Mac app Browser panel
for `$AGENTDECK_INSTANCE_ID`. Use `--session <id|title>` to select another session
or when running outside a session. IDs match exactly; duplicate titles are refused.
The selected profile applies to both the session lookup and the event bus.
Relative paths use the CLI working directory. A directory selects `index.html`.
Files must exist and be regular files. URLs must use HTTP(S), have a host, and
contain no credentials. Other schemes and malformed URLs are refused.

The command works on local hosts and remote hosts without a second transport or
log. It commits `macapp.open` to the existing profile bus, followed by a wait for
an acknowledgment within three seconds of publishing. App followers subscribe
with `events follow --kind macapp.` on each host they watch.

```json
{"kind":"macapp.open","session_id":"session-id","event_id":"unique-event-id","data":{"host":"source-hostname","path":"/absolute/report/index.html","requested_at":"2026-10-01T10:00:00.123456789Z"}}
```

The standard frame also supplies `cursor` and `ts`. For a URL request,
`url` replaces `path`. `host` is the source's OS hostname; the follower connection
identifies the SSH host to read from. `requested_at` is UTC RFC 3339 with nanoseconds.
The app must deduplicate by top-level `event_id`, including URL-scheme deliveries, and
acknowledge only after the requested page is displayed:

```sh
agent-deck events publish --kind macapp.open.ack --session session-id \
  --data '{"open_event_id":"unique-event-id","opened":true}'
```

Both `macapp.open` and `macapp.open.ack` are available without `[macapp] plugins = true`.
Other generic `events publish` kinds still require that setting. The app must
publish its ack on the same host and profile as the request. Only the exact
session and open event ID match. Old, unrelated and empty acknowledgments cannot
produce success.

A matching ack with `opened: true` and no error prints `Opened in AgentDeck (session <title>)`, exit 0. Otherwise,
the CLI prints `Queued for AgentDeck: it opens when the app is connected`, exit 0.
A correlated rejection (`opened: false`, missing `opened`, or a nonempty `error`)
exits nonzero with an explicit app error; it cannot claim opened or queued.
If the bus is disabled or cannot commit the request, the command exits nonzero
and explicitly says it was not queued.

After the acknowledgment deadline, an unacknowledged request on a local Mac
also invokes `open agentdeck://open?...` to launch the app. The query contains
`event=<event_id>`, `session=<session_id>`, the payload fields and effective
`profile`. It does not use `request_id`. The event ID is assigned by the bus
before enqueue and is identical in the committed frame and launch URL.
No acknowledgment is claimed for that launch. No URL
scheme is invoked on Linux or in an SSH environment (`SSH_CONNECTION`,
`SSH_CLIENT`, or `SSH_TTY`). The fallback is bounded to one additional second.
There is no separate connected-app registry: absence of the correlated ack
triggers the launch fallback. A connected app that handles the request in time
never receives a duplicate URL-scheme delivery.

## Remote page folders

```sh
agent-deck file bundle /absolute/report --session session-id > report.tar
```

This writes an uncompressed tar to stdout. A directory argument exports that
directory; a file exports its parent directory, preserving relative asset paths.
The page's basename is the cache entry point for a file argument. For a directory
opened by `open`, the entry point is `index.html`. The archive names are relative
and never contain a leading slash or `..` traversal. The app must extract into an
owned cache directory and grant the WebView read access only to that directory.

The entire tar, including headers, padding and end markers, is capped at 20 MiB.
The archive is buffered within that limit before stdout is written, so validation
failures emit no partial tar. Every observed symlink, including links inside the
folder, and every special file is refused. Reads are anchored with `os.OpenRoot`
to prevent concurrent symlink replacements escaping the folder. Regular-file
identity is checked again after open. Files can still change their contents during
export; this is a read-only transfer, not a filesystem snapshot.

`--session` is required routing context, not an authorization boundary. Export
runs with the SSH user's normal file permissions and does not open the session
catalog, create files, update metadata, or emit telemetry. It also works when the
catalog is unavailable. No temporary archive is written on the source host.
Use a dedicated report folder: file export includes all sibling assets, subject
to the same size and file-type restrictions.

## Remote hosts

`open` and `file bundle` run on whichever host owns the session, against that
host's own core and profile. The request frame is committed to that host's
bus, so an app that follows the host (for example over SSH with
`agent-deck events follow --json --kind macapp.`) receives it and publishes
its `macapp.open.ack` back on the same host. Forwarding these frames through
`agent-deck remote <name> events follow` is a separate change; until a core
offers it, followers read each host's bus directly. Older cores that lack
`open` or `file bundle` refuse them as unknown commands, which a client can
use to detect the capability.
