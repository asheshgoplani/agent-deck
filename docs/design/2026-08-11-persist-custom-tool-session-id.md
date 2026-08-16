# Persist custom-tool conversation IDs across reboot

**Branch:** `feat/persist-custom-tool-session-id`  
**Date:** 2026-08-11  
**Status:** implementation in progress

## Problem

Custom tools configured via `[tools.*]` can declare `resume_flag` /
`session_id_env` so agent-deck restarts with `<cmd> <resume_flag> <id>`.
Until this change, that id only lived in the **live tmux environment**.

After a machine reboot tmux is gone. The deck row still knows *tool* and
*title*, but not *which conversation*, so every custom-tool seat has to be
rebound by hand.

Built-in tools already persist conversation ids in `tool_data` (SQLite)
— Claude, Gemini, Codex, OpenCode, Pi, Copilot, Crush, Cursor, Hermes,
and so on. Custom `[tools.*]` entries do not get that path. That is the
gap this change closes.

## Scope

**In:** every tool defined under `[tools.*]` in `config.toml` that uses
`resume_flag` (and optionally `session_id_env` / JSON id capture).

**Out:**

- New first-class tool adapters
- Automatic “newest session under this cwd” discovery (unsafe when many
  seats share one path)
- Changing built-in resume paths (Claude / Gemini / Codex / …)

## GitHub tree check (contribution framing)

| Finding | Implication |
|---------|-------------|
| First-class adapters for individual vendor CLIs are a long-term commitment; maintainers have declined some of those | Land this as **generic custom-tool durability**, not as support for one CLI |
| Existing code: `buildGenericCommand` / `CanRestartGeneric` | Correct hook; only durable storage was missing |
| CONTRIBUTING: features → Discussion first | Frame as custom-tool session-id durability across reboot |

## Approach

Mirror the `last_started_at` / extras-zone pattern (#1704):

1. `generic_session_id` (+ optional `generic_detected_at`) in `tool_data` JSON.
2. Sticky merge so full-table saves do not wipe an asynchronously written id.
3. `GetGenericSessionID()`: live tmux env first, else persisted field.
4. `CanRestartGeneric()`: needs `resume_flag` + non-empty resolved id (env **or** DB).
5. `session set tool-session-id <id>` for operator binding (any custom tool).
6. Write-through when tmux env has a value and DB does not (or differs).
7. Document `resume_flag` / `session_id_env` / capture fields in config-reference.

## Config pattern

```toml
[tools.example]
command = "/path/to/cli"
busy_patterns = ["Thinking", "Working"]
resume_flag = "--resume"   # or whatever that CLI uses
# optional: if the tool exports an id into the pane environment
# session_id_env = "EXAMPLE_SESSION_ID"
```

Bind once after the conversation exists:

```bash
agent-deck session set <deck-title> tool-session-id <conversation-id>
agent-deck session restart <deck-title>
```

Subsequent reboots restart as `<command> <resume_flag> <conversation-id>`.

Per-tool requirements (still up to each CLI):

- `resume_flag` must match that CLI’s resume API
- Id must be learned once (manual set, `session_id_env` write-through, or optional JSON capture flags)
- CLIs that only support “continue last in this cwd” without an id remain multi-seat unsafe

## Tests

- Round-trip tool_data read/write
- SQLite load/save preserves id
- `GetGenericSessionID` falls back to DB without tmux
- `buildGenericCommand` / restart shape includes `<resume_flag> <id>` with only DB id
- `SetField(tool-session-id)` updates instance + sticky save
