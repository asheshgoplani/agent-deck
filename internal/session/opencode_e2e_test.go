package session

import (
	"testing"
	"time"
)

func TestOpenCodeDetectionE2E(t *testing.T) {
	projectPath := "/Users/ashesh/claude-deck"

	t.Log("=== E2E OpenCode Detection Test ===")

	// Create instance like agent-deck does
	inst := &Instance{
		Tool:        "opencode",
		ProjectPath: projectPath,
	}

	t.Logf("Instance created: Tool=%s, ProjectPath=%s", inst.Tool, inst.ProjectPath)
	t.Logf("Before detection: OpenCodeSessionID=%q", inst.OpenCodeSessionID)

	// Call detection synchronously (not in goroutine)
	t.Log("Calling detectOpenCodeSessionAsync()...")

	// Run detection in goroutine and wait for it
	done := make(chan bool)
	go func() {
		inst.detectOpenCodeSessionAsync()
		done <- true
	}()

	// Wait up to 15 seconds (detection has delays built in)
	select {
	case <-done:
		t.Log("Detection completed")
	case <-time.After(15 * time.Second):
		t.Fatal("Detection timed out after 15 seconds")
	}

	t.Logf("After detection: OpenCodeSessionID=%q", inst.OpenCodeSessionID)
	t.Logf("After detection: OpenCodeDetectedAt=%v", inst.OpenCodeDetectedAt)

	if inst.OpenCodeSessionID == "" {
		t.Error("❌ FAILED: OpenCodeSessionID is empty!")
	} else {
		t.Logf("✅ SUCCESS: Detected session ID: %s", inst.OpenCodeSessionID)
	}
}

// TestQueryOpenCodeSessionDirect tests the query function directly without delays
func TestQueryOpenCodeSessionDirect(t *testing.T) {
	projectPath := "/Users/ashesh/claude-deck"

	inst := &Instance{
		Tool:        "opencode",
		ProjectPath: projectPath,
	}

	t.Log("Testing queryOpenCodeSession directly...")

	sessionID := inst.queryOpenCodeSession()

	t.Logf("queryOpenCodeSession returned: %q", sessionID)

	if sessionID == "" {
		t.Error("❌ FAILED: queryOpenCodeSession returned empty string!")
	} else {
		t.Logf("✅ SUCCESS: Got session ID: %s", sessionID)
	}
}
