package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// TestStatusFileWatcher_HandleOverflow_ResyncsFromDisk verifies that when
// fsnotify reports IN_Q_OVERFLOW (ErrEventOverflow), the watcher re-walks
// the hooks directory from disk and replaces its in-memory map atomically.
//
// Regression: hook_watcher.go previously dropped overflow events silently
// (logged at Warn but did not re-sync), leaving stale state until restart.
func TestStatusFileWatcher_HandleOverflow_ResyncsFromDisk(t *testing.T) {
	tmpDir := t.TempDir()
	hooksDir := filepath.Join(tmpDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	w := &StatusFileWatcher{
		hooksDir: hooksDir,
		statuses: make(map[string]*HookStatus),
	}

	// Pre-populate with a STALE entry that is NOT on disk (simulates a
	// session that stopped writing files but is still in the in-memory map).
	w.statuses["stale-inst"] = &HookStatus{
		Status:    "running",
		Event:     "old",
		UpdatedAt: time.Now().Add(-time.Hour),
	}

	// Write fresh files to disk that the watcher never observed (simulates
	// the inotify queue overflow window where many writes are dropped).
	for _, inst := range []struct{ id, status string }{
		{"fresh-1", "waiting"},
		{"fresh-2", "running"},
	} {
		data, _ := json.Marshal(map[string]any{
			"status":     inst.status,
			"session_id": "sid-" + inst.id,
			"event":      "UserPromptSubmit",
			"ts":         time.Now().Unix(),
		})
		if err := os.WriteFile(filepath.Join(hooksDir, inst.id+".json"), data, 0644); err != nil {
			t.Fatalf("write %s: %v", inst.id, err)
		}
	}

	// Trigger the overflow recovery path. This method does not yet exist;
	// the test fails until the implementation is added.
	w.handleOverflow(fsnotify.ErrEventOverflow)

	// After recovery, the map MUST reflect disk contents:
	if w.GetHookStatus("fresh-1") == nil {
		t.Error("fresh-1 missing after overflow recovery (re-walk did not happen)")
	}
	if w.GetHookStatus("fresh-2") == nil {
		t.Error("fresh-2 missing after overflow recovery")
	}
	// And the stale entry that no longer has a backing file MUST be gone:
	if w.GetHookStatus("stale-inst") != nil {
		t.Error("stale-inst still present after overflow recovery (map not replaced atomically)")
	}
}

// TestStatusFileWatcher_HandleOverflow_LogsWarn verifies that the overflow
// recovery emits a WARN-level log line so an operator can correlate
// flickering sessions with an inotify overflow event.
func TestStatusFileWatcher_HandleOverflow_LogsWarn(t *testing.T) {
	logging.Shutdown()
	logDir := t.TempDir()
	logging.Init(logging.Config{
		Debug:  true,
		LogDir: logDir,
		Level:  "debug",
		Format: "json",
	})
	defer logging.Shutdown()

	hooksDir := filepath.Join(t.TempDir(), "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	w := &StatusFileWatcher{
		hooksDir: hooksDir,
		statuses: make(map[string]*HookStatus),
	}

	w.handleOverflow(fsnotify.ErrEventOverflow)

	// Force log flush
	logging.Shutdown()

	data, err := os.ReadFile(filepath.Join(logDir, "debug.log"))
	if err != nil {
		t.Fatalf("read debug.log: %v", err)
	}
	if !strings.Contains(string(data), `"hook_watcher_overflow_resync"`) {
		t.Errorf("expected hook_watcher_overflow_resync WARN log; got:\n%s", string(data))
	}
	if !strings.Contains(string(data), `"level":"WARN"`) {
		t.Errorf("expected WARN level; got:\n%s", string(data))
	}
}

// TestStatusFileWatcher_OverflowDispatch_DetectsErrEventOverflow verifies
// that errors.Is(err, fsnotify.ErrEventOverflow) is the trigger condition,
// not a string match. Catches future fsnotify versions that wrap the error.
func TestStatusFileWatcher_OverflowDispatch_DetectsErrEventOverflow(t *testing.T) {
	wrapped := fmt.Errorf("inotify: %w", fsnotify.ErrEventOverflow)
	if !errors.Is(wrapped, fsnotify.ErrEventOverflow) {
		t.Skip("fsnotify error wrapping precondition not met")
	}
	if !isOverflowError(wrapped) {
		t.Errorf("isOverflowError must use errors.Is, not strings.Contains")
	}
	if isOverflowError(errors.New("some other error")) {
		t.Errorf("isOverflowError matched a non-overflow error")
	}
	if isOverflowError(nil) {
		t.Errorf("isOverflowError matched nil")
	}
}
