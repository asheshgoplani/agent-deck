package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStatusEventWatcher_DetectsNewFile(t *testing.T) {
	// Create a temp events dir
	tmpDir := t.TempDir()
	eventsDir := filepath.Join(tmpDir, "events")
	if err := os.MkdirAll(eventsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Create watcher directly with our temp dir
	watcher, err := NewStatusEventWatcher("")
	if err != nil {
		t.Fatalf("NewStatusEventWatcher: %v", err)
	}

	// Override the eventsDir to our temp
	watcher.eventsDir = eventsDir
	// Re-add watch on new dir
	_ = watcher.watcher.Add(eventsDir)

	defer watcher.Stop()
	go watcher.Start()

	// Give watcher time to start
	time.Sleep(200 * time.Millisecond)

	// Write an event file
	event := StatusEvent{
		InstanceID: "detect-test",
		Title:      "test",
		Tool:       "claude",
		Status:     "running",
		PrevStatus: "waiting",
		Timestamp:  time.Now().Unix(),
	}
	data, _ := json.Marshal(event)
	filePath := filepath.Join(eventsDir, "detect-test.json")
	tmpPath := filePath + ".tmp"
	_ = os.WriteFile(tmpPath, data, 0644)
	_ = os.Rename(tmpPath, filePath)

	// Wait for event delivery
	select {
	case received := <-watcher.EventCh():
		if received.InstanceID != "detect-test" {
			t.Errorf("InstanceID = %q, want detect-test", received.InstanceID)
		}
		if received.Status != "running" {
			t.Errorf("Status = %q, want running", received.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for event delivery")
	}
}

func TestStatusEventWatcher_FilterByInstance(t *testing.T) {
	tmpDir := t.TempDir()
	eventsDir := filepath.Join(tmpDir, "events")
	if err := os.MkdirAll(eventsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Create watcher filtered to "wanted-instance"
	watcher, err := NewStatusEventWatcher("wanted-instance")
	if err != nil {
		t.Fatalf("NewStatusEventWatcher: %v", err)
	}
	watcher.eventsDir = eventsDir
	_ = watcher.watcher.Add(eventsDir)

	defer watcher.Stop()
	go watcher.Start()

	time.Sleep(200 * time.Millisecond)

	// Write event for UNWANTED instance (should be filtered out)
	unwanted := StatusEvent{
		InstanceID: "other-instance",
		Title:      "other",
		Tool:       "claude",
		Status:     "running",
		Timestamp:  time.Now().Unix(),
	}
	data, _ := json.Marshal(unwanted)
	_ = os.WriteFile(filepath.Join(eventsDir, "other-instance.json"), data, 0644)

	// Write event for WANTED instance
	wanted := StatusEvent{
		InstanceID: "wanted-instance",
		Title:      "wanted",
		Tool:       "claude",
		Status:     "waiting",
		Timestamp:  time.Now().Unix(),
	}
	data, _ = json.Marshal(wanted)
	filePath := filepath.Join(eventsDir, "wanted-instance.json")
	tmpPath := filePath + ".tmp"
	_ = os.WriteFile(tmpPath, data, 0644)
	_ = os.Rename(tmpPath, filePath)

	// Should only receive the wanted event
	select {
	case received := <-watcher.EventCh():
		if received.InstanceID != "wanted-instance" {
			t.Errorf("InstanceID = %q, want wanted-instance", received.InstanceID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for filtered event")
	}
}

func TestStatusEventWatcher_WaitForStatus(t *testing.T) {
	tmpDir := t.TempDir()
	eventsDir := filepath.Join(tmpDir, "events")
	if err := os.MkdirAll(eventsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	watcher, err := NewStatusEventWatcher("")
	if err != nil {
		t.Fatalf("NewStatusEventWatcher: %v", err)
	}
	watcher.eventsDir = eventsDir
	_ = watcher.watcher.Add(eventsDir)

	defer watcher.Stop()
	go watcher.Start()

	time.Sleep(200 * time.Millisecond)

	// Write an event asynchronously after a short delay
	go func() {
		time.Sleep(300 * time.Millisecond)
		event := StatusEvent{
			InstanceID: "wait-test",
			Title:      "test",
			Tool:       "claude",
			Status:     "waiting",
			PrevStatus: "running",
			Timestamp:  time.Now().Unix(),
		}
		data, _ := json.Marshal(event)
		filePath := filepath.Join(eventsDir, "wait-test.json")
		tmpPath := filePath + ".tmp"
		_ = os.WriteFile(tmpPath, data, 0644)
		_ = os.Rename(tmpPath, filePath)
	}()

	// WaitForStatus should return when "waiting" event arrives
	event, err := watcher.WaitForStatus([]string{"waiting", "idle"}, 5*time.Second)
	if err != nil {
		t.Fatalf("WaitForStatus: %v", err)
	}
	if event.Status != "waiting" {
		t.Errorf("Status = %q, want waiting", event.Status)
	}
}

func TestStatusEventWatcher_StopClosesChannel(t *testing.T) {
	watcher, err := NewStatusEventWatcher("")
	if err != nil {
		t.Skipf("cannot create watcher: %v", err)
	}

	watcher.Stop()

	// Channel should be closed — reading should return zero value immediately
	select {
	case _, ok := <-watcher.EventCh():
		if ok {
			t.Error("expected channel to be closed after Stop()")
		}
	case <-time.After(1 * time.Second):
		t.Error("channel read should not block after Stop()")
	}
}

func TestStatusEventWatcher_WaitForStatus_Timeout(t *testing.T) {
	tmpDir := t.TempDir()
	eventsDir := filepath.Join(tmpDir, "events")
	if err := os.MkdirAll(eventsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	watcher, err := NewStatusEventWatcher("")
	if err != nil {
		t.Fatalf("NewStatusEventWatcher: %v", err)
	}
	watcher.eventsDir = eventsDir
	_ = watcher.watcher.Add(eventsDir)

	defer watcher.Stop()
	go watcher.Start()

	time.Sleep(200 * time.Millisecond)

	// WaitForStatus with short timeout and no events
	_, err = watcher.WaitForStatus([]string{"waiting"}, 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}
