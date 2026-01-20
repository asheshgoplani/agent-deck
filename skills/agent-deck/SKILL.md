---
name: agent-deck
description: Terminal session manager for AI coding agents. Use when user mentions "agent-deck", "session", "sub-agent", "MCP attach", "git worktree", or needs to (1) create/start/stop/restart/fork sessions, (2) attach/detach MCPs, (3) manage groups/profiles, (4) get session output, (5) configure agent-deck, (6) troubleshoot issues, (7) launch sub-agents, or (8) create/manage worktree sessions. Covers CLI commands, TUI shortcuts, config.toml options, and automation.
compatibility: claude, opencode
---

# Agent Deck

Terminal session manager for AI coding agents. Built with Go + Bubble Tea.

**Version:** 0.8.14 | **Repo:** [github.com/asheshgoplani/agent-deck](https://github.com/asheshgoplani/agent-deck)

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

**Status:** `●` running | `◐` waiting | `○` idle | `✕` error

## Sub-Agent Launch

**Use when:** User says "launch sub-agent", "create sub-agent", "spawn agent"

```bash
scripts/launch-subagent.sh "Title" "Prompt" [--mcp name] [--wait]
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
| `M` | MCP Manager |
| `f/F` | Fork Claude session |
| `d` | Delete |
| `m` | Move to group |

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
# Export current session to file
skills/session-share/scripts/export.sh
# Output: ~/session-shares/session-<date>-<title>.json

# Import received session
skills/session-share/scripts/import.sh ~/Downloads/session-file.json
```

**See:** [session-share skill](../session-share/SKILL.md) for full documentation.

## Critical Rules

1. **Flags before arguments:** `session start -m "Hello" name` (not `name -m "Hello"`)
2. **Restart after MCP attach:** Always run `session restart` after `mcp attach`
3. **Never poll from other agents** - can interfere with target session

## References

- [cli-reference.md](references/cli-reference.md) - Complete CLI command reference
- [config-reference.md](references/config-reference.md) - All config.toml options
- [tui-reference.md](references/tui-reference.md) - TUI features and shortcuts
- [troubleshooting.md](references/troubleshooting.md) - Common issues and bug reporting
- [session-share skill](../session-share/SKILL.md) - Export/import sessions for collaboration
