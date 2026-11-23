# Recall timeline and follow

Since the Mac app surface (docs/macapp-core.md) both commands answer with
typed **rows** read straight from the native transcript; see "Rows" below.
The slice-6 shape described in this first part stays available with `--v1`.

`agent-deck recall timeline <session> --json --v1` returns `session`, ordered
`turns`, and `through_cursor`. `agent-deck recall follow <session> --after
<through_cursor> --jsonl --v1` streams one `turn` frame per new event. Each frame
has its own cursor. If the source changes before the cursor, follow emits
`resync_required`; call timeline again and resume from its new cursor.

Both commands require `[recall] enabled = true`. The source transcript must
exist on the box where the command runs. Recall cards imported from another
host contain no transcript.

| Harness | Native records mapped to kinds |
| --- | --- |
| Claude Code | `user`/`assistant` text: `message`; `tool_use`: `tool_call`, or `bash`, `edit`, `todo`, `subagent`, `skill`, `permission` by tool name; `tool_result`: `tool_result`; compact boundary/summary: `compaction`; permission mode: `permission`; known metadata: `system` |
| Codex | response messages: `message`; function/custom/local shell calls: `tool_call` or `bash`/`edit`; outputs: `tool_result`; compacted: `compaction`; context, usage and lifecycle: `system` |
| Pi | user/assistant text: `message`; tool call blocks: `tool_call` or named special kind; toolResult: `tool_result`; compaction: `compaction`; metadata: `system` |
| Gemini | user/gemini text: `message`; toolCalls: `tool_call` or named special kind, with `tool_result` when present; info/error: `system` |
| OpenCode | text parts: `message`; tool parts: `tool_call` or named special kind, plus `tool_result` when output exists; reasoning/step parts: `system` |
| Hermes | user/assistant content: `message`; assistant tool_calls: `tool_call` or named special kind; tool rows: `tool_result`; compacted rows: `compaction` |

Known workflow tools are mapped by their native names: Bash, shell and
terminal to `bash`; Edit, MultiEdit, Write and NotebookEdit to `edit`;
TodoWrite, TaskCreate and TaskUpdate to `todo`; Agent and Task to
`subagent`; Skill to `skill`; AskUserQuestion and permission prompts to
`permission`. Unrecognized record and part types become `other` with their
raw JSON payload. Source order is preserved even when timestamps tie or
arrive out of order.

| Field | Meaning |
| --- | --- |
| `seq` | One-based event position within this timeline |
| `role` | Native speaker, normalized to `user`, `assistant`, `tool`, or `system` |
| `kind` | Typed event from the table above, or `other` |
| `timestamp` | Native event time in UTC when present |
| `tool_name` | Native tool name for calls and results when available |
| `text` | Readable message, command, summary, or tool result text |
| `raw` | Native JSON for events the client may need to inspect, always present for `other` |

Follow polls the native source every 250 ms. This keeps new lines independent
of the background index sweep and meets the two second local append target.

## Rows (default, schema `agent-deck.recall.rows/v2`)

```
agent-deck recall timeline <session> --json [--since <cursor> | --before <cursor> --limit N] [--limit N] [--tail N] [--agent <id>] [--kinds a,b] [--skip-empty-body]
agent-deck recall follow   <session> --after <cursor|end> --jsonl [--status]
agent-deck recall timeline --json --transcript <file> --harness claude|codex|pi
agent-deck recall timeline --json [--tail N] [--kinds a,b] [--skip-empty-body] --sessions <id1>,<id2>,...
```

Both need `[recall] enabled = true` (exit 2 otherwise) but never need the
index: `<session>` resolves, without touching recall.db, as a deck session id,
a Claude, Codex or Pi conversation id bound to a deck session, a deck title
or id prefix, or a conversation id whose file exists under any Claude config
dir or Codex home. Only then is the index asked (`#n`, card ids, other
harnesses). The native file is parsed directly, so a session whose source the
sweep deferred answers at once. Claude Code, Codex and Pi are read and
streamed directly; Gemini, OpenCode and Hermes come from the index as v1
turns mapped onto rows (`source: "index"`) and are streamed with `--v1`.

A Pi deck session is read from its exact session document: the saved Pi
session id and path, or the single session file in its own
`~/.pi/agent-deck/<id>` directory. With several files and no saved identity
(after `/new` or `/resume` in Pi), or on a remote, the CLI asks the index
instead, and says which when the index has nothing either. Rows follow the
file in order, so a Pi session branched with `/tree` shows every branch; a
`branch_summary` record becomes a `system` row titled "Branch summary" where
the branch was left. Following only the active branch is not done yet.

Timeline:

```json
{ "schema": "agent-deck.recall.rows/v2",
  "session": { "id": "<deck id>", "harness": "claude", "native_id": "…", "path": "…/<id>.jsonl", "title": "…", "cwd": "…" },
  "source": "native",
  "turns": [ Row, … ],
  "through_cursor": "<opaque>",
  "before_cursor": "<opaque older-history boundary>",
  "status": { "session_id": "…", "running": true, "session_status": "running", "verb": "Cogitating…", "elapsed_s": 72, … } }
```

`--tail N` returns the last N rows, reading growing windows from the end of
the file (a 100 MB transcript answers from its last 256 KB). `--limit N`
stops after N rows with the cursor there; `--since <cursor>` returns what
changed after a cursor: new rows in `turns`, changes to earlier rows in
`updates` (merge by id) and ids to drop in `removed`. Paging with
`--limit`/`--since` until the cursor stops moving yields exactly the full
timeline. `--agent <id>` returns one Claude sub-agent sidechain.

### Only some rows (`--kinds`, `--skip-empty-body`)

A client that shows a conversation as messages, not as a full activity log,
can ask the owner host to drop the other rows before they are printed, so
they never cross an SSH link:

```
agent-deck recall timeline <session> --json --kinds user,assistant --tail 100
agent-deck recall timeline <session> --json --kinds user,assistant --since <next_cursor>
agent-deck recall timeline <session> --json --skip-empty-body --tail 100
```

- `--kinds` keeps only rows whose `kind` is in the comma-separated list:
  `user`, `assistant`, `thinking`, `tool`, `bash`, `edit`, `read`,
  `subagent`, `todo`, `question`, `skill`, `command`, `system`,
  `compaction`, `turn_end`, `other`. An unknown or empty kind is refused
  with exit 2 (`INVALID_OPERATION`) and the list of valid kinds.
- `--skip-empty-body` drops rows whose `body` is empty or only whitespace.
  A tool call still waiting for its result (`finished: false`) is kept: its
  output arrives later as an update to that row, and a client can only merge
  an update into a row it already holds. Both flags can be combined.
- `--tail N`, `--limit N` and `--before <cursor> --limit N` count rows that
  are kept: `--kinds user --tail 20` answers the last 20 user rows, however
  many other rows sit between them.
- A filtered answer carries `next_cursor`. Resume with
  `--since <next_cursor>`, never with a cursor built from the last row in
  `turns`: when the newest rows were filtered out, that row is older than
  what this read covered, and the next call would fetch the same window
  again. `next_cursor` equals `through_cursor`, which already covers every
  row the read passed, kept or not. Without a filter `next_cursor` is
  absent and the output is byte for byte what it was before the flags
  existed.
- `before_cursor` names the first kept row, so older pages with the same
  filter continue exactly where the last one stopped.
- In `--since` answers, `updates` for rows the filter dropped do not cross
  the wire. A filtered cursor lists the open rows it dropped (a tool call
  waiting for its result, a queued message; the newest 64), so the result
  patch that arrives later is dropped too, and the id leaves the cursor once
  its result has been swallowed. An update that names a kind outside the
  list is also dropped. Other updates are for rows the client holds and are
  kept. `removed` is unchanged.
- Sub-agent children attached to a kept `subagent` row are filtered the
  same way, so a sidechain's dropped kinds do not ride along.
- Rows read from the index (`source: "index"`) and `--agent` sidechains
  are filtered the same way; they have no cursor and no `next_cursor`.
  On the index path `--limit N` keeps the first N kept rows.
- Filtering runs on the owner host, so `remote <name> recall timeline
  --kinds ...` and an `ssh <host> agent-deck recall timeline --kinds ...`
  both carry only the kept rows. `--v1` is refused with a filter.

`agent-deck version --json` lists `recall_timeline_kinds` in `features` on
cores that filter; an older core rejects `--kinds` as an unknown flag, and
`remote <name>` reports it as predating `recall timeline --kinds`.

### First screens in one call (`--sessions`)

`--sessions <id1>,<id2>,...` answers up to 50 sessions in one call, so a
client can read every first screen on a box in a few calls instead of one
round trip per session:

```json
{ "schema": "agent-deck.recall.timeline-batch/v1",
  "sessions": [
    { "session": "<id1>", "ok": true, "timeline": { …the object `recall timeline <id1> --json --tail N` prints… } },
    { "session": "<id2>", "ok": false, "error": "no session, conversation or indexed transcript matches \"<id2>\"", "code": "NOT_FOUND" } ] }
```

Entries follow the input order, and each `timeline` equals the single-id
answer. One id failing never fails the batch: the call exits 0 once the request
is valid. Only `--tail`, `--kinds` and `--skip-empty-body` combine with `--sessions`; `--since`, `--before`,
`--limit`, `--agent`, `--v1`, `--transcript` and a positional `<session>` are
refused with `INVALID_OPERATION` (exit 2), as are more than 50 ids or an empty
id. Sessions and the recall index are opened once per call and transcripts are
tailed four at a time, each read bounded exactly as on the single path.
`agent-deck version --json` lists `recall_timeline_batch` in `features` on cores
that serve it; an older core prints its plain version line there, and a client
falls back to one id per call.

For older history, begin with `--tail N`, then request
`--before <before_cursor> --limit N`. Each page contains at most N rows in
source order, exclusively before the first row of the preceding page. The
cursor preserves boundaries within multi-block native records. An empty page
has no `before_cursor`, so paging stops. A `through_cursor` can also bootstrap
an older-history read from that snapshot's end. Appended records do not move
the historical snapshot; rewritten, truncated or moved sources require resync.
`--before` requires a positive limit, cannot combine with `--since`, `--tail`,
`--agent` or `--v1`, and is supported for directly read Claude, Codex and Pi transcripts.
Older cores reject the unknown option, allowing clients to fall back to tail
pagination. Pages transmit only the requested rows. The native parser still
uses growing windows through the snapshot end to preserve later tool results
and queue updates. A first pass retains lightweight row identities and ordering;
a second pass retains bodies only for the requested page. Memory still grows
with the number of identities in the window, and backward paging still reparses
the previously shown suffix. Attached sub-agent children retain their existing
behavior and can independently contain a large transcript.

### Over HTTP (`agent-deck web`)

`GET /api/sessions/{id}/timeline` answers the same rows v2 object for a deck
session id, so a web or other HTTP client reads what `recall timeline <id>
--json` prints. The query parameters are the CLI's flags: `tail`, `limit`,
`since`, `before` (with `limit`), `agent`, `kinds` and `skip-empty-body`,
with the same rules. `kinds=user,assistant` is the allowlist; a present but
empty `kinds` or an unknown kind is a 400 that lists the valid kinds.
`skip-empty-body` takes `true`/`false` (present without a value means
`true`). A filtered answer carries `next_cursor`, the cursor to pass to
`since` next. It reads
the session's live Claude Code, Codex or Pi transcript and never touches the
index. The route follows the web server's token like every other `/api`
route and needs `[recall] enabled = true` like the CLI.

Without `tail` or `since` the route reads the whole transcript on every
request, as the CLI does. A web client should always send one of them: `tail`
for the first screen, then `since` with the returned `through_cursor`.

The pane `status` the CLI adds is left out: the web menu stream already
carries session status. A cursor names its transcript, so one issued for
another file is refused rather than read.

| Status | Code | When |
| --- | --- | --- |
| 200 | | the rows v2 object; `turns` is `[]` when nothing changed since the cursor |
| 400 | `INVALID_REQUEST` | a parameter is not an integer or is negative, the combination is refused (`before` without `limit`), `agent` is not a sub-agent id, or `agent` is set on a session that is not Claude Code |
| 404 | `NOT_FOUND` | no such session, its transcript does not exist on this machine yet, or no sub-agent sidechain matches `agent` |
| 409 | `RESYNC_REQUIRED` | the cursor is malformed or no longer describes the transcript (rewritten, moved, or not this session's); reload without it |
| 500 | `INTERNAL_ERROR` | the session data or the transcript could not be read |

#### Stream (`timeline/stream`)

`GET /api/sessions/{id}/timeline/stream?after=<cursor|end>` is the SSE form
of `recall follow <id> --after <cursor|end> --jsonl`. Each event is one
follow frame: the event name is the frame (`row`, `update`, `remove`,
`status`, `resync_required`) and `data` is the frame's JSON, the same line
the CLI prints. A frame's `cursor` is also the event `id`, and a
`Last-Event-ID` header wins over `after`, so an EventSource that reconnects
resumes where it stopped.

```text
id: eyJ2IjoyLC...
event: row
data: {"frame":"row","row":{"id":"…","kind":"assistant","body":"…"},"cursor":"eyJ2IjoyLC..."}
```

- `after` (required): a `through_cursor`, `next_cursor` or frame cursor, or
  `end` to start at the current end. The first block is always an id-only
  event with the start cursor (for `end`, the resolved end), so a client
  that drops before the first frame reconnects without skipping the rows
  written in between.
- `kinds`, `skip-empty-body`: the timeline filter. Rows the filter drops are
  not sent, nor their later `update`/`remove` frames. When a held-back frame
  carried a cursor, the stream sends an id-only event (an `id:` line, no
  data): that id is the stream's `next_cursor`. Keep the last id you saw,
  not the cursor of the last row you rendered, or a resume re-reads the
  filtered window.
- `status=true`: a `status` frame with `session_id`, `session_status` and
  `running`, every second while the session runs and once when it stops.
  The pane facts the CLI adds (verb, tokens, current tool) are left out.

The stream uses the same token check as the snapshot route (the
`Authorization` header or the web UI's token cookie; a `?token=` query is
refused). It ends after `resync_required`, and when the client disconnects;
an idle stream sends a `: keepalive` comment every 15 seconds. On
`resync_required`, close the EventSource and reload with a fresh snapshot:
an EventSource left open reconnects with the same stale `Last-Event-ID` and
gets `resync_required` again. A server keeps at most 32 timeline streams
open; past that a new one gets 429 `RATE_LIMITED` until a client
disconnects. An open stream does not hold back a headless `agent-deck web`
from restarting into a new build, as with `/events/*`. Before the
first event, errors are JSON as on the snapshot route (a missing `after`
is a 400).
| 501 | `NOT_IMPLEMENTED` | the session's tool is not Claude Code, Codex or Pi; the CLI falls back to the index for those, the route does not |
| 503 | `RECALL_DISABLED` | `[recall] enabled` is false |

`GET /api/remotes/{name}/sessions/{id}/timeline` is the same read for a
session on a configured remote: the server runs the remote's `agent-deck
recall timeline <id> --json` over that remote's SSH runner, with the same
query parameters, and answers its rows v2 object without the pane `status`.
The session must be one the remote fleet scan (`GET /api/remotes`) lists.
Cursors are passed through as they are, so `since` and `before` page the
remote's transcript. Recall must be on at both ends: with `[recall] enabled`
false on this machine the route answers 503 before any SSH, so turning recall
off here means no transcript leaves through this server. What the remote can
read decides the rest: its own `[recall] enabled`, its harnesses, and its
version. Each request is one SSH
round trip, so poll it no faster than the remote list. A remote serves three
of these reads at once; the next one gets 429 until one ends.

| Status | Code | When |
| --- | --- | --- |
| 200 | | the remote's rows v2 object |
| 400 | `INVALID_REQUEST` | as above (`agent` on a session that is not Claude Code included), or a cursor that is not base64url |
| 404 | `NOT_FOUND` | the fleet scan does not list the remote or the session, the remote is not configured, or the remote answers not found (with a fixed message, never the remote's own text) |
| 409 | `RESYNC_REQUIRED` | the remote refused the cursor; reload without it |
| 429 | `RATE_LIMITED` | three reads of this remote are already in flight; retry after `Retry-After` |
| 501 | `NOT_IMPLEMENTED` | the remote's agent-deck has no `recall timeline` with these flags; update it |
| 502 | `INTERNAL_ERROR` | SSH failed, the answer was not this session's timeline, or it was over 8 MB |
| 503 | `REMOTE_OFFLINE` | the fleet scan sees the remote offline, even when it lists none of the remote's sessions yet, or has not finished its first scan since the server started |
| 503 | `RECALL_DISABLED` | `[recall] enabled` is false on this machine or on the remote |
| 504 | `INTERNAL_ERROR` | the remote did not answer within 30 seconds |

Row:

| Field | Meaning |
| --- | --- |
| `id` | Native and stable: Claude `uuid` (`uuid#n` for block n of a multi-block message), the `tool_use` id for a tool row, `queue:<enqueue ts>:<hash>` for a typed-while-busy message; Codex item id, call id, `turn:<turn_id>`; content-hash ids for rows without a native id. Never a line index. |
| `kind` | `user`, `assistant`, `thinking`, `tool`, `bash`, `edit`, `read`, `subagent`, `todo`, `question`, `skill`, `command`, `system`, `compaction`, `turn_end`, `other` |
| `ts` | Native timestamp (for an absorbed queued message: when Claude absorbed it) |
| `title` | The one line a client shows: tool description, `Edit <path>`, `/command args`, `Worked for 7s`, `Context compacted · 844.0k → 19.0k`, first line of a hook or system injection, the question |
| `body` | Full text: prose, thinking, system body, sub-agent prompt; for a tool row its output (capped at 32 KiB, `meta.truncated`; also set when Pi truncated a direct shell command's output itself) |
| `summary` | The terminal's `⎿` wording: `Read 120 lines`, `Added 3 lines, removed 1 line`, `Backgrounded agent`, `Done (4 tool uses)`, `2 of 5 done`, `Which? → A`, `Exit code 2`, `… +12 lines` |
| `detail` | The dim mono line: command, path or pattern, sub-agent type |
| `tool_id` | Native tool call id on tool rows |
| `finished` | Tool rows: false until the result lands |
| `is_error` | Tool result was an error or a non-zero exit |
| `queued` | `true` while a message typed during a turn waits; `false` once absorbed |
| `meta` | `tool_name`, `path`, `input` (todo list, question options, edit arguments, Codex changes), `lines`, `exit_code`, `added`/`removed`, `answers`, `agent_id`, `status`, `duration_ms`, `pre_tokens`/`post_tokens`, `tokens`, `images`, `delivery` (`queued`/`absorbed`), `model`, `raw` (only on `other`) |
| `children` | Sub-agent sidechain rows under a `subagent` row (`<session>/subagents/agent-<id>.jsonl`), ids prefixed `sub:<agent id>:` |
| `raw_type` | The native record type (`assistant`, `system/turn_duration`, `item_completed/CommandExecution`, …) |

Claude Code mapping: `queue-operation enqueue` is a `user` row with
`queued: true`; `remove` with `reason: absorbed_mid_turn` is an update with
`queued: false` and the remove `ts` (the client moves the row there, where the
terminal printed it; no user row is ever written for it); `dequeue` lets the
following user row replace the queued copy (a `remove` frame for it); a plain
`remove` drops it. The matching `queued_command` attachment is deduplicated.
`isMeta`, `[INBOX]`, `<system-reminder>`, `Stop hook feedback:`,
`<task-notification>` and local-command output are `system`; `<command-name>`
user rows and `local_command` are `command`; `turn_duration` is `turn_end`
with `meta.duration_ms`; `compact_boundary` is `compaction` with
`meta.pre_tokens/post_tokens`; `stop_hook_summary` is `system` only when it
carries text; hook errors and hook context attachments are `system`; pure
model context (file snapshots, titles, modes, token reminders, skill listings,
deferred tool lists) is dropped. `toolUseResult` feeds `summary`.

Codex mapping: AGENTS.md/environment boilerplate and developer messages are
dropped; `event_msg item_completed` `CommandExecution` is `bash` (or `read`
when every parsed command is read-only), `FileChange` is `edit` with +/-
counts, `McpToolCall` is `tool`, `SubAgentActivity`/`CollabAgentToolCall`
update their spawn/wait rows; the `AgentMessage`/`UserMessage`/`Reasoning`
items duplicate response items and are skipped, as is a code-mode `exec`
script whose commands arrive as their own items. `task_complete` is
`turn_end` with the duration and the last `token_count` in `meta.tokens`;
`turn_aborted` is an interrupted `turn_end`; a model switch in `turn_context`
or `thread_settings_applied` is a `system` row.

Follow frames, one JSON object per line:

| `frame` | Payload | Client action |
| --- | --- | --- |
| `row` | `row` | append (or replace the row with the same id) |
| `update` | `row` with `id` and only what changed (a tool gaining its result, a queued message absorbed, a sub-agent's children) | merge the present fields into that row, meta keys merged; `queued:false` with a `ts` moves the row to the end; ignore unknown ids |
| `remove` | `id` | drop that row |
| `status` (with `--status`) | `session_id, running, session_status, verb, elapsed_s, tokens, current_tool, facts{account,model,cwd,ctx,in,out,5h,7d \| model,context_left,weekly_left,window}, permission, auto_compact_pct, queued[], notice` | the live status strip; sent once a second while the session runs and once when it stops |
| `resync_required` | `reason`: `invalid_cursor`, `source_rewritten`, `source_shortened`, `source_replaced`, `source_missing`, `source_moved`, `source_unresolved` | call timeline again |

Only the last frame produced by one native line carries `cursor`, so every
cursor is a clean resume point: resuming neither loses nor duplicates a
frame. The cursor holds the byte offset, a hash of the 256 bytes before it,
and the pending queue ids. Follow polls every 200 ms and reads only the new
bytes. `--after end` starts at the current end. `source_moved` fires when a
Codex session's live rollout changes (docs/macapp-core.md, Codex identity).
`source_unresolved` fires when a Pi session stops resolving to one session
document while it is followed (a second file after `/new`, no saved identity).
The status frame comes from the session's state.db status and, while it
runs, a read-only capture of its pane (the `✻ Verb… (1m 12s · ↓ 3.7k tokens)`
or `• Working (30m 26s • esc to interrupt)` line, the `⎿` line under it, the
statusline, the permission mode, `N% until auto-compact`, queued inputs and
`✘` notices), so a client never reads tmux.
