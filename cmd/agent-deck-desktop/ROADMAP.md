# Agent Deck Desktop - Feature Roadmap

## v0.1.0 - Prototype ✓
**Status:** In validation
**Goal:** Prove the concept is worth pursuing

### Features
- [x] Basic terminal emulation with PTY
- [x] Tmux session listing and attachment
- [x] Session selector UI
- [x] Cmd+F search in scrollback
- [x] Navigation (back button, Cmd+,)
- [x] Cmd+W close confirmation
- [x] DevTools and logging infrastructure

### Known Limitations
- Remote sessions disabled
- No session management (create/delete)
- No MCP management
- Scrollback loading shows visual transition
- No command palette

---

## v0.2.0 - Core UX Fixes
**Priority:** Critical
**Goal:** Fix critical UX issues and add command palette
**Estimated:** 2-3 weeks

### Critical Fixes

**Scrollback Visual Artifacts Fix** 🔥
**Priority:** Critical - Blocks professional appearance

See `TECHNICAL_DEBT.md` for full technical analysis.

**Current issue:** Visual chaos when attaching to tmux sessions with search enabled
**Proper fix:** Polling approach instead of `tmux attach`
**Implementation:** ~1-2 days
**Status:** Documented, ready to implement

### Command Palette (Cmd+K) 🎯
**Priority:** Critical - Core UX differentiator

**Design:**
- Fuzzy search interface (like VSCode, Linear, Notion)
- Global access via Cmd+K from anywhere
- Categories: Sessions, Actions, Shortcuts

**Features:**
1. **Jump to Session**
   - Type session name to instantly switch
   - Show recent sessions first
   - Display session status (running/idle/waiting)
   - Keyboard navigation (arrows, Enter)

2. **Quick Actions**
   - "New Terminal"
   - "Refresh Sessions"
   - "Clear Terminal" (when in terminal view)
   - "Close Terminal" (when in terminal view)
   - "Toggle DevTools" (dev mode only)

3. **Shortcut Discovery**
   - List all keyboard shortcuts
   - Show context-relevant shortcuts first
   - Click or press Enter to execute

**Implementation Notes:**
- Component: `CommandPalette.jsx`
- Fuzzy search: Use `fuse.js` or similar
- Render as modal overlay
- Auto-focus search input
- ESC to close
- Track usage for ranking

**Acceptance Criteria:**
- [ ] Cmd+K opens palette from anywhere
- [ ] Fuzzy search works on session names
- [ ] Can jump to session with Enter
- [ ] Shows all available actions
- [ ] ESC closes palette
- [ ] Fast (<100ms to open)

---

### UX Improvements

**Session Selector Enhancements:**
- [ ] Show session preview on hover
- [ ] Filter by status (running/idle/waiting)
- [ ] Sort options (recent, name, status)
- [ ] Keyboard navigation (arrows, Enter)
- [ ] Search/filter sessions inline

**Terminal View:**
- [ ] Status bar (show session name, tool, status)
- [ ] Split pane support (future)
- [ ] Tabs for multiple terminals (future)
- [ ] Better loading states (progress bar)

**Visual Polish:**
- [ ] Smooth animations (fade in/out, slide)
- [ ] Better loading indicators
- [ ] Error messages with actions
- [ ] Tooltips on buttons
- [ ] Keyboard shortcut hints in UI

---

## v0.3.0 - Remote Sessions
**Priority:** High
**Goal:** Support remote Agent Deck sessions via SSH

### Features
- [ ] Enable remote session selection
- [ ] SSH connection management
- [ ] Remote scrollback loading
- [ ] Connection status indicator
- [ ] Reconnect on disconnect

### Challenges
- SSH authentication (keys, agents)
- Network latency handling
- Scrollback performance over network
- Error handling for connection issues

---

## v0.4.0 - Session Management
**Priority:** Medium
**Goal:** Full session lifecycle from desktop app

### Features
- [ ] Create new Agent Deck session (with project picker)
- [ ] Delete sessions
- [ ] Rename sessions
- [ ] Move sessions between groups
- [ ] Duplicate/fork sessions (Claude only)
- [ ] Session metadata editing

---

## v0.5.0 - MCP Management
**Priority:** Medium
**Goal:** Manage MCPs without touching config files

### Features
- [ ] View attached MCPs for session
- [ ] Attach/detach MCPs (modal dialog)
- [ ] MCP status indicators
- [ ] MCP server configuration
- [ ] Test MCP connection

---

## v0.6.0 - Advanced Terminal Features
**Priority:** Low (Nice to have)

### Features
- [ ] Split panes (horizontal/vertical)
- [ ] Tabs for multiple terminals
- [ ] Terminal profiles (color schemes, fonts)
- [ ] Ligature support
- [ ] GPU acceleration
- [ ] Custom key bindings
- [ ] Copy/paste improvements
- [ ] URL click handling improvements

---

## v0.7.0 - Performance & Polish
**Priority:** Medium (After core features)

### Features
- [ ] Reduce memory usage
- [ ] Optimize scrollback rendering
- [ ] Lazy load session list
- [ ] Virtual scrolling for large outputs
- [ ] Debounced search
- [ ] Better resize handling (no jitter)

---

## v0.8.0 - Integrations
**Priority:** Low (Future exploration)

### Features
- [ ] GitHub integration (PR status in session list)
- [ ] Linear integration (issue status)
- [ ] Slack notifications for session events
- [ ] Export session logs
- [ ] Session recording/playback

---

## Backlog / Ideas

**Not prioritized - needs validation:**

- Global hotkey to show/hide app (Cmd+Shift+Space)
- System tray icon with quick session access
- Auto-attach to last session on startup
- Session templates (pre-configured setups)
- Collaborative sessions (shared tmux)
- AI assistant integration (Claude in sidebar)
- Session analytics (time spent, commands run)
- Backup/restore session configs
- Custom shell integration (session-aware prompt)

---

## Release Schedule (Tentative)

Assuming prototype validates:

- **v0.1.0** - Prototype (current)
- **v0.2.0** - Command Palette (2-3 weeks)
- **v0.3.0** - Remote Sessions (3-4 weeks)
- **v0.4.0** - Session Management (2-3 weeks)
- **v0.5.0** - MCP Management (2 weeks)
- **v0.6.0+** - TBD based on feedback

---

## Decision Log

**Why Wails over Electron?**
- Smaller binary size (~5MB vs ~150MB)
- Native performance (Go backend)
- Better terminal integration
- Faster startup time

**Why xterm.js?**
- Industry standard (VSCode, Hyper, etc.)
- Excellent addon ecosystem
- Active development
- Full ANSI/VT support

**Why not native macOS app?**
- Cross-platform potential (Linux, Windows)
- Web tech for UI is faster to iterate
- Easier to find contributors

**Why tmux integration vs direct shell?**
- Session persistence
- Leverage existing Agent Deck infrastructure
- Familiar to power users
- Window management (future splits)
