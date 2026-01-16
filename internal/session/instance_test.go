package session

import (
	"os"
	"os/exec"
	"path/filepath"
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
	// Isolate from user's environment to ensure CLAUDE_CONFIG_DIR is NOT explicit
	origConfigDir := os.Getenv("CLAUDE_CONFIG_DIR")
	origHome := os.Getenv("HOME")
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	os.Setenv("HOME", t.TempDir())
	ClearUserConfigCache()
	defer func() {
		if origConfigDir != "" {
			os.Setenv("CLAUDE_CONFIG_DIR", origConfigDir)
		}
		os.Setenv("HOME", origHome)
		ClearUserConfigCache()
	}()

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

	// Command should use --session-id pattern (avoids capture-resume with "." prompt)
	// When not explicitly configured, CLAUDE_CONFIG_DIR should NOT be set
	// (allows shell environment to take precedence)
	if strings.Contains(cmd, "CLAUDE_CONFIG_DIR=") {
		t.Errorf("Fork() should NOT set CLAUDE_CONFIG_DIR when not explicitly configured, got: %s", cmd)
	}
	if !strings.Contains(cmd, "--resume abc-123 --fork-session") {
		t.Errorf("Fork() should include resume and fork-session flags, got: %s", cmd)
	}
	if !strings.Contains(cmd, "uuidgen") {
		t.Errorf("Fork() should generate UUID with uuidgen, got: %s", cmd)
	}
	if !strings.Contains(cmd, "tmux set-environment CLAUDE_SESSION_ID") {
		t.Errorf("Fork() should store session ID in tmux env, got: %s", cmd)
	}
	if !strings.Contains(cmd, `--session-id "$session_id"`) {
		t.Errorf("Fork() should pass session ID to claude CLI, got: %s", cmd)
	}
}

// TestInstance_Fork_ExplicitConfig tests Fork with explicit CLAUDE_CONFIG_DIR
func TestInstance_Fork_ExplicitConfig(t *testing.T) {
	// Isolate from user's environment (don't pick up their config.toml)
	origHome := os.Getenv("HOME")
	tmpDir := t.TempDir()
	os.Setenv("HOME", tmpDir)
	ClearUserConfigCache()

	os.Setenv("CLAUDE_CONFIG_DIR", "/tmp/test-claude-config")
	defer func() {
		os.Unsetenv("CLAUDE_CONFIG_DIR")
		os.Setenv("HOME", origHome)
		ClearUserConfigCache()
	}()

	inst := NewInstance("test", "/tmp/test")
	inst.ClaudeSessionID = "abc-123"
	inst.ClaudeDetectedAt = time.Now()

	cmd, err := inst.Fork("forked-test", "")
	if err != nil {
		t.Errorf("Fork() failed: %v", err)
	}

	// When explicitly configured, CLAUDE_CONFIG_DIR SHOULD be set
	if !strings.Contains(cmd, "CLAUDE_CONFIG_DIR=/tmp/test-claude-config") {
		t.Errorf("Fork() should set CLAUDE_CONFIG_DIR when explicitly configured, got: %s", cmd)
	}
}

// TestInstance_CreateForkedInstance tests the CreateForkedInstance method
func TestInstance_CreateForkedInstance(t *testing.T) {
	// Isolate from user's environment to ensure CLAUDE_CONFIG_DIR is NOT explicit
	origConfigDir := os.Getenv("CLAUDE_CONFIG_DIR")
	origHome := os.Getenv("HOME")
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	os.Setenv("HOME", t.TempDir())
	ClearUserConfigCache()
	defer func() {
		if origConfigDir != "" {
			os.Setenv("CLAUDE_CONFIG_DIR", origConfigDir)
		}
		os.Setenv("HOME", origHome)
		ClearUserConfigCache()
	}()

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

	// Verify command includes fork flags
	// When not explicitly configured, CLAUDE_CONFIG_DIR should NOT be set
	if strings.Contains(cmd, "CLAUDE_CONFIG_DIR=") {
		t.Errorf("Command should NOT set CLAUDE_CONFIG_DIR when not explicitly configured, got: %s", cmd)
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

// TestInstance_CreateForkedInstance_ExplicitConfig tests CreateForkedInstance with explicit config
func TestInstance_CreateForkedInstance_ExplicitConfig(t *testing.T) {
	// Isolate from user's environment (don't pick up their config.toml)
	origHome := os.Getenv("HOME")
	tmpDir := t.TempDir()
	os.Setenv("HOME", tmpDir)
	ClearUserConfigCache()

	os.Setenv("CLAUDE_CONFIG_DIR", "/tmp/test-claude-config")
	defer func() {
		os.Unsetenv("CLAUDE_CONFIG_DIR")
		os.Setenv("HOME", origHome)
		ClearUserConfigCache()
	}()

	inst := NewInstance("original", "/tmp/test")
	inst.ClaudeSessionID = "abc-123"
	inst.ClaudeDetectedAt = time.Now()

	_, cmd, err := inst.CreateForkedInstance("forked", "")
	if err != nil {
		t.Errorf("CreateForkedInstance() failed: %v", err)
	}

	// When explicitly configured, CLAUDE_CONFIG_DIR SHOULD be set
	if !strings.Contains(cmd, "CLAUDE_CONFIG_DIR=/tmp/test-claude-config") {
		t.Errorf("Command should set CLAUDE_CONFIG_DIR when explicitly configured, got: %s", cmd)
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
	// Isolate from user's environment to ensure CLAUDE_CONFIG_DIR is NOT explicit
	origConfigDir := os.Getenv("CLAUDE_CONFIG_DIR")
	origHome := os.Getenv("HOME")
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	os.Setenv("HOME", t.TempDir()) // Use temp dir so config.toml isn't found
	ClearUserConfigCache()
	defer func() {
		if origConfigDir != "" {
			os.Setenv("CLAUDE_CONFIG_DIR", origConfigDir)
		}
		os.Setenv("HOME", origHome)
		ClearUserConfigCache()
	}()

	inst := NewInstanceWithTool("test", "/tmp/test", "claude")

	// Test with simple "claude" command
	cmd := inst.buildClaudeCommand("claude")

	// When CLAUDE_CONFIG_DIR is NOT explicitly configured,
	// the command should NOT include CLAUDE_CONFIG_DIR
	if strings.Contains(cmd, "CLAUDE_CONFIG_DIR=") {
		t.Errorf("Should NOT contain CLAUDE_CONFIG_DIR when not explicitly configured, got: %s", cmd)
	}

	// Should use capture-resume pattern: -p "." to get session ID
	if !strings.Contains(cmd, `-p "."`) {
		t.Errorf("Should use -p \".\" for session ID capture, got: %s", cmd)
	}

	// Should use --output-format json to extract session ID
	if !strings.Contains(cmd, "--output-format json") {
		t.Errorf("Should use --output-format json for capture, got: %s", cmd)
	}

	// Should store session ID in tmux environment
	if !strings.Contains(cmd, "tmux set-environment CLAUDE_SESSION_ID") {
		t.Errorf("Should store session ID in tmux env, got: %s", cmd)
	}

	// Should use --resume to continue with captured session
	if !strings.Contains(cmd, `--resume "$session_id"`) {
		t.Errorf("Should use --resume flag for captured session, got: %s", cmd)
	}

	// Should NOT use --session-id flag (it doesn't work for NEW sessions)
	if strings.Contains(cmd, "--session-id") {
		t.Errorf("Should NOT use --session-id (only works for resume), got: %s", cmd)
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

// TestBuildClaudeCommand_ExplicitConfig tests that CLAUDE_CONFIG_DIR is set when explicitly configured
func TestBuildClaudeCommand_ExplicitConfig(t *testing.T) {
	// Isolate from user's environment
	origHome := os.Getenv("HOME")
	tmpDir := t.TempDir()
	os.Setenv("HOME", tmpDir) // Use temp dir so config.toml isn't found
	ClearUserConfigCache()

	// Set environment variable to explicitly configure CLAUDE_CONFIG_DIR
	os.Setenv("CLAUDE_CONFIG_DIR", "/tmp/test-claude-config")
	defer func() {
		os.Unsetenv("CLAUDE_CONFIG_DIR")
		os.Setenv("HOME", origHome)
		ClearUserConfigCache()
	}()

	inst := NewInstanceWithTool("test", "/tmp/test", "claude")
	cmd := inst.buildClaudeCommand("claude")

	// When CLAUDE_CONFIG_DIR IS explicitly configured via env var,
	// the command SHOULD include it (and use default "claude" command)
	if !strings.Contains(cmd, "CLAUDE_CONFIG_DIR=/tmp/test-claude-config") {
		t.Errorf("Should contain CLAUDE_CONFIG_DIR when explicitly configured, got: %s", cmd)
	}

	// Should use capture-resume pattern with explicit config
	if !strings.Contains(cmd, `-p "."`) {
		t.Errorf("Should use -p \".\" for session ID capture with explicit config, got: %s", cmd)
	}
	if !strings.Contains(cmd, `--resume "$session_id"`) {
		t.Errorf("Should use --resume flag with explicit config, got: %s", cmd)
	}
}

// TestBuildClaudeCommand_CustomAlias tests that custom command aliases (e.g., cdw, cdp)
// skip the CLAUDE_CONFIG_DIR prefix since the alias handles it
func TestBuildClaudeCommand_CustomAlias(t *testing.T) {
	// Create temp config with custom command
	origHome := os.Getenv("HOME")
	tmpDir := t.TempDir()
	os.Setenv("HOME", tmpDir)

	// Create ~/.agent-deck/config.toml with custom command
	configDir := filepath.Join(tmpDir, ".agent-deck")
	os.MkdirAll(configDir, 0755)
	configContent := `[claude]
command = "cdw"
config_dir = "~/.claude-work"
`
	os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(configContent), 0644)

	ClearUserConfigCache()
	defer func() {
		os.Setenv("HOME", origHome)
		ClearUserConfigCache()
	}()

	inst := NewInstanceWithTool("test", "/tmp/test", "claude")
	cmd := inst.buildClaudeCommand("claude")

	// Should use "cdw" instead of "claude"
	if !strings.Contains(cmd, "cdw -p") {
		t.Errorf("Should use custom command 'cdw', got: %s", cmd)
	}

	// Should NOT include CLAUDE_CONFIG_DIR prefix (alias handles it)
	if strings.Contains(cmd, "CLAUDE_CONFIG_DIR=") {
		t.Errorf("Should NOT include CLAUDE_CONFIG_DIR when using custom command alias, got: %s", cmd)
	}

	// Should still use capture-resume pattern
	if !strings.Contains(cmd, `--resume "$session_id"`) {
		t.Errorf("Should use --resume flag with custom alias, got: %s", cmd)
	}
}

// TestCreateForkedInstance_SessionIDPattern tests that forked sessions
// use the --session-id pattern with pre-generated UUID (avoids "." prompt)
func TestCreateForkedInstance_SessionIDPattern(t *testing.T) {
	inst := NewInstance("original", "/tmp/test")
	inst.ClaudeSessionID = "parent-abc-123"
	inst.ClaudeDetectedAt = time.Now()

	forked, cmd, err := inst.CreateForkedInstance("forked", "")
	if err != nil {
		t.Fatalf("CreateForkedInstance() failed: %v", err)
	}

	// Command SHOULD use --session-id pattern (not capture-resume with ".")
	if !strings.Contains(cmd, "uuidgen") {
		t.Errorf("Fork command should generate UUID with uuidgen, got: %s", cmd)
	}
	if !strings.Contains(cmd, "--resume parent-abc-123 --fork-session") {
		t.Errorf("Fork command should contain --resume with parent ID and --fork-session, got: %s", cmd)
	}
	if !strings.Contains(cmd, "--session-id") {
		t.Errorf("Fork command should use --session-id to specify UUID, got: %s", cmd)
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

	// Isolate from user's environment (don't pick up their config.toml)
	origHome := os.Getenv("HOME")
	tmpDir := t.TempDir()
	os.Setenv("HOME", tmpDir)
	ClearUserConfigCache()
	defer func() {
		os.Setenv("HOME", origHome)
		ClearUserConfigCache()
	}()

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

func TestInstance_RegenerateMCPConfig_ReturnsError(t *testing.T) {
	// This test verifies that regenerateMCPConfig() returns an error type
	// The actual error propagation from WriteMCPJsonFromConfig is tested
	// by verifying the function compiles with error return type and handles
	// the various early-return cases correctly.

	// Test case 1: No .mcp.json exists - returns nil (nothing to regenerate)
	inst := &Instance{
		ID:          "test-123",
		Title:       "Test Session",
		ProjectPath: "/nonexistent/path",
		Tool:        "claude",
	}
	err := inst.regenerateMCPConfig()
	if err != nil {
		t.Errorf("expected nil error for nonexistent path (no MCPs to regenerate), got: %v", err)
	}

	// Test case 2: Valid path with empty .mcp.json - returns nil
	tmpDir, err := os.MkdirTemp("", "agentdeck-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create an empty .mcp.json
	mcpPath := filepath.Join(tmpDir, ".mcp.json")
	if err := os.WriteFile(mcpPath, []byte(`{"mcpServers":{}}`), 0644); err != nil {
		t.Fatalf("failed to write .mcp.json: %v", err)
	}

	inst.ProjectPath = tmpDir
	err = inst.regenerateMCPConfig()
	if err != nil {
		t.Errorf("expected nil error for empty .mcp.json, got: %v", err)
	}

	// Test case 3: .mcp.json with MCPs but not in config.toml - returns nil
	// (Local() returns MCP names, but WriteMCPJsonFromConfig skips unknown MCPs)
	mcpJSON := `{"mcpServers":{"unknown-mcp":{"command":"echo","args":["hello"]}}}`
	if err := os.WriteFile(mcpPath, []byte(mcpJSON), 0644); err != nil {
		t.Fatalf("failed to write .mcp.json: %v", err)
	}

	err = inst.regenerateMCPConfig()
	// This returns nil because "unknown-mcp" is not in GetAvailableMCPs()
	// so WriteMCPJsonFromConfig writes an empty mcpServers, which succeeds
	if err != nil {
		t.Errorf("expected nil error for unknown MCP (not in config.toml), got: %v", err)
	}

	// Note: To test actual write failure would require:
	// 1. An MCP defined in config.toml
	// 2. That MCP also in .mcp.json
	// 3. Directory made read-only after .mcp.json creation
	// This is an integration test scenario rather than unit test
}

func TestInstance_RegenerateMCPConfig_WriteFailure(t *testing.T) {
	// Skip on non-Unix systems where permission changes might not work
	if os.Getenv("CI") != "" {
		t.Skip("Skipping permission-based test in CI")
	}

	// Create a temp directory
	tmpDir, err := os.MkdirTemp("", "agentdeck-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() {
		// Restore permissions before cleanup
		_ = os.Chmod(tmpDir, 0755)
		_ = os.RemoveAll(tmpDir)
	}()

	// Create .mcp.json with an MCP that exists in GetAvailableMCPs()
	// We'll use a real MCP name that might exist, or the test gracefully handles it
	mcpPath := filepath.Join(tmpDir, ".mcp.json")

	// First, check what MCPs are available
	availableMCPs := GetAvailableMCPs()
	if len(availableMCPs) == 0 {
		t.Skip("No MCPs configured in config.toml, skipping write failure test")
	}

	// Use the first available MCP
	var mcpName string
	for name := range availableMCPs {
		mcpName = name
		break
	}

	mcpJSON := `{"mcpServers":{"` + mcpName + `":{"command":"echo","args":["hello"]}}}`
	if err := os.WriteFile(mcpPath, []byte(mcpJSON), 0644); err != nil {
		t.Fatalf("failed to write .mcp.json: %v", err)
	}

	// Make directory read-only AFTER writing .mcp.json
	if err := os.Chmod(tmpDir, 0555); err != nil {
		t.Fatalf("failed to make directory read-only: %v", err)
	}

	inst := &Instance{
		ID:          "test-write-failure",
		Title:       "Test Write Failure",
		ProjectPath: tmpDir,
		Tool:        "claude",
	}

	// Clear MCP info cache to ensure fresh read
	ClearMCPCache(tmpDir)

	err = inst.regenerateMCPConfig()
	// We expect an error because the directory is read-only
	if err == nil {
		t.Error("expected error for read-only directory, got nil")
	} else {
		t.Logf("Got expected error: %v", err)
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

// TestInstance_Restart_SkipMCPRegenerate tests that SkipMCPRegenerate prevents double-write
// race condition when MCP dialog Apply() is followed immediately by Restart()
func TestInstance_Restart_SkipMCPRegenerate(t *testing.T) {
	// This test verifies that SkipMCPRegenerate prevents double-write
	inst := &Instance{
		ID:                "test-skip-123",
		Title:             "Test Skip Regen",
		ProjectPath:       t.TempDir(),
		Tool:              "claude",
		SkipMCPRegenerate: true,
	}

	// Write a marker file to detect if regenerateMCPConfig was called
	mcpFile := filepath.Join(inst.ProjectPath, ".mcp.json")
	originalContent := `{"mcpServers":{"marker":{"command":"test"}}}`
	if err := os.WriteFile(mcpFile, []byte(originalContent), 0644); err != nil {
		t.Fatalf("failed to write marker file: %v", err)
	}

	// After Restart with SkipMCPRegenerate=true, original content should be preserved
	// (In real scenario, Restart would fail because no tmux, but the flag check happens first)

	// Verify the flag is set
	if !inst.SkipMCPRegenerate {
		t.Error("SkipMCPRegenerate should be true")
	}

	// Call Restart - it will fail due to no tmux session, but we can verify
	// the flag was consumed by checking if it's now false
	_ = inst.Restart() // Will fail, but that's expected

	// Verify the flag was cleared after use
	if inst.SkipMCPRegenerate {
		t.Error("SkipMCPRegenerate should be false after Restart() consumes it")
	}

	// Verify the original content was preserved (regenerateMCPConfig was skipped)
	content, err := os.ReadFile(mcpFile)
	if err != nil {
		t.Fatalf("failed to read marker file: %v", err)
	}

	if string(content) != originalContent {
		t.Errorf("MCP config was modified when it should have been skipped.\nOriginal: %s\nActual: %s", originalContent, string(content))
	}
}

// TestInstance_WorktreeFields tests the worktree-related fields and IsWorktree method
func TestInstance_WorktreeFields(t *testing.T) {
	// Test 1: Instance with worktree fields set should report IsWorktree() = true
	inst := NewInstance("test", "/tmp/worktree-path")
	inst.WorktreePath = "/tmp/worktree-path"
	inst.WorktreeRepoRoot = "/tmp/original-repo"
	inst.WorktreeBranch = "feature-x"

	if !inst.IsWorktree() {
		t.Error("IsWorktree should return true when WorktreePath is set")
	}

	// Verify all fields are set correctly
	if inst.WorktreePath != "/tmp/worktree-path" {
		t.Errorf("WorktreePath = %q, want %q", inst.WorktreePath, "/tmp/worktree-path")
	}
	if inst.WorktreeRepoRoot != "/tmp/original-repo" {
		t.Errorf("WorktreeRepoRoot = %q, want %q", inst.WorktreeRepoRoot, "/tmp/original-repo")
	}
	if inst.WorktreeBranch != "feature-x" {
		t.Errorf("WorktreeBranch = %q, want %q", inst.WorktreeBranch, "feature-x")
	}

	// Test 2: Instance without worktree fields should report IsWorktree() = false
	inst2 := NewInstance("test2", "/tmp/regular-path")
	if inst2.IsWorktree() {
		t.Error("IsWorktree should return false when WorktreePath is empty")
	}

	// Test 3: Instance with only WorktreePath set (edge case)
	inst3 := NewInstance("test3", "/tmp/edge-case")
	inst3.WorktreePath = "/tmp/some-worktree"
	if !inst3.IsWorktree() {
		t.Error("IsWorktree should return true even when only WorktreePath is set")
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

func TestInstance_GetJSONLPath(t *testing.T) {
	t.Run("non-claude session returns empty", func(t *testing.T) {
		inst := NewInstance("test", "/tmp/project")
		inst.Tool = "shell"
		inst.ClaudeSessionID = "abc123"

		path := inst.GetJSONLPath()
		if path != "" {
			t.Errorf("GetJSONLPath() for non-claude should be empty, got: %s", path)
		}
	})

	t.Run("claude session without session ID returns empty", func(t *testing.T) {
		inst := NewInstance("test", "/tmp/project")
		inst.Tool = "claude"
		inst.ClaudeSessionID = ""

		path := inst.GetJSONLPath()
		if path != "" {
			t.Errorf("GetJSONLPath() without session ID should be empty, got: %s", path)
		}
	})

	t.Run("claude session with missing file returns empty", func(t *testing.T) {
		inst := NewInstance("test", "/tmp/project")
		inst.Tool = "claude"
		inst.ClaudeSessionID = "nonexistent-session-id"

		path := inst.GetJSONLPath()
		if path != "" {
			t.Errorf("GetJSONLPath() with missing file should be empty, got: %s", path)
		}
	})

	t.Run("claude session with existing file returns path", func(t *testing.T) {
		// Create a temp directory structure that mimics Claude's layout
		tempDir := t.TempDir()
		projectPath := filepath.Join(tempDir, "myproject")
		if err := os.MkdirAll(projectPath, 0755); err != nil {
			t.Fatalf("Failed to create project dir: %v", err)
		}

		// Resolve symlinks in project path (same as GetJSONLPath does)
		resolvedPath := projectPath
		if resolved, err := filepath.EvalSymlinks(projectPath); err == nil {
			resolvedPath = resolved
		}

		// Create mock Claude config structure using the RESOLVED path
		claudeDir := filepath.Join(tempDir, ".claude")
		projectDirName := strings.ReplaceAll(resolvedPath, "/", "-")
		claudeProjectDir := filepath.Join(claudeDir, "projects", projectDirName)
		if err := os.MkdirAll(claudeProjectDir, 0755); err != nil {
			t.Fatalf("Failed to create claude project dir: %v", err)
		}

		// Create a mock JSONL file
		sessionID := "test-session-123"
		jsonlFile := filepath.Join(claudeProjectDir, sessionID+".jsonl")
		if err := os.WriteFile(jsonlFile, []byte(`{"type":"assistant"}`), 0644); err != nil {
			t.Fatalf("Failed to create jsonl file: %v", err)
		}

		// Resolve claudeDir too for comparison
		resolvedClaudeDir := claudeDir
		if resolved, err := filepath.EvalSymlinks(claudeDir); err == nil {
			resolvedClaudeDir = resolved
		}

		// Override claude config dir for test - must be done BEFORE clearing cache
		oldClaudeConfigDir := os.Getenv("CLAUDE_CONFIG_DIR")
		os.Setenv("CLAUDE_CONFIG_DIR", resolvedClaudeDir)
		defer os.Setenv("CLAUDE_CONFIG_DIR", oldClaudeConfigDir)

		// Clear cached config so GetClaudeConfigDir picks up the new env var
		userConfigCacheMu.Lock()
		userConfigCache = nil
		userConfigCacheMu.Unlock()
		defer func() {
			userConfigCacheMu.Lock()
			userConfigCache = nil
			userConfigCacheMu.Unlock()
		}()

		// Verify GetClaudeConfigDir returns the right path
		configDir := GetClaudeConfigDir()
		t.Logf("GetClaudeConfigDir() = %s (expected: %s)", configDir, resolvedClaudeDir)

		inst := NewInstance("test", projectPath)
		inst.Tool = "claude"
		inst.ClaudeSessionID = sessionID

		path := inst.GetJSONLPath()
		t.Logf("GetJSONLPath() = %s", path)
		t.Logf("Expected jsonlFile = %s", jsonlFile)
		if path == "" {
			t.Errorf("GetJSONLPath() with existing file should return path")
		}
		// Compare resolved paths since EvalSymlinks might differ
		expectedResolved := jsonlFile
		if r, err := filepath.EvalSymlinks(jsonlFile); err == nil {
			expectedResolved = r
		}
		if path != expectedResolved {
			t.Errorf("GetJSONLPath() = %s, want %s", path, expectedResolved)
		}
	})
}
