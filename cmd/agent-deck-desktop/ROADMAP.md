# Agent Deck Desktop - Feature Roadmap

TIL:
- the app supports multiple instances at once
>>   How it works:
  - First instance launched becomes "primary" (manages the notification bar)
  - Additional instances run as "secondary" (fully functional, but don't manage notifications to avoid conflicts)
  - Both can view/attach/manage the same sessions

### Action for us: enable a button in the app for this and keyboard and launcher shortcut, probably CMD + N, and reserve CMD +T for new tab

## Priorities to implement
- add an easy ability to create numerous Claude sessions in the same directory and to easily view them in menu trees and the launcher.
- add option to launcher when searching for a session that asks to open an existing session (or choose from several existing sessions in a single directory, or open a new one in an existing directory)
   - This will require thought and planning to avoid confusion
   - end goal... allow users to work on multiple things at once in the same directory, with separate agents, and add labels to each duplicated session to distinguish them.  If no label provided by user, auto-apply incremental label #1, #2, etc. (brainstorm with claude what feels right here before implementing, there may be a better way)


### Recently implemented

- turn front page view of sessions into a list of agents on left, preview pane on right (like TMUX)
- add hostname to top status bar in session view to clearly indicate which host the session is running in
- add path to same status menu, for same reason
- Bug: determine why branch sometimes not shown in this status bar (i.e. for agent-deck, no branch shown
- add button & launcher action & keyboard shortcut to hide all groups (i.e. collapse to single view)
- support keybindings, combos that allow the keyboard to jump one word at a time on mac, or to EoL, beg. of lines
- Update documentation to show capabilities of desktop app -- dedicated Readme, plus mention on front page Readme
   - Add remote SSH capabilities to main ireadme

--

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

## v0.2.0 - Core UX Fixes ✅
**Status:** Complete
**Goal:** Fix critical UX issues and add command palette

### Critical Fixes ✅

**Scrollback Visual Artifacts Fix** ✅
- Fixed with polling approach instead of `tmux attach`
- See `TECHNICAL_DEBT.md` for details

### Command Palette (Cmd+K) ✅

**Implemented Features:**
- [x] Fuzzy search via fuse.js
- [x] Jump to sessions with keyboard navigation
- [x] Quick actions (New Terminal, Refresh, Toggle Quick Launch)
- [x] ESC to close, Enter to select
- [x] Cmd+Enter for tool picker

### Project-Aware Session Launching ✅

**4-Phase Implementation Complete:**

1. **Phase 1 - Command Palette:** Fuzzy search across sessions, projects, and actions
2. **Phase 2 - Project Discovery:**
   - Scans configured paths for projects (git, npm, go, rust, python, CLAUDE.md)
   - Frecency scoring (use count × recency multiplier)
   - Config via `[project_discovery]` in `~/.agent-deck/config.toml`
3. **Phase 3 - Quick Launch Bar:**
   - Horizontal bar with pinned favorite projects
   - Right-click context menu for remove/edit shortcut
   - Persists to `~/.agent-deck/quick-launch.toml`
4. **Phase 4 - Custom Shortcuts:**
   - Assign keyboard shortcuts to favorites (Cmd+Shift+letter)
   - Conflict detection with reserved system keys
   - Global shortcut handling

**Key Files Added:**
- `frontend/src/CommandPalette.jsx` - Fuzzy search modal
- `frontend/src/ToolPicker.jsx` - Tool selection (Claude/Gemini/OpenCode)
- `frontend/src/QuickLaunchBar.jsx` - Pinned favorites bar
- `frontend/src/ShortcutEditor.jsx` - Custom shortcut recording
- `frontend/src/utils/shortcuts.js` - Shared shortcut formatting
- `frontend/src/utils/tools.js` - Shared tool config
- `project_discovery.go` - Project scanning + frecency
- `quick_launch.go` - Favorites persistence

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

**Tool Icons:**
- [x] Change shell icon from `$` to `>` (right caret) - more common representation
- [x] Replace Claude `C` icon with Anthropic's official Claude logo
- [ ] Consider official logos for Gemini and OpenCode as well

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
