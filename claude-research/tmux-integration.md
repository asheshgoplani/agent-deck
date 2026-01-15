# tmux Integration - Deep Engineering Research

**Date:** 2026-01-15
**Agent:** Explore

## Executive Summary

The `internal/tmux/` package is a **production-grade session lifecycle manager** with a sophisticated **activity-aware status detection system**. Core innovation: non-blocking spike detection with a 2-second activity cooldown that prevents false "busy" status while reliably detecting sustained work.

**Key Metrics:**
- **97% subprocess reduction** via session caching (60 → 1-2 calls per tick)
- **30× CPU improvement** for idle sessions (15% → 0.5%)
- **7 detection mechanisms** for busy state
- **13+ terminal emulator support** with capability detection

---

## L2 Architecture: Core Components

### File Structure

| File | LOC | Purpose |
|------|-----|---------|
| tmux.go | 2049 | Session lifecycle, status detection engine, cache management, log maintenance |
| detector.go | 406 | Prompt detection (tool-specific), ANSI stripping, Claude busy detection |
| pty.go | 273 | PTY attach/detach, signal handling, Ctrl+Q interception |
| watcher.go | 130 | fsnotify-based log file monitoring |

### Session State Machine

```
NEW → START (register cache, config, enable logging)
   → RUNNING (500ms GetStatus() polling)
   → IDLE/WAITING (cooldown expiry)
   → KILL (cleanup, remove cache)
```

### Key Data Structure: StateTracker

```go
type StateTracker struct {
    lastHash              string     // SHA256 of normalized content
    lastChangeTime        time.Time  // For 2s cooldown
    acknowledged          bool       // User has "seen" this state
    lastActivityTimestamp time.Time  // window_activity from tmux
    activityCheckStart    time.Time  // Spike detection window start
    activityChangeCount   int        // Changes within detection window
}
```

Tracks 3-state model:
- **GREEN** ("active") - Agent actively processing
- **YELLOW** ("waiting") - Stopped, unacknowledged
- **GRAY** ("idle") - Stopped, user acknowledged

---

## L3 Implementation Details

### Status Detection Engine (GetStatus)

**Non-Blocking Spike Detection Algorithm:**

```
1. Get window_activity timestamp from cache (~4ms)
2. If timestamp changed:
   → Start 1-second detection window
   → Return previous status (don't flash GREEN)
3. If 2+ changes within window:
   → Confirmed sustained activity → GREEN
4. If only 1 change in window:
   → Spike detected and filtered → previous status
5. Check cooldown (2s) for GREEN, else:
   → YELLOW (unacknowledged) or GRAY (acknowledged)
```

**Critical Insight:** Waits 1 second to confirm activity. Prevents GREEN flashing on status bar updates (e.g., "(45s · 1234 tokens)" changing to "(46s · 1235 tokens)").

### Busy Indicator Detection (7 Mechanisms)

1. **Text indicators** (highest priority):
   - "esc to interrupt"
   - "(esc to interrupt)"

2. **Whimsical words + tokens** (Claude-specific):
   - All 90 thinking words: Flibbertigibbeting, Wibbling, Discombobulating...
   - Only when paired with "(Xs · Y tokens)" pattern

3. **Spinner characters**:
   - Braille dots: ⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏ (from cli-spinners)

4. **Generic indicators**:
   - "processing", "loading", "please wait", "working"
   - Only at start of line

5. **Custom patterns**:
   - Via `SetCustomPatterns()` for generic tools

6. **Prompt detection**:
   - Fallback checking for input prompts

7. **Content hash changes**:
   - Last resort (getStatusFallback)
   - When window_activity unavailable

### Content Normalization (Stable Hashing)

**6-Step Process:**

```go
func normalizeContent(content string) string {
    // 1. Strip ANSI color codes
    content = StripANSI(content)

    // 2. Remove control characters (keep \t, \n, \r)
    content = stripControlChars(content)

    // 3. Remove spinner runes
    content = removeSpinners(content)  // ⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏

    // 4. Replace dynamic status patterns
    // "(45s · 1234 tokens)" → "(STATUS)"
    content = statusRegex.ReplaceAllString(content, "(STATUS)")

    // 5. Replace progress indicators
    // "[====>   ] 45%" → "[PROGRESS]"
    // "1.2MB/5.6MB" → "[PROGRESS]"
    content = progressRegex.ReplaceAllString(content, "[PROGRESS]")

    // 6. Normalize whitespace
    // Trim trailing spaces, collapse 3+ newlines to 2
    content = normalizeWhitespace(content)

    return content
}
```

**Performance:** O(n) single-pass using strings.Builder with pre-allocation. Fixed O(n²) regex issue that caused 2-11s UI freezes.

### Prompt Detection (detector.go)

**Tool-Specific Checks (Claude is most complex):**

```go
func HasPrompt(content string, tool string) bool {
    // Claude: Check busy FIRST
    if tool == "claude" {
        if hasBusyIndicator(content) {
            return false  // Not waiting for input
        }
    }

    // Permission prompts
    if strings.Contains(content, "Yes, allow once") ||
       strings.Contains(content, "│ Do you want") {
        return true
    }

    // Input prompt (dangerous mode)
    lastLine := getLastLine(content)
    if lastLine == ">" || lastLine == "> " {
        return true
    }

    // Question prompts
    if strings.Contains(lastLine, "Continue?") ||
       strings.Contains(lastLine, "(Y/n)") {
        return true
    }

    // Completion signals
    if strings.Contains(content, "Task completed") &&
       strings.Contains(content, ">") {
        return true
    }

    return false
}
```

**ANSI Stripping:** O(n) single-pass state machine avoiding catastrophic backtracking on malformed escape sequences.

### PTY Attach/Detach (pty.go)

**Architecture (4 coordinated goroutines):**

```
1. SIGWINCH handler → pty.Setsize() on resize
2. PTY→stdout copier (io.Copy)
3. stdin→PTY reader with Ctrl+Q (ASCII 17) interception
4. Command waiter (cmd.Wait())
```

**Ctrl+Q Behavior:**
- Reads stdin byte-by-byte
- Single byte 17 = detach signal (cancels context, closes detachCh)
- All other bytes forwarded to PTY
- Discards first 50ms of terminal control sequences (terminal init noise)

**Cleanup:**
- All goroutines tracked in WaitGroup
- Terminal state restored via signal.Stop() + close(sigwinchDone)

### Log Watching (watcher.go)

**Event-Driven Architecture:**

```go
type LogWatcher struct {
    watcher  *fsnotify.Watcher
    callback func(sessionName string)
    done     chan struct{}
}

func (w *LogWatcher) Start() {
    // Monitor ~/.agent-deck/logs/
    w.watcher.Add(logDir)

    for {
        select {
        case event := <-w.watcher.Events:
            if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
                sessionName := extractSessionName(event.Name)
                w.callback(sessionName)
            }
        case <-w.done:
            return
        }
    }
}
```

**Integration:**
- LogWatcher callback → Session.SignalFileActivity()
- Updates lastChangeTime → GREEN status

**Log Rotation:**
- `TruncateLargeLogFiles(maxSizeMB, maxLines)`
- `CleanupOrphanedLogs()` for deleted sessions
- `RunLogMaintenance()` at startup + every 5 minutes

---

## L4 Scale & Performance

### Session Cache Impact (2025-12-23 optimization)

| Metric | Before | After | Reduction |
|--------|--------|-------|-----------|
| Subprocesses/tick (30 sessions) | 60 | 1-2 | **97%** |
| CPU (idle) | 15% | 0.5% | **30×** |
| Cache memory | N/A | ~1KB | Negligible |

**Implementation:**

```go
// Single call per 2 seconds
tmux list-sessions -F '#{session_name}\t#{session_activity}'
```

Cached with RWMutex protection, 2s TTL.

### GetStatus() Performance

**Worst Case (new activity):**
- Get timestamp: ~4ms (from cache)
- CapturePane(): 100-200ms (tmux subprocess)
- hasBusyIndicator(): ~1-5ms (regex on last 5 lines)
- Total: ~110-210ms per detection

**Optimization:** Only calls CapturePane() when:
- Timestamp changed (real activity)
- Within cooldown (still working)
- In spike detection window

**Result:** Most ticks skip expensive CapturePane().

### Content Normalization Performance

**StripANSI():** O(n) single-pass
- 100KB output: <1ms
- 1MB output: <10ms

Before: O(n²) with string concatenation in loop (caused freezes)

### Concurrent Session Limits

Tested: 50+ sessions
- Session cache: 1 call per 2s (no contention)
- Bottleneck: tmux subprocess creation (10-20 calls in parallel)
- Mitigated by cache (only 1 per 2s for Exists check)

---

## L5 Extension Points

### Custom Tool Support

```go
session.SetCustomPatterns(
    "vibe",
    []string{"executing", "running tool"},  // Busy patterns
    []string{"vibe> ", "approve?"},         // Prompt patterns
    []string{"mistral vibe"},               // Detect patterns
)
```

Patterns checked FIRST (before built-in patterns), same regex matching.

### Status Detection Hooks

| Method | Purpose |
|--------|---------|
| `Acknowledge()` | Manual "idle" mark |
| `ResetAcknowledged()` | Mark "waiting" (after task complete) |
| `AcknowledgeWithSnapshot()` | Mark + 300ms grace period (Ctrl+Q) |
| `SignalFileActivity()` | Force GREEN (from LogWatcher) |
| `GetLastActivityTime()` | Query activity timestamp |

### Terminal Detection & Capabilities

```go
DetectTerminal() → "warp", "iterm2", "kitty", "alacritty", "vscode", etc.

GetTerminalInfo() → struct {
    SupportsOSC8    bool  // Hyperlinks
    SupportsOSC52   bool  // Clipboard
    SupportsTrueColor bool
}
```

**Used for:** OSC 8 hyperlinks, OSC 52 clipboard, true color support.

### Config Extension

```toml
[tools.custom-ai]
command = "my-ai"
busy_patterns = ["thinking...", "processing..."]
prompt_patterns = ["ready> ", "user input:"]
session_id_env = "MYAI_SESSION"
resume_flag = "--continue"
```

---

## Critical Design Decisions

| Decision | Trade-off | Benefit | Mitigation |
|----------|-----------|---------|------------|
| **Non-Blocking Spike Detection** | 1s latency before GREEN | No false positives on status bar updates | Shows previous status during detection |
| **2-Second Activity Cooldown** | Aggressive timeout | Quick YELLOW when work stops | 300ms grace period after acknowledge |
| **Session Cache Expiry (2s)** | 2s lag for detach detection | 97% subprocess reduction | Manual refresh available |
| **Content Hash Fallback** | Heavy (200ms CapturePane per tick) | Works on all systems | Rare (window_activity works on all tmux) |

---

## Failure Modes & Recovery

| Failure | Symptom | Status | Fix |
|---------|---------|--------|-----|
| Stuck Spinner | GREEN forever when Claude hangs | Open | Staleness check (>30s) |
| Progress Flicker | Progress bars cause GREEN flashing | **FIXED** | Regex normalization |
| Acknowledge Race | Immediate re-flash after detach | **FIXED** | 300ms grace period |
| Cache Stale | Detach undetected (2s lag) | Design | Manual refresh fallback |
| PTY Goroutine Leak | Orphaned goroutines on resize | **FIXED** | WaitGroup + signal.Stop() |

---

## Testing Strategy

### Unit Tests (tmux_test.go)
- Session uniqueness, name sanitization
- Prompt detection (all tools)
- Terminal detection

### Regression Tests (status_fixes_test.go)
- Whimsical word detection (all 90 words)
- Progress bar normalization
- Thinking pattern regex
- Acknowledge grace period

**Key Insight:** Tests document BUGS first (e.g., TestValidate_WhimsicalWordDetection_CurrentBehavior shows "only 'Thinking'/'Connecting' detected"), then validate fixes.

---

## Known Limitations

1. **No spinner staleness check** - Hung Claude with stuck spinner shows GREEN forever
2. **Activity timestamp precision** - 1-second granularity (tmux limitation)
3. **WSL1 socket pooling disabled** - Falls back to stdio (memory overhead)
4. **Tool detection cache** - 30s TTL (won't detect tool change mid-session)

---

## Summary

The tmux package is **enterprise-grade** with:
- **Intelligent status detection** (7 mechanisms + spike filtering)
- **Performance optimization** (97% subprocess reduction)
- **Extensibility** (custom tool patterns, terminal detection)
- **Robustness** (multiple fallbacks, graceful degradation)
- **User experience** (fast detach, no flicker, accurate activity)

Main trade-off: 1-second latency to confirm sustained activity vs. false positive prevention.
