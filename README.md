<div align="center">

<!-- Status Grid Logo -->
<img src="site/logo.svg" alt="Agent Deck Logo" width="120">

# Agent Deck

**Your AI agent command center**

[![GitHub Stars](https://img.shields.io/github/stars/asheshgoplani/agent-deck?style=for-the-badge&logo=github&color=yellow&labelColor=1a1b26)](https://github.com/asheshgoplani/agent-deck/stargazers)
[![Go Version](https://img.shields.io/badge/Go-1.24+-00ADD8?style=for-the-badge&logo=go&labelColor=1a1b26)](https://go.dev)
[![License](https://img.shields.io/badge/License-MIT-9ece6a?style=for-the-badge&labelColor=1a1b26)](LICENSE)
[![Platform](https://img.shields.io/badge/Platform-macOS%20%7C%20Linux%20%7C%20WSL-7aa2f7?style=for-the-badge&labelColor=1a1b26)](https://github.com/asheshgoplani/agent-deck)
[![Latest Release](https://img.shields.io/github/v/release/asheshgoplani/agent-deck?style=for-the-badge&color=e0af68&labelColor=1a1b26)](https://github.com/asheshgoplani/agent-deck/releases)

[Features](#features) • [Installation](#installation) • [Usage](#usage) • [CLI Commands](#cli-commands) • [Documentation](#documentation)

</div>

---

<details>
<summary><b>💡 Ask AI about Agent Deck</b></summary>

**Option 1: Claude Code Skill** (recommended for Claude Code users)
```bash
/plugin marketplace add asheshgoplani/agent-deck
/plugin install agent-deck@agent-deck-help
```
Then ask: *"How do I set up MCP pooling?"*

**Option 2: Any LLM** (ChatGPT, Claude, Gemini, etc.)
```
Read https://raw.githubusercontent.com/asheshgoplani/agent-deck/main/llms-full.txt
and answer: How do I fork a session?
```

</details>

---

https://github.com/user-attachments/assets/e4f55917-435c-45ba-92cc-89737d0d1401

## The Problem

Running Claude Code on 10 projects? OpenCode on 5 more? Another agent somewhere in the background?

**Managing multiple AI sessions gets messy fast.** Too many terminal tabs. Hard to track what's running, what's waiting, what's done. Switching between projects means hunting through windows.

## The Solution

**Agent Deck is mission control for your AI coding agents.**

One terminal. All your agents. Complete visibility.

- 🎯 **See everything at a glance** - Running, waiting, or idle - know the status of every agent instantly
- ⚡ **Switch in milliseconds** - Jump between any session with a single keystroke
- 🔍 **Never lose track** - Search across all conversations, filter by status, find anything in seconds
- 🌳 **Stay organized** - Group sessions by project, client, or experiment with collapsible hierarchies
- 🔌 **Zero config switching** - Built on tmux - sessions persist through disconnects and reboots

## Features

### 🍴 Explore Multiple Solutions in Parallel

**Try different approaches without losing context.** Fork any Claude conversation instantly. Each fork inherits the full conversation history - perfect for comparing solutions or experimenting without risk.

![Fork Session Demo](demos/fork-session.gif)

- Press `f` for quick fork, `F` to customize name/group
- Fork your forks - explore as many branches as you need
- Session IDs auto-detected even after restarts

**Why this matters:** Ever wished you could try two different approaches to the same problem? Now you can. Fork, experiment, compare results, keep what works.

### 🔌 Add Superpowers On-Demand

**Attach MCP servers without touching config files.** Need web search? Browser automation? GitHub integration? Toggle them on per project or globally - Agent Deck handles the restart automatically.

https://github.com/user-attachments/assets/6a4af5ba-bacb-4234-ac72-a019d424d593

- Press `M` to open, `Space` to toggle any MCP server
- **LOCAL** scope (just this project) or **GLOBAL** (everywhere)
- Session auto-restarts with new capabilities loaded

**Why this matters:** Stop editing TOML files. Stop remembering restart commands. Just toggle what you need - Agent Deck takes care of the rest.

**Adding Available MCPs:**

Define your MCPs once in `~/.agent-deck/config.toml`, then toggle them per project:

```toml
# Web search
[mcps.exa]
command = "npx"
args = ["-y", "exa-mcp-server"]
env = { EXA_API_KEY = "your-api-key" }
description = "Web search via Exa AI"

# GitHub integration
[mcps.github]
command = "npx"
args = ["-y", "@modelcontextprotocol/server-github"]
env = { GITHUB_PERSONAL_ACCESS_TOKEN = "ghp_your_token" }
description = "GitHub repos, issues, PRs"

# Browser automation
[mcps.playwright]
command = "npx"
args = ["-y", "@playwright/mcp@latest"]
description = "Browser automation & testing"

# Memory across sessions
[mcps.memory]
command = "npx"
args = ["-y", "@modelcontextprotocol/server-memory"]
description = "Persistent memory via knowledge graph"
```

<details>
<summary>More MCP examples</summary>

```toml
# YouTube transcripts
[mcps.youtube-transcript]
command = "npx"
args = ["-y", "@kimtaeyoon83/mcp-server-youtube-transcript"]
description = "Get YouTube transcripts"

# Web scraping
[mcps.firecrawl]
command = "npx"
args = ["-y", "firecrawl-mcp"]
env = { FIRECRAWL_API_KEY = "your-key" }
description = "Web scraping and crawling"

# Notion
[mcps.notion]
command = "npx"
args = ["-y", "@notionhq/notion-mcp-server"]
env = { NOTION_TOKEN = "your-token" }
description = "Notion workspace access"

# Sequential thinking
[mcps.sequential-thinking]
command = "npx"
args = ["-y", "@modelcontextprotocol/server-sequential-thinking"]
description = "Step-by-step reasoning"

# Context7 - code docs
[mcps.context7]
command = "npx"
args = ["-y", "@upstash/context7-mcp@latest"]
description = "Up-to-date code documentation"

# Anthropic docs
[mcps.anthropic-docs]
command = "npx"
args = ["-y", "anthropic-docs-mcp", "--transport", "stdio"]
description = "Search Claude & Anthropic docs"

# ─────────────── HTTP/SSE MCPs ───────────────

# DeepWiki - GitHub repo docs (HTTP, no auth)
[mcps.deepwiki]
url = "https://mcp.deepwiki.com/mcp"
transport = "http"
description = "GitHub repo documentation"

# Asana - Project management (SSE, requires OAuth)
[mcps.asana]
url = "https://mcp.asana.com/sse"
transport = "sse"
description = "Asana project management"
```

</details>

### 🧠 MCP Socket Pool (Heavy Users)

**Running 20+ Claude sessions? Each one spawns separate MCP processes.** That's a lot of memory - 30 sessions with 5 MCPs each = 150 node processes eating gigabytes of RAM.

**MCP Socket Pool shares MCP processes across all sessions via Unix sockets.** One memory server. One exa server. One firecrawl server. All sessions share them.

```
Without pool:              With pool:
Session 1 → memory         Session 1 ─┐
Session 2 → memory         Session 2 ─┼─→ memory (shared)
Session 3 → memory         Session 3 ─┘
= 3 processes              = 1 process

Memory savings: 85-90% for MCP processes
```

**Platform Support:**

| Platform | Socket Pool | Notes |
|----------|-------------|-------|
| macOS | ✅ Full support | Works out of the box |
| Linux | ✅ Full support | Works out of the box |
| WSL2 | ✅ Full support | Works out of the box |
| WSL1 | ❌ Auto-disabled | Uses stdio mode (MCPs still work fine) |
| Windows | ❌ Not supported | Use WSL instead |

> **WSL1 users:** MCP pooling is automatically disabled because Unix sockets don't work reliably. MCPs still work perfectly in stdio mode - you just use more memory with many sessions. Upgrade to WSL2 for socket pooling: `wsl --set-version <distro> 2`

**Enable in `~/.agent-deck/config.toml`:**

```toml
[mcp_pool]
enabled = true     # Enable socket pooling
pool_all = true    # Pool all available MCPs

# Optional: exclude specific MCPs from pool
exclude_mcps = ["chrome-devtools"]
```

When enabled, all MCPs defined in `[mcps.*]` start as socket proxies at launch. Sessions connect via Unix sockets instead of spawning separate processes.

**Indicators:**
- 🔌 in MCP Manager shows pooled MCPs
- Sessions auto-use socket configs on restart

**Why this matters:** If you're a power user running many Claude sessions, this dramatically reduces memory usage. Your laptop stops struggling. Swap stops thrashing. Everything runs smoother.

### 🔍 Find Anything in Seconds

**Fuzzy search across all sessions.** Type a few letters, instantly filter. Need to find that bug fix conversation from last week? The session where you were experimenting with authentication? Just start typing.

Press `/` to search. Filter by status with `!` (running), `@` (waiting), `#` (idle), `$` (error).

**Why this matters:** When you're managing 20+ sessions across different projects, memory fails. Search doesn't.

### 🎯 Know What's Happening, Instantly

**Smart status detection shows you what every agent is doing right now.** No more guessing which session is waiting for input, which is thinking, which finished an hour ago.

| Status | Symbol | What It Means |
|--------|--------|---------------|
| **Running** | `●` green | Agent is actively working |
| **Waiting** | `◐` yellow | Needs your input |
| **Idle** | `○` gray | Ready for commands |
| **Error** | `✕` red | Something went wrong |

Works with Claude Code, Gemini CLI, OpenCode, Codex, Cursor, and any terminal tool.

**Why this matters:** Stop checking every session manually. See the full picture at a glance. Respond when needed. Stay in flow.

## Installation

**Works on:** macOS • Linux • Windows (WSL)

```bash
curl -fsSL https://raw.githubusercontent.com/asheshgoplani/agent-deck/main/install.sh | bash
```

The installer downloads the binary, installs tmux if needed, and configures tmux for mouse/clipboard support.

Then run: `agent-deck`

> **Windows:** [Install WSL](https://learn.microsoft.com/en-us/windows/wsl/install) first.

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

If you use Claude Code, install the agent-deck skill for AI-assisted session management:

```bash
/plugin marketplace add asheshgoplani/agent-deck
/plugin install agent-deck@agent-deck
```

This teaches Claude how to create sessions, manage MCPs, fork conversations, and orchestrate sub-agents.

**Spawn sub-agents with individual MCPs:**

https://github.com/user-attachments/assets/d8056955-c147-451a-b2f6-fad34bce8a15

*Two research agents running in parallel - one with Reddit MCP for community insights, another with GitHub MCP for code research. Each agent has its own context and tools.*

### Uninstalling

```bash
agent-deck uninstall              # Interactive uninstall
agent-deck uninstall --dry-run    # Preview what would be removed
agent-deck uninstall --keep-data  # Remove binary only, keep sessions
```

Or use the standalone script:
```bash
curl -fsSL https://raw.githubusercontent.com/asheshgoplani/agent-deck/main/uninstall.sh | bash
```

<details>
<summary>What gets removed</summary>

- **Binary:** `~/.local/bin/agent-deck` or `/usr/local/bin/agent-deck`
- **Homebrew:** `agent-deck` package (if installed via brew)
- **tmux config:** The `# agent-deck configuration` block in `~/.tmux.conf`
- **Data directory:** `~/.agent-deck/` (sessions, logs, config)

Use `--keep-data` to preserve your sessions and configuration.

</details>

## Usage

```bash
agent-deck                    # Launch TUI
agent-deck add .              # Add current directory as session
agent-deck add -c claude .    # Add with Claude Code
agent-deck list               # List all sessions
```

### Keyboard Shortcuts

| Key | Action |
|-----|--------|
| `j/k` or `↑/↓` | Navigate |
| `Enter` | Attach to session |
| `n` | New session |
| `g` | New group |
| `r` | Rename |
| `d` | Delete |
| `f` | Fork Claude session |
| `M` | MCP Manager |
| `/` | Search |
| `Ctrl+Q` | Detach from session |
| `?` | Help |

## CLI Commands

Agent Deck provides a full CLI for automation and scripting. All commands support `--json` for machine-readable output and `-p, --profile` for profile selection.

> **Note:** Flags must come BEFORE positional arguments (Go flag package standard).

### Quick Reference

```bash
agent-deck                              # Launch TUI
agent-deck add -c claude .              # Add session with Claude
agent-deck list --json                  # List sessions as JSON
agent-deck status                       # Quick status overview
agent-deck session attach my-project    # Attach to session
```

### Session Commands

Manage individual sessions. Sessions can be identified by:
- **Title**: `my-project` (exact or partial match)
- **ID prefix**: `a1b2c3` (first 6+ chars)
- **Path**: `/Users/me/project`

```bash
# Start/Stop/Restart
agent-deck session start <id>           # Start session's tmux process
agent-deck session stop <id>            # Stop/kill session process
agent-deck session restart <id>         # Restart (Claude: reloads MCPs)

# Fork (Claude only)
agent-deck session fork <id>            # Fork with inherited context
agent-deck session fork <id> -t "exploration"       # Custom title
agent-deck session fork <id> -g "experiments"       # Into specific group

# Attach/Show
agent-deck session attach <id>          # Attach interactively
agent-deck session show <id>            # Show session details
agent-deck session show                 # Auto-detect current session (in tmux)
agent-deck session current              # Auto-detect current session and profile
agent-deck session current -q           # Just session name (for scripting)
agent-deck session current --json       # JSON output (for automation)
```

**Fork flags:**
| Flag | Description |
|------|-------------|
| `-t, --title` | Custom title for forked session |
| `-g, --group` | Target group for forked session |

### MCP Commands

Manage Model Context Protocol servers for Claude sessions.

```bash
# List available MCPs (from config.toml)
agent-deck mcp list
agent-deck mcp list --json

# Show attached MCPs for a session
agent-deck mcp attached <id>
agent-deck mcp attached                 # Auto-detect current session

# Attach/Detach MCPs
agent-deck mcp attach <id> github       # Attach to LOCAL scope
agent-deck mcp attach <id> exa --global # Attach to GLOBAL scope
agent-deck mcp attach <id> memory --restart  # Attach and restart session

agent-deck mcp detach <id> github       # Detach from LOCAL
agent-deck mcp detach <id> exa --global # Detach from GLOBAL
```

**MCP flags:**
| Flag | Description |
|------|-------------|
| `--global` | Apply to global Claude config (all projects) |
| `--restart` | Restart session after change (loads new MCPs) |

### Group Commands

Organize sessions into hierarchical groups.

```bash
# List groups
agent-deck group list
agent-deck group list --json

# Create groups
agent-deck group create work            # Create root group
agent-deck group create frontend --parent work  # Create subgroup

# Delete groups
agent-deck group delete old-projects    # Delete (fails if has sessions)
agent-deck group delete old-projects --force    # Move sessions to default, then delete

# Move sessions
agent-deck group move my-session work   # Move session to group
```

**Group flags:**
| Flag | Description |
|------|-------------|
| `--parent` | Parent group for creating subgroups |
| `--force` | Force delete by moving sessions to default group |

### Status Command

Quick status check without launching the TUI.

```bash
agent-deck status                       # Compact: "2 waiting - 5 running - 3 idle"
agent-deck status -v                    # Verbose: detailed list by status
agent-deck status -q                    # Quiet: just waiting count (for prompts)
agent-deck status --json                # JSON output
```

### Global Flags

These flags work with all commands:

| Flag | Description |
|------|-------------|
| `--json` | Output as JSON (for automation) |
| `-q, --quiet` | Minimal output, rely on exit codes |
| `-p, --profile <name>` | Use specific profile |

### Examples

**Scripting with JSON output:**
```bash
# Get all running sessions
agent-deck list --json | jq '.[] | select(.status == "running")'

# Count waiting sessions
agent-deck status -q  # Returns just the number

# Check if specific session exists
agent-deck session show my-project --json 2>/dev/null && echo "exists"
```

**Automation workflows:**
```bash
# Start all sessions in a group
agent-deck list --json | jq -r '.[] | select(.group == "work") | .id' | \
  xargs -I{} agent-deck session start {}

# Attach MCP to all Claude sessions
agent-deck list --json | jq -r '.[] | select(.tool == "claude") | .id' | \
  xargs -I{} agent-deck mcp attach {} memory --restart
```

**Current session detection (inside tmux):**
```bash
# Auto-detect current session and profile (NEW!)
agent-deck session current              # Full info with auto-detected profile
agent-deck session current -q           # Just session name (for scripting)
agent-deck session current --json       # JSON output (for automation)

# Show current session info (legacy, still works)
agent-deck session show

# Show MCPs for current session
agent-deck mcp attached

# Use in workflows (auto-detect parent and profile)
PARENT=$(agent-deck session current -q)
PROFILE=$(agent-deck session current --json | jq -r '.profile')
agent-deck -p "$PROFILE" add -t "Subtask" --parent "$PARENT" -c claude /tmp/subtask
```

## Updates

Agent-deck checks for updates automatically and notifies you when a new version is available.

### Update Notification

When you open the TUI, a yellow banner appears if an update is available:
```
⬆ Update available: v0.8.1 → v0.8.2 (run: agent-deck update)
```

CLI commands (`list`, `status`) also show a notification:
```
💡 Update available: v0.8.1 → v0.8.2 (run: agent-deck update)
```

### Update Commands

```bash
agent-deck update              # Check and install update
agent-deck update --check      # Just check, don't install
agent-deck update --force      # Force check (bypass 24h cache)
```

### Configuration

Add to `~/.agent-deck/config.toml`:

```toml
[updates]
# Automatically install updates without prompting (default: false)
auto_update = true

# Enable update checks on startup (default: true)
check_enabled = true

# How often to check for updates in hours (default: 24)
check_interval_hours = 24

# Show update notification in CLI commands (default: true)
notify_in_cli = true
```

### Auto-Update

When `auto_update = true`, agent-deck will:
1. Check for updates on startup
2. Automatically download and install if available
3. Exit so you can restart with the new version

## FAQ

### How is this different from just using tmux?

Agent Deck adds **AI-specific intelligence** on top of tmux:
- **Smart status detection** - Knows when Claude is thinking vs. waiting for input (not just "session exists")
- **Session forking** - Duplicate Claude conversations with full context inheritance
- **MCP manager** - Visual interface for attaching/detaching Model Context Protocol servers
- **Global search** - Find conversations across all sessions instantly
- **Organized groups** - Hierarchical project organization instead of flat session lists

Think of it as **tmux + AI awareness**. The sessions run in tmux (reliability), but Agent Deck adds the layer that understands what AI agents are doing.

### Does it work with tools besides Claude Code?

**Yes!** Agent Deck works with any terminal-based tool:
- ✅ **Claude Code** - Full integration (session detection, MCP management, fork, resume)
- ✅ **Gemini CLI** - Full integration (session detection, MCP management, resume)
  - Session detection from `~/.gemini/tmp/<project-hash>/chats/`
  - Resume with `gemini --resume <id>`
  - MCP management via UI (press `M`)
  - Response extraction via `session output`
  - **Note:** No fork support (use sub-sessions instead)
- ✅ OpenCode
- ✅ Cursor (terminal mode)
- ✅ Codex
- ✅ Custom shell scripts
- ✅ Any command-line tool

Claude and Gemini get full integration with session management, MCP configuration, and response extraction. Other tools get status detection, organization, and search.

### Can I use it on Windows?

**Yes, via WSL (Windows Subsystem for Linux).**

1. [Install WSL](https://learn.microsoft.com/en-us/windows/wsl/install) (Ubuntu recommended)
2. Open WSL terminal
3. Run the installer: `curl -fsSL https://raw.githubusercontent.com/asheshgoplani/agent-deck/main/install.sh | bash`

Agent Deck runs inside WSL and works exactly like it does on macOS/Linux.

**WSL Version Differences:**

| Feature | WSL1 | WSL2 |
|---------|------|------|
| Core functionality | ✅ | ✅ |
| MCP management | ✅ | ✅ |
| Session forking | ✅ | ✅ |
| MCP socket pooling | ❌ Auto-disabled | ✅ |
| Clipboard (via clip.exe) | ✅ | ✅ |

**WSL2 is recommended** for the best experience. Check your version with `wsl --list --verbose`. Upgrade with `wsl --set-version <distro> 2`.

On WSL1, MCP socket pooling is automatically disabled (Unix sockets don't work reliably), but everything else works fine. MCPs run in stdio mode instead.

### Will it interfere with my existing tmux setup?

**No.** Agent Deck creates its own tmux sessions with the prefix `agentdeck_*`. Your existing sessions are untouched.

The installer adds optional tmux config (mouse support, clipboard integration) but:
- It backs up your existing `~/.tmux.conf` first
- You can skip it with `--skip-tmux-config` flag
- It only adds to your config, never removes

### How do I add more MCP servers?

Edit `~/.agent-deck/config.toml` and add your servers:

**Stdio MCPs** (local command-line tools):
```toml
[mcps.your-server]
command = "npx"
args = ["-y", "your-mcp-package"]
env = { API_KEY = "your-key" }
description = "What this server does"
```

**HTTP/SSE MCPs** (remote servers):
```toml
[mcps.remote-api]
url = "https://mcp.example.com/mcp"
transport = "http"  # or "sse"
description = "Remote MCP server"
```

Then press `M` in Agent Deck to toggle it on/off for any session. [See MCP examples](#adding-available-mcps).

### Why is Agent Deck using so much memory?

If you're running many Claude sessions (10+), each spawns its own MCP processes. This adds up fast.

**Enable MCP Socket Pool** to share processes across sessions (macOS, Linux, WSL2 only):

```toml
# ~/.agent-deck/config.toml
[mcp_pool]
enabled = true
pool_all = true
```

Restart Agent Deck. All sessions now share MCP processes via Unix sockets. Memory usage drops 85-90% for MCP-related processes.

> **Note:** On WSL1 and Windows, socket pooling is automatically disabled. MCPs work in stdio mode instead, which uses more memory but is fully functional.

### What if a session crashes?

tmux sessions persist even if Agent Deck closes. If a session crashes:

1. **Check logs**: `~/.agent-deck/logs/agentdeck_<session-name>_<id>.log`
2. **Restart it**: `agent-deck session restart <session-id>`
3. **Or delete and recreate**: `agent-deck remove <id>` then `agent-deck add <path>`

Sessions are stored in `~/.agent-deck/profiles/default/sessions.json` with automatic backups (`.bak`, `.bak.1`, `.bak.2`).

## Documentation

### Project Organization

```
▼ Projects (3)
  ├─ frontend     ●
  ├─ backend      ◐
  └─ api          ○
▼ Personal
  └─ blog         ○
```

Sessions are organized in collapsible groups. Create nested groups, reorder items, and import existing tmux sessions with `i`.

### Configuration

Data stored in `~/.agent-deck/`:

```
~/.agent-deck/
├── sessions.json     # Sessions and groups
└── config.toml       # User config (optional)
```

For custom Claude profile directory:

```toml
[claude]
config_dir = "~/.claude-work"
```

### tmux Configuration

The installer configures tmux automatically. For manual setup, see the [tmux configuration guide](https://github.com/asheshgoplani/agent-deck/wiki/tmux-Configuration).

## Troubleshooting

### Mouse Scrolling Not Working (WSL)

If scrolling doesn't work or you can't scroll back through output on WSL:

**Quick Fix: Re-run the installer**
```bash
curl -fsSL https://raw.githubusercontent.com/asheshgoplani/agent-deck/main/install.sh | bash
```

The installer will detect outdated tmux config and offer to update it with the WSL-compatible version.

**Manual Fix:**

1. Remove old agent-deck config from `~/.tmux.conf`:
   ```bash
   sed -i '/# agent-deck configuration/,/# End agent-deck configuration/d' ~/.tmux.conf
   ```

2. Add the updated config:
   ```bash
   cat >> ~/.tmux.conf << 'EOF'
   # agent-deck configuration
   # agent-deck-tmux-config-version: 2

   set -g default-terminal "tmux-256color"
   set -ag terminal-overrides ",xterm*:Tc:smcup@:rmcup@"
   set -ag terminal-overrides ",*256col*:Tc"
   set -sg escape-time 0
   set -g history-limit 50000
   set -g mouse on

   # Auto-enter copy-mode on scroll up (critical for WSL)
   bind-key -n WheelUpPane if-shell -F -t = "#{mouse_any_flag}" "send-keys -M" "if -Ft= '#{pane_in_mode}' 'send-keys -M' 'copy-mode -e'"

   # Scroll bindings in copy-mode
   bind-key -T copy-mode-vi WheelUpPane send-keys -X scroll-up
   bind-key -T copy-mode-vi WheelDownPane send-keys -X scroll-down
   bind-key -T copy-mode WheelUpPane send-keys -X scroll-up
   bind-key -T copy-mode WheelDownPane send-keys -X scroll-down

   # Clipboard (WSL uses clip.exe)
   bind-key -T copy-mode-vi MouseDragEnd1Pane send-keys -X copy-pipe-and-cancel "clip.exe"
   bind-key -T copy-mode MouseDragEnd1Pane send-keys -X copy-pipe-and-cancel "clip.exe"
   # End agent-deck configuration
   EOF
   ```

3. Reload tmux:
   ```bash
   tmux source-file ~/.tmux.conf
   ```

**Why this happens:** The original config only had `set -g mouse on`, which works on macOS but not reliably on all WSL terminal emulators. The fix adds explicit `WheelUpPane` bindings that enter copy-mode when scrolling up.

### Clipboard Not Working (WSL)

If copying to clipboard doesn't work in WSL:

1. **Verify clip.exe is accessible:**
   ```bash
   echo "test" | clip.exe && powershell.exe Get-Clipboard
   # Should output: test
   ```

2. **If clip.exe not found**, ensure Windows system32 is in PATH:
   ```bash
   export PATH="$PATH:/mnt/c/Windows/System32"
   ```

### tmux History Too Short

If you can't scroll back far enough:

1. The default is now 50,000 lines. To increase:
   ```bash
   # In ~/.tmux.conf
   set -g history-limit 100000
   ```

2. Reload: `tmux source-file ~/.tmux.conf`

**Note:** Higher limits use more memory per pane.

### Sessions Lost After Reboot

tmux sessions don't persist through system reboots. However, agent-deck preserves your session **metadata** (titles, groups, paths).

After reboot:
1. Your sessions will show as "error" status (tmux process gone)
2. Press `r` on any session to restart it
3. Claude/Gemini sessions will resume from last conversation if session ID was captured

To preserve conversation context across reboots, ensure sessions have captured session IDs (shown in session details with `agent-deck session show`).

## Development

```bash
make build    # Build
make test     # Test
make lint     # Lint
```

## Contributing

Contributions welcome! Found a bug? Have a feature idea? Want to improve the docs?

1. Fork the repo
2. Create a branch (`git checkout -b feature/amazing-feature`)
3. Make your changes
4. Open a PR

See [CONTRIBUTING.md](CONTRIBUTING.md) for details.

## Star History

If Agent Deck saves you time, **give us a star!** ⭐ It helps others discover the project.

[![Star History Chart](https://api.star-history.com/svg?repos=asheshgoplani/agent-deck&type=Date)](https://star-history.com/#asheshgoplani/agent-deck&Date)

## License

MIT License - see [LICENSE](LICENSE)

---

<div align="center">

Built with [Bubble Tea](https://github.com/charmbracelet/bubbletea) and [tmux](https://github.com/tmux/tmux)

**[Documentation](https://github.com/asheshgoplani/agent-deck/wiki) • [Issues](https://github.com/asheshgoplani/agent-deck/issues) • [Discussions](https://github.com/asheshgoplani/agent-deck/discussions)**

</div>
