# Core surface for the Mac app

Everything the Mac app (and any other client) needs from agent-deck, as CLI
commands with `--json`. All of it is additive: nothing here changes the TUI,
and anything that could change existing behaviour is off by default. The
acceptance list is the conductor's `macapp-core-needs.md`; this page is the
reference. Nothing here reads or writes another session's files except the
read-only transcript and pane reads named below.

| Need | Command | Gate | Reference |
| --- | --- | --- | --- |
| Conversation rows for live and busy sessions | `recall timeline <session> --json [--since c] [--limit N] [--tail N]`; older pages with `--before <before_cursor> --limit N` | `[recall] enabled` | docs/recall-timeline.md, "Rows" |
| Live rows, status strip, send states | `recall follow <session> --after <cursor\|end> --jsonl [--status]` | `[recall] enabled` | same |
| Status transitions without polling | `events follow --json --kind session.status,session.turn` | `[macapp] status_events` (status owners: TUI, notify daemon) | docs/events.md |
| Transcript growth frames | `session.transcript` on the bus | `[macapp] transcript_events` (notify daemon) | docs/events.md |
| Browser reports | `open <file\|url> [--session <id\|title>]`, `file bundle <dir\|file> --session <id>` | built-in | docs/macapp-open.md |
| Plugin frames | `events publish --kind macapp.<name> --session <id> --data-file -` | `[macapp] plugins` | docs/events.md |
| Send that is never silently lost | `session send <id> --message-file - --json --queue`, `session send-status <send-id> --json` | none | below |
| Send that never types into a menu | `session send <id> … --require-input-prompt` (probe `session send --help`) | none | below |
| Release or cancel a queued send | `session queue list <session> --json`, `session queue release <id> --json`, `session queue cancel <id> --json` | none | below |
| Images | `session send <id> … --image <path>` | none | below |
| Image staging (local or remote) | `session image-upload <id> --name <uuid>.<ext> --json` (bytes on stdin) | none | below |
| Codex identity | `session show <id> --json` → `transcript_path`, `transcript_ids` | none | below |
| Harness facts | `harness list --json`, `harness status <name> --json` | none | below |
| Usage limits | `limits --json` | `[macapp] plugins` | below |
| Preferences | `config get <key> --json`, `config set <key> <value> --json`, `config schema --json` | none | below |
| Favourites | `session set <id> favorite true\|false`; `favorite` in `list --json` / `session show --json` | none | below |
| Transcript location in listings | `transcript_path`, `claude_session_id` / `codex_session_id` in `list --json` and `remote sessions <name> --json` | none | docs/remote-recall.md |
| Remote conversations, status stream, send states | `remote <name> recall timeline\|follow …`, `remote <name> events follow --jsonl [--since c]`, `remote <name> session send-status <send-id> --json` | the owner host's own gates | docs/remote-recall.md |

## Queued send

```
agent-deck session send <id> --message-file - --json
{"send_id":"01K5…","state":"queued","verdict":"queued","reason":"","target_status":"unknown","session_id":"…",
 "message":"…","created_at":"…","updated_at":"…","deadline":"…","attempts":0}
agent-deck session send-status 01K5… --json
{… "state":"landed","verdict":"delivered","landed_row_id":"<uuid or queue:<ts>:<hash>>","landed_at":"…","attempts":1}
```

The record is written under `<profile dir>/sendqueue/<send_id>.json` and a
detached worker (`session send-worker`, one per target, serialised by a lock
file) takes it from there:

1. Claude accepts input while busy, so its worker delivers immediately to a
   running turn. Codex, Pi, shell and unknown harnesses wait for idle. A target that is not running fails
   at once with `reason: "target not running"` (exit 1).
2. When delivery can start the record moves to `typing` (attempts, sent_at
   and the transcript offset are written to disk first), and a `session
   send` child types and submits it through the composer guard and bounded
   verification. The child reads the message
   from `<send_id>.message` and writes its JSON result to
   `<send_id>.result`, so it finishes even if the worker dies.
3. The child's result decides the next state: `submitted` when the harness
   confirmed it, `typed` otherwise. Only refusals that guarantee nothing was
   typed (`target_busy`, `composer_blocked`, Codex `acceptance_refused`)
   go back to `queued` and are retried. Anything else (`menu_open`, a
   readiness timeout, `no_evidence`, a crash) may have typed the text: the
   record stays `typed` with `reason: "outcome unknown (…)"` and the
   transcript decides. It is never `failed`, because a client that resends
   on `failed` would double the message.
4. A separate watcher checks the native transcript from the byte offset before the
   send until the text lands: state `landed` with `landed_row_id`, the same
   id `recall timeline`/`follow` give that row. Only delivery counts: a user
   row (a command row for a `/name` message), or a Claude queued message
   once it is absorbed into the turn. An enqueue alone is not delivery, and
   a row stamped before the send is an earlier message, never this one. The
   typing worker can submit later queued sends while the first message is
   still waiting for its turn. The watcher is locked per send and can be
   restarted after a process exit without retyping.
5. The retry budget is 30 minutes (`deadline`), then `failed` with a reason
   (only ever when nothing was typed). A refusal before typing
   (`composer_blocked`, `target_busy`) is retried after a wait that doubles
   from the worker poll up to 1 minute (`AGENTDECK_SEND_RETRY_BACKOFF_MAX`).
   A failed send is reported to the sender: `sender` is on every
   `session.send` frame, and a sender session gets a `send_failed` record
   in its inbox. The health journal gets one record per attempt and one
   final record (`final: true`) per send. A send not seen in the transcript
   within 2 minutes, or sent to a harness with no transcript reader (not
   Claude or Codex), keeps its state, gets a reason and `settled: true`.

Delivery is at most once. A worker that finds a `typing` record (its
predecessor died mid-send) waits for the child, takes its result file, and
without one settles the record `typed` with an unknown outcome. No worker
ever types a record that has left `queued`.

Send ids sort in send order, also for callers in the same millisecond.
Each state or verdict change is also a `session.send` bus frame and, in a running
`recall follow` for that session, a `{"frame":"delivery","send_id","state","verdict"}`
line. `send-status`, `events follow` and `recall follow` restart the worker
for a send that is still in flight (after a reboot, say). Finished records
are pruned after 7 days. Exit codes: 0 queued or sent, 1 delivery failed,
2 usage error, unknown session, unknown send id or unsupported image.

## Queue control

```
agent-deck session queue list <session> --json
{"session_id":"…","queue":[{"id":"01K5…","text_preview":"…","enqueued_at":"…","state":"queued"}]}
agent-deck session queue release 01K5… --json
{"id":"01K5…","outcome":"delivered", … the send-status record, with "delivery_evidence" …}
agent-deck session queue cancel 01K5… --json
{"id":"01K5…","outcome":"cancelled","state":"cancelled", …}
```

`session show <id> --json` carries the same `queue` array. Entries are the
durable queued sends of the session, oldest first, including finished ones
until they are pruned; `text_preview` is the message with whitespace
collapsed, at most 120 characters.

`release` and `cancel` never type anything themselves. They file a request
next to the record (`<send_id>.control`) and the target's worker answers it
at the typing boundary, under the same per-target lock it holds for every
delivery, also while it waits on an older entry or sits in a retry backoff.
The call waits for that answer (3 minutes at most) and exits 0 with one of
these `outcome` values:

- `delivered`: release typed it and the harness confirmed submission (or
  the row landed).
- `unconfirmed`: release typed it but submission was not confirmed; the
  transcript watch decides, and it is never typed again.
- `refused`: release typed nothing (`reason` says why: the target is busy
  and does not take input while busy, it is not running, or the composer
  refused before typing). The entry stays queued and the worker goes on as
  before.
- `cancelled`: removed before any typing began. The record is final
  (`state: "cancelled"`, attempts unchanged) and is never typed.
- `already_sent`: the entry had left the queue and its delivering child
  left evidence (`delivery_evidence`, the child's own `session send --json`
  result). A call that meets an entry being typed waits for that evidence.
- `not_found`: no pending entry with that id (unknown, or already failed
  with nothing typed; `state` and `reason` say which).
- `unknown`: the outcome could not be proven (a typing entry without child
  evidence, or no answer before the timeout).

Release goes through the same `session send` child the worker uses, always
as a guarded send (`--require-input-prompt`, see "Guarded send") without
the readiness wait; it skips the queue's own wait and backoff. A Codex,
Pi, shell or unknown target that is busy is refused untyped rather than
typed into, and so is a target showing a menu or no input prompt. A
released entry stays guarded on any later attempt.
Each answer is also a `queue.released` or `queue.cancelled` bus frame, and a
cancellation is a `session.send` frame with `state: "cancelled"`; the health
journal records it with `outcome: "cancelled"`.

`remote <name> session queue list|release|cancel` forwards to the remote's
own queue. A remote whose agent-deck predates the command answers with one
refusal and nothing is run there: `session queue is unsupported on this
remote "<name>" …` (under `--json`, `{"error","remote","remote_version"}`),
exit 1.

## Images

`--image <path>` (repeatable) copies the file to
`<session working dir>/.agentdeck-images/<ms>-<n>-<name>` (spaces in the
name become dashes; the directory gets a `.gitignore` of `*`) and appends
`@<copy>` to the message for Claude Code and Gemini CLI, which read `@path`
from the composer. A queued record's `images` lists the copies. Codex takes images only at launch (`codex -i`), so a
running Codex session exits 2 with `images not supported for codex in a
running session`; other harnesses exit 2 too. Only png, jpg, jpeg, gif,
webp and pdf files are accepted. `harness list` reports `images: true|false`.

### Staging an attachment on the owning host

```
agent-deck session image-upload <id|title> --name <uuid>.png --json < image.png
agent-deck remote <host> session image-upload <id|title> --name <uuid>.pdf --json < document.pdf
{"path":"/absolute/path/on/the/owning/host","bytes":123}
```

The bytes arrive on stdin (over the existing SSH route for a remote) and
are written to `macapp-uploads/<session id>/<name>` beside the profile's
state database, resolved by the core itself, never from the SSH user's
HOME. Pass the returned path to `session send <id> --image <path>` on the
same host and profile. Clients must use the returned path rather than
build it.

- Names: a plain file name ending in png, jpg, gif, webp or pdf; no
  separators, no traversal, no leading dot, at most 200 bytes.
- Size: 1 byte to 20 MiB; more is refused with an error and nothing is kept.
- Safety: the directory is 0700 and the file 0600. The file is written to
  a temporary name and published with an exclusive atomic rename, so an
  existing target (including a symlink) is refused and never overwritten;
  a retry with the same name fails. Symlinked upload directories are refused.
- Cleanup: removing the session (CLI, TUI or web) removes its folder.
  Opening writable storage prunes uploads older than 7 days, once per
  profile per process.
- Exit codes: 0 with the receipt, 1 with `Error: <reason>` on stderr.
- Probe: `session image-upload --help` exits 0 and prints the Go flag
  usage (`Usage of session image-upload:` with `-name` and `-json`) on
  stdout, with nothing on stderr. A core without the command answers
  `unknown session command`.

## Guarded send

`session send <id> … --require-input-prompt` refuses rather than type into a
harness menu (an AskUserQuestion picker, a permission dialog, a trust dialog,
a Codex picker). It is advertised in `session send --help`; a client probes
for it there and sends without it on an older core.

Under the per-target send lock the core already holds, the pane is read
immediately before every tmux keystroke batch: the paste or each fallback
chunk, every Enter (including retry Enters) and every vim insert key. The
send always uses the tmux transport, never the messaging socket, because a
socket write has no keystroke boundary to check at.

- Before anything is typed, an open menu refuses with `delivery: "menu_open"`
  and a missing or unreadable input prompt with `delivery:
  "composer_blocked"`. The error text says `no keys typed`; exit 1.
- Once the body is typed, only a visible menu withholds Enter. A prompt
  redraw, a wrapped input box or a pane read error does not, so the text is
  never left typed and unsent on a false alarm. If a real menu opens between
  the paste and Enter, the result is `delivery: "typed_not_submitted"` with
  the number of typed batches and the pane tail in the error text. Nothing is
  retyped.
- A menu needs picker evidence: a picker instruction ("navigate", "Enter to
  select", "Enter to confirm"), or other menu chrome ("Allow once", "Esc to
  cancel", …) beside at least two selectable choices. Text inside the live
  input box, a delivered message that quotes those words and question prose
  without choices are not a menu. The same rule drives `session show`'s
  `interactive-menu` substate and the plain send's `menu_open` verdict.
- `--draft`, `--no-wait`, `--wait` and the queue keep the guard. A queued
  send records `require_input_prompt: true`, its worker passes the flag to
  the child that types it, and a refusal with `no keys typed` goes back to
  `queued` for a later attempt.
- `remote <name> session send … --require-input-prompt` first reads the
  remote's `session send --help`; a remote that does not advertise the flag
  is refused locally with `--require-input-prompt unsupported on this remote`
  and nothing is sent.

## Codex identity

Codex re-creates its rollout after the trust prompt and a sub-agent writes a
rollout of its own, so the stored `codex_session_id` can name no file. The
live rollout is resolved read-only on every call, and only ever to this
session's own thread:

1. the stored id's rollout, unless it is a sub-agent thread;
2. else the thread the pane's own Codex process holds open;
3. else the one user-thread rollout (`thread_source` user, no parent
   thread, not `codex exec`) in the session's working directory, written
   since the session was created and not bound to another deck session,
   whose structured id fields reference the stored id. Two or more are
   ambiguous and resolve to nothing.

A user thread that is merely the only one in the directory is never bound:
it may be the user's own Codex. A fresh session has no `transcript_path`
until its own rollout exists.

`session show --json` adds `transcript_path` (Claude JSONL or that rollout)
and `transcript_ids` (the live rollout's thread and the stored id), both
omitted when unknown. `recall follow` answers `resync_required` with
`reason: source_moved` when the live rollout changes.

A Codex `session send` keeps the exact-acceptance guard on every path,
plain, queued and `--json --wait`: an unresolved earlier submission, an
identity owned by another session, a held acceptance lock or a remote
rollout refuse with exit 1 and `delivery: "acceptance_refused"` (nothing was
typed). For the first message after the trust prompt, when the identity is
provably unavailable (none yet, or a stored id with no rollout),
`--codex-composer-fallback` sends through the verified composer path
instead of refusing; it is never used for `--json --wait`, and the send
queue never passes it.

## Harnesses

`harness list --json` returns one object per harness (claude, codex, gemini,
opencode, pi, hermes):

```json
{ "name": "codex", "display_name": "Codex", "binary": "codex",
  "install_command": "npm install -g @openai/codex", "login_command": "codex login",
  "docs_url": "https://github.com/openai/codex", "images": false,
  "installed": true, "path": "/opt/homebrew/bin/codex", "version": "0.155.1",
  "logged_in": true, "accounts": [{"name": "work", "config_dir": "…", "logged_in": true}],
  "hooks_installed": true, "last_used": "2026-09-23T08:00:33Z", "sessions": 3,
  "limit_reached": false, "state": "ready" }
```

`state` is `not_installed`, `not_logged_in`, `limit_reached` or `ready`, in
that order of precedence. `logged_in` reads marker files only, never a
secret: Claude `.claude.json` `oauthAccount` (or `.credentials.json`) in each
config dir, Codex `auth.json` in each CODEX_HOME, Gemini `oauth_creds.json`,
OpenCode and Pi `auth.json`, Hermes `.env`; an API key in the environment
also counts. `version` comes from `<binary> --version` (3 s timeout). The
install/login table is `internal/harness/table.go`, versioned with the
release; override any entry in config.toml:

```toml
[harnesses.codex]
install_command = "brew install codex"
login_command = "codex login --device-auth"
```

`harness status <name> --json` is the same object for one harness (exit 2 for
an unknown name).

## Limits

`limits --json` (needs `[macapp] plugins = true`; `remote <name> limits
--json` forwards it read-only and returns the remote's own accounts; a remote
that predates `limits` exits 1 with `unsupported remote command "limits" on
remote "NAME"; update its agent-deck`, as `{error, remote, remote_version}`
under `--json`):

```json
{ "accounts": [
  { "harness": "claude", "name": "personal", "windows": [
      {"window": "5h", "used_pct": 40, "resets_at": "2026-09-23T12:10:00Z"},
      {"window": "7d", "used_pct": 20, "resets_at": "2026-09-27T09:00:00Z"}],
    "source": "quota cache (statusLine ingester)", "updated_at": "…", "stale": false },
  { "harness": "codex", "name": "default", "windows": [
      {"window": "weekly", "used_pct": 13, "resets_at": "2026-09-29T16:40:00Z"}],
    "source": "last token_count in ~/.codex/sessions", "updated_at": "…", "stale": false } ] }
```

Claude figures come per account slot from the quota cache that
`agent-deck hooks install` wires (the statusLine ingester), with `resets_at`
as Claude reported it. Codex figures come per CODEX_HOME from the newest
`token_count` frame with `rate_limits` in its five most recently written
rollouts: the same numbers the Codex footer shows. `error` explains an
account with no windows (`no feed: run agent-deck hooks install`, `no data
yet`, `no rate-limit frame in recent rollouts`); `stale` marks data older
than 30 minutes.

## Status line

`usage statusline --session <id|title> --json` prints the last Claude
status-line record for that session (exit 0), or
`{"error":"no statusline record"}` with exit 1:

```json
{ "claude_session_id": "…", "captured_at": "2026-10-01T12:00:00.000Z",
  "model": {"id": "claude-opus-4-6", "display_name": "Opus 4.6"}, "cwd": "/project",
  "context_window": {"used_percentage": 37.5, "context_window_size": 200000,
    "total_input_tokens": 75000, "total_output_tokens": 1234},
  "rate_limits": {"five_hour": {"used_percentage": 23.5, "resets_at": 1790860000},
    "seven_day": {"used_percentage": 48, "resets_at": 1791400000}},
  "account": "work", "permission_mode": "acceptEdits" }
```

`account` (the configured Claude account slot the status line ran under) and
`permission_mode` (from the payload, else the transcript's newest
`permissionMode`) are omitted when unknown; never guessed.

Each ingest also publishes the same record as a built-in `usage.statusline`
event (no `[macapp] plugins` needed) whose `session_id` is the agent-deck
session. `hooks install` wires the feed for the active Claude config and every
account slot (`usage statusline-wrap`, opt out with `[claude] statusline_feed
= false`). See [events.md](events.md#claude-statusline-metadata).

## Config

`config schema --json` lists every settable key: `key`, `section`, `label`,
`help`, `type` (bool, int, float, string, enum, list), `values`, `default`,
`restart_required`, `aliases`. The keys are one per TUI Settings row that
edits config.toml (theme, default tool, Claude/Gemini/Codex/Hermes options,
updates, logs, global search, preview, sync title, maintenance, system
stats, display, tool picker, interface) plus `recall.enabled`,
`macapp.plugins`, `macapp.transcript_events`, `macapp.status_events` and
`core.daemon`. The TUI's
visible-tools editor writes per-tool entries and is not a single key.

`config get <key> --json` → `{key, value, type, default, restart_required,
path}` (the default when unset). `config set <key> <value> --json` validates
the value, writes it with the same writer the TUI Settings panel uses
(`SaveUserConfig`, which keeps every other section and a `.bak`), and prints
the value read back. Lists are comma-separated (`system_stats.show cpu,load`).
`ui.theme` is an alias of `theme`. Exit 2 for an unknown key or an invalid
value.

## Favourites

`session set <id> favorite true|false` sets a favourite flag, stored in the
session's tool_data extras (no schema change). `list --json` and
`session show --json` include `"favorite": true` for a favourite and omit the
key otherwise. A remote session is set with `remote <host> session set <id>
favorite true` (the remote must run this version). The TUI star and
Favourites section are not part of this change: the TUI is untouched here,
so that parity item remains open.
