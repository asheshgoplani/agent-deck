# Agent Deck Native App - Prototype Plan

## Competitive Landscape

### Apps Evaluated (January 2026)

| Project | What It Is | Verdict |
|---------|-----------|---------|
| **[Agent Sessions](https://github.com/jazzyalex/agent-sessions)** | macOS session browser + analytics | **Search-only** - not an interactive terminal |
| **[opcode](https://opcode.sh/)** (formerly Claudia) | Tauri desktop app | **Broken** - out of date, doesn't work |
| **[Simple Code GUI](https://github.com/DonutsDelivery/simple-code-gui)** | Electron app with tabbed terminals | **Closest match** - but significant issues |

### Agent Sessions - Search Only

- Reads session files from `~/.claude/sessions`, `~/.gemini/tmp`, etc.
- Excellent SQLite-backed full-text search through historical transcripts
- Good analytics (usage trends, heatmaps)
- "Resume" just opens Terminal/iTerm with a new Claude Code instance
- **Not an interactive terminal** - cannot Cmd+F while actively working
- Useful as a companion tool, but doesn't solve the core pain point

### opcode (Claudia) - Non-Functional

- v0.2.0 is outdated and doesn't launch properly
- Project appears abandoned or in transition
- Not a viable option

### Simple Code GUI - Close But Flawed

**What it does well:**
- Tabbed terminal interface with xterm.js
- Multi-backend support (Claude, Gemini, Codex, OpenCode)
- Project sidebar for organization
- Direct PTY spawning (no tmux dependency)

**What's broken or missing:**
- `/Commands` button often shows empty menu
- GSD and Beads integrations feel half-baked
- No keyboard shortcuts for tab navigation (Cmd+1, Cmd+2, etc.)
- No remote SSH capability
- Sessions die when app closes (no persistence)
- Polyform Noncommercial license (commercial use needs permission)

**Architecture limitation:** Sessions are tied to the Electron process. Close the app, laptop sleeps, or app crashes → session is lost.

---

## Strategic Decision: Keep Tmux Backend

After evaluating alternatives, the decision is to **keep tmux as the session backend** rather than adopt a direct PTY approach.

### Why Tmux Matters

| Capability | With Tmux | Without Tmux (Simple Code GUI style) |
|------------|-----------|--------------------------------------|
| **Session persistence** | Sessions survive app close, crashes, sleep | Session dies when app/tab closes |
| **Remote SSH** | Already works in Agent Deck | Would need to build from scratch |
| **Detach/reattach** | Can switch between Terminal.app, iTerm, native app | Locked to spawning app |
| **Recovery** | Laptop dies → remote session survives | Everything dies |

### The Insight

**You don't need to abandon tmux to get searchable scrollback.**

The problem with the current Agent Deck TUI is that tmux's scrollback is the only buffer. A native app can:

1. Connect to tmux session via PTY (`tmux attach -t session`)
2. Mirror ALL output to its own xterm.js buffer
3. Provide Cmd+F search over that local buffer

Result: **tmux persistence + remote SSH + searchable scrollback**

### Why Not Fork Simple Code GUI

| Issue | Impact |
|-------|--------|
| No remote SSH | Massive gap - would need to build from scratch |
| Sessions are ephemeral | Unacceptable for long-running agent work |
| Technical debt | Half-baked features (GSD, Beads, broken commands) |
| License | Polyform Noncommercial - commercial use needs permission |
| Would gut most of it | Might as well build clean |

---

## Prototype Plan

Build a Wails-based native app that connects to tmux sessions.

### Goal

Validate core hypotheses:

1. **Does xterm.js feel native?** - Full terminal emulation with powerlevel10k support
2. **Does Cmd+F search work through scrollback?** - Major UX win over tmux copy-mode
3. **Can we connect to existing Agent Deck sessions?** - Reuse existing infrastructure
4. **Does the tmux-backed architecture work?** - Best of both worlds

### Architecture

```
┌─────────────────────────────────────┐
│  Native App (Wails)                 │
│  ┌───────────────────────────────┐  │
│  │ xterm.js                      │  │
│  │ - Renders terminal output     │  │
│  │ - Captures to searchable buf  │  │
│  │ - Cmd+F searches buffer       │  │
│  └───────────────────────────────┘  │
│              ↕ PTY                  │
│  ┌───────────────────────────────┐  │
│  │ tmux attach -t session        │  │
│  └───────────────────────────────┘  │
└─────────────────────────────────────┘
              ↕ (or SSH for remote)
┌─────────────────────────────────────┐
│  tmux server                        │
│  - Session persistence              │
│  - Detach/reattach from anywhere    │
│  - Remote sessions via SSH          │
└─────────────────────────────────────┘
```

### File Structure

```
agent-deck/
├── cmd/
│   ├── agent-deck/          # Existing TUI entry point
│   └── agent-deck-desktop/  # NEW: Wails entry point
│       ├── main.go          # Wails app initialization
│       ├── app.go           # Backend bindings
│       └── frontend/        # React + xterm.js
│           ├── src/
│           │   ├── App.jsx
│           │   ├── Terminal.jsx
│           │   └── main.jsx
│           ├── package.json
│           ├── vite.config.js
│           └── index.html
├── internal/
│   ├── tmux/                # REUSE: Session management
│   ├── session/             # REUSE: Data model, storage
│   └── desktop/             # NEW: Wails-specific handlers
│       └── pty.go           # WebSocket PTY streaming
└── wails.json               # Wails configuration
```

---

## Implementation Phases

### Phase 1: Wails Skeleton

**Step 1.1: Initialize Wails project**
```bash
cd agent-deck
wails init -n agent-deck-desktop -t react -d cmd/agent-deck-desktop
```

**Step 1.2: Create minimal app.go**
- `App` struct with context
- `GetVersion()` binding for testing frontend↔backend communication
- `SpawnShell()` placeholder

**Step 1.3: Verify hot reload works**
```bash
wails dev
```

### Phase 2: xterm.js Integration

**Step 2.1: Add xterm.js dependencies**
```bash
cd cmd/agent-deck-desktop/frontend
npm install @xterm/xterm @xterm/addon-fit @xterm/addon-search @xterm/addon-web-links
```

**Step 2.2: Create Terminal.jsx component**
- Initialize xterm.js with configuration:
  - `fontFamily: 'MesloLGS NF, monospace'` (for powerlevel10k)
  - `scrollback: 50000` (match tmux history-limit)
  - `cursorBlink: true`
- Load addons: FitAddon, SearchAddon, WebLinksAddon
- Handle resize events

**Step 2.3: Style terminal container**
- Full viewport terminal
- Proper focus handling

### Phase 3: PTY Streaming

**Step 3.1: Create internal/desktop/pty.go**
- Use `creack/pty` (already a dependency)
- WebSocket handler for bidirectional streaming
- Goroutine: PTY read → WebSocket write
- Main loop: WebSocket read → PTY write

**Step 3.2: Wire WebSocket in app.go**
- Start HTTP server on random port
- Expose port to frontend via binding
- Handle terminal resize messages

**Step 3.3: Connect frontend to WebSocket**
- Use `@xterm/addon-attach` for automatic routing
- Or manual: `socket.onmessage → terminal.write()`

### Phase 4: Search Integration

**Step 4.1: Enable SearchAddon**
```javascript
const searchAddon = new SearchAddon();
terminal.loadAddon(searchAddon);
```

**Step 4.2: Add search UI**
- Cmd+F triggers search input overlay
- `searchAddon.findNext(query)` on Enter
- `searchAddon.findPrevious(query)` on Shift+Enter
- ESC closes search

**Step 4.3: Test with tmux scrollback**
- Connect to existing tmux session
- Pre-load scrollback via `tmux capture-pane -p -S -`
- Verify Cmd+F searches through history

### Phase 5: Tmux Integration

**Step 5.1: Reuse existing tmux code**
- Import `internal/tmux` package
- Use `CapturePane()` for scrollback history
- Use existing session listing

**Step 5.2: Add session attachment**
- `AttachSession(sessionID)` binding
- Load scrollback → write to terminal
- Attach to tmux session via PTY

**Step 5.3: Add session list endpoint**
- `ListSessions()` binding returns existing Agent Deck sessions
- Simple dropdown or list in UI for testing

### Phase 6: Polish & Validation

**Step 6.1: Keyboard shortcuts**
- Cmd+K: Clear terminal
- Cmd+W: Close window / confirm dialog
- Cmd+F: Open search
- Cmd+1/2/3: Tab switching (future)

**Step 6.2: Test shell compatibility**
- Verify powerlevel10k prompt renders
- Verify zsh-autosuggestions works
- Verify syntax highlighting works
- Test with existing aliases

**Step 6.3: Performance check**
- Test scrollback with 10k+ lines
- Verify no lag on fast output

---

## Key Files to Create

| File | Purpose |
|------|---------|
| `cmd/agent-deck-desktop/main.go` | Wails app entry point |
| `cmd/agent-deck-desktop/app.go` | Backend bindings (Go→JS) |
| `cmd/agent-deck-desktop/frontend/src/Terminal.jsx` | xterm.js component |
| `cmd/agent-deck-desktop/frontend/src/Search.jsx` | Search overlay component |
| `internal/desktop/pty.go` | WebSocket PTY streaming |
| `internal/desktop/tmux.go` | Tmux integration for desktop |
| `wails.json` | Project configuration |

## Key Files to Reuse (No Changes)

| File | What It Provides |
|------|------------------|
| `internal/tmux/executor.go` | TmuxExecutor interface |
| `internal/tmux/executor_local.go` | CapturePane, SendKeys |
| `internal/tmux/detector.go` | Status detection, ANSI stripping |
| `internal/session/instance.go` | Session data model |
| `internal/session/storage.go` | JSON persistence |

## Dependencies to Add

**Go:**
- `github.com/wailsapp/wails/v2` - Desktop framework
- `github.com/gorilla/websocket` - WebSocket server
- (already have: `github.com/creack/pty`)

**JavaScript (frontend/package.json):**
- `@xterm/xterm` - Terminal emulator
- `@xterm/addon-fit` - Auto-resize
- `@xterm/addon-search` - Cmd+F search
- `@xterm/addon-web-links` - Clickable URLs
- `@xterm/addon-attach` - WebSocket attachment (optional)

---

## Verification Checklist

After prototype is complete, validate:

- [ ] **Shell compatibility**: Open app, type commands, verify powerlevel10k prompt looks correct
- [ ] **Aliases work**: Run `lst`, `gfp`, other aliases from .zshrc
- [ ] **Autosuggestions**: Type partial command, see gray suggestion
- [ ] **Syntax highlighting**: Type command, see colors
- [ ] **Scrollback search**: Run `seq 1 10000`, press Cmd+F, search for "5555"
- [ ] **Tmux session**: Connect to existing Agent Deck session, verify content loads
- [ ] **Search in session**: Cmd+F searches through pre-loaded tmux history
- [ ] **Performance**: Fast output (run `yes | head -5000`) doesn't lag

## Build & Run Commands

```bash
# Development with hot reload
cd agent-deck
wails dev -appargs "desktop"

# Production build
wails build

# Output: build/bin/agent-deck-desktop (or .exe on Windows)
```

## Risk Mitigation

| Risk | Mitigation |
|------|------------|
| Wails v2 single-window limit | Accept for prototype; plan tabs UI |
| xterm.js font issues | Test with MesloLGS NF early |
| WebSocket complexity | Start with Wails Events, add WS if needed |
| Performance with large scrollback | Lazy-load if >50k lines |

## Success Criteria

The prototype is successful if:

1. You can open the app and get a working zsh shell
2. Your powerlevel10k prompt renders correctly
3. Cmd+F search finds text in 10k+ lines of scrollback
4. Connecting to an existing tmux session works
5. The experience "feels right" for further investment

---

## Next Steps After Prototype

If prototype validates successfully:

- [ ] Add tab support for multiple sessions
- [ ] Session list sidebar (reuse Agent Deck session discovery)
- [ ] Keyboard shortcuts for tab navigation (Cmd+1, Cmd+2, etc.)
- [ ] Remote session support (SSH executor integration)
- [ ] MCP management UI
- [ ] Session creation dialog
