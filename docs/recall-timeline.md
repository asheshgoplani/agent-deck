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
