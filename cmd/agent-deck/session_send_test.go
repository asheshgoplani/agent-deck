package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// mockStatusChecker implements statusChecker for testing waitForCompletion.
type mockStatusChecker struct {
	statuses []string // statuses returned in order
	errors   []error  // errors returned in order (nil = no error)
	idx      atomic.Int32
}

func (m *mockStatusChecker) GetStatus() (string, error) {
	i := int(m.idx.Add(1) - 1)
	if i >= len(m.statuses) {
		// Stay on last status if we exceed the list
		i = len(m.statuses) - 1
	}
	var err error
	if i < len(m.errors) {
		err = m.errors[i]
	}
	return m.statuses[i], err
}

func TestWaitForCompletion_ImmediateWaiting(t *testing.T) {
	mock := &mockStatusChecker{
		statuses: []string{"waiting"},
	}
	status, err := waitForCompletion(mock, 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "waiting" {
		t.Errorf("expected status 'waiting', got %q", status)
	}
}

func TestWaitForCompletion_ActiveThenWaiting(t *testing.T) {
	mock := &mockStatusChecker{
		statuses: []string{"active", "active", "waiting"},
	}
	status, err := waitForCompletion(mock, 30*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "waiting" {
		t.Errorf("expected status 'waiting', got %q", status)
	}
}

func TestWaitForCompletion_ActiveThenIdle(t *testing.T) {
	mock := &mockStatusChecker{
		statuses: []string{"active", "idle"},
	}
	status, err := waitForCompletion(mock, 30*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "idle" {
		t.Errorf("expected status 'idle', got %q", status)
	}
}

func TestWaitForCompletion_ActiveThenInactive(t *testing.T) {
	mock := &mockStatusChecker{
		statuses: []string{"active", "inactive"},
	}
	status, err := waitForCompletion(mock, 30*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "inactive" {
		t.Errorf("expected status 'inactive', got %q", status)
	}
}

func TestWaitForCompletion_TransientErrors(t *testing.T) {
	mock := &mockStatusChecker{
		statuses: []string{"", "", "waiting"},
		errors:   []error{fmt.Errorf("tmux error"), fmt.Errorf("tmux error"), nil},
	}
	status, err := waitForCompletion(mock, 30*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "waiting" {
		t.Errorf("expected status 'waiting', got %q", status)
	}
}

func TestWaitForCompletion_Timeout(t *testing.T) {
	mock := &mockStatusChecker{
		statuses: []string{"active"}, // Stays active forever
	}
	// Use a very short timeout so the test doesn't block
	_, err := waitForCompletion(mock, 2*time.Second)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

// TestWaitOutputRetrieval_StaleSessionID verifies that --wait correctly
// retrieves output even when the initially-loaded ClaudeSessionID is stale.
// This simulates the bug where inst.GetLastResponse() fails because the
// session ID stored in the DB doesn't match the actual JSONL file on disk.
func TestWaitOutputRetrieval_StaleSessionID(t *testing.T) {
	// Set up a temp Claude config dir with a JSONL file
	tmpDir := t.TempDir()
	projectPath := "/test/wait-project"
	encodedPath := session.ConvertToClaudeDirName(projectPath)

	projectsDir := filepath.Join(tmpDir, "projects", encodedPath)
	if err := os.MkdirAll(projectsDir, 0755); err != nil {
		t.Fatalf("failed to create projects dir: %v", err)
	}

	// Override config dir for test
	origConfigDir := os.Getenv("CLAUDE_CONFIG_DIR")
	os.Setenv("CLAUDE_CONFIG_DIR", tmpDir)
	defer os.Setenv("CLAUDE_CONFIG_DIR", origConfigDir)
	session.ClearUserConfigCache()
	defer session.ClearUserConfigCache()

	// Create the "real" session JSONL file (what Claude actually wrote to)
	realSessionID := "real-session-id-after-start"
	realJSONL := filepath.Join(projectsDir, realSessionID+".jsonl")
	jsonlContent := `{"type":"summary","sessionId":"` + realSessionID + `"}
{"message":{"role":"user","content":"hello"},"sessionId":"` + realSessionID + `","type":"user","timestamp":"2026-01-01T00:00:00Z"}
{"message":{"role":"assistant","content":[{"type":"text","text":"Hello! How can I help?"}]},"sessionId":"` + realSessionID + `","type":"assistant","timestamp":"2026-01-01T00:00:01Z"}`
	if err := os.WriteFile(realJSONL, []byte(jsonlContent), 0644); err != nil {
		t.Fatalf("failed to write JSONL: %v", err)
	}

	t.Run("stale session ID fails to find file", func(t *testing.T) {
		// Instance with stale session ID (doesn't match any JSONL file)
		inst := session.NewInstance("wait-test", projectPath)
		inst.Tool = "claude"
		inst.ClaudeSessionID = "stale-old-session-id"

		_, err := inst.GetLastResponse()
		if err == nil {
			t.Fatal("expected error with stale session ID, got nil")
		}
	})

	t.Run("correct session ID finds file", func(t *testing.T) {
		// Instance with correct session ID
		inst := session.NewInstance("wait-test", projectPath)
		inst.Tool = "claude"
		inst.ClaudeSessionID = realSessionID

		resp, err := inst.GetLastResponse()
		if err != nil {
			t.Fatalf("unexpected error with correct session ID: %v", err)
		}
		if resp.Content != "Hello! How can I help?" {
			t.Errorf("expected 'Hello! How can I help?', got %q", resp.Content)
		}
	})

	t.Run("refreshing session ID fixes retrieval", func(t *testing.T) {
		// Simulates the --wait fix: start with stale ID, then refresh
		inst := session.NewInstance("wait-test", projectPath)
		inst.Tool = "claude"
		inst.ClaudeSessionID = "stale-old-session-id"

		// First attempt fails (stale ID)
		_, err := inst.GetLastResponse()
		if err == nil {
			t.Fatal("expected error with stale session ID")
		}

		// Simulate refreshing session ID (as the fix does from tmux env)
		inst.ClaudeSessionID = realSessionID
		inst.ClaudeDetectedAt = time.Now()

		// Second attempt succeeds with refreshed ID
		resp, err := inst.GetLastResponse()
		if err != nil {
			t.Fatalf("unexpected error after refresh: %v", err)
		}
		if resp.Content != "Hello! How can I help?" {
			t.Errorf("expected 'Hello! How can I help?', got %q", resp.Content)
		}
	})
}
