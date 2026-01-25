# Agent Deck Desktop

A native macOS desktop application for managing AI coding agents. Agent Deck Desktop provides a modern GUI alternative to the TUI, with enhanced features like multi-pane layouts, live session previews, and full keyboard navigation.

## Installation

### macOS (Recommended)

**Prerequisites:**
- macOS 12.0 (Monterey) or later
- [tmux](https://github.com/tmux/tmux) installed (`brew install tmux`)

**Install via Homebrew:**
```bash
brew install --cask agent-deck-desktop
```

**Or download directly:**
1. Download the latest `.dmg` from [Releases](https://github.com/asheshgoplani/agent-deck/releases)
2. Open the DMG and drag Agent Deck to Applications
3. Launch from Applications folder

> **Note:** On first launch, macOS may show a security warning. Go to System Preferences > Security & Privacy > General and click "Open Anyway".

### Building from Source

```bash
# Install Wails CLI
go install github.com/wailsapp/wails/v2/cmd/wails@latest

# Clone and build
git clone https://github.com/asheshgoplani/agent-deck.git
cd agent-deck/cmd/agent-deck-desktop
wails build
```

## Features

### List+Preview Layout

The session selector uses a split-pane layout with sessions on the left and a live terminal preview on the right. Navigate sessions and see real-time output before attaching.

- **Left pane:** Session list with groups, status indicators, and search
- **Right pane:** Live scrollback preview of selected session
- **Smart sizing:** Preview pane auto-adjusts based on window width

### Multi-Pane Terminal Layout

Work with multiple sessions simultaneously in a flexible pane layout:

- **Split panes:** Divide your workspace horizontally or vertically
- **Layout presets:** Quick switch between 1, 2, or 4 pane layouts
- **Zoom mode:** Temporarily maximize a single pane
- **Drag to resize:** Adjust pane sizes with the mouse

### Status Bar

Each terminal pane displays a status bar with contextual information:

```
@hostname  ~/project/path  main  session-name  claude
```

- **Hostname:** Local machine name or remote host for SSH sessions
- **Path:** Current working directory (updates as you navigate)
- **Branch:** Git branch name with worktree indicator
- **Session:** Session name or custom label
- **Tool:** AI tool icon and name (claude, aider, etc.)

### Session Groups

Organize sessions hierarchically with collapsible groups:

- **Collapse/Expand All:** Use `Cmd+Shift+H` or the toggle button
- **Nested groups:** Create subgroups for complex project structures
- **Persistent state:** Group expansion state persists across restarts

### Quick Launch Bar

Pin frequently used projects for one-click access:

- Click to launch with default tool
- `Cmd+Click` to open tool picker
- Assign custom keyboard shortcuts

## Keyboard Shortcuts

### Navigation

| Shortcut | Action |
|----------|--------|
| `Cmd+K` | Open command menu |
| `Cmd+F` | Search in terminal scrollback |
| `Cmd+,` | Open settings |
| `Cmd+Esc` | Return to session list |
| `Arrow Keys` | Navigate session list |
| `Enter` | Attach to selected session |

### Tabs & Windows

| Shortcut | Action |
|----------|--------|
| `Cmd+T` | New tab |
| `Cmd+N` | New terminal session |
| `Cmd+W` | Close current tab |
| `Cmd+1-9` | Switch to tab N |
| `Cmd+[` | Previous tab |
| `Cmd+]` | Next tab |

### Panes

| Shortcut | Action |
|----------|--------|
| `Cmd+D` | Split pane right |
| `Cmd+Shift+D` | Split pane down |
| `Cmd+Option+Arrow` | Navigate between panes |
| `Cmd+Shift+W` | Close current pane |
| `Cmd+Shift+Z` | Zoom/unzoom pane |
| `Cmd+Option+=` | Balance pane sizes |

### Layout Presets

| Shortcut | Action |
|----------|--------|
| `Cmd+Option+1` | Single pane |
| `Cmd+Option+2` | 2 columns |
| `Cmd+Option+3` | 2 rows |
| `Cmd+Option+4` | 2x2 grid |

### Text Editing (Terminal)

| Shortcut | Action |
|----------|--------|
| `Option+Left` | Jump word backward |
| `Option+Right` | Jump word forward |
| `Cmd+Left` | Jump to line start |
| `Cmd+Right` | Jump to line end |
| `Option+Delete` | Delete word backward |
| `Cmd+Delete` | Delete to line start |

### Groups

| Shortcut | Action |
|----------|--------|
| `Cmd+Shift+H` | Toggle all groups (collapse/expand) |

### Font Size

| Shortcut | Action |
|----------|--------|
| `Cmd++` | Increase font size |
| `Cmd+-` | Decrease font size |
| `Cmd+0` | Reset font size |

### Help

| Shortcut | Action |
|----------|--------|
| `?` or `Cmd+/` | Show keyboard shortcuts help |
| `Esc` | Close dialogs/modals |

## Command Menu

Press `Cmd+K` to open the command menu for quick access to:

- **Sessions:** Search and attach to any session
- **Projects:** Open projects with your preferred AI tool
- **Actions:** Split panes, save layouts, manage sessions

**Menu shortcuts:**
- `Arrow Up/Down` - Navigate items
- `Enter` - Select item
- `Cmd+Enter` - Open with tool picker
- `Cmd+P` - Pin to Quick Launch bar
- `Esc` - Close palette

## Remote SSH Sessions

Agent Deck Desktop supports remote sessions via SSH. See the main [README](../../README.md#auto-discover-remote-sessions) for SSH configuration.

**Desktop-specific features:**

- **Connection status indicators:** Green dot for connected, red for disconnected
- **Auto-reconnection:** Automatically reconnects when SSH connection drops
- **Host picker:** Visual interface for selecting remote hosts when creating sessions

## Configuration

Agent Deck Desktop shares configuration with the CLI/TUI version. Settings are stored in:

```
~/.agent-deck/config.toml
```

### Desktop-specific settings

```toml
# Terminal settings
[terminal]
font_size = 14          # 8-32, default 14
soft_newline = "both"   # "shift_enter", "alt_enter", "both", "disabled"

# Theme
[desktop]
theme = "dark"          # "dark", "light", "auto"
```

## Troubleshooting

### App won't launch

1. **Check tmux is installed:**
   ```bash
   which tmux
   # Should output: /usr/local/bin/tmux or /opt/homebrew/bin/tmux
   ```

2. **Verify permissions:**
   - Go to System Preferences > Security & Privacy > Privacy
   - Ensure Agent Deck has Full Disk Access (if needed for your project paths)

### Sessions not appearing

1. **Check tmux sessions exist:**
   ```bash
   tmux list-sessions
   ```

2. **Verify session prefix:**
   Agent Deck manages sessions prefixed with `agentdeck_`. Sessions created outside Agent Deck won't appear.

### Terminal not rendering correctly

1. **Update terminal settings:**
   - Press `Cmd+,` to open settings
   - Try adjusting font size
   - Toggle theme if colors look wrong

2. **Check tmux config:**
   Ensure your `~/.tmux.conf` has proper terminal settings:
   ```bash
   set -g default-terminal "tmux-256color"
   ```

### Keyboard shortcuts not working

1. **Check for conflicts:**
   - System Preferences > Keyboard > Shortcuts
   - Disable conflicting global shortcuts

2. **Reset to defaults:**
   - Delete `~/Library/Preferences/com.agentdeck.desktop.plist`
   - Relaunch the app

### SSH sessions failing

1. **Test SSH connection:**
   ```bash
   ssh -o ConnectTimeout=5 user@host echo "Connected"
   ```

2. **Check config.toml:**
   ```toml
   [ssh_hosts.myhost]
   host = "192.168.1.100"
   user = "developer"
   identity_file = "~/.ssh/id_rsa"
   ```

3. **Verify SSH key:**
   ```bash
   ssh-add -l  # Should list your key
   ```

### Performance issues

1. **Too many sessions:**
   - Close unused sessions
   - Use groups to organize and collapse

2. **Large scrollback:**
   - Reduce history limit in tmux:
     ```bash
     set -g history-limit 10000
     ```

3. **MCP memory usage:**
   - Enable MCP socket pooling in config.toml
   - See [MCP Socket Pool](../../README.md#mcp-socket-pool-heavy-users)

## Development

### Live Development

To run in live development mode:

```bash
wails dev
```

This runs a Vite development server with hot reload for frontend changes. A dev server also runs on http://localhost:34115 for browser-based development with access to Go methods.

### Building

To build a redistributable, production mode package:

```bash
wails build
```

### Project Structure

```
cmd/agent-deck-desktop/
├── frontend/           # React frontend (Vite)
│   ├── src/
│   │   ├── App.jsx           # Main application component
│   │   ├── SessionSelector.jsx  # Session list + preview
│   │   ├── StatusBar.jsx     # Terminal status bar
│   │   ├── Terminal.jsx      # xterm.js terminal
│   │   └── hooks/            # React hooks
│   └── wailsjs/        # Auto-generated Go bindings
├── app.go              # Main Wails application
├── tmux.go             # tmux session management
├── ssh_bridge.go       # SSH remote session support
└── wails.json          # Wails configuration
```

## Logs

Application logs are stored at:
```
~/Library/Logs/AgentDeck/agent-deck-desktop.log
```

To enable verbose logging:
```bash
AGENT_DECK_DEBUG=1 open /Applications/Agent\ Deck.app
```

## License

MIT License - see [LICENSE](../../LICENSE)
