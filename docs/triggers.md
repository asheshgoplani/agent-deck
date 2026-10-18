# Triggers: what wakes each session

Sessions are woken by many things: launchd jobs, systemd user units, cron
scripts, watchers, the conductor heartbeat, the Telegram, Slack and Discord
bridges, and the web command center. `agent-deck triggers` lists them in
one place: which sender wakes which session, how often, what it said last,
and whether its target still exists.

A trigger is found in two ways:

- **Push.** Every `session send` records a fire for its sender. A sender names
  itself with `--source <name>` (or the `AGENTDECK_SOURCE` environment
  variable). Without one, the CLI works out the sender itself: the sending
  session (`AGENTDECK_INSTANCE_ID`, unless `--no-tag`), then the launchd job
  label (`XPC_SERVICE_NAME`), the systemd unit (`INVOCATION_ID` plus
  `/proc/self/cgroup`), a cron parent process (named after the script it ran),
  an ssh login (`SSH_CONNECTION`), and finally `cli`. An inferred sender shows
  `"inferred": true`.
- **Scan.** `agent-deck triggers scan` reads `~/Library/LaunchAgents/*.plist`
  and `~/.config/systemd/user/*.{service,timer,path}` and keeps the units whose
  command, or the script it runs (followed one level), calls agent-deck. It
  reads the verb (`send`, `start`, `restart`, `launch`), the target as written,
  the `-p` profile and the schedule. A target held in a shell variable is
  resolved from a plain assignment in the same script; one that only comes
  from the environment stays empty rather than guessed. The scan reads at most
  500 files and 256 KB per script, runs on demand, and `triggers list` reruns
  it when the last scan is more than a day old. It never runs from the TUI's
  render path and never changes anything on the host.

A tagged send from a unit and the scanned unit land on the same row: a bare
`--source nightly-poll` sent from inside a systemd unit or launchd job joins
that unit's id (`systemd:<unit>`, `launchd:<label>`) and keeps the given name
for display.

## Source names

`--source` takes `kind:name` for a known kind, or a bare job name:

| Value | Trigger id | Used by |
| --- | --- | --- |
| `heartbeat:<conductor>` | `heartbeat:<conductor>` | the conductor heartbeat script and the bridge's heartbeat loop |
| `channel:telegram`, `channel:slack`, `channel:discord` | same | the conductor bridge |
| `command-center` | `channel:command-center` | the web command center's ask |
| `watcher:<name>`, `interval:<hook>`, `cron:<name>`, `launchd:<label>`, `systemd:<unit>`, `ssh:<name>` | same | your own senders |
| any other name | `job:<name>` (or the enclosing unit's id, see above) | scripts and jobs |

Session-to-session sends (`session:<id>`) and a person typing at a shell
(`cli`) are recorded too, but they are messages, not triggers: they show only
with `--all`.

## Commands

| Command | What it does |
| --- | --- |
| `agent-deck triggers [list]` | Table of triggers on this host: HOST KIND NAME WAKES EVERY LAST HEALTH NOISE. A dead target is marked `✗`; the footer counts triggers, dead targets and failing ones. |
| `… --session <id\|title>` | "What wakes this session": every trigger whose target resolves to it, or that fired at it in the last 24 h, busiest first. |
| `… --kind <kind>`, `--health <health\|problems>`, `--all` | Filters. `problems` keeps everything not `ok` plus very noisy triggers. |
| `… --host <name\|all>` | A remote's triggers (forwarded as `triggers --json` over SSH), or this host plus every configured remote. |
| `… --json` | The JSON below. |
| `agent-deck triggers scan [--json]` | Rescan the unit directories now and print the list. |
| `agent-deck triggers mute <id> [--for 24h] [--json]` | Mute a trigger at the receiving end (see below). |
| `agent-deck triggers unmute <id> [--json]` | End the mute. |
| `agent-deck triggers run <id> [--json]` | Start a scanned unit once now: `systemctl --user start <unit>.service` or `launchctl kickstart gui/<uid>/<label>`. Only scanned launchd/systemd triggers can run. |
| `agent-deck triggers fires <id> [--limit N] [--json]` | The fire log, newest first: `{"trigger_id", "count", "fires": [{"ts", "target", "session_id", "outcome", "line", "noop"}]}`. |

`agent-deck session send … --source <name>` tags a send. Unknown trigger ids
exit 2 (`NOT_FOUND`), like an unknown session.

### `triggers --json`

```json
{
  "triggers": [
    {
      "id": "systemd:agentdeck-overnight",
      "host": "g14",
      "kind": "systemd",
      "source": "agentdeck-overnight",
      "config_path": "/home/u/.config/systemd/user/agentdeck-overnight.service",
      "verb": "send",
      "target": {"as_written": "overnight-orchestrator", "by": "title", "session_id": "", "title": "", "resolved": false},
      "schedule": "every 5min",
      "origin": "push+scan",
      "inferred": false,
      "last_fire": "2026-10-18T00:40:00Z",
      "last_line": "tick: 0 waiting jobs",
      "last_outcome": "dead-target",
      "fires_24h": 288,
      "noop_share_24h": 1,
      "health": "dead-target",
      "muted": false,
      "paused": false,
      "runnable": true
    }
  ],
  "hosts": {"deckbox": "ok", "g14": "unknown"},
  "count": 1,
  "generated_at": "2026-10-18T09:00:00Z"
}
```

Every key is always present, empty (`""`) when unknown, except `last_fire`
(omitted when the trigger never fired) and `muted_until` (omitted when not
muted); `noop_share_24h` is `null` without fires in the last 24 h. `verb` is
`""` when the scan could not read it, `target.as_written` is `""` when the
target is held in a variable the scan could not resolve. `paused` is always `false`: agent-deck never pauses host jobs. All
fields are additive; readers ignore keys they do not know.

## Health

Each trigger has one health, by the first rule that applies:

1. `dead-target`: the target as written resolves to no session in any profile
   (by exact id, an id prefix of at least 8 characters, or exact title; the
   sender's `-p` profile narrows the search), or a trigger with no written
   target last fired at a session that did not exist.
2. `failing`: its last fire failed (the send errored, or the target was not
   running).
3. `never-fired`: a scanned unit with no recorded fire.
4. `ok`: otherwise (delivered, queued or muted).

`unknown` appears only for a host that could not say: a remote whose
agent-deck is too old for `triggers` (it exits 2 on the unknown command),
answers something that is not the triggers JSON, or is unreachable. Such a host
is listed in `hosts` as `"unknown"` with no rows; it is never guessed.

A unit's own exit status does not count: systemd reports "success" for a
script whose send went to a title that no longer exists. Health comes from the
receiving side.

## Noise

A fire is a no-op when its message is the same text as the trigger's previous
fire (compared by a hash of the whole message without the
`[agent-deck from:…]` envelope; the text itself is never stored). The noise
column is the share of no-op fires in the last 24 h. A trigger at 90 % or more
counts as very noisy and shows under `--health problems`.

## Mute

A mute works at the receiver. While a trigger is muted, its sends are
accepted: `session send` exits 0 and prints success
(`{"success": true, "muted": true, "muted_until": …, "trigger_id": …}` with
`--json`), the fire is logged with outcome `muted`, and nothing is typed into
the pane. The host job keeps running on its schedule; agent-deck never
pauses, stops or disables a launchd job, systemd unit or cron entry. A mute
expires on its own (default 24 h, `--for` to change it), survives a rescan,
and ends early with `triggers unmute`.

## Storage and retention

Triggers live in the profile's `state.db`, in two tables: `triggers` (one row
per sender: kind, source, target as written, schedule, config path, whether it
was pushed and/or scanned, and the mute) and `trigger_fires` (the fire log:
time, target, receiving session id, outcome, first line, text hash, no-op
flag). Fires older than 7 days are dropped, and at most 2000 fires are kept
per trigger; both bounds apply on every insert, so no sweeper is needed. A
rescan drops units that disappeared unless they also fired by push. Every
change bumps the `triggers_rev` metadata key and publishes a
`session.trigger` frame on the event bus (docs/events.md), so the TUI and the
Mac app refresh live.

Recording a fire is best-effort and cheap (a handful of indexed statements on
the local database); a failure is logged and never changes the result or the
exit code of the send.

## Privacy

Only the first non-empty line of a message is stored, after the send
envelope is removed. It passes through the same display sanitizer as remote
data (control characters dropped), token shapes are redacted (GitHub,
`sk-…`, Slack `xox…-`, AWS, Google, bot tokens, `Bearer`/`Basic` credentials,
JWTs, long hex and base64 runs, and `TOKEN=`/`password:`-style assignments
become `[redacted]`), and it is cut to 80 display cells, all before it is
written. The rest of the message is only kept as a short hash for the no-op
rule.

## Remotes

`agent-deck remote <name> triggers --json` forwards the list (with
`--session`, `--kind`, `--health`, `--all`) to a remote; it is read-only, and
`mute`, `unmute`, `run`, `scan` and `--host` are refused before SSH. `triggers
--host <name>` and `--host all` use the same forward and label the remote's rows
with the remote's name.
