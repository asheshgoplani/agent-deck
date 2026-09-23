# Recall timeline and follow

`agent-deck recall timeline <session> --json` returns `session`, ordered
`turns`, and `through_cursor`. `agent-deck recall follow <session> --after
<through_cursor> --jsonl` streams one `turn` frame per new event. Each frame
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

## Typed rows (`--rows`, schema `agent-deck.recall.rows/v2`)

The Mac app and any other client that renders a conversation use the row
model instead of the v1 turns. It is additive: without `--rows` both commands
behave exactly as above.

```
agent-deck recall timeline <session> --rows --json [--tail-bytes N] [--agent <id>]
agent-deck recall follow   <session> --rows --jsonl --after <through_cursor|cursor|end>
agent-deck recall timeline --rows --json --transcript <file> --harness claude|codex
```

`--rows` resolves `<session>` (id, id prefix, title or path) in the
agent-deck store and parses that session's native transcript directly: the
Claude Code JSONL (`<config dir>/projects/<project>/<claude_session_id>.jsonl`)
or the Codex rollout. It needs neither `[recall] enabled` nor the index, so a
session whose transcript the sweep deferred still answers at once. Claude
Code and Codex are read directly; other harnesses keep using the v1 path.

Timeline result:

```json
{ "schema": "agent-deck.recall.rows/v2",
  "session": { "id": "…", "title": "…", "tool": "claude", "harness": "claude", "path": "…jsonl", "native_id": "…" },
  "source": "native",
  "rows": [ { "id": "tool:toolu_01…", "kind": "bash", "ts": "…", "title": "Run hello.py",
              "command": "python3 hello.py", "result": { "text": "1\n2", "lines": 2, "exit_code": 0 } } ],
  "through_cursor": "…",
  "status": { "state": "running", "verb": "Cogitating…", "elapsed": "1m 12s", "tokens": "↓ 3.7k tokens",
              "current_tool": "Running go build…", "footer": "…", "mode": "bypass permissions on" } }
```

| Row field | Meaning |
| --- | --- |
| `id` | Native and stable: Claude `uuid:block`, `tool:<tool_use_id>`, `queue:<enqueue ts>:<hash>`; Codex `codex:<item id>`, `tool:<call_id>`, `turn:<turn_id>`. Never a line index, so ids survive tail windows and reconnects. |
| `kind` | `user`, `assistant`, `thinking`, `tool`, `bash`, `edit`, `read`, `subagent`, `todo`, `question`, `skill`, `command`, `system`, `compaction`, `turn_end`, `other` |
| `title` | The one-line text a client shows (tool description, `/command args`, `Worked for 7s`, `Context compacted · 844.0k → 19.0k`, first line of a hook/system injection) |
| `text` | Full text: user and assistant prose, thinking, system body, sub-agent prompt |
| `command`, `path`, `tool_name`, `input` | Tool call details; `input` carries the todo list, question options, edit arguments or Codex file changes |
| `result` | Merged tool result: `text` (capped at 32 KiB, `truncated`), `lines`, `is_error`, `exit_code`, `added`/`removed` for edits, `answers` for questions |
| `delivery` | `queued` (typed while a turn ran) or `absorbed` (Claude took it mid-turn; no user row is ever written for it) |
| `parent_id`, `agent_id` | Sidechain rows point at their `subagent` row; the subagent row names the agent |
| `status`, `duration_ms`, `tokens`, `images` | Tool/turn status (`interrupted`, `completed`), turn duration, token footer numbers, image count on a user row |
| `raw` | Native JSON, only on `other` rows |

Claude Code mapping highlights: `queue-operation enqueue` is a `user` row
with `delivery: queued`; `remove` with `reason: absorbed_mid_turn` moves it
to the remove time with `delivery: absorbed`; `dequeue` lets the following
user row replace it; a plain `remove` drops it. The matching `queued_command`
attachment is deduplicated. `isMeta`, `[INBOX]`, `<system-reminder>`, stop
hook feedback and task notifications are `system`; `<command-name>` rows and
`local_command` are `command`; `turn_duration` is `turn_end`;
`stop_hook_summary` is a `system` row only when it carries text; pure model
context (file snapshots, titles, modes, token reminders, skill listings) is
dropped. Sub-agent sidechains (`<session>/subagents/agent-<id>.jsonl`) are
inserted under their `subagent` row; `--agent <id>` returns one sidechain.

Codex mapping highlights: AGENTS.md and environment boilerplate and developer
messages are dropped; `item_completed` `CommandExecution` is `bash` (or `read`
when every parsed command is read-only), `FileChange` is `edit`,
`McpToolCall` is `tool`; the `AgentMessage`/`UserMessage`/`Reasoning` items
duplicate response items and are skipped, as is a code-mode `exec` script
whose commands arrive as their own items. `task_complete` is `turn_end` with
the duration and the last `token_count`; `turn_aborted` is an interrupted
`turn_end`; a model switch in `turn_context` is a `system` row.

Follow frames, one JSON object per line:

| `type` | Payload | Client action |
| --- | --- | --- |
| `row` | `row` | append (or replace a row with the same id) |
| `update` | `row` with the id and only the changed fields | merge non-empty fields into that row; ignore unknown ids |
| `remove` | `id` | drop that row |
| `status` | `status` (as in the timeline) | show as the live status strip; sampled once a second, sent only when it changes; never persisted |
| `resync_required` | `reason` (`invalid_cursor`, `source_rewritten`, `source_shortened`, `source_replaced`, `source_missing`, `source_moved`) | call timeline again |

Only the last frame produced by one native line carries `cursor`, so every
cursor is a clean resume point: resuming never duplicates or loses a frame.
The cursor records the byte offset, a hash of the bytes before it, and the
pending queue ids. Follow polls every 200 ms and reads only new bytes, so a
line appended to a 100 MB transcript streams in well under the 500 ms budget.
`--after end` starts at the current end. The `status` frame comes from the
session's state.db status and, while it runs, from a read-only capture of its
pane (spinner verb, elapsed, tokens, current tool line, queued inputs, footer,
permission mode, notices), so a client never reads tmux.
