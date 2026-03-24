<div align="center">

<!-- Status Grid Logo -->
<img src="site/logo.svg" alt="Agent Deck Logo" width="120">

# Agent Deck

**Your AI agent command center**

[![GitHub Stars](https://img.shields.io/github/stars/asheshgoplani/agent-deck?style=for-the-badge&logo=github&color=yellow&labelColor=1a1b26)](https://github.com/asheshgoplani/agent-deck/stargazers)
[![Downloads](https://img.shields.io/github/downloads/asheshgoplani/agent-deck/total?style=for-the-badge&logo=github&color=bb9af7&labelColor=1a1b26)](https://github.com/asheshgoplani/agent-deck/releases)
[![Go Version](https://img.shields.io/badge/Go-1.24+-00ADD8?style=for-the-badge&logo=go&labelColor=1a1b26)](https://go.dev)
[![License](https://img.shields.io/badge/License-MIT-9ece6a?style=for-the-badge&labelColor=1a1b26)](LICENSE)
[![Platform](https://img.shields.io/badge/Platform-macOS%20%7C%20Linux%20%7C%20WSL-7aa2f7?style=for-the-badge&labelColor=1a1b26)](https://github.com/asheshgoplani/agent-deck)
[![Latest Release](https://img.shields.io/github/v/release/asheshgoplani/agent-deck?style=for-the-badge&color=e0af68&labelColor=1a1b26)](https://github.com/asheshgoplani/agent-deck/releases)
[![Discord](https://img.shields.io/discord/1469423271144587379?style=for-the-badge&logo=discord&logoColor=white&label=Discord&color=5865F2&labelColor=1a1b26)](https://discord.gg/e4xSs6NBN8)

[Features](#features) . [Conductor](#conductor) . [Install](#installation) . [Quick Start](#quick-start) . [Docs](#documentation) . [Discord](https://discord.gg/e4xSs6NBN8) . [FAQ](#faq)

</div>

<details>
<summary><b>Ask AI about Agent Deck</b></summary>

**Option 1: Claude Code Skill** (recommended for Claude Code users)
```bash
/plugin marketplace add asheshgoplani/agent-deck
/plugin install agent-deck@agent-deck-help
```
Then ask: *"How do I set up MCP pooling?"*

**Option 2: OpenCode** (has built-in Claude skill compatibility)
```bash
# Create skill directory
mkdir -p ~/.claude/skills/agent-deck/references

# Download skill and references
curl -sL https://raw.githubusercontent.com/asheshgoplani/agent-deck/main/skills/agent-deck/SKILL.md \
  > ~/.claude/skills/agent-deck/SKILL.md
for f in cli-reference config-reference tui-reference troubleshooting; do
  curl -sL "https://raw.githubusercontent.com/asheshgoplani/agent-deck/main/skills/agent-deck/references/${f}.md" \
    > ~/.claude/skills/agent-deck/references/${f}.md
done
```
OpenCode will auto-discover the skill from `~/.claude/skills/`.

**Option 3: Any LLM** (ChatGPT, Claude, Gemini, etc.)
```
Read https://raw.githubusercontent.com/asheshgoplani/agent-deck/main/llms-full.txt
and answer: How do I fork a session?
```

</details>

https://github.com/user-attachments/assets/e4f55917-435c-45ba-92cc-89737d0d1401

## The Problem

Running Claude Code on 10 projects? OpenCode on 5 more? Another agent somewhere in the background?

**Managing multiple AI sessions gets messy fast.** Too many terminal tabs. Hard to track what's running, what's waiting, what's done. Switching between projects means hunting through windows.

## The Solution

**Agent Deck is mission control for your AI coding agents.**

One terminal. All your agents. Complete visibility.

- **See everything at a glance** — running, waiting, or idle status for every agent instantly
- **Switch in milliseconds** — jump between any session with a single keystroke
- **Stay organized** — groups, search, notifications, and git worktrees keep everything manageable

## Features

### Fork Sessions

Try different approaches without losing context. Fork any Claude conversation instantly. Each fork inherits the full conversation history.

- Press `f` for quick fork, `F` to customize name/group
- Fork your forks to explore as many branches as you need

### MCP Manager

Attach MCP servers without touching config files. Need web search? Browser automation? Toggle them on per project or globally. Agent Deck handles the restart automatically.

- Press `m` to open, `Space` to toggle, `Tab` to cycle scope (LOCAL/GLOBAL), type to jump
- Define your MCPs once in `~/.agent-deck/config.toml`, then toggle per session — see [Configuration Reference](skills/agent-deck/references/config-reference.md)

### Skills Manager

Attach/detach Claude skills per project with a managed pool workflow.

- Press `s` to open Skills Manager for a Claude session
- Available list is pool-only (`~/.agent-deck/skills/pool`) to keep attach/detach deterministic
- Apply writes project state to `.agent-deck/skills.toml` and materializes into `.claude/skills`
- Type-to-jump is supported in the dialog (same pattern as MCP Manager)

### MCP Socket Pool

Running many sessions? Socket pooling shares MCP processes across all sessions via Unix sockets, reducing MCP memory usage by 85-90%. Connections auto-recover from MCP crashes in ~3 seconds via a reconnecting proxy. Enable with `pool_all = true` in [config.toml](skills/agent-deck/references/config-reference.md).

### Search

Press `/` to fuzzy-search across all sessions. Filter by status with `!` (running), `@` (waiting), `#` (idle), `$` (error). Press `G` for global search across all Claude conversations.

### Status Detection

Smart polling detects what every agent is doing right now:

| Status | Symbol | What It Means |
|--------|--------|---------------|
| **Running** | `●` green | Agent is actively working |
| **Waiting** | `◐` yellow | Needs your input |
| **Idle** | `○` gray | Ready for commands |
| **Error** | `✕` red | Something went wrong |

### Notification Bar

Waiting sessions appear right in your tmux status bar. Press `Ctrl+b`, release, then press `1`–`6` to jump directly to them.

```
⚡ [1] frontend [2] api [3] backend
```

### Git Worktrees

Multiple agents can work on the same repo without conflicts. Each worktree is an isolated working directory with its own branch.

- `agent-deck add . -c claude --worktree feature/a --new-branch` creates a session in a new worktree
- `agent-deck add . --worktree feature/b -b --location subdirectory` places the worktree under `.worktrees/` inside the repo
- `agent-deck worktree finish "My Session"` merges the branch, removes the worktree, and deletes the session
- `agent-deck worktree cleanup` finds and removes orphaned worktrees

Configure the default worktree location in `~/.agent-deck/config.toml`:

```toml
[worktree]
default_location = "subdirectory"  # "sibling" (default), "subdirectory", or a custom path
```

`sibling` creates worktrees next to the repo (`repo-branch`). `subdirectory` creates them inside it (`repo/.worktrees/branch`). A custom path like `~/worktrees` or `/tmp/worktrees` creates repo-namespaced worktrees at `<path>/<repo_name>/<branch>`. The `--location` flag overrides the config per session.

#### Worktree Setup Script

Gitignored files (`.env`, `.mcp.json`, etc.) aren't copied into new worktrees. To automate this, create a setup script at `.agent-deck/worktree-setup.sh` in your repo. Agent-deck runs it automatically after creating a worktree.

```sh
#!/bin/sh
for f in .env .env.local .mcp.json; do
    [ -f "$AGENT_DECK_REPO_ROOT/$f" ] && cp "$AGENT_DECK_REPO_ROOT/$f" "$AGENT_DECK_WORKTREE_PATH/$f"
done
```

The script receives two environment variables:
- `AGENT_DECK_REPO_ROOT` — path to the main repository
- `AGENT_DECK_WORKTREE_PATH` — path to the new worktree

The script runs via `sh -e` with a 60-second timeout. If it fails, the worktree is still created — you'll see a warning but the session proceeds normally.

### Docker Sandbox

Run sessions inside isolated Docker containers. The project directory is bind-mounted read-write, so agents work on your code while the rest of the system stays protected.

- Check "Run in Docker sandbox" when creating a session, or set `default_enabled = true` in config
- Press `T` on a sandboxed session to open a container shell
- `agent-deck try "task description"` runs a one-shot sandboxed session

Host tool auth (Claude, Gemini, Codex, etc.) is automatically shared into containers via shared sandbox directories — no re-authentication needed. On macOS, Keychain credentials are extracted too.

```toml
[docker]
default_enabled = true
mount_ssh = true
auto_cleanup = true    # Remove containers when sessions end (default: true)
```

Set `auto_cleanup = false` to keep containers alive after session termination, which is useful for debugging container state or inspecting logs.

See the [Docker Sandbox Guide](skills/agent-deck/references/sandbox.md) for the full reference including overlay details, custom images, and troubleshooting.

### Conductor

Conductors are persistent agent sessions that monitor and orchestrate all your other sessions. They watch for sessions that need help, auto-respond when confident, and escalate to you when they can't. Optionally connect **Telegram** and/or **Slack** for remote control.

Create as many conductors as you need per profile:

```bash
# First-time setup (asks about Telegram/Slack, then creates the conductor)
agent-deck -p work conductor setup ops --description "Ops monitor"

# Add more conductors to the same profile (no prompts)
agent-deck -p work conductor setup infra --description "Infra watcher"
agent-deck conductor setup personal --description "Personal project monitor"

# Run a conductor on Codex instead of Claude Code
agent-deck -p work conductor setup review --agent codex --description "Codex reviewer"

# Use a custom agent endpoint via environment variables
agent-deck conductor setup glm-bot \
  -env ANTHROPIC_BASE_URL=https://api.z.ai/api/anthropic \
  -env ANTHROPIC_AUTH_TOKEN=<token> \
  -env ANTHROPIC_DEFAULT_OPUS_MODEL=glm-5

# Or use an env file
agent-deck conductor setup glm-bot -env-file ~/.conductor.env
```

Each conductor gets its own directory, identity, and settings:

```
~/.agent-deck/conductor/
├── CLAUDE.md           # Shared knowledge for Claude conductors
├── AGENTS.md           # Shared knowledge for Codex conductors
├── bridge.py           # Bridge daemon (Telegram/Slack, if configured)
├── ops/
│   ├── CLAUDE.md       # Identity: "You are ops, a conductor for the work profile"
│   ├── meta.json       # Config: name, profile, description, env vars
│   ├── state.json      # Runtime state
│   └── task-log.md     # Action log
└── review/
    ├── AGENTS.md
    └── meta.json
```

Claude conductors use `CLAUDE.md`. Codex conductors use `AGENTS.md`. Shared `POLICY.md` and `LEARNINGS.md` remain agent-neutral.

**CLI commands:**

```bash
agent-deck conductor list                    # List all conductors
agent-deck conductor list --profile work     # Filter by profile
agent-deck conductor status                  # Health check (all)
agent-deck conductor status ops              # Health check (specific)
agent-deck conductor teardown ops            # Stop a conductor
agent-deck conductor teardown --all --remove # Remove everything
```

**Telegram bridge** (optional): Connect a Telegram bot for mobile monitoring. The bridge routes messages to specific conductors using a `name: message` prefix:

```
ops: check the frontend session      → routes to conductor-ops
infra: restart all error sessions    → routes to conductor-infra
/status                              → aggregated status across all profiles
```

**Slack bridge** (optional): Connect a Slack bot for channel-based monitoring via Socket Mode. The bot listens in a dedicated channel and replies in threads to keep the channel clean. Uses the same `name: message` routing, plus slash commands:

```
ops: check the frontend session      → routes to conductor-ops (reply in thread)
/ad-status                           → aggregated status across all profiles
/ad-sessions                         → list all sessions
/ad-restart [name]                   → restart a conductor
/ad-help                             → list available commands
```

<details>
<summary><b>Slack setup</b></summary>

1. Create a Slack app at [api.slack.com/apps](https://api.slack.com/apps)
2. Enable **Socket Mode** → generate an app-level token (`xapp-...`)
3. Under **OAuth & Permissions**, add bot scopes: `chat:write`, `channels:history`, `channels:read`, `app_mentions:read`
4. Under **Event Subscriptions**, subscribe to bot events: `message.channels`, `app_mention`
5. If using slash commands, create: `/ad-status`, `/ad-sessions`, `/ad-restart`, `/ad-help`
6. Install the app to your workspace
7. Invite the bot to your channel (`/invite @botname`)
8. Run `agent-deck conductor setup <name>` and enter your bot token (`xoxb-...`), app token (`xapp-...`), and channel ID (`C01234...`)

</details>

Both Telegram and Slack can run simultaneously — the bridge daemon handles both concurrently and relays responses on-demand, plus periodic heartbeat alerts to configured platforms.

**Built-in status-driven notifications**: conductor setup also installs a transition notifier daemon (`agent-deck notify-daemon`) that watches status transitions and sends parent nudges when child sessions move `running -> waiting|error|idle`.

**Heartbeat-driven monitoring**: heartbeats still run on the configured interval (default 15 minutes) as a secondary safety net. If a conductor response includes `NEED:`, the bridge forwards that alert to Telegram and/or Slack.

**Permission prompts during automation**: if a conductor keeps pausing on permission requests, set `[claude].allow_dangerous_mode = true` (or `dangerous_mode = true`) in `~/.agent-deck/config.toml`, then run `agent-deck session restart conductor-<name>`. See [Troubleshooting](skills/agent-deck/references/troubleshooting.md#conductor-keeps-asking-for-permissions).

**Legacy external watcher scripts**: optional only. `~/.agent-deck/events/` is not required for notification routing.

**Launching sessions from inside a conductor**:

```bash
# Inherit current conductor as parent (default when AGENT_DECK_SESSION_ID is set)
agent-deck -p work launch . -t "child-task" -c claude -m "Do task"

# Keep parent notifications and still force a custom group
agent-deck -p work launch . -t "review-phantom" -g ard -c claude -m "Review dataset"

# Tool command with extra args is supported directly
agent-deck -p work launch . -c "codex --dangerously-bypass-approvals-and-sandbox"
```

When `--cmd` includes extra args, agent-deck auto-wraps the tool command so args are preserved reliably.
Use `--no-parent` only when you explicitly want to disable parent routing/notifications.

### Multi-Tool Support

Agent Deck works with any terminal-based AI tool:

| Tool | Integration Level |
|------|-------------------|
| **Claude Code** | Full (status, MCP, fork, resume) |
| **Gemini CLI** | Full (status, MCP, resume) |
| **OpenCode** | Status detection, organization |
| **Codex** | Status detection, organization, conductor |
| **Cursor** (terminal) | Status detection, organization |
| **Custom tools** | Configurable via `[tools.*]` in config.toml |

### Cost Tracking Dashboard

Track token usage and costs across all your AI agent sessions in real-time.

- **Automatic collection** — Claude Code hook integration reads transcript files on each turn. Gemini/Codex/MiniMax support via output parsing (untested)
- **13 models priced** — Claude Opus/Sonnet/Haiku, Gemini Pro/Flash, GPT-4o/4.1, o3, o4-mini, MiniMax M2.7/M2.7-highspeed/M2.5/M2.5-highspeed with daily price refresh
- **TUI dashboard** — press `$` to view today/week/month costs, top sessions, model breakdown
- **Web dashboard** — `/costs` page with Chart.js charts, group drill-down, session detail views, SSE live updates
- **Budget limits** — configurable daily/weekly/monthly/per-group/per-session limits with 80% warning and 100% hard stop (untested)
- **Historical sync** — `agent-deck costs sync` backfills cost data from existing Claude transcript files
- **Export** — CSV/JSON export from web dashboard

```toml
# Optional config (~/.agent-deck/config.toml)
[costs]
retention_days = 90

[costs.budgets]
daily_limit = 50.00
weekly_limit = 200.00

[costs.pricing.overrides]
"custom-model" = { input_per_mtok = 1.0, output_per_mtok = 5.0 }
```

## Installation

**Works on:** macOS, Linux, Windows (WSL)

```bash
curl -fsSL https://raw.githubusercontent.com/asheshgoplani/agent-deck/main/install.sh | bash
```

Then run: `agent-deck`

<details>
<summary>Other install methods</summary>

**Homebrew**
```bash
brew install asheshgoplani/tap/agent-deck
```

**Go**
```bash
go install github.com/asheshgoplani/agent-deck/cmd/agent-deck@latest
```

**From Source**
```bash
git clone https://github.com/asheshgoplani/agent-deck.git && cd agent-deck && make install
```

</details>

### Claude Code Skill

Install the agent-deck skill for AI-assisted session management:

```bash
/plugin marketplace add asheshgoplani/agent-deck
/plugin install agent-deck@agent-deck
```

<details>
<summary>Uninstalling</summary>

```bash
agent-deck uninstall              # Interactive uninstall
agent-deck uninstall --keep-data  # Remove binary only, keep sessions
```

See [Troubleshooting](skills/agent-deck/references/troubleshooting.md#uninstalling) for full details.

</details>

## Quick Start

```bash
agent-deck                        # Launch TUI
agent-deck add . -c claude        # Add current dir with Claude
agent-deck session fork my-proj   # Fork a Claude session
agent-deck mcp attach my-proj exa # Attach MCP to session
agent-deck skill attach my-proj docs --source pool --restart # Attach skill + restart
agent-deck web                    # Start web UI on http://127.0.0.1:8420
```

### Web Mode

Open the left menu + browser terminal UI:

```bash
agent-deck web
```

Read-only browser mode (output only):

```bash
agent-deck web --read-only
```

Change the listen address (default: `127.0.0.1:8420`):

```bash
agent-deck web --listen 127.0.0.1:9000
```

Protect API + WebSocket access with a bearer token:

```bash
agent-deck web --token my-secret
# then open: http://127.0.0.1:8420/?token=my-secret
```

### Key Shortcuts

| Key | Action |
|-----|--------|
| `Enter` | Attach to session |
| `n` | New session |
| `f` / `F` | Fork (quick / dialog) |
| `m` | MCP Manager |
| `s` | Skills Manager (Claude) |
| `$` | Cost Dashboard |
| `M` | Move session to group |
| `S` | Settings |
| `/` / `G` | Search / Global search |
| `r` | Restart session |
| `d` | Delete |
| `S` | Settings |
| `T` | Container shell (sandboxed sessions) |
| `?` | Full help |

See [TUI Reference](skills/agent-deck/references/tui-reference.md) for all shortcuts and [CLI Reference](skills/agent-deck/references/cli-reference.md) for all commands.

## Documentation

| Guide | What's Inside |
|-------|---------------|
| [CLI Reference](skills/agent-deck/references/cli-reference.md) | Commands, flags, scripting examples |
| [Configuration](skills/agent-deck/references/config-reference.md) | config.toml, MCP setup, custom tools, socket pool, skills registry paths, docker |
| [Docker Sandbox](skills/agent-deck/references/sandbox.md) | Containers, overlays, custom images, troubleshooting |
| [TUI Reference](skills/agent-deck/references/tui-reference.md) | Keyboard shortcuts, status indicators, navigation |
| [Troubleshooting](skills/agent-deck/references/troubleshooting.md) | Common issues, debugging, recovery, uninstalling |

Additional resources:
- [CONTRIBUTING.md](CONTRIBUTING.md) — how to contribute
- [CHANGELOG.md](CHANGELOG.md) — release history
- [llms-full.txt](llms-full.txt) — full context for LLMs

### Updates

Agent Deck checks for updates automatically.
- Standalone/manual install: run `agent-deck update` to install.
- Homebrew install: run `brew upgrade asheshgoplani/tap/agent-deck`.
- Optional: set `auto_update = true` in [config.toml](skills/agent-deck/references/config-reference.md) for automatic update prompts.

## FAQ

<details>
<summary><b>How is this different from just using tmux?</b></summary>

Agent Deck adds AI-specific intelligence on top of tmux: smart status detection (knows when Claude is thinking vs. waiting), session forking with context inheritance, MCP management, global search across conversations, and organized groups. Think of it as tmux plus AI awareness.

</details>

<details>
<summary><b>Can I use it on Windows?</b></summary>

Yes, via WSL (Windows Subsystem for Linux). [Install WSL](https://learn.microsoft.com/en-us/windows/wsl/install), then run the installer inside WSL. WSL2 is recommended for full feature support including MCP socket pooling.

</details>

<details>
<summary><b>Can I use different Claude accounts/configs per profile?</b></summary>

Yes. Set a global Claude config dir, then add optional per-profile overrides in `~/.agent-deck/config.toml`:

```toml
[claude]
config_dir = "~/.claude"             # Global default

[profiles.work.claude]
config_dir = "~/.claude-work"        # Work account
```

Run with the target profile:

```bash
agent-deck -p work
```

You can verify which Claude config path is active with:

```bash
agent-deck hooks status
agent-deck hooks status -p work
```

See [Configuration Reference](skills/agent-deck/references/config-reference.md#claude-section) for full details.

</details>

<details>
<summary><b>Will it interfere with my existing tmux setup?</b></summary>

No. Agent Deck creates its own tmux sessions with the prefix `agentdeck_*`. Your existing sessions are untouched. The installer backs up your `~/.tmux.conf` before adding optional config, and you can skip it with `--skip-tmux-config`.

</details>

## Development

```bash
make build    # Build
make test     # Test
make lint     # Lint
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for details.

## Star History

If Agent Deck saves you time, give us a star! It helps others discover the project.

[![Star History Chart](https://api.star-history.com/svg?repos=asheshgoplani/agent-deck&type=Date)](https://star-history.com/#asheshgoplani/agent-deck&Date)

## License

MIT License — see [LICENSE](LICENSE)

---

<div align="center">

Built with [Bubble Tea](https://github.com/charmbracelet/bubbletea) and [tmux](https://github.com/tmux/tmux)

**[Docs](skills/agent-deck/references/) . [Discord](https://discord.gg/e4xSs6NBN8) . [Issues](https://github.com/asheshgoplani/agent-deck/issues) . [Discussions](https://github.com/asheshgoplani/agent-deck/discussions)**

</div>
