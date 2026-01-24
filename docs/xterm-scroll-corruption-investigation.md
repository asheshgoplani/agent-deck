# xterm.js Scroll Rendering Corruption Investigation

**Bug:** Box-drawing characters (`─` U+2500) corrupt to replacement glyphs (diamond `?` characters) during scroll operations in Wails/WKWebView environment.

**Status:** Under investigation
**Branch:** `fix/xterm-resize-reflow`
**Last Updated:** 2025-01-24

---

## Root Cause Analysis

**Confirmed:** The xterm.js **buffer data is 100% correct**. Corruption is purely in the **canvas/rendering layer** - stale bitmaps are displayed instead of correct glyphs.

**Evidence:**
- Backend tmux data: Clean (verified via hex dump)
- xterm.js buffer: Clean (verified via `translateToString()`)
- Screen display: Corrupted (shows replacement glyphs)
- Manual text selection: FIXES corruption (forces repaint)
- Scroll to boundary (top/bottom): FIXES corruption
- Window resize: FIXES corruption

**Environment:**
- macOS with Wails v2 (uses WKWebView)
- xterm.js v5.5.0 with CanvasAddon
- tmux backend for session persistence

---

## Attempted Solutions (Failed)

### 1. Renderer Type Changes

| Approach | Result | Notes |
|----------|--------|-------|
| `rendererType: 'dom'` | No effect | Option deprecated/ignored in xterm v6 |
| WebglAddon | **Crashes app** | Incompatible with Wails/WKWebView |
| CanvasAddon (v5.5.0) | Still corrupts | customGlyphs enabled but issue persists |

### 2. Programmatic Refresh Attempts

| Approach | Result | Notes |
|----------|--------|-------|
| `term.refresh(0, rows-1)` | No effect | Only refreshes viewport, not bitmap cache |
| `term.clearTextureAtlas()` | No effect | Doesn't clear stale bitmaps |
| `fitAddon.fit()` after scroll | No effect | No dimension change = no recalculation |

### 3. Programmatic Selection Tricks

| Approach | Result | Notes |
|----------|--------|-------|
| `selectAll()` + `clearSelection()` | No effect | Programmatic selection doesn't trigger same repaint as mouse |

### 4. Scroll-Based Workarounds

| Approach | Result | Notes |
|----------|--------|-------|
| Micro-scroll (`scrollLines(+1/-1)`) | **Made it worse** | Infinite loop issues, more artifacts |
| Scroll to bottom and back | **Made it worse** | Visual judder, still showed corruption |

### 5. Data Sanitization

| Approach | Result | Notes |
|----------|--------|-------|
| Enhanced escape sequence sanitization | No effect | Data already clean |
| Unicode11Addon | No effect | Character width not the issue |

### 6. Version Changes

| Approach | Result | Notes |
|----------|--------|-------|
| Downgrade xterm v6 → v5.5.0 | Still corrupts | Needed for CanvasAddon anyway |

---

## Unexplored Solutions

### A. CSS Workarounds (GPU Compositing)
Force different compositing path in WKWebView:
```css
/* Option 1: Force GPU layer */
.xterm-screen {
  transform: translateZ(0);
  will-change: transform;
}

/* Option 2: Disable hardware acceleration hints */
.xterm-screen canvas {
  transform: none !important;
  will-change: auto !important;
}
```

### B. Terminal Options
```javascript
const term = new Terminal({
  customGlyphs: true,
  rescaleOverlappingGlyphs: true,
});
```

### C. Force Resize After Scroll
```javascript
terminal.onScroll(() => {
  clearTimeout(scrollTimeout);
  scrollTimeout = setTimeout(() => {
    const currentCols = terminal.cols;
    const currentRows = terminal.rows;
    terminal.resize(currentCols, currentRows - 1);
    requestAnimationFrame(() => {
      terminal.resize(currentCols, currentRows);
    });
  }, 50);
});
```

### D. Alternative Libraries
- **hterm** (Google) - Different rendering implementation
- **SwiftTerm** - Native Swift terminal (hybrid architecture)

### E. File xterm.js Bug Report
With detailed reproduction steps and WKWebView context.

---

## Known xterm.js Issues (From Research)

1. **Safari/WKWebView ImageBitmap bug** - xterm.js v5.1.0 disabled canvas ImageBitmap optimization on Safari because `createImageBitmap` is broken

2. **CharAtlas Life Cycling Bug (Issue #3548)** - Canvas renderer fails to properly refresh character atlas under certain conditions

3. **customGlyphs limitation** - Only works with Canvas/WebGL renderers, not DOM

---

## Reproduction Steps

1. Launch app: `cd cmd/agent-deck-desktop && ~/go/bin/wails dev`
2. Connect to any session with tables (worktree list shows this)
3. Scroll UP through history (away from bottom)
4. Stop scrolling mid-viewport (not at boundaries)
5. Observe: `─────` sequences show as `───???───`
6. Scroll to bottom: Corruption disappears
7. Scroll back to middle: Corruption reappears

---

## Session Log

### 2025-01-24 - Session Start
- Received handoff documenting ~5 hours of prior investigation
- Root cause confirmed: canvas bitmap caching issue in WKWebView
- Next steps: Try CSS workarounds, then resize trick

### 2025-01-24 - Session 2 (Current)

#### Attempted Fix #1: CSS GPU Compositing
Added to `.xterm-screen`:
```css
transform: translateZ(0);
will-change: transform;
```
**Result:** No effect on corruption.

#### Attempted Fix #2: Force Resize After Scroll (term.resize)
Called `term.resize(cols, rows-1)` then `term.resize(cols, rows)` after scroll settles.
**Result:** No effect. Programmatic resize doesn't trigger same repaint as manual resize.

#### Attempted Fix #3: Force Container Resize
Changed container height by 1px to trigger ResizeObserver → fitAddon.fit().
**Result:** No effect.

#### Attempted Fix #4: Canvas Renderer Reload
Dispose and recreate CanvasAddon after scroll settles.
```javascript
canvasAddonInstance.dispose();
canvasAddonInstance = new CanvasAddon();
term.loadAddon(canvasAddonInstance);
```
**Result:** Canvas reload executes successfully (confirmed via logs) but does NOT fix corruption.

#### Logging Mystery
- Added `LogFrontendDiagnostic()` calls to send logs to backend log file
- Backend log shows scroll events firing during initial load (`[SCROLL:content]`)
- Backend log shows viewport scroll events (`[SCROLL:viewport]`)
- User reports NO console logs visible in browser devtools when manually scrolling
- Possible issue: Events may not be firing for user scroll, only programmatic scroll during load

#### Key Discovery: Wrong Scroll Event
- `term.onScroll()` fires for CONTENT scroll (new output pushing content up)
- Does NOT fire for VIEWPORT scroll (user scrolling with mouse/trackpad)
- Added listener to `.xterm-viewport` element's native scroll event
- Viewport listener attaches successfully (`scrollHeight=608`)
- But user reports no visible feedback when scrolling manually

#### Attempted Fix #5: Wheel Event Listener
Switched from `scroll` event to `wheel` event on terminal container.
```javascript
terminalRef.current.addEventListener('wheel', handleWheel, { passive: true });
```
**Result:** Wheel events do NOT fire when user scrolls manually. Only fire during initial session load.

#### Critical Finding: No User Scroll Events in WKWebView
- `term.onScroll()` - Only fires for content scroll (new output), not user scrolling
- `.xterm-viewport` scroll event - Does not fire for user scrolling
- `wheel` event on container - Does not fire for user scrolling
- **All scroll events only fire during initial session load, never during manual user scrolling**

This suggests xterm.js in WKWebView is either:
1. Using a custom scroll implementation that doesn't fire standard DOM events
2. WKWebView is intercepting/blocking scroll events before they reach JavaScript
3. There's a capture-phase event handler preventing propagation

#### Current Status
- All programmatic fixes to force repaint have failed
- Canvas renderer reload works but doesn't fix corruption
- **Cannot detect user scroll events at all** - this blocks any scroll-triggered fix
- Root cause appears to be xterm.js + WKWebView interaction, not fixable from application code

#### Potential Next Steps
1. File xterm.js bug report with detailed WKWebView reproduction
2. Investigate xterm.js source for scroll handling in Safari/WebKit
3. Try alternative terminal libraries (hterm, SwiftTerm)
4. Accept limitation and document as known issue
5. Investigate if Wails has options for different WebView engines

### 2026-01-24 - Session 3

#### Research: Known xterm.js Issues

Found multiple GitHub issues documenting the scroll problem:
- **Issue #3864** - "onScroll event does not get fired on user scroll" - CLOSED as fixed in v5.6.0-beta (merged into v6.0.0)
- **Issue #3201** - "onScroll doesn't emit when user is scrolling" - Same issue, older report
- **Issue #3575** - "Does not work in WKWebView" - Safari detection doesn't catch WKWebView

Key finding: v6.0.0 completely rewrote the viewport/scrollbar using VS Code's implementation.

#### Attempted Fix #6: Upgrade to xterm.js v6.0.0

Upgraded from v5.5.0 to v6.0.0 (CanvasAddon removed, DOM renderer default).

**Changes:**
- Removed `@xterm/addon-canvas` (no longer exists in v6)
- Updated all addons to v6-compatible versions
- Removed deprecated options (`windowsMode`, `fastScrollModifier`)

**Result:** Corruption STILL occurs with DOM renderer. This proves the issue is NOT specific to CanvasAddon - it's a deeper WKWebView rendering issue.

#### Attempted Fix #7: WebGL Renderer (v6)

Added `@xterm/addon-webgl` to test if WebGL rendering path avoids the corruption.

**Result:**
- WebGL addon loads successfully (no crash like in v5!)
- However, WebGL appears to break scroll detection
- Corruption STILL occurs with WebGL renderer

#### Key Observations with v6

1. **Scroll events momentarily worked during hot-reload** - User reported seeing data logs every scroll line briefly, then stopped after full restart
2. **scrollTop stays at 0** even when scrolling - The `.xterm-viewport` element's scrollTop doesn't update
3. **All three renderers corrupt:** Canvas (v5), DOM (v6), WebGL (v6) - problem is in WKWebView itself

#### Attempted Fix #8: RAF Polling for Scroll Detection

Implemented requestAnimationFrame loop to poll `scrollTop` every frame.

**Result:** Polling runs but scrollTop never changes from 0, even when user is visibly scrolling.

#### Current Understanding

The corruption occurs across ALL xterm.js renderers (Canvas, DOM, WebGL). This means:
1. It's NOT a Canvas-specific bitmap caching issue
2. It's NOT a renderer implementation issue
3. It's likely a **WKWebView compositing/rendering bug**

The fact that manual actions (text selection, window resize) fix the corruption suggests WKWebView is caching rendered layers incorrectly during scroll operations.

#### Still To Try

1. **Disable Unicode11Addon** - May be affecting character width calculations
2. **Different font** - Try system monospace font instead of MesloLGS NF
3. **Manual refresh button** - Accept limitation, provide user workaround
4. **hterm library** - Completely different implementation, may not have this issue

#### Current Code State (End of Session 3)

**xterm.js version:** 6.0.0 with DOM renderer (Canvas removed in v6, WebGL breaks scroll detection)

**Scroll detection methods currently implemented:**
1. `term.onScroll()` - xterm.js native API
2. DOM scroll listener on `.xterm-viewport`
3. Wheel event listener with capture phase on terminal container

**Unicode11Addon:** Currently DISABLED for testing

**Key file:** `cmd/agent-deck-desktop/frontend/src/Terminal.jsx`

#### Summary of Findings

| Renderer | Version | Corruption? | Scroll Detection? |
|----------|---------|-------------|-------------------|
| Canvas   | v5.5.0  | YES         | NO (no events in WKWebView) |
| DOM      | v6.0.0  | YES         | **YES with capture phase!** |
| WebGL    | v6.0.0  | YES         | NO (breaks detection) |

**Critical Finding:** The rendering corruption occurs with ALL three renderers, indicating this is a **WKWebView compositing bug**, not an xterm.js renderer issue.

### BREAKTHROUGH: Wheel Events Work with Capture Phase!

**Solution for scroll detection:**
```javascript
terminalRef.current?.addEventListener('wheel', handleWheel, { passive: true, capture: true });
```

The `capture: true` flag is critical. In capture phase, events travel from document root DOWN to target, intercepting BEFORE xterm.js can handle/stop propagation.

**Why this works:**
- Standard event listeners use "bubble" phase (target → root)
- xterm.js handles wheel events internally and may stop propagation
- Capture phase listeners fire BEFORE bubble phase (root → target)
- Our capture listener intercepts wheel events before xterm.js sees them

**Confirmed working:** WHEEL events now fire in real-time during user scrolling in WKWebView!

#### Remaining Work

Now that we can detect scroll, we need to:
1. **Implement a repaint strategy** that actually fixes the corruption
2. **Test various repaint approaches** triggered after scroll settles:
   - `term.refresh(0, rows-1)` - Already tried, didn't work alone
   - Canvas addon reload - Tried, didn't work
   - Micro-resize trick
   - Force texture atlas clear
   - CSS layer invalidation

#### Recommended Next Steps (Priority Order)

1. **Implement scroll-triggered repaint** - Now that we can detect scroll, try various repaint strategies
2. **Test if disabling Unicode11Addon helps rendering** - Currently disabled, check if corruption reduced
3. **Try hterm** - If repaint strategies don't work, different library may not have WKWebView issues

---

### 2026-01-24 - Session 4 (Current)

#### Attempted Fix #9: Scroll-Triggered Repaint Strategies

Implemented debounced repaint after wheel events settle (150ms):
```javascript
const attemptRepaint = async () => {
    // Strategy 1: Standard xterm.js refresh
    term.refresh(0, term.rows - 1);

    // Strategy 2: CSS opacity toggle (force recomposite)
    screenEl.style.opacity = '0.999';
    await raf();
    screenEl.style.opacity = '1';

    // Strategy 3: CSS transform toggle (force new compositing layer)
    screenEl.style.transform = 'translateZ(0)';
    await raf();
    screenEl.style.transform = '';
};
```

**Result:** ❌ FAILED. Corruption persists. Logs confirm repaint strategies execute, but visual corruption remains.

#### CRITICAL CORRECTION: Resize Doesn't Fix the Corruption!

**Previous assumption was WRONG.** We thought window resize fixed the corruption.

**Actual behavior:**
- Resize triggers `scrollToBottom()` as a side effect
- When scrolled to bottom, content renders correctly
- When scrolled back up, corruption reappears
- **Resize itself does nothing - it's the scroll-to-bottom that "fixes" it**

This is NOT a viable solution because forcing scroll-to-bottom destroys UX.

#### Key Insight: Bottom vs Scrollback Rendering Difference

**Corruption appears:** When viewing scrollback history (scrolled up from bottom)
**Corruption disappears:** When viewport is at the bottom (viewing live output area)

**The question:** Why does the same content render correctly at the bottom but corrupt when scrolled up?

#### Analysis: What's Different Between Bottom and Scrollback?

**1. xterm.js Buffer Perspective:**
- Buffer data is the same - verified via `translateToString()`
- Viewport position is different - `viewportY` vs `baseY`
- When at bottom: `viewportY === baseY` (viewing active area)
- When scrolled up: `viewportY < baseY` (viewing scrollback)

**2. Rendering Perspective:**
- At bottom: Rows are "live" - new PTY output continuously updates them
- Scrolled up: Rows are "frozen" - no new content, just cached render

**3. tmux Perspective:**
- Normal mode: Application writes directly to visible area
- Copy mode: Not relevant here - our scrolling is xterm.js internal
- Scrollback capture: Static snapshot via `capture-pane -p`

**4. WKWebView Compositing Hypothesis:**
- Visible rows at bottom may be in "active" compositing layer
- Off-screen scrollback may be in different/cached layer
- When scrolling brings cached layers into view, they're stale
- But when at bottom, the active layer is always fresh

**5. Possible Data Difference:**
- Scrollback loaded via `RefreshScrollback()` - static capture
- Live output via PTY streaming - dynamic with escape sequences
- Could there be encoding/escape sequence differences?

#### Why Manual Actions Fix It (Temporarily)

| Action | Why it works |
|--------|-------------|
| Text selection | Forces repaint of selected rows |
| Window resize | Forces `scrollToBottom()` + full re-layout |
| Scroll to bottom | Brings active layer into view |
| Scroll to exact top | Boundary condition triggers something |

All of these either force a full repaint OR move viewport to a boundary where rendering behaves differently.

#### Still To Investigate

1. **Is the corrupted content in xterm.js buffer itself, or just visual?**
   - Call `buffer.getLine(row).translateToString()` for visible corrupted rows
   - If data is clean, problem is purely rendering

2. **Does the corruption exist in the DOM/Canvas elements?**
   - Inspect the actual rendered characters in devtools
   - Compare visible glyph to expected Unicode codepoint

3. **Re-write visible rows from tmux on scroll settle?**
   - Instead of trying to "repaint", actually re-fetch and re-write the visible portion
   - More aggressive but might work

4. **Force rows into active rendering path?**
   - Can we trick xterm.js into treating scrollback rows as "active"?

#### Next Approach to Try

**Re-fetch visible scrollback region after scroll:**
```javascript
const attemptRepaint = async () => {
    // Get current viewport position
    const viewportY = term.buffer.active.viewportY;
    const visibleRows = term.rows;

    // Calculate which tmux lines are visible
    // Re-capture just that portion from tmux
    // Clear and rewrite those specific rows
};
```

This is more invasive but attacks the root cause: if the static scrollback capture is corrupting, re-fetch it fresh.

---

### 2026-01-24 - Session 4 (Continued)

#### MAJOR BREAKTHROUGH: Wheel vs Scrollbar Rendering

**Critical Discovery:** Corruption ONLY occurs with mouse wheel scrolling. Dragging the scrollbar renders everything perfectly!

| Scroll Method | Corruption? |
|---------------|-------------|
| Mouse wheel / trackpad | YES - box chars corrupt |
| Dragging scrollbar | NO - renders perfectly |

**Implications:**
1. The xterm.js buffer and rendering code is fine
2. WKWebView handles wheel scroll vs scrollbar drag differently at the compositor level
3. Wheel scrolling may use different GPU compositing path (smooth/momentum scroll)
4. Scrollbar drag may trigger synchronous repaint while wheel uses async/batched

**Potential Solutions:**
1. Intercept wheel events and translate to programmatic `scrollLines()` or `scrollToLine()` calls
2. Disable native wheel handling and implement custom scroll behavior
3. After wheel scroll, programmatically "nudge" the scrollbar to trigger the good rendering path
4. Use CSS to disable smooth scrolling / momentum effects

#### THE FIX: Intercept Wheel Events + Programmatic Scroll

**Solution implemented:**
```javascript
const handleWheel = (e) => {
    // PREVENT default wheel behavior
    e.preventDefault();
    e.stopPropagation();

    // Calculate lines to scroll based on deltaY
    const linesToScroll = Math.sign(e.deltaY) * Math.max(1, Math.ceil(Math.abs(e.deltaY) / 30));

    // Use xterm's programmatic scroll - uses same rendering path as scrollbar
    xtermRef.current.scrollLines(linesToScroll);
};

// Must use passive: false to allow preventDefault
terminalRef.current?.addEventListener('wheel', handleWheel, { passive: false, capture: true });
```

**Why it works:**
- Native wheel scroll in WKWebView uses GPU compositor with buggy layer caching
- Scrollbar drag uses a different (correct) rendering path
- `term.scrollLines()` programmatically updates the viewport, triggering the same clean rendering as scrollbar drag
- By intercepting wheel events and preventing default, we bypass WKWebView's broken wheel scroll compositor

**Result:** ✅ FIXED! Smooth scrolling with no corruption.

---

## Summary: Root Cause and Fix

**Root Cause:** WKWebView's GPU compositor handles native wheel scroll events differently than programmatic scroll/scrollbar drag. The wheel scroll path has a bug that causes stale/corrupted bitmap layers to be displayed for Unicode box-drawing characters.

**Fix:** Intercept wheel events, prevent default browser handling, and use xterm.js's `scrollLines()` API to scroll programmatically. This uses the same rendering path as scrollbar drag, which renders correctly.

**Key insight:** The corruption was never in the data or xterm.js - it was purely a WKWebView compositor bug triggered only by native wheel scroll events.

