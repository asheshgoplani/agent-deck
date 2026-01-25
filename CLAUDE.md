# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Development Commands

```bash
make build          # Build binary to ./build/agent-deck
make test           # Run all tests
make lint           # Run golangci-lint
make fmt            # Format code
make run            # Run in development mode
make dev            # Auto-reload on changes (requires air)
make install-user   # Install to ~/.local/bin (no sudo)
```

**Single test file:**
```bash
go test -v ./internal/session -run TestStorageSave
```

**Debug mode:**
```bash
AGENTDECK_DEBUG=1 go run ./cmd/agent-deck
```

## Git & Pull Requests

**IMPORTANT:** This is a fork. Never submit PRs to the upstream repository. When asked to create a PR, always target our fork's `main` branch only.

## Architecture Overview

Agent Deck is a terminal session manager for AI coding agents (Claude Code, Gemini CLI, OpenCode), built with Go and Bubble Tea TUI framework. It runs sessions inside tmux for persistence.

### Layer Structure

```
cmd/agent-deck/         CLI entry point & commands
    └─ main.go, *_cmd.go (session, group, mcp, worktree, try commands)

internal/
├── session/            Core business logic
│   ├── instance.go     Session data model (Instance struct)
│   ├── storage.go      JSON persistence (~/.agent-deck/profiles/)
│   ├── claude.go       Claude Code integration (session detection, MCP reading)
│   ├── gemini.go       Gemini CLI integration
│   ├── opencode_*.go   OpenCode integration
│   ├── groups.go       Hierarchical group management
│   ├── mcp_catalog.go  MCP server definitions from config
│   └── pool_manager.go MCP socket pooling for memory efficiency
│
├── ui/                 Bubble Tea TUI (Model-View-Update pattern)
│   ├── home.go         Main TUI model, event loop, keyboard handling
│   ├── list.go         Session/group tree rendering
│   ├── search.go       Fuzzy search with status filters
│   ├── mcp_dialog.go   MCP attach/detach modal
│   ├── forkdialog.go   Fork session modal (Claude only)
│   └── styles.go       Lipgloss color schemes
│
├── tmux/               tmux integration
│   ├── tmux.go         Session cache (O(1) lookups), session management
│   ├── detector.go     Status detection (running/waiting/idle/error)
│   └── pty.go          Pane content capture
│
├── mcppool/            Unix socket pooling for shared MCP processes
├── git/                Git worktree operations
├── experiments/        Quick experiments (try command)
├── platform/           OS detection (Unix vs Windows)
├── profile/            Claude profile detection
└── update/             Auto-update mechanism
```

### Key Data Flow

1. **Session Creation**: `Instance` created → saved to `sessions.json` → tmux session started → AI tool launched
2. **Status Detection**: tmux cache refreshed → pane content captured → pattern matching → status updated
3. **MCP Management**: Config parsed → MCPs toggled in `.claude.json` or `.mcp.json` → session restarted
4. **Session Fork** (Claude): Session files copied with new ID → new Instance linked to parent → MCPs restored

### Important Patterns

- **Session Cache**: `tmux.RefreshSessionCache()` is called once per tick, reducing O(n) subprocess calls to O(1)
- **Responsive Layout**: TUI adapts based on terminal width (<50, 50-79, 80+ columns)
- **Atomic Writes**: Storage uses temp files + rename for crash safety, keeps 3 backup generations
- **Platform Files**: Use `_unix.go` and `_windows.go` suffixes for platform-specific code

## Configuration

- **User config**: `~/.agent-deck/config.toml` (MCP definitions, defaults, pool settings)
- **Session data**: `~/.agent-deck/profiles/{profile}/sessions.json`
- **Logs**: `~/.agent-deck/logs/agentdeck_*.log`

## Testing Conventions

- Tests use temporary directories, no external dependencies
- Integration tests may spawn real tmux sessions (slower)
- Use `t.TempDir()` for file-based tests
- Platform-specific tests use build tags

## Native Desktop App (In Development)

A Wails-based native app (`cmd/agent-deck-desktop/`) complements the TUI with better UX:

- **xterm.js** for terminal emulation with Cmd+F searchable scrollback
- **Keeps tmux as backend** for session persistence, SSH support, and crash recovery
- Attaches to existing Agent Deck sessions via `tmux attach-session`
- Pre-loads scrollback into xterm.js buffer so search works through history

**Key files:**
- `cmd/agent-deck-desktop/` - Wails app (Go backend + React frontend)
- `docs/native-app-prototype-plan.md` - Implementation phases and architecture
- `docs/native-app-research.md` - Design rationale and competitive analysis

**Run in dev mode:**
```bash
cd cmd/agent-deck-desktop && wails dev
```
