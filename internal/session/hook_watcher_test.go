package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStatusFileWatcher_ProcessFile(t *testing.T) {
	tmpDir := t.TempDir()
	hooksDir := filepath.Join(tmpDir, "hooks")
	_ = os.MkdirAll(hooksDir, 0755)

	// Create a watcher (don't start the goroutine, just test processFile)
	w := &StatusFileWatcher{
		hooksDir: hooksDir,
		statuses: make(map[string]*HookStatus),
	}

	// Write a status file
	status := struct {
		Status    string `json:"status"`
		SessionID string `json:"session_id"`
		Event     string `json:"event"`
		Timestamp int64  `json:"ts"`
	}{
		Status:    "running",
		SessionID: "abc-123",
		Event:     "UserPromptSubmit",
		Timestamp: time.Now().Unix(),
	}
	data, _ := json.Marshal(status)
	filePath := filepath.Join(hooksDir, "instance-001.json")
	_ = os.WriteFile(filePath, data, 0644)

	// Process the file
	w.processFile(filePath)

	// Verify the status was recorded
	hs := w.GetHookStatus("instance-001")
	if hs == nil {
		t.Fatal("Expected hook status to be set")
	}
	if hs.Status != "running" {
		t.Errorf("Status = %q, want running", hs.Status)
	}
	if hs.SessionID != "abc-123" {
		t.Errorf("SessionID = %q, want abc-123", hs.SessionID)
	}
	if hs.Event != "UserPromptSubmit" {
		t.Errorf("Event = %q, want UserPromptSubmit", hs.Event)
	}
}

func TestStatusFileWatcher_LoadExisting(t *testing.T) {
	tmpDir := t.TempDir()
	hooksDir := filepath.Join(tmpDir, "hooks")
	_ = os.MkdirAll(hooksDir, 0755)

	// Pre-create some status files
	for _, inst := range []struct {
		id     string
		status string
	}{
		{"inst-1", "running"},
		{"inst-2", "idle"},
		{"inst-3", "waiting"},
	} {
		data, _ := json.Marshal(map[string]any{
			"status":     inst.status,
			"session_id": "sess-" + inst.id,
			"event":      "Test",
			"ts":         time.Now().Unix(),
		})
		_ = os.WriteFile(filepath.Join(hooksDir, inst.id+".json"), data, 0644)
	}

	w := &StatusFileWatcher{
		hooksDir: hooksDir,
		statuses: make(map[string]*HookStatus),
	}

	w.loadExisting()

	// All three should be loaded
	for _, id := range []string{"inst-1", "inst-2", "inst-3"} {
		if w.GetHookStatus(id) == nil {
			t.Errorf("Missing status for %s", id)
		}
	}

	// Verify correct statuses
	if w.GetHookStatus("inst-1").Status != "running" {
		t.Error("inst-1 should be running")
	}
	if w.GetHookStatus("inst-2").Status != "idle" {
		t.Error("inst-2 should be idle")
	}
	if w.GetHookStatus("inst-3").Status != "waiting" {
		t.Error("inst-3 should be waiting")
	}
}

func TestStatusFileWatcher_NonExistentInstance(t *testing.T) {
	w := &StatusFileWatcher{
		statuses: make(map[string]*HookStatus),
	}

	hs := w.GetHookStatus("nonexistent")
	if hs != nil {
		t.Error("Expected nil for nonexistent instance")
	}
}

func TestStatusFileWatcher_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	hooksDir := filepath.Join(tmpDir, "hooks")
	_ = os.MkdirAll(hooksDir, 0755)

	w := &StatusFileWatcher{
		hooksDir: hooksDir,
		statuses: make(map[string]*HookStatus),
	}

	// Write invalid JSON
	filePath := filepath.Join(hooksDir, "bad-inst.json")
	_ = os.WriteFile(filePath, []byte("not json"), 0644)

	// Should not panic
	w.processFile(filePath)

	// Should not create an entry
	if w.GetHookStatus("bad-inst") != nil {
		t.Error("Should not create status from invalid JSON")
	}
}

func TestConsumeCompactBlocked(t *testing.T) {
	w := &StatusFileWatcher{
		statuses: make(map[string]*HookStatus),
	}

	// No status yet: should return false
	if w.ConsumeCompactBlocked("inst-1") {
		t.Error("Expected false when no status exists")
	}

	// Set status to compact_blocked
	w.statuses["inst-1"] = &HookStatus{
		Status:    "compact_blocked",
		SessionID: "s1",
		Event:     "PreCompact",
		UpdatedAt: time.Now(),
	}

	// First consume should return true and clear to "waiting"
	if !w.ConsumeCompactBlocked("inst-1") {
		t.Error("Expected true on first consume of compact_blocked")
	}
	if w.statuses["inst-1"].Status != "waiting" {
		t.Errorf("Status after consume = %q, want waiting", w.statuses["inst-1"].Status)
	}

	// Second consume should return false (already cleared)
	if w.ConsumeCompactBlocked("inst-1") {
		t.Error("Expected false on second consume (already cleared)")
	}
}

func TestStatusFileWatcher_UpdatesExisting(t *testing.T) {
	tmpDir := t.TempDir()
	hooksDir := filepath.Join(tmpDir, "hooks")
	_ = os.MkdirAll(hooksDir, 0755)

	w := &StatusFileWatcher{
		hooksDir: hooksDir,
		statuses: make(map[string]*HookStatus),
	}

	filePath := filepath.Join(hooksDir, "inst-x.json")

	// First write: running
	data1, _ := json.Marshal(map[string]any{
		"status": "running", "session_id": "s1", "event": "UserPromptSubmit", "ts": time.Now().Unix(),
	})
	_ = os.WriteFile(filePath, data1, 0644)
	w.processFile(filePath)

	if w.GetHookStatus("inst-x").Status != "running" {
		t.Error("Expected running after first write")
	}

	// Second write: idle
	data2, _ := json.Marshal(map[string]any{
		"status": "idle", "session_id": "s1", "event": "Stop", "ts": time.Now().Unix(),
	})
	_ = os.WriteFile(filePath, data2, 0644)
	w.processFile(filePath)

	if w.GetHookStatus("inst-x").Status != "idle" {
		t.Error("Expected idle after second write")
	}
}
