package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteStatusEvent(t *testing.T) {
	// Use a temp dir for the events directory
	tmpDir := t.TempDir()
	eventsDir := filepath.Join(tmpDir, "events")

	// Override GetEventsDir by writing directly
	if err := os.MkdirAll(eventsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	event := StatusEvent{
		InstanceID: "test-123",
		Title:      "my-session",
		Tool:       "claude",
		Status:     "running",
		PrevStatus: "waiting",
		Timestamp:  time.Now().Unix(),
	}

	// Write event file directly (simulating WriteStatusEvent without overriding GetEventsDir)
	jsonData, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	filePath := filepath.Join(eventsDir, event.InstanceID+".json")
	tmpPath := filePath + ".tmp"
	if err := os.WriteFile(tmpPath, jsonData, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Rename(tmpPath, filePath); err != nil {
		t.Fatalf("rename: %v", err)
	}

	// Read back and verify
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var readEvent StatusEvent
	if err := json.Unmarshal(data, &readEvent); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if readEvent.InstanceID != "test-123" {
		t.Errorf("InstanceID = %q, want test-123", readEvent.InstanceID)
	}
	if readEvent.Title != "my-session" {
		t.Errorf("Title = %q, want my-session", readEvent.Title)
	}
	if readEvent.Tool != "claude" {
		t.Errorf("Tool = %q, want claude", readEvent.Tool)
	}
	if readEvent.Status != "running" {
		t.Errorf("Status = %q, want running", readEvent.Status)
	}
	if readEvent.PrevStatus != "waiting" {
		t.Errorf("PrevStatus = %q, want waiting", readEvent.PrevStatus)
	}
}

func TestWriteStatusEvent_Integration(t *testing.T) {
	// Test the actual WriteStatusEvent function (writes to ~/.agent-deck/events/)
	event := StatusEvent{
		InstanceID: "test-integration-" + t.Name(),
		Title:      "integration-test",
		Tool:       "claude",
		Status:     "waiting",
		PrevStatus: "running",
		Timestamp:  time.Now().Unix(),
	}

	if err := WriteStatusEvent(event); err != nil {
		t.Fatalf("WriteStatusEvent: %v", err)
	}

	// Read it back via ReadPrevEventStatus
	status := ReadPrevEventStatus(event.InstanceID)
	if status != "waiting" {
		t.Errorf("ReadPrevEventStatus = %q, want waiting", status)
	}

	// Clean up
	eventsDir := GetEventsDir()
	_ = os.Remove(filepath.Join(eventsDir, event.InstanceID+".json"))
}

func TestReadPrevEventStatus_Missing(t *testing.T) {
	status := ReadPrevEventStatus("nonexistent-instance-id")
	if status != "" {
		t.Errorf("ReadPrevEventStatus for missing file = %q, want empty", status)
	}
}

func TestCleanStaleEventFiles(t *testing.T) {
	tmpDir := t.TempDir()
	eventsDir := filepath.Join(tmpDir, "events")
	if err := os.MkdirAll(eventsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Create a fresh file
	freshFile := filepath.Join(eventsDir, "fresh.json")
	if err := os.WriteFile(freshFile, []byte(`{"status":"running"}`), 0644); err != nil {
		t.Fatalf("write fresh: %v", err)
	}

	// Create a stale file (set mtime to 25 hours ago)
	staleFile := filepath.Join(eventsDir, "stale.json")
	if err := os.WriteFile(staleFile, []byte(`{"status":"dead"}`), 0644); err != nil {
		t.Fatalf("write stale: %v", err)
	}
	staleTime := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(staleFile, staleTime, staleTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	// Create a non-json file (should be ignored)
	txtFile := filepath.Join(eventsDir, "readme.txt")
	if err := os.WriteFile(txtFile, []byte("ignored"), 0644); err != nil {
		t.Fatalf("write txt: %v", err)
	}

	// Run cleanup directly on this dir (can't override GetEventsDir, so test manually)
	entries, err := os.ReadDir(eventsDir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}

	cutoff := time.Now().Add(-24 * time.Hour)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(eventsDir, entry.Name()))
		}
	}

	// Verify: fresh.json kept, stale.json removed, readme.txt kept
	if _, err := os.Stat(freshFile); os.IsNotExist(err) {
		t.Error("fresh.json should still exist")
	}
	if _, err := os.Stat(staleFile); !os.IsNotExist(err) {
		t.Error("stale.json should have been removed")
	}
	if _, err := os.Stat(txtFile); os.IsNotExist(err) {
		t.Error("readme.txt should still exist (not .json)")
	}
}

func TestStatusEvent_JSON(t *testing.T) {
	event := StatusEvent{
		InstanceID: "abc-123",
		Title:      "My Session",
		Tool:       "gemini",
		Status:     "idle",
		PrevStatus: "running",
		Timestamp:  1707900000,
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var read StatusEvent
	if err := json.Unmarshal(data, &read); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if read.InstanceID != "abc-123" {
		t.Errorf("InstanceID = %q, want abc-123", read.InstanceID)
	}
	if read.Title != "My Session" {
		t.Errorf("Title = %q, want My Session", read.Title)
	}
	if read.Tool != "gemini" {
		t.Errorf("Tool = %q, want gemini", read.Tool)
	}
	if read.Status != "idle" {
		t.Errorf("Status = %q, want idle", read.Status)
	}
	if read.PrevStatus != "running" {
		t.Errorf("PrevStatus = %q, want running", read.PrevStatus)
	}
	if read.Timestamp != 1707900000 {
		t.Errorf("Timestamp = %d, want 1707900000", read.Timestamp)
	}
}
