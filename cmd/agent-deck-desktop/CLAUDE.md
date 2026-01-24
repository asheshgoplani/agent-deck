# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Development Commands

```bash
wails dev                   # Run in dev mode with hot reload
wails build                 # Build production binary
cd frontend && npm install  # Install frontend dependencies
```

**Debug log location:** `/tmp/agent-deck-desktop-debug.log` (written by terminal.go)

## Architecture Overview

RevDen (Agent Deck Desktop) is a Wails v2 native app wrapping the Agent Deck TUI. It provides xterm.js-based terminal emulation with searchable scrollback, connecting to existing tmux sessions managed by Agent Deck.

### Key Constraint

**tmux is the backend for session persistence** - the app attaches to existing Agent Deck sessions via `tmux attach-session`, never manages sessions directly. This enables crash recovery, SSH support, and sharing sessions with the TUI.

### Layer Structure

```
Go Backend (app.go, main.go)
├── Terminal (terminal.go)      PTY + tmux polling for display updates
│   ├── StartTmuxSession()      Polling mode: history → attach → poll
│   ├── pollTmuxLoop()          80ms polling, diff viewport updates
│   └── sanitizeHistoryForXterm() Strip escape sequences for scrollback
├── TmuxManager (tmux.go)       Session CRUD, reads sessions.json
├── ProjectDiscovery            Frecency-ranked project scanning
├── QuickLaunchManager          Pinned favorites with shortcuts
├── LaunchConfigManager         Per-tool launch configurations
└── DesktopSettingsManager      Theme, font size, soft newline mode

React Frontend (frontend/src/)
├── App.jsx                     Main state: tabs, sessions, modals, keyboard shortcuts
├── Terminal.jsx                xterm.js with polling event handling
│   ├── terminal:history        Initial scrollback from tmux
│   ├── terminal:data           Streaming updates (history gaps + viewport diffs)
│   └── Wheel interception      Programmatic scrollLines() for reliable rendering
├── UnifiedTopBar.jsx           Session tabs + quick launch bar
├── CommandPalette.jsx          Cmd+K fuzzy search (sessions + projects)
├── SessionSelector.jsx         Main session list view
└── SettingsModal.jsx           Theme, font size, soft newline preferences
```

### Data Flow: Terminal Polling Mode

The app uses polling instead of direct PTY streaming to solve xterm.js scrollback accumulation issues:

1. **Connect**: `StartTmuxSession()` → resize tmux → capture full history → emit `terminal:history` → attach PTY (input only) → start polling
2. **Poll loop** (80ms): Check alt-screen state → fetch history gap (new scrollback) → diff viewport → emit `terminal:data`
3. **Viewport diff**: `HistoryTracker.DiffViewport()` compares current vs last viewport, generates minimal ANSI update sequence
4. **History gap**: Lines that scrolled off viewport between polls are emitted verbatim for scrollback

### Key Files

| File | Purpose |
|------|---------|
| `terminal.go` | PTY management, tmux polling, escape sequence sanitization |
| `history_tracker.go` | Viewport diffing for efficient polling updates |
| `tmux.go` | Session listing, creation, git info, sessions.json parsing |
| `project_discovery.go` | Frecency-based project scanning from config.toml paths |
| `launch_config.go` | Launch configurations (dangerous mode, MCP configs, extra args) |
| `frontend/src/Terminal.jsx` | xterm.js setup, event handlers, wheel interception |
| `frontend/src/App.jsx` | State management, tab handling, keyboard shortcuts |

### Configuration

- **User config**: `~/.agent-deck/config.toml` (project_discovery.scan_paths)
- **Session data**: `~/.agent-deck/profiles/default/sessions.json`
- **Desktop settings**: `~/.agent-deck/desktop-settings.json` (theme, font size, soft newline)
- **Quick launch**: `~/.agent-deck/quick_launch.json`
- **Launch configs**: `~/.agent-deck/launch_configs.json`
- **Frecency data**: `~/.agent-deck/frecency.json`

### Important Patterns

- **Polling over streaming**: PTY output is discarded; display comes from `tmux capture-pane` polling. This prevents TUI escape sequences from corrupting xterm.js scrollback.
- **Wheel interception**: Native wheel events cause rendering corruption in WKWebView. The app intercepts wheel and calls `xterm.scrollLines()` programmatically.
- **History sanitization**: `sanitizeHistoryForXterm()` strips cursor positioning, screen clearing, and alternate buffer sequences while preserving colors.
- **Tab state**: Tabs are React state (`openTabs`), not persisted. Each tab holds a session reference and unique ID.

### Keyboard Shortcuts (App.jsx)

- `Cmd+K` - Command palette
- `Cmd+T` - New tab via palette
- `Cmd+W` - Close current tab
- `Cmd+1-9` - Switch to tab by number
- `Cmd+[` / `Cmd+]` - Previous/next tab
- `Cmd+F` - Search in terminal
- `Cmd+,` - Back to session selector
- `Cmd+Shift+,` - Settings
- `Cmd++` / `Cmd+-` - Font size
