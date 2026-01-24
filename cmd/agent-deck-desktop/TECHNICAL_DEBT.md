# Technical Debt & Known Issues

## Critical Issues

### 1. Scrollback Pre-loading Visual Artifacts
**Priority:** High (v0.2.0)
**Status:** Known limitation in v0.1.0 prototype
**Impact:** Visual chaos when attaching to tmux sessions - overlapping text, misaligned content

#### The Problem
When attaching to existing tmux sessions with Cmd+F search enabled, scrollback is pre-loaded into xterm's buffer to enable search across history. However, `tmux attach-session` sends absolute cursor positioning commands that assume a clean terminal slate, causing visual conflict with pre-loaded content.

**User-visible symptoms:**
- Overlapping text from multiple prompts
- Misaligned file listings
- Garbled terminal state on initial attach
- Search works correctly, but display is broken

#### Root Cause
1. We pre-load scrollback: `tmux capture-pane -e -p -S -10000`
2. We write it to xterm buffer for search functionality
3. We attach to tmux session: `tmux attach-session -t <name>`
4. Tmux sends its own redraw with absolute cursor positions
5. **Conflict:** Tmux's cursor positions are relative to empty screen, but xterm has pre-loaded content

#### What We've Tried (DO NOT RETRY)

**Attempt 1: Clear screen after scrollback load**
```javascript
term.write(scrollback);
term.write('\x1b[2J\x1b[H'); // Clear screen
await AttachSession();
```
**Result:** Failed. Cleared visible area but cursor position still offset. Tmux redraw still conflicted.

**Attempt 2: Terminal reset**
```javascript
term.write(scrollback);
term.reset(); // Hard reset terminal state
await AttachSession();
```
**Result:** Failed. `term.reset()` also clears buffer, destroying scrollback needed for search.

**Attempt 3: Visual separator + scroll to bottom**
```javascript
term.write(scrollback);
term.write('\r\n─── Reconnecting ───\r\n');
term.scrollToBottom();
await AttachSession();
```
**Result:** Failed. Scrolling doesn't fix cursor position conflict. Tmux still redraws with wrong positioning.

**Attempt 4: Delay between scrollback and attach**
```javascript
term.write(scrollback);
await sleep(100);
await AttachSession();
```
**Result:** Failed. Timing doesn't solve the fundamental cursor position conflict.

**Why these failed:**
The core issue is `tmux attach-session` sends VT100 escape sequences with **absolute cursor positioning** (e.g., `ESC[5;10H` = row 5, column 10) that assume the terminal started clean. Pre-loading content shifts everything, making these positions wrong.

#### The Proper Fix

**Approach: Polling Instead of Attach**

Instead of using `tmux attach-session`, stream tmux output via polling:

**Architecture:**
1. **Pre-load scrollback** into xterm buffer (for search)
2. **Don't attach** - instead, poll tmux state
3. **Send user input** to tmux via `tmux send-keys`
4. **Poll for updates** via `tmux capture-pane` every 100ms
5. **Diff output** and only send new content to xterm
6. **Full control** over rendering - no cursor position conflicts

**Implementation Plan:**

```go
// terminal.go - New method
func (t *Terminal) AttachTmuxPolled(tmuxSession string, cols, rows int) error {
    // 1. Pre-load scrollback to xterm (via frontend)
    // Frontend handles this via GetScrollback()

    // 2. Start polling loop instead of attach
    go t.pollTmux(tmuxSession)

    // 3. Handle user input via send-keys
    go t.inputLoop(tmuxSession)

    return nil
}

func (t *Terminal) pollTmux(session string) {
    var lastContent string
    ticker := time.NewTicker(100 * time.Millisecond)

    for {
        // Capture current pane state
        cmd := exec.Command("tmux", "capture-pane", "-t", session, "-p", "-e")
        output, err := cmd.Output()
        if err != nil {
            continue
        }

        content := string(output)

        // Diff and send only new content
        if content != lastContent {
            diff := computeDiff(lastContent, content)
            runtime.EventsEmit(t.ctx, "terminal:data", diff)
            lastContent = content
        }

        <-ticker.C
    }
}

func (t *Terminal) inputLoop(session string) {
    // Read from user input channel
    // Send to tmux via: tmux send-keys -t <session> <keys>
}
```

**Frontend changes:**
```javascript
// Load scrollback once
const scrollback = await GetScrollback(session.tmuxSession);
term.write(scrollback);

// Start polling (replaces AttachSession)
await StartTmuxPolling(session.tmuxSession, cols, rows);

// Input handling changes from PTY write to tmux send-keys
```

**Advantages:**
- ✅ Clean visual (no cursor position conflicts)
- ✅ Full search on scrollback
- ✅ Complete rendering control
- ✅ Can add smart diffing to minimize updates

**Disadvantages:**
- ⚠️ 100ms polling latency (acceptable for AI coding workflows)
- ⚠️ Higher CPU usage (polling vs. event-driven)
- ⚠️ Need to handle tmux session resize separately
- ⚠️ Edge cases with rapid output (need buffering)

**Complexity:** ~1-2 days of implementation + testing

**Alternative considered:**
Use `tmux pipe-pane` to stream output to a file, tail the file. Rejected because:
- More complex than polling
- File I/O overhead
- Need cleanup logic for temp files

#### Acceptance Criteria for Fix

When implemented, the fix should pass these tests:

1. **Visual test:**
   - Attach to session with lots of scrollback (run `seq 1 1000` first)
   - Terminal displays cleanly without overlapping text
   - Current prompt is visible and correctly positioned

2. **Search test:**
   - Cmd+F search finds text in pre-loaded scrollback
   - Can navigate through matches with Enter
   - Search works on new output after attach

3. **Interaction test:**
   - Type commands - they execute correctly
   - Run interactive programs (vim, top) - work correctly
   - Resize window - tmux pane resizes correctly

4. **Performance test:**
   - Run `yes` command (rapid output)
   - Terminal keeps up without lag
   - CPU usage stays reasonable (<5% continuous)

---

## Medium Priority Issues

### 2. Window Resize Jitter (Prototype Acceptable)
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

### 3. Remote Sessions Disabled (Feature Gap)
**Status:** Not implemented in prototype
**Impact:** Can't connect to remote Agent Deck sessions via SSH

**Blocked by:** Need SSH connection management infrastructure

**Implementation plan:** See ROADMAP.md v0.3.0

---

## Low Priority Issues

### 4. No Command Palette (Feature Gap)
**Status:** Cmd+K reserved but not implemented
**Impact:** Can't quick-jump to sessions

**Plan:** v0.2.0 priority feature (see ROADMAP.md)

---

### 5. Terminal Doesn't Reflow on Resize (Standard Behavior)
**Status:** Expected terminal behavior
**Impact:** Old content stays wrapped at original width after resize

**Not a bug:** This is how all terminal emulators work (iTerm2, Terminal.app, etc.). Content already printed doesn't reflow when terminal size changes.

**No fix planned:** This is standard behavior users expect.

---

## Resolved Issues

### ~~Blank Terminal on Navigation~~ ✅
**Fixed:** Terminal.jsx now calls CloseTerminal() on unmount
**Version:** v0.1.0
**Commit:** TBD

### ~~Search Only Finds Visible Text~~ ✅
**Fixed:** Pre-load scrollback into xterm buffer
**Version:** v0.1.0
**Side-effect:** Created Issue #1 (scrollback artifacts)

---

## Technical Debt Prioritization

**For v0.2.0:**
1. Fix scrollback visual artifacts (polling approach) - **CRITICAL**
2. Implement command palette (Cmd+K) - **HIGH**
3. Fix resize jitter - **NICE TO HAVE**

**For v0.3.0:**
1. Remote sessions support
2. Session management (create/delete from app)

**Deferred:**
- Terminal reflow (not doing - standard behavior)
- Split panes (v0.6.0+)
- Tabs (v0.6.0+)
