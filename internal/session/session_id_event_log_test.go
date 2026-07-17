package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteSessionIDLifecycleEvent_AppendsJSONL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	first := SessionIDLifecycleEvent{
		InstanceID: "inst-1",
		Tool:       "claude",
		Action:     "bind",
		Source:     "tmux_env",
		NewID:      "session-a",
	}
	second := SessionIDLifecycleEvent{
		InstanceID: "inst-1",
		Tool:       "claude",
		Action:     "rebind",
		Source:     "hook_payload",
		OldID:      "session-a",
		NewID:      "session-b",
	}

	if err := WriteSessionIDLifecycleEvent(first); err != nil {
		t.Fatalf("WriteSessionIDLifecycleEvent(first) error: %v", err)
	}
	if err := WriteSessionIDLifecycleEvent(second); err != nil {
		t.Fatalf("WriteSessionIDLifecycleEvent(second) error: %v", err)
	}

	data, err := os.ReadFile(GetSessionIDLifecycleLogPath())
	if err != nil {
		t.Fatalf("read lifecycle log: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("line count = %d, want 2", len(lines))
	}

	var gotFirst, gotSecond SessionIDLifecycleEvent
	if err := json.Unmarshal([]byte(lines[0]), &gotFirst); err != nil {
		t.Fatalf("unmarshal first line: %v", err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &gotSecond); err != nil {
		t.Fatalf("unmarshal second line: %v", err)
	}
	if gotFirst.Action != "bind" || gotSecond.Action != "rebind" {
		t.Fatalf("actions = %q/%q, want bind/rebind", gotFirst.Action, gotSecond.Action)
	}
	if gotFirst.Timestamp == 0 || gotSecond.Timestamp == 0 {
		t.Fatal("timestamps should be auto-populated")
	}
}

// TestWriteSessionIDLifecycleEvent_RotatesUnderCap verifies the log can no
// longer grow unbounded: before this fix it was a raw O_APPEND that reached
// ~22MB / 92K lines on the live host. With the lumberjack writer (5MB cap), once
// total writes exceed the cap the active file rotates, so the active file stays
// well under the total bytes written and at least one backup appears.
func TestWriteSessionIDLifecycleEvent_RotatesUnderCap(t *testing.T) {
	// Reset the lazily-bound global writer so this test's HOME path takes effect.
	sessionIDLifecycleLogMu.Lock()
	if sessionIDLifecycleWriter != nil {
		_ = sessionIDLifecycleWriter.Close()
		sessionIDLifecycleWriter = nil
	}
	sessionIDLifecycleLogMu.Unlock()

	t.Setenv("HOME", t.TempDir())

	// ~1KB per event so we cross the 5MB cap in a few thousand cheap writes.
	pad := strings.Repeat("x", 1024)
	const totalBytesTarget = 7 * 1024 * 1024 // 7MB > 5MB cap, forces ≥1 rotation
	written := 0
	for written < totalBytesTarget {
		ev := SessionIDLifecycleEvent{
			InstanceID: "inst-rotate",
			Tool:       "claude",
			Action:     "bind",
			Source:     "tmux_env",
			NewID:      "session",
			Reason:     pad,
		}
		if err := WriteSessionIDLifecycleEvent(ev); err != nil {
			t.Fatalf("write event: %v", err)
		}
		written += len(pad) + 128
	}

	logPath := GetSessionIDLifecycleLogPath()
	fi, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat active log: %v", err)
	}
	const cap = 5 * 1024 * 1024
	// Active file must be bounded near the cap, not the full ~7MB written.
	if fi.Size() > cap+512*1024 {
		t.Fatalf("active log %d bytes exceeds cap+slack; rotation did not bound it", fi.Size())
	}
	// At least one rotated backup must exist alongside the active file.
	entries, err := os.ReadDir(filepath.Dir(logPath))
	if err != nil {
		t.Fatalf("read log dir: %v", err)
	}
	base := filepath.Base(logPath)
	backups := 0
	for _, e := range entries {
		if e.Name() != base && strings.HasPrefix(e.Name(), "session-id-lifecycle") {
			backups++
		}
	}
	if backups == 0 {
		t.Fatalf("expected ≥1 rotated backup in %s; got none (entries=%v)", filepath.Dir(logPath), entries)
	}
}
