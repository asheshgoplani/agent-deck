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
[![Discord](https://img.shields.io/discord/1469423271144587379?style=for-the-badge&logo=discord&logoColor=white&label=Discord&color=5865F2&labelColor=1a1b26)](https://discord.gg/e4xSs6NBN8)

[Features](#features) . [Install](#installation) . [Quick Start](#quick-start) . [Docs](#documentation) . [Discord](https://discord.gg/e4xSs6NBN8) . [FAQ](#faq)

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

- Press `M` to open, `Space` to toggle, `Tab` to cycle scope (LOCAL/GLOBAL)
- Define your MCPs once in `~/.agent-deck/config.toml`, then toggle per session — see [Configuration Reference](skills/agent-deck/references/config-reference.md)

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

Waiting sessions appear right in your tmux status bar. Press `Ctrl+b 1-6` to jump directly to them.

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

### Multi-Tool Support

Agent Deck works with any terminal-based AI tool:

| Tool | Integration Level |
|------|-------------------|
| **Claude Code** | Full (status, MCP, fork, resume) |
| **Gemini CLI** | Full (status, MCP, resume) |
| **OpenCode** | Status detection, organization |
| **Codex** | Status detection, organization |
| **Cursor** (terminal) | Status detection, organization |
| **Custom tools** | Configurable via `[tools.*]` in config.toml |

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
```

### Key Shortcuts

| Key | Action |
|-----|--------|
| `Enter` | Attach to session |
| `n` | New session |
| `f` / `F` | Fork (quick / dialog) |
| `M` | MCP Manager |
| `/` / `G` | Search / Global search |
| `r` | Restart session |
| `d` | Delete |
| `?` | Full help |

See [TUI Reference](skills/agent-deck/references/tui-reference.md) for all shortcuts and [CLI Reference](skills/agent-deck/references/cli-reference.md) for all commands.

## Documentation

| Guide | What's Inside |
|-------|---------------|
| [CLI Reference](skills/agent-deck/references/cli-reference.md) | Commands, flags, scripting examples |
| [Configuration](skills/agent-deck/references/config-reference.md) | config.toml, MCP setup, custom tools, socket pool |
| [TUI Reference](skills/agent-deck/references/tui-reference.md) | Keyboard shortcuts, status indicators, navigation |
| [Troubleshooting](skills/agent-deck/references/troubleshooting.md) | Common issues, debugging, recovery, uninstalling |

Additional resources:
- [CONTRIBUTING.md](CONTRIBUTING.md) — how to contribute
- [CHANGELOG.md](CHANGELOG.md) — release history
- [llms-full.txt](llms-full.txt) — full context for LLMs

### Updates

Agent Deck checks for updates automatically. Run `agent-deck update` to install, or set `auto_update = true` in [config.toml](skills/agent-deck/references/config-reference.md) for automatic updates.

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

**[Docs](skills/agent-deck/references/) . [Issues](https://github.com/asheshgoplani/agent-deck/issues) . [Discussions](https://github.com/asheshgoplani/agent-deck/discussions)**

</div>
