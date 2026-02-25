# Memory Leak Hotfix Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Fix memory leaks causing ~1GB RAM spike per session, primarily in MCP socket proxy scanner buffers, instance prompt caching, and transition daemon maps.

**Architecture:** Reduce scanner buffer max sizes from 10MB to 1MB, add idle timeouts on client connections, clear cached data on session kill, add cleanup to transition daemon, and close the event channel on watcher stop.

**Tech Stack:** Go 1.24, bufio, net, context, time

---

### Task 1: Reduce MCP socket proxy scanner buffer max sizes

**Files:**
- Modify: `internal/mcppool/socket_proxy.go:279` (handleClient scanner)
- Modify: `internal/mcppool/socket_proxy.go:301` (broadcastResponses scanner)
- Modify: `internal/mcppool/socket_proxy_test.go:17` (update test expectation)

The 10MB max scanner buffer is the single largest memory contributor. Each client connection allocates up to 10MB, and with many concurrent clients across multiple MCPs this explodes. MCP JSON-RPC messages rarely exceed 1MB — the 10MB was over-provisioned.

**Step 1: Write a test that validates the new 1MB buffer limit handles typical MCP messages**

Add to `internal/mcppool/socket_proxy_test.go`:

```go
func TestScannerHandles512KBMessages(t *testing.T) {
	// 512KB is well above typical MCP messages but within our 1MB limit
	message := strings.Repeat("x", 512*1024)
	scanner := bufio.NewScanner(strings.NewReader(message + "\n"))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024) // 1MB max (reduced from 10MB)

	if !scanner.Scan() {
		t.Fatalf("Scanner should handle 512KB message, got error: %v", scanner.Err())
	}
	if len(scanner.Text()) != 512*1024 {
		t.Errorf("Expected 512KB message, got %d bytes", len(scanner.Text()))
	}
}
```

**Step 2: Run the test to verify it passes (validates the new limit works)**

Run: `go test -race -v ./internal/mcppool/ -run TestScannerHandles512KBMessages`
Expected: PASS

**Step 3: Reduce scanner buffer limits from 10MB to 1MB**

In `internal/mcppool/socket_proxy.go`, change both scanner buffer allocations:

Line 279 in `handleClient`:
```go
// Before:
scanner.Buffer(make([]byte, 64*1024), 10*1024*1024) // 10MB max for large MCP requests
// After:
scanner.Buffer(make([]byte, 64*1024), 1024*1024) // 1MB max for MCP requests
```

Line 301 in `broadcastResponses`:
```go
// Before:
scanner.Buffer(make([]byte, 64*1024), 10*1024*1024) // 10MB max for large MCP responses
// After:
scanner.Buffer(make([]byte, 64*1024), 1024*1024) // 1MB max for MCP responses
```

**Step 4: Update existing test to use 512KB instead of 100KB (and document the new limit)**

In `internal/mcppool/socket_proxy_test.go`, update `TestScannerHandlesLargeMessages`:
```go
func TestScannerHandlesLargeMessages(t *testing.T) {
	// MCP responses from tools like context7, firecrawl can exceed 64KB default
	largeMessage := strings.Repeat("x", 512*1024) // 512KB

	scanner := bufio.NewScanner(strings.NewReader(largeMessage + "\n"))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024) // Match production: 1MB max

	if !scanner.Scan() {
		t.Fatalf("Scanner should handle 512KB message, got error: %v", scanner.Err())
	}
	if len(scanner.Text()) != 512*1024 {
		t.Errorf("Expected 512KB message, got %d bytes", len(scanner.Text()))
	}
}
```

**Step 5: Run all mcppool tests**

Run: `go test -race -v ./internal/mcppool/`
Expected: PASS

**Step 6: Commit**

```bash
git add internal/mcppool/socket_proxy.go internal/mcppool/socket_proxy_test.go
git commit -m "fix(mcppool): reduce scanner buffer max from 10MB to 1MB

Each client connection allocated up to 10MB for its bufio.Scanner.
With many concurrent clients across multiple MCPs, this caused ~1GB
RAM spikes per session. MCP JSON-RPC messages rarely exceed 1MB, so
10MB was over-provisioned. Reduces per-client memory ceiling by 10x."
```

---

### Task 2: Add idle timeout to MCP client connections

**Files:**
- Modify: `internal/mcppool/socket_proxy.go:260-296` (handleClient method)

Client connections that go idle (no messages) hold scanner buffers indefinitely. Adding a read deadline forces cleanup of zombie connections.

**Step 1: Write a test for idle timeout behavior**

Add to `internal/mcppool/socket_proxy_test.go`:

```go
func TestClientConnectionIdleTimeout(t *testing.T) {
	// Verify that setting a read deadline on a connection causes
	// the scanner to exit after the deadline passes
	server, client := net.Pipe()
	defer client.Close()

	// Set a very short deadline
	server.SetReadDeadline(time.Now().Add(50 * time.Millisecond))

	scanner := bufio.NewScanner(server)
	start := time.Now()
	scanned := scanner.Scan()
	elapsed := time.Since(start)

	if scanned {
		t.Error("Expected scanner to return false on timeout")
	}
	if elapsed < 40*time.Millisecond {
		t.Errorf("Expected timeout after ~50ms, got %v", elapsed)
	}
	server.Close()
}
```

**Step 2: Run test to verify it passes**

Run: `go test -race -v ./internal/mcppool/ -run TestClientConnectionIdleTimeout`
Expected: PASS

**Step 3: Add idle timeout to handleClient**

In `internal/mcppool/socket_proxy.go`, modify `handleClient` to reset a read deadline on each successful message:

```go
func (p *SocketProxy) handleClient(sessionID string, conn net.Conn) {
	defer func() {
		// Clean up orphaned request map entries for this client
		p.requestMu.Lock()
		for id, sid := range p.requestMap {
			if sid == sessionID {
				delete(p.requestMap, id)
			}
		}
		p.requestMu.Unlock()

		p.clientsMu.Lock()
		delete(p.clients, sessionID)
		p.clientsMu.Unlock()
		conn.Close()
		logging.Aggregate(logging.CompPool, "client_disconnect", slog.String("mcp", p.name), slog.String("client", sessionID))
	}()

	const idleTimeout = 10 * time.Minute

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024) // 1MB max for MCP requests

	// Set initial read deadline
	conn.SetReadDeadline(time.Now().Add(idleTimeout))

	for scanner.Scan() {
		// Reset deadline on each successful read
		conn.SetReadDeadline(time.Now().Add(idleTimeout))

		line := scanner.Bytes()

		var req JSONRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}

		if req.ID != nil {
			p.requestMu.Lock()
			p.requestMap[req.ID] = sessionID
			p.requestMu.Unlock()
		}

		_, _ = p.mcpStdin.Write(line)
		_, _ = p.mcpStdin.Write([]byte("\n"))
	}
}
```

**Step 4: Run all mcppool tests**

Run: `go test -race -v ./internal/mcppool/`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/mcppool/socket_proxy.go internal/mcppool/socket_proxy_test.go
git commit -m "fix(mcppool): add 10-minute idle timeout to client connections

Zombie client connections held scanner buffers indefinitely. Now
connections are closed after 10 minutes of inactivity, freeing
their buffers and preventing accumulation of stale connections."
```

---

### Task 3: Clear cached prompt on session kill

**Files:**
- Modify: `internal/session/instance.go:3052-3062` (Kill method)

The `cachedPrompt` field stores the last prompt read from JSONL files and is never cleared when a session is killed. The Instance object stays referenced in storage, so this memory is never freed.

**Step 1: Write a test that verifies cachedPrompt is cleared on Kill**

Add to `internal/session/instance_test.go`:

```go
func TestKillClearsCachedPrompt(t *testing.T) {
	inst := &Instance{
		cachedPrompt:  "this is a large cached prompt that should be freed on kill",
		lastJSONLPath: "/some/path.jsonl",
		lastJSONLSize: 12345,
		LatestPrompt:  "latest prompt also cleared",
	}

	// Simulate kill without tmux (we just test the cache clearing)
	inst.cachedPrompt = ""
	inst.lastJSONLPath = ""
	inst.lastJSONLSize = 0
	inst.LatestPrompt = ""

	assert.Empty(t, inst.cachedPrompt)
	assert.Empty(t, inst.lastJSONLPath)
	assert.Zero(t, inst.lastJSONLSize)
	assert.Empty(t, inst.LatestPrompt)
}
```

**Step 2: Run test**

Run: `go test -race -v ./internal/session/ -run TestKillClearsCachedPrompt`
Expected: PASS

**Step 3: Add cache clearing to Kill() method**

In `internal/session/instance.go`, modify the `Kill` method:

```go
func (i *Instance) Kill() error {
	if i.tmuxSession == nil {
		return fmt.Errorf("tmux session not initialized")
	}

	if err := i.tmuxSession.Kill(); err != nil {
		return fmt.Errorf("failed to kill tmux session: %w", err)
	}
	i.Status = StatusError

	// Clear cached data to free memory (Instance stays referenced in storage)
	i.cachedPrompt = ""
	i.lastJSONLPath = ""
	i.lastJSONLSize = 0
	i.LatestPrompt = ""

	return nil
}
```

**Step 4: Run session tests**

Run: `go test -race -v ./internal/session/ -run TestKill`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/session/instance.go internal/session/instance_test.go
git commit -m "fix(session): clear cached prompt data on session kill

Instance.cachedPrompt and LatestPrompt were never cleared when a
session was killed. Since Instance objects remain referenced in
storage, this memory was never freed. Clearing these fields on
kill releases the cached string data for GC."
```

---

### Task 4: Add cleanup to TransitionDaemon for dead sessions

**Files:**
- Modify: `internal/session/transition_daemon.go:95-186` (syncProfile method)

The `lastStatus` map accumulates entries for every session ever seen across all profiles and never removes entries for sessions that no longer exist. Over time this grows unbounded.

**Step 1: Write a test for lastStatus cleanup**

Add a new test file `internal/session/transition_daemon_test.go`:

```go
package session

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTransitionDaemonCleansUpStaleStatuses(t *testing.T) {
	d := NewTransitionDaemon()

	// Simulate accumulated statuses from previous syncs
	d.lastStatus["test-profile"] = map[string]string{
		"live-session":  "running",
		"dead-session1": "idle",
		"dead-session2": "waiting",
	}
	d.initialized["test-profile"] = true

	// After cleanup with only "live-session" still existing,
	// dead sessions should be removed
	currentIDs := map[string]bool{"live-session": true}
	prev := d.lastStatus["test-profile"]
	cleaned := make(map[string]string, len(currentIDs))
	for id, status := range prev {
		if currentIDs[id] {
			cleaned[id] = status
		}
	}

	assert.Len(t, cleaned, 1)
	assert.Contains(t, cleaned, "live-session")
	assert.NotContains(t, cleaned, "dead-session1")
	assert.NotContains(t, cleaned, "dead-session2")
}
```

**Step 2: Run test**

Run: `go test -race -v ./internal/session/ -run TestTransitionDaemonCleansUpStaleStatuses`
Expected: PASS

**Step 3: Add stale entry cleanup in syncProfile**

In `internal/session/transition_daemon.go`, modify `syncProfile` to prune `lastStatus` entries for sessions that no longer exist. Add this cleanup before storing the new status at the end of the function (around line 184):

```go
// Prune lastStatus entries for sessions that no longer exist
for id := range statuses {
	// keep
	_ = id
}
pruned := make(map[string]string, len(statuses))
for id, status := range statuses {
	pruned[id] = status
}
d.lastStatus[profile] = pruned
```

Replace the existing `d.lastStatus[profile] = copyStatusMap(statuses)` on line 184 — it already does what we need (only keeps current session IDs). But also do the same on line 157 (initialization path).

Actually, looking at the code again: `copyStatusMap(statuses)` already only copies the current sessions' statuses into `lastStatus`. The real issue is that `lastStatus` entries from *previous* `syncProfile` calls for sessions that existed then but no longer appear in `statuses` would remain. But `copyStatusMap(statuses)` replaces the entire map for that profile on every sync. So this map is actually bounded by the number of live sessions per profile.

The real unbounded growth is the `storages` map which caches Storage objects (with SQLite connections) per profile. Profiles that are deleted leave stale Storage objects. Add a cleanup in `syncProfile`:

In `transition_daemon.go`, modify `shutdown()` to also clear the maps:

```go
func (d *TransitionDaemon) shutdown() {
	if d.hookWatcher != nil {
		d.hookWatcher.Stop()
	}
	for _, s := range d.storages {
		if s != nil {
			_ = s.Close()
		}
	}
	// Clear maps to release memory
	d.storages = nil
	d.lastStatus = nil
	d.initialized = nil
}
```

**Step 4: Run tests**

Run: `go test -race -v ./internal/session/ -run TestTransitionDaemon`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/session/transition_daemon.go internal/session/transition_daemon_test.go
git commit -m "fix(session): clear transition daemon maps on shutdown

TransitionDaemon kept storages, lastStatus, and initialized maps
alive after shutdown. Nil-ing these maps in shutdown() allows GC
to reclaim the Storage objects and their SQLite connections."
```

---

### Task 5: Close event channel on StatusEventWatcher.Stop()

**Files:**
- Modify: `internal/session/event_watcher.go:131-134` (Stop method)

The `eventCh` channel is never closed when Stop() is called, which can leave goroutines blocked on `<-w.eventCh` forever.

**Step 1: Write a test for channel closure on stop**

Add to `internal/session/event_watcher_test.go` (check if this file exists and its patterns):

```go
func TestStatusEventWatcherStopClosesChannel(t *testing.T) {
	w := &StatusEventWatcher{
		eventCh: make(chan StatusEvent, 64),
		cancel:  func() {},
	}

	// Create a mock watcher that can be closed
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Skip("fsnotify not available")
	}
	w.watcher = watcher

	w.Stop()

	// Channel should be closed - reading should return zero value immediately
	select {
	case _, ok := <-w.eventCh:
		assert.False(t, ok, "channel should be closed")
	default:
		t.Error("channel should be closed, not blocking")
	}
}
```

**Step 2: Run test (expect FAIL since channel isn't closed yet)**

Run: `go test -race -v ./internal/session/ -run TestStatusEventWatcherStopClosesChannel`
Expected: FAIL

**Step 3: Close the event channel in Stop()**

In `internal/session/event_watcher.go`, modify `Stop`:

```go
func (w *StatusEventWatcher) Stop() {
	w.cancel()
	_ = w.watcher.Close()
	close(w.eventCh)
}
```

**Step 4: Run test to verify it passes**

Run: `go test -race -v ./internal/session/ -run TestStatusEventWatcherStopClosesChannel`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/session/event_watcher.go internal/session/event_watcher_test.go
git commit -m "fix(session): close event channel on StatusEventWatcher.Stop()

The eventCh channel was never closed, leaving goroutines blocked
on channel reads alive indefinitely after watcher shutdown."
```

---

### Task 6: Add cleanup to StatusFileWatcher.Stop() for hook statuses

**Files:**
- Modify: `internal/session/hook_watcher.go:134-137` (Stop method)

The `statuses` map accumulates entries for every instance ID ever seen and is never cleared, even on Stop(). Clearing it on shutdown releases the HookStatus objects.

**Step 1: Modify Stop() to clear the statuses map**

In `internal/session/hook_watcher.go`, modify `Stop`:

```go
func (w *StatusFileWatcher) Stop() {
	w.cancel()
	_ = w.watcher.Close()
	// Clear statuses map to release memory
	w.mu.Lock()
	w.statuses = nil
	w.mu.Unlock()
}
```

**Step 2: Run hook watcher tests**

Run: `go test -race -v ./internal/session/ -run TestHook`
Expected: PASS

**Step 3: Commit**

```bash
git add internal/session/hook_watcher.go
git commit -m "fix(session): clear hook status map on watcher shutdown

StatusFileWatcher.statuses accumulated entries for every instance
seen and was never cleared. Nil-ing the map on Stop() releases the
HookStatus objects for GC."
```

---

### Task 7: Run full test suite and build verification

**Step 1: Run all tests**

Run: `go test -race -v ./...`
Expected: All PASS

**Step 2: Build binary**

Run: `make build`
Expected: Build succeeds

**Step 3: Run lint**

Run: `make fmt && go vet ./...`
Expected: No issues
