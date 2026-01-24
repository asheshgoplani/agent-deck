# Technical Debt & Known Issues

## Critical Issues

*No critical issues at this time.*

---

## Medium Priority Issues

### 1. Window Resize Jitter (Prototype Acceptable)
**Status:** Minor visual artifact
**Impact:** Brief jitter when resizing window while terminal is active

**Issue:**
When resizing window, xterm.js fits to new size before PTY resize completes, causing ~1 frame of mismatch.

**Current mitigation:**
- RAF throttling reduces occurrence
- Terminal refresh after resize helps
- Doesn't affect final state

**Proper fix (if needed):**
Synchronize resize: wait for PTY resize confirmation before fitting xterm. Requires refactoring resize flow to be promise-based.

---

### 2. Remote Sessions Disabled (Feature Gap)
**Status:** Not implemented in prototype
**Impact:** Can't connect to remote Agent Deck sessions via SSH

**Blocked by:** Need SSH connection management infrastructure

**Implementation plan:** See ROADMAP.md v0.3.0

---

## Low Priority Issues

*No low priority issues at this time.*

---

## Resolved Issues

### ~~No Command Palette~~ ✅
**Fixed in:** v0.2.0
**Commits:** 0f6c23a

**The Feature:**
Full command palette with Cmd+K, including:
- Fuzzy search across sessions and projects (fuse.js)
- Project discovery with frecency scoring
- Quick Launch Bar with pinned favorites
- Custom keyboard shortcuts for favorites
- Tool picker (Claude/Gemini/OpenCode)

### ~~Scrollback Pre-loading Visual Artifacts~~ ✅
**Fixed in:** v0.2.0
**Commits:** f89302e, 96b3f41

**The Problem:**
When connecting to tmux sessions, the terminal displayed garbled/overlapping text. This was caused by two issues:
1. `tmux capture-pane` outputs LF (`\n`) line endings, but xterm.js interprets `\n` as "move cursor down" without carriage return, causing text to overlap
2. `tmux send-keys -l` doesn't handle special keys (Enter, arrows, Ctrl+C, etc.) correctly

**The Solution:**
1. **LF→CRLF conversion:** Convert all `\n` to `\r\n` in both `pollTmuxOnce()` and `GetScrollback()` before sending to xterm.js
2. **Special key handling:** `SendTmuxInput()` now maps control characters and escape sequences to tmux key names (e.g., `\r` → `Enter`, `\x1b[A` → `Up`)
3. **Polling architecture:** Use `tmux capture-pane` polling instead of `tmux attach-session` to avoid cursor position conflicts

---

### ~~Terminal Doesn't Reflow on Resize~~ ✅
**Fixed in:** v0.2.0
**Commit:** f89302e

**The Problem:**
When the window was resized, old scrollback content stayed wrapped at the original width instead of reflowing to fill the new width.

**The Solution:**
Debounced scrollback refresh on resize:
1. When resize activity stops (400ms debounce), clear xterm buffer
2. Re-fetch scrollback from tmux (which has reflowed to new width)
3. Write reflowed scrollback to xterm buffer
4. Polling continues normally for live content

This works because tmux itself reflows its scrollback buffer when resized.

---

### ~~Cmd+F Doesn't Refocus When Already Open~~ ✅
**Fixed in:** v0.2.0
**Commit:** 96b3f41

**The Problem:**
Pressing Cmd+F when search was already open did nothing visible.

**The Solution:**
Added `focusTrigger` prop that increments each time Cmd+F is pressed. Search component responds by refocusing input and selecting existing text.

---

### ~~Blank Terminal on Navigation~~ ✅
**Fixed in:** v0.1.0

Terminal.jsx now calls CloseTerminal() on unmount.

---

### ~~Search Only Finds Visible Text~~ ✅
**Fixed in:** v0.1.0

Pre-load scrollback into xterm buffer for Cmd+F search.

---

## Technical Debt Prioritization

**For v0.2.0:** ✅ COMPLETE
1. ~~Fix scrollback visual artifacts (polling approach)~~ ✅
2. ~~Fix scrollback reflow on resize~~ ✅
3. ~~Fix Cmd+F refocus~~ ✅
4. ~~Implement command palette (Cmd+K)~~ ✅
5. ~~Project-aware session launching~~ ✅

**For v0.3.0:**
1. Remote sessions support
2. Session management (create/delete from app) - partially done (CreateSession exists)
3. Add "Pin to Quick Launch" from Command Palette

**Deferred:**
- Split panes (v0.6.0+)
- Tabs (v0.6.0+)

---

## Architecture Notes (v0.2.0)

### Tmux Polling Architecture

Instead of `tmux attach-session` (which causes cursor position conflicts), we use polling:

```
┌─────────────┐     ┌──────────────┐     ┌─────────────┐
│   xterm.js  │◄────│  terminal.go │◄────│    tmux     │
│  (frontend) │     │   (backend)  │     │  (session)  │
└─────────────┘     └──────────────┘     └─────────────┘
       │                   │                    │
       │   terminal:data   │  capture-pane -p   │
       │◄──────────────────│◄───────────────────│
       │   (50ms poll)     │                    │
       │                   │                    │
       │   SendTmuxInput   │  send-keys -t      │
       │──────────────────►│───────────────────►│
       │                   │                    │
       │   ResizeTmuxPane  │  resize-window     │
       │──────────────────►│───────────────────►│
```

**Key implementation details:**
- Poll interval: 50ms (20 FPS)
- LF→CRLF conversion for proper xterm rendering
- Special key mapping for tmux send-keys
- Debounced scrollback refresh on resize (400ms)
- Debug logging to `$TMPDIR/agent-deck-desktop-debug.log`
