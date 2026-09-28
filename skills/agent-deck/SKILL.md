---
name: agent-deck
description: Terminal session manager for AI coding agents. Use when user mentions "agent-deck", "session", "sub-agent", "MCP attach", "git worktree", or needs to (1) create/start/stop/restart/fork sessions, (2) attach/detach MCPs, (3) manage groups/profiles, (4) get session output, (5) configure agent-deck, (6) troubleshoot issues, (7) launch sub-agents, or (8) create/manage worktree sessions. Covers CLI commands, TUI shortcuts, config.toml options, and automation.
metadata:
  compatibility: "claude, codex, opencode"
---

# Agent Deck

Terminal session manager for AI coding agents. Built with Go + Bubble Tea.

**Repo:** [github.com/asheshgoplani/agent-deck](https://github.com/asheshgoplani/agent-deck) | **Discord:** [discord.gg/e4xSs6NBN8](https://discord.gg/e4xSs6NBN8)

> Run `agent-deck --version` for your installed version. This skill targets v1.16.11+; most patterns work back to v1.7. See [Backward Compatibility](references/gotchas.md#backward-compatibility) for what's gated behind ≥1.16.11.

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
| `agent-deck launch . -c claude --account <name>` | Create and start a session under a named account slot |
| `agent-deck accounts [--json]` | List configured named account slots |
| `agent-deck session start/stop/restart <name>` | Control session |
| `agent-deck session send <name> "message"` | Send message |
| `agent-deck session send <name> --message-file <file>` | Send message from file (`-` = stdin); no shell quoting. Also on `launch`/`session start` |
| `agent-deck session output <name>` | Get bounded, ANSI-clean last response (JSON/quiet/copy preserve full source) |
| `agent-deck session children --json` | Child sessions' live status + asserted completions (non-blocking, read-only) |
| `agent-deck session current [-q\|--json]` | Auto-detect current session |
| `agent-deck session fork <name>` | Fork Claude/OpenCode/Pi/Codex/Oh My Pi conversation |
| `agent-deck session switch-account <name> <account>` | Switch Claude account, conversation follows |
| `agent-deck mcp list` | List available MCPs |
| `agent-deck mcp attach <name> <mcp>` | Attach MCP (then restart) |
| `agent-deck status` | Quick status summary |
| `agent-deck add --worktree <branch>` | Create session in git worktree |
| `agent-deck try <name>` | Scratch session in a dated experiment folder |
| `agent-deck worktree list` | List worktrees with sessions |
| `agent-deck worktree cleanup` | Find orphaned worktrees/sessions |
| `agent-deck feedback` | Submit feedback (opens rating prompt + optional comment) |
| `agent-deck session context <name>` | Context inspector: what is in a session's context window and what it costs |

**Status:** `●` running | `◐` waiting | `○` idle | `✕` error

## Session Identity Inside a Harness

Every session agent-deck launches (claude, codex, pi, gemini) already carries a short identity block in its instructions: session id, title, tool, group, profile, account, parent session, project path, the core CLI commands, `agent-deck session current --json` for the live record, and the completion sentinel. Custom `--cmd` sessions get the same block via `$AGENTDECK_IDENTITY_FILE`. A child therefore does not need to be told who it is or how to reach its parent; a prompt only has to state the task. Opt out per session with `--no-identity` or globally with `[launch] inject_identity = false`; gemini additionally needs its identity folder trusted (see `documentation/HARNESS_IDENTITY.md`).

## Critical Rules

1. **Flags before arguments:** `session start -m "Hello" name` (not `name -m "Hello"`)
2. **Restart after MCP attach:** Always run `session restart` after `mcp attach`
3. **Never poll from other agents** - can interfere with target session

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

## Configuration

**File:** `$XDG_CONFIG_HOME/agent-deck/config.toml` (default `~/.config/agent-deck/config.toml`; legacy `~/.agent-deck/config.toml` still honored)

```toml
[claude]
config_dir = "~/.claude-team"    # Custom Claude profile
dangerous_mode = true            # --dangerously-skip-permissions
use_chrome = false               # --chrome
use_teammate_mode = false        # --teammate-mode tmux
extra_args = ["--agent", "reviewer"]

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
cat ~/.config/agent-deck/config.toml | grep -v "KEY\|TOKEN\|SECRET"  # Sanitized config (legacy: ~/.agent-deck/config.toml)

# Create issue at:
# https://github.com/asheshgoplani/agent-deck/issues/new
```

**Include:**
1. What you tried (command/action)
2. What happened vs expected
3. Output of commands above
4. Relevant log: `tail -100 ~/.agent-deck/logs/agentdeck_<session>_*.log`

See [troubleshooting.md](references/troubleshooting.md) for detailed diagnostics.

## Contributing to agent-deck

Going beyond a bug report to a fix? agent-deck ships a dedicated contributor skill that mirrors the repo's PR intake gate and the maintainer's review machine, so an agent that follows it passes intake on the first try and scores well on all four review lenses (correctness, security, fit, intent).

Load it from the repo checkout:

```bash
# In an agent-deck clone
cat .github/skills/agent-deck-contributor/SKILL.md
```

It walks the full loop and enforces the bar: understand and reproduce the issue first, capture the human's actual ask verbatim (it goes in the PR body), one scoped problem per PR, a test that FAILS without your change, self-check locally before opening (`.github/skills/agent-deck-contributor/scripts/self-check.sh`), disclose the AI model that wrote the change, and respond directly to review verdicts. Run tests sandboxed — never against a real home directory:

```bash
HOME=$(mktemp -d) XDG_CONFIG_HOME= XDG_DATA_HOME= XDG_CACHE_HOME= go test ./...
```

## Where to Look Next

This file is the core. Read the matching reference before acting on anything beyond the commands above.

| When the task involves | Read |
|---|---|
| What agent-deck and each session's CLI (claude, codex, gemini) can do; choosing the `-c` tool for a child | [capabilities.md](references/capabilities.md) |
| Sessions messaging each other, `send` delivery guarantees, `output`, `children`, `inbox drain`, `handoff`, send pitfalls | [session-communication.md](references/session-communication.md) |
| Launching a sub-agent (`launch-subagent.sh`), retrieval modes, worker prompt conventions, the `===AGENTDECK_DONE===` completion sentinel, consulting Codex or Gemini, root-level peers (`-no-parent`) | [sub-agents.md](references/sub-agents.md) |
| Fanning out several children and supervising them non-blockingly | [fleet skill](../fleet/SKILL.md) |
| Conductors (`conductor setup`), Telegram/Slack channels, watchers (webhook, GitHub, ntfy, Slack) | [conductors.md](references/conductors.md) |
| Context inspection (`session context`), worktrees, scratch sessions (`try`), runtime health (`health`, dead letters, `remote update --from-build`), recall hints and search, session sharing, switching a session to another Claude account | [session-workflows.md](references/session-workflows.md) |
| Self-improvement (transcript mining), goals (goal-driven worker autonomy), trust-but-verify for completion claims | [autonomy.md](references/autonomy.md), then [self-improvement.md](references/self-improvement.md) or [goal.md](references/goal.md) for the deep dive |
| Using agent-deck as a daemon supervisor (it is not one), known gotchas and workarounds (`--no-wait` Enter fallback, `text file busy`, `-c "claude <subcommand>"`, config drift, channel subscription, Telegram conductor topology, v1.9.x findings), and what needs a deck newer than 1.16.11 | [gotchas.md](references/gotchas.md) |
| Any CLI command or flag | [cli-reference.md](references/cli-reference.md) |
| Any `config.toml` option | [config-reference.md](references/config-reference.md) |
| TUI keys, dialogs, search, and layout | [tui-reference.md](references/tui-reference.md) |
| Diagnosing a problem or filing a bug | [troubleshooting.md](references/troubleshooting.md) |
| Docker sandboxed sessions | [sandbox.md](references/sandbox.md) |
| Durable session hints and tags, the cross-harness transcript index, `recall context --into current`, federated search, the recall MCP server | [recall skill](recall/SKILL.md) |
| Exporting or importing a session for another developer | [session-share skill](../session-share/SKILL.md) |

**User guides (full how-to, in the repo):**

- [docs/conductor/](https://github.com/asheshgoplani/agent-deck/blob/main/docs/conductor/) - Conductor quickstart, channel pairing, state files, multi-conductor setups
- [documentation/WATCHERS.md](https://github.com/asheshgoplani/agent-deck/blob/main/documentation/WATCHERS.md) - Event-forwarding framework: doorbell model, built-in adapters, custom watchers
- [documentation/SKILLS.md](https://github.com/asheshgoplani/agent-deck/blob/main/documentation/SKILLS.md) - User-level vs pool skills, attach/detach, when to use which tier
- [documentation/WATCHDOG.md](https://github.com/asheshgoplani/agent-deck/blob/main/documentation/WATCHDOG.md) - Optional Python daemon that auto-restarts critical sessions
