# Native App Research: Agent Deck Desktop

Research notes for potentially converting Agent Deck from a terminal TUI to a native desktop application with a web-based UI.

## Motivation

The current Agent Deck is a terminal TUI (Bubble Tea) that runs inside tmux. While powerful, it has UX limitations:

- **Single window**: Everything in one terminal pane
- **Difficult scrollback search**: tmux search (`Ctrl+B, [, /`) is cumbersome
- **Arcane shortcuts**: tmux prefix combinations are hard to remember
- **Limited visual layouts**: No easy multi-window or tiling

## Proposed Solution

Build a native desktop application using **Wails** (Go + web frontend) with **xterm.js** for terminal emulation.

### Why Wails?

| Option | Pros | Cons |
|--------|------|------|
| **Wails** | Native Go backend (reuse existing code), lightweight (~30MB RAM), native window chrome | Less mature than Electron |
| Electron | Mature ecosystem | Heavy (~200MB+ RAM), requires Node.js |
| Tauri | Very lightweight | Rust-based, harder Go integration |
| Pure browser | No install needed | Requires localhost server, less native feel |

Wails is the best fit because Agent Deck is already Go - we keep all business logic and just replace the UI layer.

### Why xterm.js?

xterm.js is the industry-standard terminal emulator for web:
- Powers VS Code's integrated terminal
- Used by GitHub Codespaces, Hyper, and many others
- Full terminal emulation (not just display)
- Supports true color, ligatures, mouse events, selection
- Built-in search addon for scrollback

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│  Wails Native App                                               │
│  ┌───────────────────────────────────────────────────────────┐  │
│  │  Web UI (React/Svelte/Vue)                                │  │
│  │  - Tabs, sidebar, session list                            │  │
│  │  - Cmd+T, Cmd+W, Cmd+1/2/3 shortcuts                      │  │
│  │  - Search modal (Cmd+F)                                   │  │
│  ├───────────────────────────────────────────────────────────┤  │
│  │  xterm.js Terminal Components                             │  │
│  │  - One per session tab                                    │  │
│  │  - Full interactive terminal                              │  │
│  └───────────────────────────────────────────────────────────┘  │
│         │                                                       │
│         │ WebSocket (pty data)                                  │
│         ▼                                                       │
│  ┌───────────────────────────────────────────────────────────┐  │
│  │  Go Backend (existing Agent Deck code)                    │  │
│  │  - session/* (Instance, Storage, Groups)                  │  │
│  │  - tmux/* (session management, status detection)          │  │
│  │  - mcppool/* (MCP socket pooling)                         │  │
│  │  - git/* (worktree operations)                            │  │
│  └───────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
```

### Code Reuse

| Package | Reusable? | Notes |
|---------|-----------|-------|
| `internal/session/*` | 100% | Core business logic, storage, groups |
| `internal/tmux/*` | 100% | Session management, status detection |
| `internal/mcppool/*` | 100% | MCP socket pooling |
| `internal/git/*` | 100% | Worktree operations |
| `internal/ui/*` | 0% | Replace entirely with web UI |

The core is clean and well-separated. Main work is building the web UI layer.

## Key Benefits

### 1. Superior Keyboard Shortcuts

| Current (tmux) | Native App |
|----------------|------------|
| `Ctrl+B, D` detach | `Cmd+W` close tab |
| `Ctrl+B, [` scroll mode | Just scroll |
| `Ctrl+B, c` new window | `Cmd+T` new session |
| `Ctrl+B, 1` switch window | `Cmd+1` switch tab |
| No easy search | `Cmd+F` search |

### 2. Full Scrollback Search (Cmd+F)

This is a major UX win. The implementation:

1. When session tab opens, backend captures full tmux scrollback:
   ```bash
   tmux capture-pane -p -S - -t session_name
   ```
2. History loaded into xterm.js buffer (tmux default: 50,000 lines)
3. xterm.js search addon enables Cmd+F search
4. All matches highlighted, navigate with Enter/Shift+Enter

### 3. Multi-Window Support

Wails v2 supports multiple native windows:
- Each session can open in its own window
- Windows can be arranged across monitors
- OS-level window management (Split View, Rectangle, etc.)
- Or: single window with split panes (like VS Code)

### 4. Better Visual UX

- Native macOS/Windows window chrome
- Tabs with close buttons
- Sidebar for session/group navigation
- Drag-and-drop session organization
- Right-click context menus
- Tooltips and hover states

## Shell Compatibility

**xterm.js is a real terminal emulator**, not a display-only viewer. It connects to a real PTY running the user's actual shell.

### Tested with user's zsh config:

| Feature | Works? |
|---------|--------|
| Powerlevel10k prompt | Yes (needs Nerd Font config) |
| Oh-my-zsh plugins (git, docker, etc.) | Yes |
| zsh-autosuggestions | Yes |
| zsh-syntax-highlighting | Yes |
| Custom aliases and functions | Yes |
| NVM, SDKMAN | Yes |
| True color (COLORTERM=truecolor) | Yes |
| Tab completion | Yes |
| Command history | Yes |

### What doesn't work:

- Terminal-specific integrations (iTerm2 shell integration, kitty +kitten ssh)
- These are minor and easily worked around

## Drawbacks & Risks

### 1. Context Switching / App Juggling
**Severity: Medium-High**

Developers already have terminals open for git, servers, etc. Now another app to manage.

**Mitigation:** Accept it's a specialized tool. Could add "quick terminal" feature for misc commands.

### 2. "Electron App" Reputation
**Severity: Medium**

Developers are skeptical of web-wrapped apps (slow, memory hogs).

**Mitigation:** Wails is genuinely lightweight. Emphasize "not Electron." Ensure snappy performance.

### 3. Losing Terminal Customization
**Severity: High for power users**

Users have years of iTerm2/Kitty customization. Starting fresh in xterm.js.

**Mitigation:** xterm.js supports fonts, themes, ligatures. Can import color schemes. Won't match everything but covers most cases.

### 4. Hidden tmux Complexity
**Severity: Medium**

Sessions still run in tmux underneath. If state diverges, debugging is harder.

**Mitigation:** Good error messages, "open in native terminal" escape hatch, surface tmux state in UI.

### 5. Development & Maintenance Burden
**Severity: High**

Now maintaining: Go backend, TUI, web frontend, cross-platform packaging.

**Mitigation:** Could deprecate TUI eventually, or keep it minimal for power users.

### 6. No SSH Access
**Severity: Low-Medium**

TUI works over SSH. Native app doesn't.

**Mitigation:** Keep TUI for SSH/remote access. Both share same backend/data.

## Target Audience Analysis

| User Type | Prefers TUI | Prefers Native App |
|-----------|-------------|-------------------|
| tmux/vim veterans | X | |
| "Live in terminal" devs | X | |
| Minimalists | X | |
| Remote SSH workers | X | |
| IDE/GUI-preferring devs | | X |
| Frustrated by tmux complexity | | X |
| Want multi-window/visual layout | | X |
| Newer to terminal work | | X |
| "Just want it to work" crowd | | X |

**Assessment:** The broader developer market would prefer the native app. Terminal purists are a passionate but smaller group.

## Effort Estimate

| Component | Effort |
|-----------|--------|
| HTTP/WebSocket API layer | 1-2 weeks |
| Basic web UI (tabs, sidebar, session list) | 2-3 weeks |
| xterm.js integration + pty streaming | 1 week |
| Scrollback search | 1 week |
| Polish, edge cases, testing | 2+ weeks |
| **Total MVP** | **7-9 weeks** |

## Hybrid Approach (Recommended)

Keep both interfaces:

1. **TUI** - For power users, SSH access, minimalists
2. **Native App** - For broader audience, better UX

Both share the same:
- Session storage (`sessions.json`)
- tmux session management
- MCP configuration
- Business logic

## Open Questions

1. **Frontend framework choice?** React, Svelte, or Vue for the web UI
2. **State management?** How to sync UI state with backend session state
3. **MVP feature set?** What's the minimal feature set for v1?
4. **Distribution?** Homebrew, direct download, auto-update mechanism
5. **Name?** Keep "Agent Deck" or rebrand the native app?

## Next Steps

1. Prototype Wails app with single xterm.js terminal
2. Prove out pty streaming and scrollback capture
3. Test Powerlevel10k/oh-my-zsh rendering
4. Design basic UI mockups
5. Decide on frontend framework

---

*Research conducted: January 2025*
