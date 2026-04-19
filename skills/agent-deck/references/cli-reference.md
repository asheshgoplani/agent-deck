# CLI Command Reference

Complete reference for all agent-deck CLI commands.

## Table of Contents

- [Global Options](#global-options)
- [Basic Commands](#basic-commands)
- [Web Command](#web-command)
- [Session Commands](#session-commands)
- [MCP Commands](#mcp-commands)
- [Skill Commands](#skill-commands)
- [Group Commands](#group-commands)
- [Profile Commands](#profile-commands)
- [Conductor Commands](#conductor-commands)

## Global Options

```bash
-p, --profile <name>    Use specific profile
--json                  JSON output
-q, --quiet             Minimal output
```

## Basic Commands

### add - Create session

```bash
agent-deck add [path] [options]
```

| Flag | Description |
|------|-------------|
| `-t, --title` | Session title |
| `-g, --group` | Group path |
| `-c, --cmd` | Tool/command (claude, gemini, opencode, codex, custom) |
| `--wrapper` | Wrapper command; use `{command}` placeholder |
| `--parent` | Parent session (creates child) |
| `--no-parent` | Disable automatic parent linking |
| `--mcp` | Attach MCP (repeatable) |

```bash
agent-deck add -t "My Project" -c claude .
agent-deck add -t "Child" --parent "Parent" -c claude /tmp/x
agent-deck add -g ard --parent "conductor-ard" -c claude .
agent-deck add -c "codex --dangerously-bypass-approvals-and-sandbox" .
agent-deck add -t "Research" -c claude --mcp exa --mcp firecrawl /tmp/r
```

Notes:
- Parent auto-link is enabled by default when `AGENT_DECK_SESSION_ID` is present and neither `--parent` nor `--no-parent` is passed.
- `--parent` and `--no-parent` are mutually exclusive.
- Explicit `-g/--group` overrides inherited parent group.
- If `--cmd` contains extra args and no explicit `--wrapper` is provided, agent-deck auto-generates a wrapper to preserve those args.

### launch - Create + start (+ optional message)

```bash
agent-deck launch [path] [options]
```

Examples:

```bash
agent-deck launch . -c claude -m "Review this module"
agent-deck launch . -g ard -c claude -m "Review dataset"
agent-deck launch . -c "codex --dangerously-bypass-approvals-and-sandbox"
```

### list - List sessions

```bash
agent-deck list [--json] [--all]
agent-deck ls  # Alias
```

### remove - Remove session

```bash
agent-deck remove <id|title>
agent-deck rm  # Alias
```

### status - Status summary

```bash
agent-deck status [-v|-q|--json]
```

- Default: `2 waiting - 5 running - 3 idle`
- `-v`: Detailed list by status
- `-q`: Just waiting count (for scripts)

## Web Command

### web - Start browser UI

```bash
agent-deck web [options]
```

| Flag | Description |
|------|-------------|
| `--listen` | Listen address (default: `127.0.0.1:8420`) |
| `--read-only` | Disable terminal input, stream output only |
| `--token` | Require bearer token for API and WS access |
| `--open` | Reserved placeholder (currently no-op) |

```bash
agent-deck web
agent-deck web --read-only
agent-deck web --token my-secret
agent-deck -p work web --listen 127.0.0.1:9000
```

When token auth is enabled, open the web UI with:

```bash
http://127.0.0.1:8420/?token=my-secret
```

## Session Commands

### session start

```bash
agent-deck session start <id|title> [-m "message"] [--json] [-q]
```

`-m` sends initial message after agent is ready.
Flags can be placed before or after the session identifier.

### session stop

```bash
agent-deck session stop <id|title>
```

### session restart

```bash
agent-deck session restart <id|title>
```

Reloads MCPs without losing conversation (Claude/Gemini).

### session fork (Claude only)

```bash
agent-deck session fork <id|title> [-t "title"] [-g "group"]
```

Creates new session with same Claude conversation.

**Requirements:**
- Session must be Claude tool
- Must have valid Claude session ID

### session attach

```bash
agent-deck session attach <id|title>
```

Interactive PTY mode. Press `Ctrl+Q` to detach.

### session show

```bash
agent-deck session show [id|title] [--json] [-q]
```

Auto-detects current session if no ID provided.

**JSON output includes:**
- Session details (id, title, status, path, group, tool)
- Claude/Gemini session ID
- Attached MCPs (local, global, project)
- tmux session name

### session current

```bash
agent-deck session current [--json] [-q]
```

Auto-detect current session and profile from tmux environment.

```bash
# Human-readable
agent-deck session current
# Session: test, Profile: work, ID: c5bfd4b4, Status: running

# For scripts
agent-deck session current -q
# test

# JSON
agent-deck session current --json
# {"session":"test","profile":"work","id":"c5bfd4b4",...}
```

**Profile auto-detection priority:**
1. `AGENTDECK_PROFILE` env var
2. Parse from `CLAUDE_CONFIG_DIR` (`~/.claude-work` -> `work`)
3. Config default or `default`

### session set

```bash
agent-deck session set <id|title> <field> <value>
```

**Fields:** title, path, command, tool, claude-session-id, gemini-session-id

### session send

```bash
agent-deck session send <id|title> "message" [--no-wait] [-q] [--json]
```

Default behavior:
- Waits for agent readiness before sending.
- Verifies processing starts after send.
- If Claude leaves a pasted prompt unsent (`[Pasted text ...]`), retries `Enter` automatically.
- Avoids unnecessary retry `Enter` presses when session is already `waiting`/`idle`.

### session output

```bash
agent-deck session output [id|title] [--json] [-q]
```

Get last response from Claude/Gemini session.

### session set-parent / unset-parent

```bash
agent-deck session set-parent <session> <parent>
agent-deck session unset-parent <session>
```

## MCP Commands

### mcp list

```bash
agent-deck mcp list [--json] [-q]
```

### mcp attached

```bash
agent-deck mcp attached [id|title] [--json] [-q]
```

Shows MCPs from LOCAL, GLOBAL, PROJECT scopes.

### mcp attach

```bash
agent-deck mcp attach <session> <mcp> [--global] [--restart]
```

- `--global`: Write to Claude config (all projects)
- `--restart`: Restart session immediately

### mcp detach

```bash
agent-deck mcp detach <session> <mcp> [--global] [--restart]
```

## Skill Commands

Skills are discovered from configured sources and attached per project for supported runtimes.

### skill list

```bash
agent-deck skill list [--source <name>] [--json] [-q]
agent-deck skill ls
```

`--source` filters by source name (for example `pool`, `claude-global`, `team`).

### skill attached

```bash
agent-deck skill attached [id|title] [--json] [-q]
```

Shows:
- Manifest-managed attachments from `<project>/.agent-deck/skills.toml`
- Unmanaged entries currently present in the managed project skill roots (`<project>/.claude/skills` and `<project>/.agents/skills`)

### skill attach

```bash
agent-deck skill attach <session> <skill> [--source <name>] [--restart] [--json] [-q]
```

- `--source`: Force source when name is ambiguous
- `--restart`: Restart session immediately after attach for Claude, Gemini, and Codex sessions

Attach target root is runtime-specific:
- Claude-compatible sessions -> `<project>/.claude/skills`
- Gemini, Codex, and Pi sessions -> `<project>/.agents/skills`

### skill detach

```bash
agent-deck skill detach <session> <skill> [--source <name>] [--restart] [--json] [-q]
```

- `--source`: Filter by source when detaching
- `--restart`: Restart session immediately after detach for Claude, Gemini, and Codex sessions

### skill source list

```bash
agent-deck skill source list [--json] [-q]
agent-deck skill source ls
```

### skill source add

```bash
agent-deck skill source add <name> <path> [--description "..."] [--json] [-q]
```

### skill source remove

```bash
agent-deck skill source remove <name> [--json] [-q]
agent-deck skill source rm <name>
```

## Group Commands

### group list

```bash
agent-deck group list [--json] [-q]
```

### group create

```bash
agent-deck group create <name> [--parent <group>]
```

### group delete

```bash
agent-deck group delete <name> [--force]
```

`--force`: Move sessions to parent and delete.

### group move

```bash
agent-deck group move <session> <group>
```

Use `""` or `root` to move to default group.

## Profile Commands

```bash
agent-deck profile list
agent-deck profile create <name>
agent-deck profile delete <name>
agent-deck profile default [name]
```

## Conductor Commands

```bash
agent-deck conductor setup <name> [--description "..."] [--heartbeat|--no-heartbeat]
agent-deck conductor teardown <name> [--remove]
agent-deck conductor teardown --all [--remove]
agent-deck conductor status [name]
agent-deck conductor list [--profile <name>]
```

- `setup` creates `~/.agent-deck/conductor/<name>/` plus `meta.json` and registers `conductor-<name>` session in the selected profile.
- `setup` also installs shared `~/.agent-deck/conductor/CLAUDE.md` (or symlink via `--shared-claude-md`).
- Heartbeat timers run per conductor (default every 15 minutes) and can be disabled with `--no-heartbeat`.
- Heartbeat sends use non-blocking `session send --no-wait -q` to avoid timeout churn when sessions are busy.
- Bridge daemon is installed only when Telegram and/or Slack is configured in `[conductor]`.
- Transition notifier daemon (`agent-deck notify-daemon`) is installed by setup and sends event nudges on `running -> waiting|error|idle` transitions (parent first, then conductor fallback).

## Session Resolution

Commands accept:
- **Title:** `"My Project"` (exact match)
- **ID prefix:** `abc123` (6+ chars)
- **Path:** `/path/to/project`
- **Current:** Omit ID in tmux (uses env var)

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Error |
| 2 | Not found |
