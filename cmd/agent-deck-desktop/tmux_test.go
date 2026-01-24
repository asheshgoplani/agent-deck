package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestTmuxManagerSessionExists tests session existence checking
func TestTmuxManagerSessionExists(t *testing.T) {
	// Skip if tmux is not available
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available, skipping integration test")
	}

	tm := NewTmuxManager()

	// Non-existent session should return false
	if tm.SessionExists("definitely_not_a_real_session_name_12345") {
		t.Error("Expected SessionExists to return false for non-existent session")
	}
}

// TestTmuxManagerCreateSession tests session creation
func TestTmuxManagerCreateSession(t *testing.T) {
	// Skip if tmux is not available
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available, skipping integration test")
	}

	tm := NewTmuxManager()
	tmpDir := t.TempDir()

	// Create a session
	session, err := tm.CreateSession(tmpDir, "Test Session", "claude")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Verify session info
	if session.Title != "Test Session" {
		t.Errorf("Expected title 'Test Session', got '%s'", session.Title)
	}
	if session.ProjectPath != tmpDir {
		t.Errorf("Expected projectPath '%s', got '%s'", tmpDir, session.ProjectPath)
	}
	if session.Tool != "claude" {
		t.Errorf("Expected tool 'claude', got '%s'", session.Tool)
	}
	if session.TmuxSession == "" {
		t.Error("Expected TmuxSession to be set")
	}
	if !strings.HasPrefix(session.TmuxSession, "agentdeck_") {
		t.Errorf("Expected TmuxSession to start with 'agentdeck_', got '%s'", session.TmuxSession)
	}

	// Verify the tmux session actually exists
	if !tm.SessionExists(session.TmuxSession) {
		t.Error("Created session should exist in tmux")
	}

	// Clean up: kill the test session
	exec.Command("tmux", "kill-session", "-t", session.TmuxSession).Run()
}

// TestTmuxManagerGetRunningSessionsFormat tests session listing format
func TestTmuxManagerGetRunningSessionsFormat(t *testing.T) {
	// Skip if tmux is not available
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available, skipping integration test")
	}

	tm := NewTmuxManager()
	sessions := tm.getRunningTmuxSessions()

	// Just verify it returns a map (may be empty if no sessions running)
	if sessions == nil {
		t.Error("getRunningTmuxSessions should not return nil")
	}
}
