# Core surface for the Mac app

Everything the Mac app (and any other client) needs from agent-deck, as CLI
commands with `--json`. All of it is additive: nothing here changes the TUI,
and anything that could change existing behaviour is off by default. The
acceptance list is the conductor's `macapp-core-needs.md`; this page is the
reference. Nothing here reads or writes another session's files except the
read-only transcript and pane reads named below.

| Need | Command | Gate | Reference |
| --- | --- | --- | --- |
| Conversation rows for live and busy sessions | `recall timeline <session> --json [--since c] [--limit N] [--tail N]` | `[recall] enabled` | docs/recall-timeline.md, "Rows" |
| Live rows, status strip, send states | `recall follow <session> --after <cursor\|end> --jsonl [--status]` | `[recall] enabled` | same |
| Status transitions without polling | `events follow --json --kind session.status,session.turn` | `[macapp] status_events` (status owners: TUI, notify daemon) | docs/events.md |
| Transcript growth frames | `session.transcript` on the bus | `[macapp] transcript_events` (notify daemon) | docs/events.md |
| Plugin frames | `events publish --kind macapp.<name> --session <id> --data-file -` | `[macapp] plugins` | docs/events.md |
| Send that is never silently lost | `session send <id> --message-file - --json --queue`, `session send-status <send-id> --json` | none | below |
| Images | `session send <id> … --image <path>` | none | below |
| Codex identity | `session show <id> --json` → `transcript_path`, `transcript_ids` | none | below |
| Harness facts | `harness list --json`, `harness status <name> --json` | none | below |
| Usage limits | `limits --json` | `[macapp] plugins` | below |
| Preferences | `config get <key> --json`, `config set <key> <value> --json`, `config schema --json` | none | below |
| Favourites | `session set <id> favorite true\|false`; `favorite` in `list --json` / `session show --json` | none | below |

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
   (only ever when nothing was typed). A send not seen in the transcript
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

## Images

`--image <path>` (repeatable) copies the file to
`<session working dir>/.agentdeck-images/<ms>-<n>-<name>` (spaces in the
name become dashes; the directory gets a `.gitignore` of `*`) and appends
`@<copy>` to the message for Claude Code and Gemini CLI, which read `@path`
from the composer. A queued record's `images` lists the copies. Codex takes images only at launch (`codex -i`), so a
running Codex session exits 2 with `images not supported for codex in a
running session`; other harnesses exit 2 too. Only png, jpg, jpeg, gif and
webp files are accepted. `harness list` reports `images: true|false`.

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

`limits --json` (needs `[macapp] plugins = true`):

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
