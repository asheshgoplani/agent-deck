package session

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestNewSessionStatusFlicker tests for green flicker on new session creation
// This reproduces the issue where a session briefly shows green before first poll
func TestNewSessionStatusFlicker(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	// Create a new session with a command (like user would do)
	inst := NewInstance("test-flicker", "/tmp")
	inst.Command = "echo hello" // Non-empty command

	// BEFORE Start() - should be idle
	if inst.Status != StatusIdle {
		t.Errorf("Before Start(): Status = %s, want idle", inst.Status)
	}

	// After Start() - current behavior sets StatusRunning immediately
	// This is the source of the flicker!
	err := inst.Start()
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer func() { _ = inst.Kill() }()

	t.Logf("After Start(): Status = %s", inst.Status)

	// Current behavior: StatusRunning is set in Start() if Command != ""
	// This causes a brief GREEN flash before the first GetStatus() poll
	if inst.Status == StatusRunning {
		t.Log("WARNING: FLICKER SOURCE - Status is 'running' immediately after Start()")
		t.Log("         This shows GREEN before the first tick updates it to the actual status")
	}

	// Simulate first tick (what happens 0-500ms after creation)
	err = inst.UpdateStatus()
	if err != nil {
		t.Fatalf("UpdateStatus failed: %v", err)
	}

	t.Logf("After first UpdateStatus(): Status = %s", inst.Status)

	// After first poll, status should be 'waiting' (not 'running')
	// because GetStatus() returns "waiting" on first poll
	if inst.Status == StatusWaiting {
		t.Log("OK: First poll correctly shows 'waiting' (yellow)")
	}
}

// TestInstance_CanFork tests the CanFork method for Claude session forking
func TestInstance_CanFork(t *testing.T) {
	inst := NewInstance("test", "/tmp/test")

	// Without Claude session ID, cannot fork
	if inst.CanFork() {
		t.Error("CanFork() should be false without ClaudeSessionID")
	}

	// With Claude session ID, can fork
	inst.ClaudeSessionID = "abc-123-def"
	inst.ClaudeDetectedAt = time.Now()
	if !inst.CanFork() {
		t.Error("CanFork() should be true with recent ClaudeSessionID")
	}

	// With old detection time, cannot fork (stale)
	inst.ClaudeDetectedAt = time.Now().Add(-10 * time.Minute)
	if inst.CanFork() {
		t.Error("CanFork() should be false with stale ClaudeSessionID")
	}
}

// TestInstance_UpdateClaudeSession tests the UpdateClaudeSession method
func TestInstance_UpdateClaudeSession(t *testing.T) {
	inst := NewInstance("test", "/tmp/test")
	inst.Tool = "claude"

	// Mock: In real test, would need actual Claude running
	// For now, just test the method exists and doesn't crash
	inst.UpdateClaudeSession(nil)

	// After update with no Claude running, should have no session ID
	// (In integration test, would verify actual detection)
}

// TestInstance_Fork tests the Fork method
func TestInstance_Fork(t *testing.T) {
	inst := NewInstance("test", "/tmp/test")

	// Cannot fork without session ID
	_, err := inst.Fork("forked-test", "")
	if err == nil {
		t.Error("Fork() should fail without ClaudeSessionID")
	}

	// With session ID, Fork returns capture-resume command
	inst.ClaudeSessionID = "abc-123"
	inst.ClaudeDetectedAt = time.Now()
	cmd, err := inst.Fork("forked-test", "")
	if err != nil {
		t.Errorf("Fork() failed: %v", err)
	}

	// Command should use capture-resume pattern with fork
	if !strings.Contains(cmd, "CLAUDE_CONFIG_DIR=") {
		t.Errorf("Fork() should set CLAUDE_CONFIG_DIR, got: %s", cmd)
	}
	if !strings.Contains(cmd, "--resume abc-123 --fork-session") {
		t.Errorf("Fork() should include resume and fork-session flags for capture, got: %s", cmd)
	}
	if !strings.Contains(cmd, `--output-format json`) {
		t.Errorf("Fork() should use --output-format json for capture, got: %s", cmd)
	}
	if !strings.Contains(cmd, "tmux set-environment CLAUDE_SESSION_ID") {
		t.Errorf("Fork() should store session ID in tmux env, got: %s", cmd)
	}
	if !strings.Contains(cmd, `--resume "$session_id"`) {
		t.Errorf("Fork() should resume the captured session, got: %s", cmd)
	}
}

// TestInstance_CreateForkedInstance tests the CreateForkedInstance method
func TestInstance_CreateForkedInstance(t *testing.T) {
	inst := NewInstance("original", "/tmp/test")
	inst.GroupPath = "projects"

	// Cannot create fork without session ID
	_, _, err := inst.CreateForkedInstance("forked", "")
	if err == nil {
		t.Error("CreateForkedInstance() should fail without ClaudeSessionID")
	}

	// With session ID, creates new instance with fork command
	inst.ClaudeSessionID = "abc-123"
	inst.ClaudeDetectedAt = time.Now()
	forked, cmd, err := inst.CreateForkedInstance("forked", "")
	if err != nil {
		t.Errorf("CreateForkedInstance() failed: %v", err)
	}

	// Verify command includes config dir and fork flags
	if !strings.Contains(cmd, "CLAUDE_CONFIG_DIR=") {
		t.Errorf("Command should set CLAUDE_CONFIG_DIR, got: %s", cmd)
	}
	if !strings.Contains(cmd, "--resume abc-123 --fork-session") {
		t.Errorf("Command should include resume and fork flags, got: %s", cmd)
	}

	// Verify forked instance has correct properties
	if forked.Title != "forked" {
		t.Errorf("Forked title = %s, want forked", forked.Title)
	}
	if forked.ProjectPath != "/tmp/test" {
		t.Errorf("Forked path = %s, want /tmp/test", forked.ProjectPath)
	}
	if forked.GroupPath != "projects" {
		t.Errorf("Forked group = %s, want projects (inherited)", forked.GroupPath)
	}
	if !strings.Contains(forked.Command, "--resume abc-123 --fork-session") {
		t.Errorf("Forked command should include fork flags, got: %s", forked.Command)
	}
	if forked.Tool != "claude" {
		t.Errorf("Forked tool = %s, want claude", forked.Tool)
	}

	// Test with custom group path
	forked2, _, err := inst.CreateForkedInstance("forked2", "custom-group")
	if err != nil {
		t.Errorf("CreateForkedInstance() with custom group failed: %v", err)
	}
	if forked2.GroupPath != "custom-group" {
		t.Errorf("Forked group = %s, want custom-group", forked2.GroupPath)
	}
}

// TestNewInstanceWithTool tests that tools are set correctly without pre-assigned session IDs
func TestNewInstanceWithTool(t *testing.T) {
	// Shell tool should not have session ID (never will)
	shellInst := NewInstanceWithTool("shell-test", "/tmp/test", "shell")
	if shellInst.ClaudeSessionID != "" {
		t.Errorf("Shell session should not have ClaudeSessionID, got: %s", shellInst.ClaudeSessionID)
	}

	// Claude tool should NOT have pre-assigned ID (detection happens later)
	claudeInst := NewInstanceWithTool("claude-test", "/tmp/test", "claude")
	if claudeInst.ClaudeSessionID != "" {
		t.Errorf("Claude session should NOT have pre-assigned ClaudeSessionID (detection-based), got: %s", claudeInst.ClaudeSessionID)
	}
	if claudeInst.Tool != "claude" {
		t.Errorf("Tool = %s, want claude", claudeInst.Tool)
	}
	// ClaudeDetectedAt should be zero (detection hasn't happened yet)
	if !claudeInst.ClaudeDetectedAt.IsZero() {
		t.Error("ClaudeDetectedAt should be zero until detection happens")
	}
}

// TestBuildClaudeCommand tests that claude command is built with capture-resume pattern
func TestBuildClaudeCommand(t *testing.T) {
	inst := NewInstanceWithTool("test", "/tmp/test", "claude")

	// Test with simple "claude" command
	cmd := inst.buildClaudeCommand("claude")

	// Should contain CLAUDE_CONFIG_DIR (appears twice: once for capture, once for resume)
	if !strings.Contains(cmd, "CLAUDE_CONFIG_DIR=") {
		t.Errorf("Should contain CLAUDE_CONFIG_DIR, got: %s", cmd)
	}

	// Should use capture-resume pattern: -p "." --output-format json
	if !strings.Contains(cmd, `-p "."`) {
		t.Errorf("Should contain -p \".\" for print mode, got: %s", cmd)
	}
	if !strings.Contains(cmd, "--output-format json") {
		t.Errorf("Should contain --output-format json, got: %s", cmd)
	}

	// Should store session ID in tmux environment
	if !strings.Contains(cmd, "tmux set-environment CLAUDE_SESSION_ID") {
		t.Errorf("Should store session ID in tmux env, got: %s", cmd)
	}

	// Should resume the captured session
	if !strings.Contains(cmd, `--resume "$session_id"`) {
		t.Errorf("Should resume the captured session ID, got: %s", cmd)
	}

	// Should have fallback when capture fails (Issue #19: WSL jq parse error)
	if !strings.Contains(cmd, `|| session_id=""`) {
		t.Errorf("Should have fallback when capture fails, got: %s", cmd)
	}
	// Should check for null jq output
	if !strings.Contains(cmd, `!= "null"`) {
		t.Errorf("Should check for null session_id from jq, got: %s", cmd)
	}
	// Should start Claude even without session ID (fallback path)
	if !strings.Contains(cmd, "else CLAUDE_CONFIG_DIR=") {
		t.Errorf("Should have else branch to start Claude without session, got: %s", cmd)
	}

	// Note: --dangerously-skip-permissions is conditional on user config (dangerous_mode)
	// The command should work with or without it depending on config

	// Test with non-claude tool (should not modify)
	shellInst := NewInstance("shell-test", "/tmp/test")
	shellCmd := shellInst.buildClaudeCommand("bash")
	if shellCmd != "bash" {
		t.Errorf("Non-claude command should not be modified, got: %s", shellCmd)
	}
}

// TestCreateForkedInstance_CaptureResumePattern tests that forked sessions
// use the capture-resume pattern to reliably get the new session ID
func TestCreateForkedInstance_CaptureResumePattern(t *testing.T) {
	inst := NewInstance("original", "/tmp/test")
	inst.ClaudeSessionID = "parent-abc-123"
	inst.ClaudeDetectedAt = time.Now()

	forked, cmd, err := inst.CreateForkedInstance("forked", "")
	if err != nil {
		t.Fatalf("CreateForkedInstance() failed: %v", err)
	}

	// Command SHOULD use capture-resume pattern
	if !strings.Contains(cmd, "--output-format json") {
		t.Errorf("Fork command should use --output-format json for capture, got: %s", cmd)
	}
	if !strings.Contains(cmd, "--resume parent-abc-123 --fork-session") {
		t.Errorf("Fork command should contain --resume with parent ID and --fork-session, got: %s", cmd)
	}
	if !strings.Contains(cmd, "tmux set-environment CLAUDE_SESSION_ID") {
		t.Errorf("Fork command should store session ID in tmux env, got: %s", cmd)
	}

	// Forked instance should have empty ClaudeSessionID initially
	// (will be populated from tmux env after start)
	if forked.ClaudeSessionID != "" {
		t.Errorf("Forked instance should have empty ClaudeSessionID initially, got: %s", forked.ClaudeSessionID)
	}

	if forked.Tool != "claude" {
		t.Errorf("Forked tool = %s, want claude", forked.Tool)
	}
}

// TestWaitForClaudeSession tests the wait-for-detection functionality
func TestWaitForClaudeSession(t *testing.T) {
	inst := NewInstance("test", "/tmp/nonexistent-project-dir")
	inst.Tool = "claude"

	// Should timeout and return empty when no session file exists
	start := time.Now()
	sessionID := inst.WaitForClaudeSession(500 * time.Millisecond)
	elapsed := time.Since(start)

	if sessionID != "" {
		t.Errorf("Should return empty when no session file, got: %s", sessionID)
	}

	// Should have waited at least close to the timeout
	if elapsed < 400*time.Millisecond {
		t.Errorf("Should have waited ~500ms, but only waited %v", elapsed)
	}

	// ClaudeSessionID should still be empty
	if inst.ClaudeSessionID != "" {
		t.Errorf("ClaudeSessionID should be empty, got: %s", inst.ClaudeSessionID)
	}
}

func TestInstance_GetSessionIDFromTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	// Create instance with tmux session
	inst := NewInstanceWithTool("tmux-env-test", "/tmp", "claude")

	// Start the session
	err := inst.Start()
	if err != nil {
		t.Fatalf("Failed to start instance: %v", err)
	}
	defer func() { _ = inst.Kill() }()

	// Initially should return empty (no CLAUDE_SESSION_ID set)
	if id := inst.GetSessionIDFromTmux(); id != "" {
		t.Errorf("GetSessionIDFromTmux should return empty initially, got: %s", id)
	}

	// Set the environment variable directly via tmux
	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		t.Fatal("tmux session is nil")
	}

	testSessionID := "test-uuid-12345"
	err = tmuxSess.SetEnvironment("CLAUDE_SESSION_ID", testSessionID)
	if err != nil {
		t.Fatalf("Failed to set environment: %v", err)
	}

	// Now should return the session ID
	if id := inst.GetSessionIDFromTmux(); id != testSessionID {
		t.Errorf("GetSessionIDFromTmux = %q, want %q", id, testSessionID)
	}
}

func TestInstance_UpdateClaudeSession_TmuxFirst(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	// Create and start instance
	inst := NewInstanceWithTool("update-test", "/tmp", "claude")
	err := inst.Start()
	if err != nil {
		t.Fatalf("Failed to start instance: %v", err)
	}
	defer func() { _ = inst.Kill() }()

	// Set session ID in tmux environment
	testSessionID := "tmux-session-abc123"
	tmuxSess := inst.GetTmuxSession()
	err = tmuxSess.SetEnvironment("CLAUDE_SESSION_ID", testSessionID)
	if err != nil {
		t.Fatalf("Failed to set environment: %v", err)
	}

	// Clear any existing detection
	inst.ClaudeSessionID = ""
	inst.ClaudeDetectedAt = time.Time{}

	// Call UpdateClaudeSession
	inst.UpdateClaudeSession(nil)

	// Should have picked up from tmux environment
	if inst.ClaudeSessionID != testSessionID {
		t.Errorf("ClaudeSessionID = %q, want %q (from tmux env)", inst.ClaudeSessionID, testSessionID)
	}
}

// TestInstance_UpdateClaudeSession_PreservesExistingID verifies that existing
// session IDs from storage are preserved when tmux env is empty.
// With the new tmux-only approach, we only update when tmux env has a value.
func TestInstance_UpdateClaudeSession_PreservesExistingID(t *testing.T) {
	// Create instance with known session ID (simulating loaded from storage)
	inst := NewInstanceWithTool("preserve-id-test", "/tmp", "claude")
	existingID := "existing-session-id-abc123"
	inst.ClaudeSessionID = existingID
	oldDetectedAt := time.Now().Add(-10 * time.Minute)
	inst.ClaudeDetectedAt = oldDetectedAt

	// Call UpdateClaudeSession - without tmux session, nothing should change
	inst.UpdateClaudeSession(nil)

	// Existing session ID must be preserved (tmux env is empty, so no change)
	if inst.ClaudeSessionID != existingID {
		t.Errorf("ClaudeSessionID was changed from %q to %q - should preserve stored ID when tmux env is empty",
			existingID, inst.ClaudeSessionID)
	}

	// Timestamp should NOT change (no tmux env = no update)
	if inst.ClaudeDetectedAt != oldDetectedAt {
		t.Error("ClaudeDetectedAt should not change when tmux env is empty")
	}
}

// TestInstance_UpdateGeminiSession_PreservesExistingID verifies that existing
// Gemini session IDs from storage are preserved when tmux env is empty.
// With the new tmux-only approach, we only update when tmux env has a value.
func TestInstance_UpdateGeminiSession_PreservesExistingID(t *testing.T) {
	// Create instance with known session ID (simulating loaded from storage)
	inst := NewInstanceWithTool("preserve-gemini-test", "/tmp", "gemini")
	existingID := "existing-gemini-id-xyz789"
	inst.GeminiSessionID = existingID
	oldDetectedAt := time.Now().Add(-10 * time.Minute)
	inst.GeminiDetectedAt = oldDetectedAt

	// Call UpdateGeminiSession - without tmux session, nothing should change
	inst.UpdateGeminiSession(nil)

	// Existing session ID must be preserved (tmux env is empty, so no change)
	if inst.GeminiSessionID != existingID {
		t.Errorf("GeminiSessionID was changed from %q to %q - should preserve stored ID when tmux env is empty",
			existingID, inst.GeminiSessionID)
	}

	// Timestamp should NOT change (no tmux env = no update)
	if inst.GeminiDetectedAt != oldDetectedAt {
		t.Error("GeminiDetectedAt should not change when tmux env is empty")
	}
}

func TestInstance_Restart_ResumesClaudeSession(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	// Create instance with known session ID (simulating previous session)
	inst := NewInstanceWithTool("restart-test", "/tmp", "claude")
	inst.Command = "claude"
	inst.ClaudeSessionID = "known-session-id-xyz"
	inst.ClaudeDetectedAt = time.Now()

	// Start initial tmux session
	err := inst.Start()
	if err != nil {
		t.Fatalf("Failed to start initial session: %v", err)
	}

	// Mark as error state to allow restart
	inst.Status = StatusError

	// Kill the tmux session to simulate dead session
	_ = inst.Kill()

	// Now restart - should use --resume with the known session ID
	err = inst.Restart()
	if err != nil {
		t.Fatalf("Restart failed: %v", err)
	}
	defer func() { _ = inst.Kill() }()

	// Verify the session was created and is running
	if inst.tmuxSession == nil {
		t.Fatal("tmux session is nil after restart")
	}

	if !inst.tmuxSession.Exists() {
		t.Error("tmux session should exist after restart")
	}

	// Status should be waiting initially (will go to running on first tick if Claude shows busy indicator)
	if inst.Status != StatusWaiting {
		t.Errorf("Status = %v, want waiting", inst.Status)
	}
}

func TestInstance_Restart_InterruptsAndResumes(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	// This test requires claude to be installed (restart generates claude --resume command)
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not available - test requires claude CLI for restart functionality")
	}

	// Create instance with known session ID
	inst := NewInstanceWithTool("restart-interrupt-test", "/tmp", "claude")
	inst.Command = "claude"
	inst.ClaudeSessionID = "test-session-id-xyz"
	inst.ClaudeDetectedAt = time.Now()

	// Start initial tmux session with a simple command
	err := inst.Start()
	if err != nil {
		t.Fatalf("Failed to start initial session: %v", err)
	}
	defer func() { _ = inst.Kill() }()

	// Session is running (not error state)
	inst.Status = StatusRunning

	// CanRestart should now return true for running sessions
	if !inst.CanRestart() {
		t.Error("CanRestart() should return true for running Claude session with known ID")
	}

	// Now restart - should send Ctrl+C and resume command
	err = inst.Restart()
	if err != nil {
		t.Fatalf("Restart failed: %v", err)
	}

	// Give tmux time to respawn the pane
	time.Sleep(100 * time.Millisecond)

	// Verify the session still exists after restart
	if !inst.tmuxSession.Exists() {
		t.Error("tmux session should still exist after restart")
	}
}

func TestInstance_GeminiSessionFields(t *testing.T) {
	inst := NewInstanceWithTool("test", "/tmp/test", "gemini")

	// Should have empty Gemini session ID initially
	if inst.GeminiSessionID != "" {
		t.Errorf("GeminiSessionID should be empty initially, got %s", inst.GeminiSessionID)
	}

	// Should be able to set Gemini session ID
	testID := "abc-123-def-456"
	inst.GeminiSessionID = testID
	inst.GeminiDetectedAt = time.Now()

	if inst.GeminiSessionID != testID {
		t.Errorf("GeminiSessionID = %s, want %s", inst.GeminiSessionID, testID)
	}

	// Non-Gemini tools should not have Gemini ID
	claudeInst := NewInstanceWithTool("test", "/tmp/test", "claude")
	if claudeInst.GeminiSessionID != "" {
		t.Error("Claude session should not have GeminiSessionID")
	}
}

func TestInstance_UpdateGeminiSession(t *testing.T) {
	inst := NewInstanceWithTool("test", "/tmp/test", "gemini")
	inst.CreatedAt = time.Now()

	// For non-Gemini tools, should do nothing
	shellInst := NewInstanceWithTool("shell", "/tmp/test", "shell")
	shellInst.UpdateGeminiSession(nil)
	if shellInst.GeminiSessionID != "" {
		t.Error("Shell session should not have GeminiSessionID")
	}

	// For Gemini without sessions, should remain empty
	inst.UpdateGeminiSession(nil)
	// (No real sessions exist, so ID remains empty)

	// With existing recent ID, should not redetect
	inst.GeminiSessionID = "existing-id"
	inst.GeminiDetectedAt = time.Now()
	oldID := inst.GeminiSessionID

	inst.UpdateGeminiSession(nil)
	if inst.GeminiSessionID != oldID {
		t.Error("Should not redetect when ID is recent")
	}
}

func TestBuildGeminiCommand(t *testing.T) {
	inst := NewInstanceWithTool("test", "/tmp/test", "gemini")

	// Without session ID, should return capture-resume pattern
	cmd := inst.buildGeminiCommand("gemini")

	// Should contain json output format and session ID capture
	// NOTE: We use --output-format json (not stream-json) to let Gemini complete
	// and save the session before extracting session_id
	if !strings.Contains(cmd, "--output-format json") {
		t.Error("Should use json output format for session ID capture")
	}
	if !strings.Contains(cmd, "GEMINI_SESSION_ID") {
		t.Error("Should set GEMINI_SESSION_ID in tmux environment")
	}
	if !strings.Contains(cmd, "--resume") {
		t.Error("Should resume captured session")
	}

	// Should have fallback when capture fails (Issue #19: WSL jq parse error)
	if !strings.Contains(cmd, `|| session_id=""`) {
		t.Error("Should have fallback when capture fails")
	}
	// Should check for null jq output
	if !strings.Contains(cmd, `!= "null"`) {
		t.Error("Should check for null session_id from jq")
	}
	// Should start Gemini even without session ID (fallback path)
	if !strings.Contains(cmd, "else gemini; fi") {
		t.Error("Should have else branch to start Gemini fresh")
	}

	// With session ID, should use simple resume
	inst.GeminiSessionID = "abc-123-def"
	cmd = inst.buildGeminiCommand("gemini")
	expected := "gemini --resume abc-123-def"
	if cmd != expected {
		t.Errorf("buildGeminiCommand('gemini') = %q, want %q", cmd, expected)
	}

	// Custom commands should pass through
	customCmd := "gemini --some-flag"
	cmd = inst.buildGeminiCommand(customCmd)
	if cmd != customCmd {
		t.Errorf("buildGeminiCommand(custom) = %q, want %q", cmd, customCmd)
	}
}

func TestInstance_GetMCPInfo_Gemini(t *testing.T) {
	inst := NewInstanceWithTool("test", "/tmp/test", "gemini")

	info := inst.GetMCPInfo()
	if info == nil {
		t.Fatal("GetMCPInfo() should return info for Gemini")
	}

	// Should have Global MCPs only (no Project or Local for Gemini)
	// Actual content depends on settings.json existing
	// Here we just verify it returns a valid MCPInfo (not nil)
}

func TestInstance_GetMCPInfo_Claude(t *testing.T) {
	inst := NewInstanceWithTool("test", "/tmp/test", "claude")

	info := inst.GetMCPInfo()
	if info == nil {
		t.Fatal("GetMCPInfo() should return info for Claude")
	}

	// Claude uses GetMCPInfo() which can have Global, Project, and Local
}

func TestInstance_GetMCPInfo_Shell(t *testing.T) {
	inst := NewInstanceWithTool("test", "/tmp/test", "shell")

	info := inst.GetMCPInfo()
	if info != nil {
		t.Error("GetMCPInfo() should return nil for shell")
	}
}

func TestInstance_GetMCPInfo_Unknown(t *testing.T) {
	inst := NewInstanceWithTool("test", "/tmp/test", "unknown-tool")

	info := inst.GetMCPInfo()
	if info != nil {
		t.Error("GetMCPInfo() should return nil for unknown tools")
	}
}

func TestInstance_CanFork_Gemini(t *testing.T) {
	// Test 1: Gemini tool with valid session ID should NOT be forkable
	// Gemini CLI has NO --fork-session flag (unlike Claude)
	inst := NewInstanceWithTool("test", "/tmp/test", "gemini")
	inst.GeminiSessionID = "abc-123-def"
	inst.GeminiDetectedAt = time.Now()

	if inst.CanFork() {
		t.Error("CanFork() should be false for Gemini (not supported by Gemini CLI)")
	}

	// Test 2: Even if ClaudeSessionID were somehow set, Gemini tool should not fork
	// This tests the explicit tool check
	inst.ClaudeSessionID = "claude-session-xyz"
	inst.ClaudeDetectedAt = time.Now()

	if inst.CanFork() {
		t.Error("CanFork() should be false for Gemini tool even with ClaudeSessionID set")
	}
}

func TestParseGeminiLastAssistantMessage(t *testing.T) {
	// VERIFIED: Actual Gemini session JSON structure
	sessionJSON := `{
  "sessionId": "abc-123-def",
  "messages": [
    {
      "id": "1",
      "timestamp": "2025-12-23T00:00:00Z",
      "type": "user",
      "content": "Hello"
    },
    {
      "id": "2",
      "timestamp": "2025-12-23T00:00:05Z",
      "type": "gemini",
      "content": "Hi there! How can I help you?",
      "model": "gemini-3-pro",
      "tokens": {"input": 100, "output": 50, "total": 150}
    }
  ]
}`

	output, err := parseGeminiLastAssistantMessage([]byte(sessionJSON))
	if err != nil {
		t.Fatalf("parseGeminiLastAssistantMessage() error = %v", err)
	}

	if output.Tool != "gemini" {
		t.Errorf("Tool = %q, want 'gemini'", output.Tool)
	}

	if output.Content != "Hi there! How can I help you?" {
		t.Errorf("Content = %q, want 'Hi there! How can I help you?'", output.Content)
	}

	if output.SessionID != "abc-123-def" {
		t.Errorf("SessionID = %q, want 'abc-123-def'", output.SessionID)
	}
}

func TestParseGeminiLastAssistantMessage_MultipleMessages(t *testing.T) {
	// Test with multiple user/gemini exchanges - should return last gemini message
	sessionJSON := `{
  "sessionId": "test-456",
  "messages": [
    {"id": "1", "type": "user", "content": "First question"},
    {"id": "2", "type": "gemini", "content": "First answer", "timestamp": "2025-12-23T00:00:05Z"},
    {"id": "3", "type": "user", "content": "Second question"},
    {"id": "4", "type": "gemini", "content": "Second answer - this is the last", "timestamp": "2025-12-23T00:00:10Z"}
  ]
}`

	output, err := parseGeminiLastAssistantMessage([]byte(sessionJSON))
	if err != nil {
		t.Fatalf("parseGeminiLastAssistantMessage() error = %v", err)
	}

	if output.Content != "Second answer - this is the last" {
		t.Errorf("Content = %q, want 'Second answer - this is the last'", output.Content)
	}
}

func TestParseGeminiLastAssistantMessage_NoGeminiMessage(t *testing.T) {
	// Test with only user messages - should return error
	sessionJSON := `{
  "sessionId": "test-789",
  "messages": [
    {"id": "1", "type": "user", "content": "Hello"}
  ]
}`

	_, err := parseGeminiLastAssistantMessage([]byte(sessionJSON))
	if err == nil {
		t.Error("parseGeminiLastAssistantMessage() should return error when no gemini message found")
	}
}

func TestInstance_CanRestart_Gemini(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	// Create and start a Gemini session so tmux session exists
	inst := NewInstanceWithTool("gemini-restart-test", "/tmp", "gemini")
	inst.Command = "sleep 60"
	err := inst.Start()
	if err != nil {
		t.Fatalf("Failed to start session: %v", err)
	}
	defer func() { _ = inst.Kill() }()

	// Make it a "running" session
	inst.Status = StatusRunning

	// Without session ID, cannot restart (session exists and is running)
	if inst.CanRestart() {
		t.Error("CanRestart() should be false without session ID for running session")
	}

	// With session ID, can restart (even while running)
	inst.GeminiSessionID = "abc-123-def-456"
	if !inst.CanRestart() {
		t.Error("CanRestart() should be true with session ID")
	}

	// Stale session ID (>5 min) should still allow restart
	inst.GeminiDetectedAt = time.Now().Add(-10 * time.Minute)
	if !inst.CanRestart() {
		t.Error("CanRestart() should work with stale session ID")
	}
}

// TestInstance_Fork_PathWithSpaces tests that Fork() properly quotes paths with spaces
// Issue #16: Fork command breaks for project paths with spaces
func TestInstance_Fork_PathWithSpaces(t *testing.T) {
	inst := &Instance{
		ID:              "test-123",
		Title:           "test-session",
		ProjectPath:     "/tmp/Test Path With Spaces",
		Tool:            "claude",
		ClaudeSessionID: "session-abc-123",
		ClaudeDetectedAt: time.Now(),
	}

	cmd, err := inst.Fork("forked-session", "")
	if err != nil {
		t.Fatalf("Fork() error = %v", err)
	}

	// The cd command should have quoted path
	if !strings.Contains(cmd, `cd '/tmp/Test Path With Spaces'`) {
		t.Errorf("Fork command should quote path with spaces using single quotes.\nGot: %s", cmd)
	}

	// Should NOT contain unquoted path that would break
	if strings.Contains(cmd, "cd /tmp/Test Path With Spaces &&") {
		t.Errorf("Fork command should not have unquoted path.\nGot: %s", cmd)
	}
}

// TestInstance_Fork_RespectsDangerousMode tests that Fork() respects dangerous_mode config
// Issue #8: Fork command ignores dangerous_mode configuration
func TestInstance_Fork_RespectsDangerousMode(t *testing.T) {
	inst := &Instance{
		ID:              "test-456",
		Title:           "test-session",
		ProjectPath:     "/tmp/test",
		Tool:            "claude",
		ClaudeSessionID: "session-xyz-789",
		ClaudeDetectedAt: time.Now(),
	}

	// Test with dangerous_mode = false
	t.Run("dangerous_mode=false", func(t *testing.T) {
		// Set up config with dangerous_mode = false
		userConfigCacheMu.Lock()
		userConfigCache = &UserConfig{
			Claude: ClaudeSettings{
				DangerousMode: false,
			},
		}
		userConfigCacheMu.Unlock()
		defer func() {
			userConfigCacheMu.Lock()
			userConfigCache = nil
			userConfigCacheMu.Unlock()
		}()

		cmd, err := inst.Fork("forked", "")
		if err != nil {
			t.Fatalf("Fork() error = %v", err)
		}

		// Should NOT have --dangerously-skip-permissions when config is false
		if strings.Contains(cmd, "--dangerously-skip-permissions") {
			t.Errorf("Fork command should NOT include --dangerously-skip-permissions when dangerous_mode=false.\nGot: %s", cmd)
		}
	})

	// Test with dangerous_mode = true
	t.Run("dangerous_mode=true", func(t *testing.T) {
		// Set up config with dangerous_mode = true
		userConfigCacheMu.Lock()
		userConfigCache = &UserConfig{
			Claude: ClaudeSettings{
				DangerousMode: true,
			},
		}
		userConfigCacheMu.Unlock()
		defer func() {
			userConfigCacheMu.Lock()
			userConfigCache = nil
			userConfigCacheMu.Unlock()
		}()

		cmd, err := inst.Fork("forked", "")
		if err != nil {
			t.Fatalf("Fork() error = %v", err)
		}

		// SHOULD have --dangerously-skip-permissions when config is true
		if !strings.Contains(cmd, "--dangerously-skip-permissions") {
			t.Errorf("Fork command should include --dangerously-skip-permissions when dangerous_mode=true.\nGot: %s", cmd)
		}
	})
}
