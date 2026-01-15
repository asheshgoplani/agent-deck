# Agent Deck - Engineering Knowledge Base

**Created:** 2026-01-15
**Purpose:** In-depth technical documentation for fast iteration

---

## Overview

Agent Deck is a terminal session manager for AI coding agents (Claude, Gemini, custom tools). Built with Go + Bubble Tea TUI framework.

### Core Value Proposition
- **Unified session management** for multiple AI agents
- **MCP (Model Context Protocol)** pooling for memory efficiency
- **Status detection** with intelligent activity tracking
- **Profile isolation** for work/personal separation
- **CLI + TUI** dual interface for scripting and interactive use

---

## Architecture Diagram

```
┌─────────────────────────────────────────────────────────────────────┐
│                         User Interface                              │
├──────────────────────────────┬──────────────────────────────────────┤
│    TUI (Bubble Tea)          │          CLI (cmd/agent-deck)        │
│    internal/ui/              │                                       │
│    - home.go (5.4K LOC)      │    - main.go (root dispatcher)       │
│    - dialogs, overlays       │    - session_cmd.go                   │
│    - styles (Tokyo Night)    │    - mcp_cmd.go, group_cmd.go        │
└──────────────┬───────────────┴──────────────────┬───────────────────┘
               │                                   │
               └──────────────┬────────────────────┘
                              │
┌─────────────────────────────▼───────────────────────────────────────┐
│                       Session Layer                                  │
│                    internal/session/                                 │
├─────────────────────────────────────────────────────────────────────┤
│  Instance (session lifecycle)    │  Storage (atomic JSON persist)   │
│  GroupTree (hierarchy)           │  UserConfig (TOML parsing)       │
│  MCP Catalog (config gen)        │  Pool Manager (socket pooling)   │
└──────────────────────────────────┴──────────────────┬───────────────┘
                                                       │
               ┌───────────────────────────────────────┼───────────────┐
               │                                       │               │
┌──────────────▼──────────────┐    ┌───────────────────▼───────────┐  │
│      tmux Integration       │    │       MCP Pool Layer          │  │
│      internal/tmux/         │    │      internal/mcppool/        │  │
├─────────────────────────────┤    ├───────────────────────────────┤  │
│  Session CRUD               │    │  Socket Proxy (per-MCP)       │  │
│  Status detection (7 mech)  │    │  JSON-RPC request routing     │  │
│  Content normalization      │    │  Health monitoring            │  │
│  PTY attach/detach          │    │  External socket discovery    │  │
│  Log watching               │    │  Platform detection           │  │
└─────────────────────────────┘    └───────────────────────────────┘  │
                                                                       │
                              ┌────────────────────────────────────────┘
                              │
┌─────────────────────────────▼───────────────────────────────────────┐
│                       Platform Layer                                 │
│                    internal/platform/                                │
├─────────────────────────────────────────────────────────────────────┤
│  WSL1/WSL2 detection    │  macOS/Linux detection                    │
│  Unix socket support    │  Clipboard capabilities                   │
└─────────────────────────────────────────────────────────────────────┘
```

---

## Research Documents

| Document | Focus Area | Key Topics |
|----------|------------|------------|
| [ui-layer.md](./ui-layer.md) | TUI Architecture | Bubble Tea model, message flow, dialogs, performance |
| [session-layer.md](./session-layer.md) | Data Layer | Instance lifecycle, storage, groups, tool support |
| [tmux-integration.md](./tmux-integration.md) | tmux Integration | Status detection, PTY handling, log watching |
| [mcp-system.md](./mcp-system.md) | MCP Pooling | Socket proxy, JSON-RPC routing, health monitoring |
| [cli-commands.md](./cli-commands.md) | CLI Interface | Command dispatch, session resolution, scripting |

---

## Quick Reference

### Project Structure

```
agent-deck/
├── cmd/agent-deck/           # CLI entry point
│   ├── main.go               # Root dispatcher, TUI launch
│   ├── session_cmd.go        # session subcommands
│   ├── mcp_cmd.go            # mcp subcommands
│   ├── group_cmd.go          # group subcommands
│   └── cli_utils.go          # Session resolution, output
│
├── internal/
│   ├── ui/                   # TUI (Bubble Tea)
│   │   ├── home.go           # Main model (5.4K LOC)
│   │   ├── styles.go         # Tokyo Night theme
│   │   ├── *dialog.go        # Dialog components
│   │   └── storage_watcher.go # Multi-instance sync
│   │
│   ├── session/              # Data layer
│   │   ├── instance.go       # Session struct, lifecycle
│   │   ├── groups.go         # GroupTree hierarchy
│   │   ├── storage.go        # JSON persistence
│   │   ├── userconfig.go     # TOML config
│   │   ├── mcp_catalog.go    # MCP config generation
│   │   └── pool_manager.go   # Socket pool singleton
│   │
│   ├── tmux/                 # tmux integration
│   │   ├── tmux.go           # Session CRUD, status
│   │   ├── detector.go       # Tool/prompt detection
│   │   ├── pty.go            # PTY attach/detach
│   │   └── watcher.go        # Log file monitoring
│   │
│   ├── mcppool/              # MCP socket pooling
│   │   ├── socket_proxy.go   # Unix socket proxy
│   │   └── pool_simple.go    # Pool manager
│   │
│   └── platform/             # Platform detection
│       └── platform.go       # WSL1/WSL2/macOS/Linux
│
└── ~/.agent-deck/            # User data
    ├── config.toml           # User configuration
    ├── profiles/             # Profile storage
    │   ├── default/
    │   │   └── sessions.json
    │   └── work/
    │       └── sessions.json
    └── logs/                 # Session logs
```

### Key Patterns

| Pattern | Where Used | Purpose |
|---------|------------|---------|
| **Capture-Resume** | Claude/Gemini start | Reliable session ID capture |
| **Atomic Write** | Storage.Save() | Crash-safe persistence |
| **Socket Pooling** | MCP processes | 97% memory reduction |
| **Spike Detection** | Status tracking | Prevent false GREEN status |
| **Background Worker** | Status updates | Decoupled from UI thread |
| **TTL Caching** | Preview, analytics | Balance freshness vs CPU |

### Performance Metrics

| Metric | Value | Notes |
|--------|-------|-------|
| **CPU (idle)** | 0.5% | Was 15% before optimizations |
| **Subprocess calls/tick** | 1-2 | Was 60 before caching |
| **MCP memory** | 97% reduction | With socket pooling |
| **Status detection latency** | <100ms | 7 detection mechanisms |
| **Storage write** | Atomic | 3-generation backup |

---

## Customization Points

### Adding a New Tool

```toml
# ~/.agent-deck/config.toml
[tools.my-ai]
command = "my-ai"
icon = "🧠"
busy_patterns = ["thinking...", "processing..."]
prompt_patterns = ["> ", "Ready:"]
resume_flag = "--continue"
session_id_env = "MYAI_SESSION"
session_id_json_path = ".id"
output_format_flag = "--json"
dangerous_flag = "--yes"
```

### Adding MCPs

```toml
# ~/.agent-deck/config.toml
[mcps.my-server]
command = "npx"
args = ["-y", "@company/mcp-server"]
env = { API_KEY = "..." }
description = "My MCP server"
```

### Adding a Dialog

1. Create `internal/ui/mydialog.go` with Show/Hide/IsVisible/Update/View
2. Add to Home struct in `home.go`
3. Initialize in NewHome()
4. Add visibility check in View()
5. Add key handler in Update()

### Adding a CLI Command

1. Add case in main() dispatcher
2. Implement handler function following Load → Process → Save pattern
3. Add help text

---

## Critical Data Protection

**From CLAUDE.md - NEVER DO:**
- `tmux kill-server` - Destroys ALL sessions
- `tmux kill-session` with patterns - Destroys ALL sessions
- Commit secrets or personal docs
- Skip TestMain files (test isolation)

**Recovery:**
- Session logs: `~/.agent-deck/logs/`
- Storage backups: `sessions.json.bak{,.1,.2}`

---

## Development Commands

```bash
# Build
make build      # → ./build/agent-deck

# Test
make test       # All tests
go test ./internal/session/... -v   # Session tests
go test ./internal/ui/... -v        # UI tests

# Run
agent-deck                  # TUI
agent-deck -p work          # Work profile
agent-deck list --json      # CLI with JSON
```

---

## Extension Ideas

Based on the architecture analysis, high-value customization areas:

1. **Custom status indicators** - Add tool-specific busy/prompt patterns
2. **New session types** - Sub-sessions, task hierarchies
3. **MCP enhancements** - HTTP transport, custom routing
4. **UI themes** - Beyond Tokyo Night
5. **CLI automation** - Batch operations, scripting helpers
6. **Profile sync** - Cross-machine session sync
7. **Analytics** - Token usage, session metrics
