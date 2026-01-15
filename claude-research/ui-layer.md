# UI Layer Architecture - Deep Research

**Date:** 2026-01-15
**Agent:** Explore

## Executive Summary

Agent Deck's UI layer is a sophisticated Bubble Tea application managing terminal-based session management with 5,419 lines in home.go alone. The architecture employs advanced patterns: responsive layouts, async/await for I/O operations, background worker goroutines, TTL caching, optimized viewport rendering, and atomic operations for thread safety. It handles 100+ sessions with <100ms ticks through strategic optimizations including round-robin status updates, debounced preview fetching, and async analytics parsing.

---

## L2 Architecture: Bubble Tea Model Structure

### Core Model (Home struct - lines 108-222)

The `Home` model is the single state container for Bubble Tea's MVP (Model-View-Presenter) pattern:

```go
type Home struct {
    // Dimensions
    width, height int
    profile string

    // Data layer (thread-safe)
    instances    []*session.Instance
    instanceByID map[string]*session.Instance  // O(1) lookup
    instancesMu  sync.RWMutex                  // For background worker
    storage      *session.Storage
    groupTree    *session.GroupTree
    flatItems    []session.Item                // Flattened tree for cursor nav

    // UI Components (dialogs, overlays)
    search, globalSearch, newDialog, groupDialog, forkDialog
    confirmDialog, helpOverlay, mcpDialog, setupWizard
    settingsPanel, analyticsPanel

    // State management
    cursor, viewOffset int
    statusFilter       session.Status           // ""/running/waiting/idle/error
    isAttaching        atomic.Bool              // Prevents View() output during attach
    isReloading        bool                     // Reload guard
    initialLoading     bool                     // Splash screen

    // Async/caching
    previewCache, previewCacheTime      map[string]...
    analyticsCache, analyticsCacheTime  map[string]...
    launchingSessions, resumingSessions map[string]time.Time  // For animations

    // Background workers
    statusTrigger     chan statusUpdateRequest  // Buffered, 1 element
    statusWorkerDone  chan struct{}
    logWatcher        *tmux.LogWatcher          // Event-driven updates
    storageWatcher    *StorageWatcher           // Auto-reload on CLI changes
}
```

**Key Insight**: Thread safety achieved via:
- `instancesMu` RWMutex for background worker access
- `atomic.Bool` for simple flags (isAttaching, cachedStatusCounts.valid)
- `sync.Mutex` for small, frequently-accessed state (previewDebounceMu, reloadMu)

### Message Flow Architecture

Bubble Tea's event loop: **Update(msg) → (Model, Cmd) → Render View()**

Messages are strongly-typed structs representing all possible events:

```go
// User input
tea.KeyMsg                  // Keyboard (handled by handleMainKey)
tea.WindowSizeMsg           // Terminal resize

// Async operations returning results
loadSessionsMsg             // Storage.LoadWithGroups()
sessionCreatedMsg           // New session CLI or TUI created
sessionForkedMsg            // Session forked (Claude only)
previewFetchedMsg          // tmux capture-pane result
previewDebounceMsg         // Debounce timer elapsed
analyticsFetchedMsg        // JSONL parsing complete
updateCheckMsg             // Version check result

// Internal events
tickMsg                     // Periodic tick (1s) for status updates
refreshMsg                  // Ctrl+R reload
statusUpdateMsg             // Return from attached session
storageChangedMsg           // sessions.json changed externally
```

**Pattern**: Each async operation returns a `tea.Cmd` that executes in background and sends a message back:

```go
// Async command
func (h *Home) fetchPreview(inst *session.Instance) tea.Cmd {
    return func() tea.Msg {
        content, err := inst.PreviewFull()
        return previewFetchedMsg{sessionID, content, err}
    }
}
// Result handled in Update() case previewFetchedMsg
```

### Layout Mode System (Lines 79-91)

Responsive design with 3 breakpoints:

```
Width <50: LayoutModeSingle   (list only)
Width <80: LayoutModeStacked  (list 60%, preview 40%, vertical stack)
Width 80+: LayoutModeDual     (list 35%, preview 65%, side-by-side)
```

Each layout has its own renderer: `renderSingleColumnLayout()`, `renderStackedLayout()`, `renderDualColumnLayout()` that recalculate panel heights accounting for title (2 lines), help bar (2 lines), filter bar (1 line).

---

## L3 Implementation: Update/View Cycle

### Update() Handler (Lines 1187-1766)

The main message dispatcher uses a switch statement with proper ordering:

1. **Window resize** → recalculate sizes, sync viewport
2. **Modal dialogs** → setup wizard, settings panel
3. **Overlays** → help, search, global search, new dialog, group dialog, fork dialog, confirm dialog, MCP dialog
4. **Main view handlers** → `handleMainKey()` for navigation/actions
5. **Async messages** → loadSessionsMsg, previewFetchedMsg, etc.

**Critical pattern**: Modals block underlying interaction, overlays work on top of main view:

```go
if h.setupWizard.IsVisible() {
    h.setupWizard, cmd = h.setupWizard.Update(msg)
    return h, cmd  // Exit early - blocks everything
}
if h.search.IsVisible() {
    return h.handleSearchKey(msg)  // Search overlay
}
// Main view handling
return h.handleMainKey(msg)
```

### Key Message Handlers

#### loadSessionsMsg (Lines 1201-1274)
- Replaces entire instances slice
- Rebuilds `instanceByID` map for O(1) lookup
- Runs dedup to fix duplicate Claude session IDs
- Preserves group expanded state
- Invalidates preview/analytics caches
- Saves to disk if initial load, otherwise relies on storage watcher

**Performance**: Only triggered on startup or Ctrl+R. Prevents thrashing.

#### sessionCreatedMsg (Lines 1276-1325)
- **Critical guard**: `if h.isReloading { return h, nil }` prevents state corruption
- Appends to instances slice (incremental, not reload)
- Marks as "launching" (triggers animation)
- Expands parent group for visibility
- Auto-selects new session
- Fetches initial preview async

#### tickMsg (Lines 1600-1683)
- Runs every 1 second (configurable at top of file)
- **Status updates**: Triggers background worker only if NOT navigating/idle
- **Log maintenance**: Fast check (10s), full maintenance (5 min)
- **Animation cleanup**: Removes expired launching/resuming/MCP loading states
- **Preview refresh**: Fetches if cache expired (2s TTL) and not currently fetching

**Key optimization**: Navigation detection (300ms debounce) skips status updates during rapid scrolling

### View() Function (Lines 3159-3386)

The rendering function is PURE (no I/O, no mutations):

```
1. Early exit if attaching (prevents View() output during tea.Exec) - atomic read
2. Check minimum terminal size
3. Show splash screen during initial load
4. Show modal dialogs (setup wizard, settings panel)
5. Show full-screen overlays (help, search)
6. Render main layout:
   - Header bar (logo, title, stats)
   - Filter bar (quick status filters)
   - Update banner (if available)
   - Content area (responsive layout)
   - Help bar (context-aware shortcuts)
   - Error messages (auto-dismiss after 5s)
7. Ensure exact height constraint using ensureExactHeight()
8. Apply width constraint via lipgloss
```

**Critical insight**: View() reuses a pre-allocated 32KB string builder to reduce allocations on every tick.

### Viewport Management (Lines 594-699)

**Challenge**: Synchronize cursor position with visible viewport while accounting for dynamic "more above"/"more below" indicators.

```go
// Calculate visible height accounting for title and help bar
contentHeight := height - 1 - 2 - updateBannerHeight - 1  // header, help, banner, filter

// Panel content (after 2-line title)
panelContentHeight := contentHeight - 2

// Visible items accounting for indicators
maxVisible := panelContentHeight - 1  // Reserve 1 for "more below"
if viewOffset > 0 {
    maxVisible--  // Account for "more above"
}

// Scroll cursor into view
if cursor < viewOffset {
    viewOffset = cursor
}
if cursor >= viewOffset + effectiveMaxVisible {
    // Adjust accounting for "more above" appearing
    viewOffset = cursor - effectiveMaxVisible + 1
}
```

**Critical**: The height calculation in `syncViewport()` must EXACTLY match the calculations in `renderSessionList()` and layout renderers. Any mismatch causes flickering or missing items.

---

## L4 Scale & Performance

### Performance Optimizations Summary

| Problem | Solution | Impact |
|---------|----------|--------|
| 30 subprocess calls/tick (tmux status checks) | Session cache, batched list-sessions | 97% reduction |
| Status updates block UI with 100+ sessions | Background worker + round-robin batching | 90% CPU reduction |
| Preview subprocess spawning on every keystroke | 150ms debounce, 2s TTL cache | <100ms per key |
| High CPU during idle periods | User activity tracking (2s window) | No polling when idle |
| Rapid navigation causes flickering | Navigation settle time (300ms) + suspension | Smooth scrolling |
| High memory with many MCPs (30 sessions × 5 MCPs) | Socket pooling (Unix sockets) | 80% reduction |
| Log files growing unchecked | Fast check (10s), full maintenance (5 min), truncation | Prevents crashes |

### Background Status Worker (Lines 1051-1185)

Runs in dedicated goroutine, decoupled from UI:

```go
// Round-robin algorithm
visible_sessions.UpdateStatus()  // Always first
remaining := batchSize (2)
for i < len(all_sessions) && remaining > 0:
    if not_updated:
        session.UpdateStatus()
        remaining--

// Only invalidate cache if status actually changed
if statusChanged {
    cachedStatusCounts.valid.Store(false)
}
```

**Thread-safe design**:
- Takes snapshot under RWMutex lock
- Updates happen in worker goroutine (no locks needed)
- Only main goroutine modifies UI state (cursor, viewport)

### Async Fetching Patterns

**Three tiers of async operations**:

1. **Preview content** (fetchPreview)
   - Spawns `tmux capture-pane` (expensive)
   - Result cached with 2s TTL
   - Debounced 150ms during navigation
   - One fetch at a time (previewFetchingID)

2. **Analytics** (fetchAnalytics)
   - Parses JSONL file (I/O bound)
   - Result cached with 5s TTL
   - One fetch at a time
   - Only for Claude sessions with analytics enabled

3. **Update checks** (checkForUpdate)
   - HTTP call on startup
   - One-time, low priority

---

## L5 Extension Points & Architecture

### Dialog Component Pattern

All dialogs follow consistent interface (Show/Hide/IsVisible/Update/View):

```go
type Dialog interface {
    Show()
    Hide()
    IsVisible() bool
    Update(msg tea.Msg) (Dialog, tea.Cmd)
    View() string
    SetSize(width, height int)
}
```

**Implementations**:
- `NewDialog` - Create/edit session (text inputs + suggestions)
- `GroupDialog` - Create/rename/move groups (multi-mode)
- `ForkDialog` - Fork session options (Claude only)
- `ConfirmDialog` - Destructive action confirmation
- `MCPDialog` - Attach/detach MCPs (two-column, two-scope)
- `HelpOverlay` - Keyboard shortcuts
- `SetupWizard` - First-run configuration
- `SettingsPanel` - User config editing
- `AnalyticsPanel` - Session analytics display
- `GlobalSearch` - Full-text search across Claude projects

### Adding a New View/Dialog

1. **Create component file** (`internal/ui/mydialog.go`)
   ```go
   type MyDialog struct {
       visible bool
       width, height int
       // ... component state
   }

   func NewMyDialog() *MyDialog { ... }
   func (m *MyDialog) Show() { m.visible = true }
   func (m *MyDialog) Hide() { m.visible = false }
   func (m *MyDialog) IsVisible() bool { return m.visible }
   func (m *MyDialog) SetSize(w, h int) { m.width, m.height = w, h }
   func (m *MyDialog) Update(msg tea.Msg) (*MyDialog, tea.Cmd) { ... }
   func (m *MyDialog) View() string { ... }
   ```

2. **Add to Home struct** (home.go line ~130)
   ```go
   myDialog *MyDialog
   ```

3. **Initialize in NewHome()** (home.go line ~340)
   ```go
   myDialog: NewMyDialog(),
   ```

4. **Add to View() overlay check** (home.go line ~3210)
   ```go
   if h.myDialog.IsVisible() {
       return h.myDialog.View()
   }
   ```

5. **Add to Update() dispatcher** (home.go line ~1750)
   ```go
   if h.myDialog.IsVisible() {
       return h.handleMyDialogKey(msg)
   }
   ```

6. **Implement key handler** (e.g., home_handlers.go)
   ```go
   func (h *Home) handleMyDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
       var cmd tea.Cmd
       h.myDialog, cmd = h.myDialog.Update(msg)
       if /* user pressed submit */ {
           h.myDialog.Hide()
           // Handle submission
       }
       return h, cmd
   }
   ```

### Adding a Keyboard Shortcut

1. **Add case in handleMainKey()** (home.go ~2000+)
   ```go
   case "x":
       h.myDialog.Show()
       h.myDialog.SetSize(h.width, h.height)
   ```

2. **Update help overlay** (help.go ~100)
   ```go
   {"x", "My action"},
   ```

3. **Test with `make test`**
   ```bash
   go test ./internal/ui/...
   ```

### Theming System (styles.go)

Tokyo Night color scheme with light alternative:

```go
// Set at package init, changed via InitTheme()
var darkColors = struct {
    Bg, Surface, Border, Text, TextDim lipgloss.Color
    Accent, Purple, Cyan, Green, Yellow lipgloss.Color
    Orange, Red, Comment lipgloss.Color
}

// All UI styles recreated on theme change
func initStyles() {
    BaseStyle = NewStyle().Foreground(ColorText).Background(ColorBg)
    SessionStatusRunning = NewStyle().Foreground(ColorGreen)
    // ... 100+ style definitions
}
```

### Multi-Instance Synchronization (storage_watcher.go)

**Challenge**: Multiple TUI instances editing same sessions.json file

**Solution**: File watcher with debouncing and ignore window

```go
// Monitor parent directory for atomic renames
watcher.Add(filepath.Dir(storagePath))

// On file change:
// 1. Check modification time changed
// 2. Check within 500ms of TUI's own save (ignore self)
// 3. If external change detected, trigger reload
// 4. Preserve UI state (cursor, scroll, expanded groups)
```

**Key patterns**:
- Pre-resolve symlinks at init (handles /tmp → /private/tmp on macOS)
- Compare ABSOLUTE paths, not just basenames (prevents cross-profile contamination)
- Debounce 100ms (batch rapid writes)
- Buffered channel (prevents blocking if TUI busy)
- NotifySave() called immediately before save (timing critical)

---

## Key Files Reference

| File | LOC | Purpose |
|------|-----|---------|
| home.go | 5,419 | Main model, Update/View, message handlers, status worker |
| styles.go | 691 | Tokyo Night theme, color schemes, style presets |
| storage_watcher.go | 206 | File monitoring for auto-reload |
| newdialog.go | 400+ | Session creation with path suggestions, worktree support |
| group_dialog.go | 200+ | Group management (create/rename/move) |
| mcp_dialog.go | 300+ | MCP attach/detach with two-scope, two-column interface |
| search.go | 200+ | Local session search, fuzzy matching |
| global_search.go | 300+ | Full-text search across Claude projects |
| preview.go | 93 | Session terminal output preview pane |
| help.go | 199 | Keyboard shortcuts overlay |
| menu.go | 41 | Bottom menu bar (legacy, mostly unused) |
| confirm_dialog.go | 190 | Destructive action confirmation |
| tree.go | 106 | Tree navigation (legacy, folder management) |

---

## Critical Design Decisions

1. **Pure View() function**: No I/O, no mutations. Prevents Bubble Tea Issue #431 (View output during tea.Exec).

2. **Background worker goroutine**: Decouples expensive operations (tmux calls) from UI thread. Critical for 100+ session scalability.

3. **Atomic bools for simple flags**: isAttaching, cachedStatusCounts.valid avoid RWMutex overhead for single bits.

4. **TTL caching over immediate refresh**: Preview (2s), analytics (5s) balances freshness with CPU cost.

5. **Debouncing over rate limiting**: 150ms debounce for preview allows user to rapidly navigate without spawning subprocesses.

6. **State preservation across reload**: cursor position, scroll offset, expanded groups restored after storage reload.

7. **Responsive layouts**: Adapt to 40-char mobile terminals up to 160-char wide monitors without hardcoded widths.

8. **Pre-allocated buffers**: 32KB string builder in View() reduces GC pressure on every tick.

---

## Performance Metrics

- **CPU**: 15% → 0.5% for idle sessions (97% reduction via session cache)
- **Tick time**: <100ms with 10 sessions under load (round-robin batching)
- **Memory**: 80% reduction with MCP pooling (150 node processes → 5 shared)
- **Subprocess calls**: 30 → 1 per tick (batched tmux list-sessions)
- **Log growth**: Capped via truncation (prevents 1.6GB in minutes)

---

## References

- **Bubble Tea**: github.com/charmbracelet/bubbletea (MVP framework)
- **Lipgloss**: github.com/charmbracelet/lipgloss (styling)
- **Bubbles**: github.com/charmbracelet/bubbles (components like textinput)
