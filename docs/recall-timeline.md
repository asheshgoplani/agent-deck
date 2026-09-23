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
agent-deck recall timeline <session> --json [--since <cursor>] [--limit N] [--tail N] [--agent <id>]
agent-deck recall follow   <session> --after <cursor|end> --jsonl [--status]
agent-deck recall timeline --json --transcript <file> --harness claude|codex
```

Both need `[recall] enabled = true` (exit 2 otherwise) but never need the
index: `<session>` resolves, without touching recall.db, as a deck session id,
a Claude/Codex conversation id bound to a deck session, a deck title or id
prefix, or a conversation id whose file exists under any Claude config dir or
Codex home. Only then is the index asked (`#n`, card ids, other harnesses).
The native file is parsed directly, so a session whose source the sweep
deferred answers at once. Claude Code and Codex are read and streamed
directly; Pi, Gemini, OpenCode and Hermes come from the index as v1 turns
mapped onto rows (`source: "index"`) and are streamed with `--v1`.

Timeline:

```json
{ "schema": "agent-deck.recall.rows/v2",
  "session": { "id": "<deck id>", "harness": "claude", "native_id": "…", "path": "…/<id>.jsonl", "title": "…", "cwd": "…" },
  "source": "native",
  "turns": [ Row, … ],
  "through_cursor": "<opaque>",
  "status": { "session_id": "…", "running": true, "session_status": "running", "verb": "Cogitating…", "elapsed_s": 72, … } }
```

`--tail N` returns the last N rows, reading growing windows from the end of
the file (a 100 MB transcript answers from its last 256 KB). `--limit N`
stops after N rows with the cursor there; `--since <cursor>` returns what
changed after a cursor: new rows in `turns`, changes to earlier rows in
`updates` (merge by id) and ids to drop in `removed`. Paging with
`--limit`/`--since` until the cursor stops moving yields exactly the full
timeline. `--agent <id>` returns one Claude sub-agent sidechain.

Row:

| Field | Meaning |
| --- | --- |
| `id` | Native and stable: Claude `uuid` (`uuid#n` for block n of a multi-block message), the `tool_use` id for a tool row, `queue:<enqueue ts>:<hash>` for a typed-while-busy message; Codex item id, call id, `turn:<turn_id>`; content-hash ids for rows without a native id. Never a line index. |
| `kind` | `user`, `assistant`, `thinking`, `tool`, `bash`, `edit`, `read`, `subagent`, `todo`, `question`, `skill`, `command`, `system`, `compaction`, `turn_end`, `other` |
| `ts` | Native timestamp (for an absorbed queued message: when Claude absorbed it) |
| `title` | The one line a client shows: tool description, `Edit <path>`, `/command args`, `Worked for 7s`, `Context compacted · 844.0k → 19.0k`, first line of a hook or system injection, the question |
| `body` | Full text: prose, thinking, system body, sub-agent prompt; for a tool row its output (capped at 32 KiB, `meta.truncated`) |
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
| `resync_required` | `reason`: `invalid_cursor`, `source_rewritten`, `source_shortened`, `source_replaced`, `source_missing`, `source_moved` | call timeline again |

Only the last frame produced by one native line carries `cursor`, so every
cursor is a clean resume point: resuming neither loses nor duplicates a
frame. The cursor holds the byte offset, a hash of the 256 bytes before it,
and the pending queue ids. Follow polls every 200 ms and reads only the new
bytes. `--after end` starts at the current end. `source_moved` fires when a
Codex session's live rollout changes (docs/macapp-core.md, Codex identity).
The status frame comes from the session's state.db status and, while it
runs, a read-only capture of its pane (the `✻ Verb… (1m 12s · ↓ 3.7k tokens)`
or `• Working (30m 26s • esc to interrupt)` line, the `⎿` line under it, the
statusline, the permission mode, `N% until auto-compact`, queued inputs and
`✘` notices), so a client never reads tmux.
