# Testing & Debugging Guide

## Development Tools Enabled

### Chrome DevTools
When running in development mode (`wails dev`), Chrome DevTools opens automatically in the app window.

**Access:**
- DevTools opens on startup in dev mode
- Right-click anywhere → "Inspect Element"

**Features:**
- Console for viewing logs
- Network tab for Go ↔ Frontend communication
- Elements tab for inspecting DOM
- Performance profiling

### Logging

#### Frontend Logging
Comprehensive logging via `logger.js` utility:

```javascript
import { createLogger } from './logger';
const logger = createLogger('ComponentName');

logger.debug('Debug message');  // Dev only
logger.info('Info message');     // Always shown
logger.warn('Warning');          // Yellow, bold
logger.error('Error');           // Red, bold
```

**Active loggers:**
- `[App]` - Navigation, view changes
- `[Terminal]` - PTY lifecycle, attach/detach
- `[SessionSelector]` - Session loading

**Console output:**
```
[Terminal] Initializing terminal session: hypercurrent
[Terminal] Attaching to tmux session: agentdeck_hypercurrent_cc94c5bb
[Terminal] Attached successfully
```

#### Backend Logging
Go logs via Wails logger:

```go
runtime.LogInfo(ctx, "Message")
runtime.LogDebug(ctx, "Debug info")
runtime.LogError(ctx, "Error details")
```

**Log levels:**
- Development: DEBUG
- Production: ERROR

### Session Lifecycle Logging

Watch for these events in DevTools console:

1. **Session Selection:**
   ```
   [App] Selecting session: hypercurrent
   [Terminal] Initializing terminal session: hypercurrent
   [Terminal] Attaching to tmux session: agentdeck_hypercurrent_cc94c5bb
   [Terminal] Attached successfully
   ```

2. **Back to Selector:**
   ```
   [App] Returning to session selector
   [Terminal] Cleaning up terminal
   ```

3. **Errors:**
   ```
   [Terminal] Failed to start terminal: <error>
   [SessionSelector] Failed to load sessions: <error>
   ```

## Testing Approach

### Manual Testing (Current)
Use DevTools console to observe:
- Navigation flows
- PTY lifecycle
- Error conditions
- Performance issues

### Unit Testing (Future)

**Frontend (Vitest):**
```bash
cd frontend
npm run test
```

Test files: `*.test.jsx` alongside components

**Backend (Go):**
```bash
go test ./...
```

Test files: `*_test.go`

### E2E Testing (Future Consideration)

**Limitations:**
- No Playwright support for Wails apps
- Desktop automation is platform-specific

**Possible approaches:**
1. **macOS Accessibility API** - UI automation via AppleScript/Swift
2. **Manual test checklist** - Documented workflows
3. **Screenshot testing** - Visual regression (via Wails API)

**Current recommendation:** Manual testing with comprehensive logging is most practical for prototype phase.

## Bug Reports

When filing issues, capture:
1. **Console logs** - Copy from DevTools
2. **Go logs** - From terminal running `wails dev`
3. **Steps to reproduce** - Numbered list
4. **Screenshots** - If visual issue
5. **Environment** - macOS version, terminal shell

## Common Issues & Debugging

### Blank Terminal After Navigation
**Symptom:** Terminal shows nothing after selecting session
**Check:** Console for errors, look for "Cleaning up terminal" followed by "Initializing terminal"
**Fix:** Ensure PTY closes cleanly before new session starts

### Session List Empty
**Symptom:** "No active sessions found"
**Debug:** Check `[SessionSelector] Loaded sessions: 0`
**Verify:** `cat ~/.agent-deck/profiles/default/sessions.json`

### Garbled Terminal Output
**Symptom:** Text corruption, overlapping prompts
**Debug:** Look for terminal resize events in console
**Check:** PTY size matches xterm dimensions

### DevTools Not Opening
**Symptom:** No inspector on startup
**Fix:** Ensure running `wails dev` (not production build)
**Verify:** Logs show "Using Frontend DevServer URL"

## Performance Profiling

**Terminal rendering:**
1. Open DevTools → Performance tab
2. Start recording
3. Navigate between sessions
4. Stop recording
5. Analyze flamegraph for bottlenecks

**Memory leaks:**
1. Heap snapshots before/after navigation
2. Look for detached DOM nodes
3. Check PTY cleanup in Go (via logging)

## Future Improvements

- [ ] Screenshot API via Wails
- [ ] Automated smoke tests for core flows
- [ ] Performance benchmarks (terminal attach time)
- [ ] Error boundary with user-friendly messages
- [ ] Crash reporting (local logs)
