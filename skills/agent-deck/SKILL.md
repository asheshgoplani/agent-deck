---
name: agent-deck
description: Terminal session manager for AI coding agents. Use when user mentions "agent-deck", "session", "sub-agent", "MCP attach", "git worktree", or needs to (1) create/start/stop/restart/fork sessions, (2) attach/detach MCPs, (3) manage groups/profiles, (4) get session output, (5) configure agent-deck, (6) troubleshoot issues, (7) launch sub-agents, or (8) create/manage worktree sessions. Covers CLI commands, TUI shortcuts, config.toml options, and automation.
metadata:
  compatibility: "claude, opencode"
---

# Agent Deck

Terminal session manager for AI coding agents. Built with Go + Bubble Tea.

**Version:** 0.8.98 | **Repo:** [github.com/asheshgoplani/agent-deck](https://github.com/asheshgoplani/agent-deck) | **Discord:** [discord.gg/e4xSs6NBN8](https://discord.gg/e4xSs6NBN8)

## Script Path Resolution (IMPORTANT)

This skill includes helper scripts in its `scripts/` subdirectory. When Claude Code loads this skill, it shows a line like:

```
Base directory for this skill: /path/to/.../skills/agent-deck
```

**You MUST use that base directory path to resolve all script references.** Store it as `SKILL_DIR`:

```bash
# Set SKILL_DIR to the base directory shown when this skill was loaded
SKILL_DIR="/path/shown/in/base-directory-line"

# Then run scripts as:
$SKILL_DIR/scripts/launch-subagent.sh "Title" "Prompt" --wait
```

**Common mistake:** Do NOT use `<project-root>/scripts/launch-subagent.sh`. The scripts live inside the skill's own directory (plugin cache or project skills folder), NOT in the user's project root.

**For plugin users**, the path looks like: `~/.claude/plugins/cache/agent-deck/agent-deck/<hash>/skills/agent-deck/scripts/`
**For local development**, the path looks like: `<repo>/skills/agent-deck/scripts/`

## Quick Start

```bash
# Launch TUI
agent-deck

# Create and start a session
agent-deck add -t "Project" -c claude /path/to/project
agent-deck session start "Project"

# Send message and get output
agent-deck session send "Project" "Analyze this codebase"
agent-deck session output "Project"
```

## Essential Commands

| Command | Purpose |
|---------|---------|
| `agent-deck` | Launch interactive TUI |
| `agent-deck add -t "Name" -c claude /path` | Create session |
| `agent-deck session start/stop/restart <name>` | Control session |
| `agent-deck session send <name> "message"` | Send message |
| `agent-deck session output <name>` | Get last response |
| `agent-deck session current [-q\|--json]` | Auto-detect current session |
| `agent-deck session fork <name>` | Fork Claude conversation |
| `agent-deck mcp list` | List available MCPs |
| `agent-deck mcp attach <name> <mcp>` | Attach MCP (then restart) |
| `agent-deck status` | Quick status summary |
| `agent-deck add --worktree <branch>` | Create session in git worktree |
| `agent-deck worktree list` | List worktrees with sessions |
| `agent-deck worktree cleanup` | Find orphaned worktrees/sessions |
| `agent-deck feedback` | Submit feedback (opens rating prompt + optional comment) |

**Status:** `●` running | `◐` waiting | `○` idle | `✕` error

## Sub-Agent Launch

**Use when:** User says "launch sub-agent", "create sub-agent", "spawn agent"

```bash
$SKILL_DIR/scripts/launch-subagent.sh "Title" "Prompt" [--mcp name] [--wait]
```

The script auto-detects current session/profile and creates a child session.

### Retrieval Modes

| Mode | Command | Use When |
|------|---------|----------|
| **Fire & forget** | (no --wait) | Default. Tell user: "Ask me to check when ready" |
| **On-demand** | `agent-deck session output "Title"` | User asks to check |
| **Blocking** | `--wait` flag | Need immediate result |

### Recommended MCPs

| Task Type | MCPs |
|-----------|------|
| Web research | `exa`, `firecrawl` |
| Code documentation | `context7` |
| Complex reasoning | `sequential-thinking` |

## Consult Another Agent (Codex, Gemini)

**Use when:** User says "consult with codex", "ask gemini", "get codex's opinion", "what does codex think", "consult another agent", "brainstorm with codex/gemini", "get a second opinion"

**IMPORTANT:** You MUST use the `--tool` flag to specify which agent. Without it, the script defaults to Claude.

### Quick Reference

```bash
# Consult Codex (MUST include --tool codex)
$SKILL_DIR/scripts/launch-subagent.sh "Consult Codex" "Your question here" --tool codex --wait --timeout 120

# Consult Gemini (MUST include --tool gemini)
$SKILL_DIR/scripts/launch-subagent.sh "Consult Gemini" "Your question here" --tool gemini --wait --timeout 120
```

**DO NOT** try to create Codex/Gemini sessions manually with `agent-deck add`. Always use the script above. It handles tool-specific initialization, readiness detection, and output retrieval automatically.

### Full Options

```bash
$SKILL_DIR/scripts/launch-subagent.sh "Title" "Prompt" \
  --tool codex|gemini \     # REQUIRED for non-Claude agents
  --path /project/dir \     # Working directory (auto-inherits parent path if omitted)
  --wait \                  # Block until response is ready
  --timeout 180 \           # Seconds to wait (default: 300)
  --mcp exa                 # Attach MCP servers (can repeat)
```

### Supported Tools

| Tool | Flag | Notes |
|------|------|-------|
| Claude | `--tool claude` | Default, no flag needed |
| Codex | `--tool codex` | Requires `codex` CLI installed |
| Gemini | `--tool gemini` | Requires `gemini` CLI installed |

### How It Works

1. Script auto-detects current session and profile
2. Creates a child session with the specified tool in the parent's project directory
3. Waits for the tool to initialize (handles Codex approval prompts automatically)
4. Sends the question/prompt
5. With `--wait`: polls until the agent responds, then returns the full output
6. Without `--wait`: returns immediately, check output later with `agent-deck session output "Title"`

### Examples

```bash
# Code review from Codex
$SKILL_DIR/scripts/launch-subagent.sh "Codex Review" "Read cmd/main.go and suggest improvements" --tool codex --wait --timeout 180

# Architecture feedback from Gemini
$SKILL_DIR/scripts/launch-subagent.sh "Gemini Arch" "Review the project structure and suggest better patterns" --tool gemini --wait --timeout 180

# Both in parallel (consult both, compare answers)
$SKILL_DIR/scripts/launch-subagent.sh "Ask Codex" "Best way to handle errors in Go?" --tool codex --wait --timeout 120 &
$SKILL_DIR/scripts/launch-subagent.sh "Ask Gemini" "Best way to handle errors in Go?" --tool gemini --wait --timeout 120 &
wait
```

### Cleanup

After getting the response, remove the consultation session:

```bash
agent-deck remove "Consult Codex"
# Or remove multiple at once:
agent-deck remove "Codex Review" && agent-deck remove "Gemini Arch"
```

## Peer (Root) Sessions vs Sub-Agents

**The default — sub-agent linkage:** `agent-deck launch` and `agent-deck add`, when invoked from *inside* an existing agent-deck session, automatically link the new session as a child of the calling session (sets `parent_session_id`, inherits the parent's group when `-g` is omitted, and grants `--add-dir` to the parent's project path). This is usually what you want for short-lived work sessions (plan / verify / release / consult).

**When the default is wrong — root-level peer sessions:** if you are creating a session that should stand independently at the root — a peer conductor, a standalone project session, a session that should outlive the current one, or anything that semantically is NOT a child of the calling session — pass the `-no-parent` flag.

| Use case | Parent linkage | Flag |
|---|---|---|
| Plan / impl / verify worker for the current task | ✅ child | (default) |
| Consultation (codex / gemini / research) | ✅ child | (default) |
| Another conductor (root-level peer) | ❌ child | `-no-parent` |
| Project session unrelated to current work | ❌ child | `-no-parent` |
| Session intended to outlive the caller | ❌ child | `-no-parent` |

```bash
# Root-level peer conductor, no parent linkage:
agent-deck launch ~/projects/foo -t "conductor-foo" -g "conductor" -c claude -no-parent -m "..."

# Verify after spawn:
agent-deck list --json | jq '.[] | select(.title=="conductor-foo") | .parent_session_id'
# Must print: null
```

**Symptoms you created a sub-agent when you wanted a peer:**
- `parent_session_id` is non-null in `list --json` output
- The new session's baked `pane_start_command` contains `--add-dir <caller's path>` even though you gave it a different project path
- Transition events for the new session's children flow to the caller instead of the new peer
- Event routing and heartbeat parent-linkage puts it under the caller's tree in the TUI

**Fix for an already-created sub-agent:** stop + remove the session, re-launch with `-no-parent`. There is no in-place un-parent flag.

**Note on the launch-subagent.sh script:** that script is specifically designed to create sub-agents (the name says so). It does NOT support `-no-parent`. For peer sessions, skip the script and invoke `agent-deck launch -no-parent` directly.

## TUI Keyboard Shortcuts

### Navigation
| Key | Action |
|-----|--------|
| `j/k` or `↑/↓` | Move up/down |
| `h/l` or `←/→` | Collapse/expand groups |
| `Enter` | Attach to session |

### Session Actions
| Key | Action |
|-----|--------|
| `n` | New session |
| `r/R` | Restart (reloads MCPs) |
| `m` | MCP Manager |
| `s` | Skills Manager (Claude) |
| `f/F` | Fork Claude session |
| `d` | Delete |
| `M` | Move to group |

### Search & Filter
| Key | Action |
|-----|--------|
| `/` | Local search |
| `G` | Global search (all Claude conversations) |
| `!@#$` | Filter by status (running/waiting/idle/error) |

### Global
| Key | Action |
|-----|--------|
| `?` | Help overlay |
| `Ctrl+Q` | Detach (keep tmux running) |
| `Ctrl+E` | Open feedback dialog |
| `q` | Quit |

## MCP Management

**Default:** Do NOT attach MCPs unless user explicitly requests.

```bash
# List available
agent-deck mcp list

# Attach and restart
agent-deck mcp attach <session> <mcp-name>
agent-deck session restart <session>

# Or attach on create
agent-deck add -t "Task" -c claude --mcp exa /path
```

**Scopes:**
- **LOCAL** (default) - `.mcp.json` in project, affects only that session
- **GLOBAL** (`--global`) - Claude config, affects all projects

## Worktree Workflows

### Create Session in Git Worktree

When working on a feature that needs isolation from main branch:

```bash
# Create session with new worktree and branch
agent-deck add /path/to/repo -t "Feature Work" -c claude --worktree feature/my-feature --new-branch

# Create session in existing branch's worktree
agent-deck add . --worktree develop -c claude
```

### List and Manage Worktrees

```bash
# List all worktrees and their associated sessions
agent-deck worktree list

# Show detailed info for a session's worktree
agent-deck worktree info "My Session"

# Find orphaned worktrees/sessions (dry-run)
agent-deck worktree cleanup

# Actually clean up orphans
agent-deck worktree cleanup --force
```

### When to Use Worktrees

| Use Case | Benefit |
|----------|---------|
| **Parallel agent work** | Multiple agents on same repo, different branches |
| **Feature isolation** | Keep main branch clean while agent experiments |
| **Code review** | Agent reviews PR in worktree while main work continues |
| **Hotfix work** | Quick branch off main without disrupting feature work |

## Configuration

**File:** `~/.agent-deck/config.toml`

```toml
[claude]
config_dir = "~/.claude-work"    # Custom Claude profile
dangerous_mode = true            # --dangerously-skip-permissions

[logs]
max_size_mb = 10                 # Max before truncation
max_lines = 10000                # Lines to keep

[mcps.exa]
command = "npx"
args = ["-y", "exa-mcp-server"]
env = { EXA_API_KEY = "key" }
description = "Web search"
```

See [config-reference.md](references/config-reference.md) for all options.

## Troubleshooting

| Issue | Solution |
|-------|----------|
| Session shows error | `agent-deck session start <name>` |
| MCPs not loading | `agent-deck session restart <name>` |
| Flag not working | Put flags BEFORE arguments: `-m "msg" name` not `name -m "msg"` |

### Get Help

- **Discord:** [discord.gg/e4xSs6NBN8](https://discord.gg/e4xSs6NBN8) for quick questions and community support
- **GitHub Issues:** For bug reports and feature requests

### Report a Bug

If something isn't working, create a GitHub issue with context:

```bash
# Gather debug info
agent-deck version
agent-deck status --json
cat ~/.agent-deck/config.toml | grep -v "KEY\|TOKEN\|SECRET"  # Sanitized config

# Create issue at:
# https://github.com/asheshgoplani/agent-deck/issues/new
```

**Include:**
1. What you tried (command/action)
2. What happened vs expected
3. Output of commands above
4. Relevant log: `tail -100 ~/.agent-deck/logs/agentdeck_<session>_*.log`

See [troubleshooting.md](references/troubleshooting.md) for detailed diagnostics.

## Session Sharing

Share Claude sessions between developers for collaboration or handoff.

**Use when:** User says "share session", "export session", "send to colleague", "import session"

```bash
# Export current session to file (session-share is a sibling skill)
$SKILL_DIR/../session-share/scripts/export.sh
# Output: ~/session-shares/session-<date>-<title>.json

# Import received session
$SKILL_DIR/../session-share/scripts/import.sh ~/Downloads/session-file.json
```

**See:** [session-share skill](../session-share/SKILL.md) for full documentation.

## Critical Rules

1. **Flags before arguments:** `session start -m "Hello" name` (not `name -m "Hello"`)
2. **Restart after MCP attach:** Always run `session restart` after `mcp attach`
3. **Never poll from other agents** - can interfere with target session

## Known Gotchas (v1.7.0+)

Friction points discovered during real usage. Work around them per the patterns below.

### `session send --no-wait` can leave prompts typed-but-not-submitted

On a freshly-launched Claude session, `agent-deck session send --no-wait <id> "..."` may paste the message into the input buffer before Claude is fully ready, leaving it TYPED but not SUBMITTED. Classic race.

**Workaround (always safe):**
```bash
agent-deck -p <profile> session send <id> "..." --no-wait -q
sleep 3
# Get the tmux session name and send Enter to submit
TMUX=$(agent-deck -p <profile> session show --json <id> | jq -r .tmux_session)
tmux send-keys -t "$TMUX" Enter
```

The Enter is idempotent — if already submitted, it's just a no-op newline. Use this pattern every time you `session send --no-wait` to a freshly-launched session.

**Alternative:** omit `--no-wait` so the built-in 60s readiness wait kicks in before submitting.

### Replacing the binary while agent-deck is running (`text file busy`)

If `/usr/local/bin/agent-deck` is a symlink to a build artifact and the binary is currently running (any tmux session, any daemon), a direct `cp` over it fails with `Text file busy`.

**Workaround — move-then-copy (keeps running processes on the old inode):**
```bash
INSTALL=$(which agent-deck)
TARGET=$(readlink -f "$INSTALL")
go build -ldflags "-X main.Version=X.Y.Z" -o /tmp/agent-deck-new ./cmd/agent-deck
mv "$TARGET" "$TARGET.old"
cp /tmp/agent-deck-new "$TARGET" && chmod +x "$TARGET"
agent-deck --version    # verify
rm "$TARGET.old"
```

Kernel tracks inodes, not names. Running processes keep a reference to the renamed inode; new invocations resolve through the original name to the new inode.

### Cross-machine config drift (macOS ↔ Linux)

If `~/.agent-deck/skills/sources.toml` (or other config files) were copied verbatim from a macOS machine, paths like `/Users/<name>/` won't exist on Linux (should be `/home/<user>/`). The symptom: `agent-deck skill list` returns "No skills found" while the pool directory is clearly populated.

**Check & fix:**
```bash
grep -n "/Users/" ~/.agent-deck/skills/sources.toml
# If any matches, substitute the Linux home path:
sed -i "s|/Users/<mac-user>|$HOME|g" ~/.agent-deck/skills/sources.toml
```

### Channel subscription for conductor/bot sessions (v1.7.0+)

For a session to receive Telegram/Discord/Slack messages as conversation turns (not just as MCP tool calls), it MUST be started with `--channels <plugin-id>`. Use the first-class field:

```bash
# At creation (preferred):
agent-deck -p personal add --channel plugin:telegram@claude-plugins-official -c claude -t my-bot /path

# Or after creation, then restart:
agent-deck -p personal session set my-bot channels plugin:telegram@claude-plugins-official
agent-deck -p personal session restart my-bot
```

The `channels` field persists and every `session start` / `session restart` rebuilds the claude invocation with `--channels`. Do NOT rely on `.mcp.json` telegram entries — those load the plugin as a regular MCP (tools only), not a channel (inbound delivery).

**Note — v1.7.0 display bug:** `agent-deck session show --json <id>` currently omits the `channels` field (fix pending). `agent-deck list --json | jq '.[] | select(.id==<id>)'` shows it correctly. Data is persisted fine regardless.

### Many competing telegram pollers after multiple session starts

Telegram's Bot API `getUpdates` is single-consumer per bot token. If N Claude sessions all load the telegram plugin, N `bun` pollers race for messages — deliveries land in whichever wins, not where you want them.

**Correct topology:** exactly ONE session loads the telegram channel plugin (normally the conductor, via `--channels` at start-time). All other sessions should NOT have telegram in their enabled plugins.

**Disable globally:** in `~/.claude/settings.json`:
```json
"enabledPlugins": {
  "telegram@claude-plugins-official": false
}
```

**Enable per-session:** via `--channel` on the specific session that should receive messages. See "Channel subscription" above.

**Debug:** `pgrep -af "bun.*telegram" | wc -l` should return 1. Anything higher means a race. Kill extras: `pkill -f "bun.*telegram"` then restart only the intended session.

### Telegram conductor topology (v1.7.22+)

**Supported topology — enforce this on every conductor host:**

- Telegram is activated **per-session** via `--channels plugin:telegram@claude-plugins-official`. This is the only supported activation path for a conductor bot.
- `TELEGRAM_STATE_DIR` is injected **exclusively** via `[conductors.<name>.claude].env_file` in `~/.agent-deck/config.toml`. The env file sources deterministically on both fresh-start and `--resume` spawns.
- One bot token = one channel-owning session. Never share tokens between sessions.
- `enabledPlugins."telegram@claude-plugins-official"` in the profile `settings.json` must be **absent or false**. Global enablement makes every claude subprocess (including child agents) load the plugin.

**Codified anti-patterns — agent-deck v1.7.22 emits warnings for these:**

| Anti-pattern | Code | Why it breaks |
|---|---|---|
| `enabledPlugins."telegram@claude-plugins-official" = true` in profile settings | `GLOBAL_ANTIPATTERN` | Every claude process loads the plugin, including every child agent the conductor spawns. Each one starts a `bun telegram` poller. |
| Global enablement **AND** `--channels plugin:telegram@...` on the same session | `DOUBLE_LOAD` | The plugin loads twice in one claude process. Two bun pollers race on one bot token and Telegram rejects with 409 Conflict. |
| `session set wrapper "TELEGRAM_STATE_DIR=... {command}"` | `WRAPPER_DEPRECATED` | Works on the resume path; silently fails on fresh-start due to `bash -c` argv splitting. The env var never reaches claude, so the plugin falls back to the default state dir and two conductors collide. Use `env_file` instead. |
| Relying on `.mcp.json` telegram entries for inbound delivery | — | `.mcp.json` loads the plugin as an MCP server (tool-use only). Inbound message → conversation-turn delivery requires `--channels`. |
| Using the same bot token for multiple concurrent sessions | — | `getUpdates` is single-consumer per token. |
| Assuming an empty `TELEGRAM_STATE_DIR` is fine | — | The plugin falls back to `~/.claude/channels/telegram/`; any DM approval there leaks across unrelated conductors. |

**Verifying steady state (conductor host):**

```bash
pgrep -af 'bun.*telegram' | grep -v grep | wc -l   # expect: exactly one per conductor bot
for PID in $(pgrep -f 'bun.*telegram.*start'); do
  echo "PID=$PID TSD=$(tr '\0' '\n' < /proc/$PID/environ | grep ^TELEGRAM_STATE_DIR= | cut -d= -f2-)"
done
# Each PID must show a distinct TELEGRAM_STATE_DIR; collisions indicate env_file is not being sourced.
```

**When agent-deck emits a `⚠  GLOBAL_ANTIPATTERN` / `DOUBLE_LOAD` / `WRAPPER_DEPRECATED` warning**, the problem is in your topology, not in agent-deck. Fix the profile settings or the conductor env_file; the warning is a leading indicator of the 409-Conflict symptom that follows minutes-to-hours later.

## References

- [cli-reference.md](references/cli-reference.md) - Complete CLI command reference
- [config-reference.md](references/config-reference.md) - All config.toml options
- [tui-reference.md](references/tui-reference.md) - TUI features and shortcuts
- [troubleshooting.md](references/troubleshooting.md) - Common issues and bug reporting
- [session-share skill](../session-share/SKILL.md) - Export/import sessions for collaboration
