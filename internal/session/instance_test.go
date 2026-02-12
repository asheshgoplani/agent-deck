package session

import (
	"fmt"
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
	skipIfNoTmuxServer(t)

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

	// With session ID, Fork returns uuidgen + --session-id command
	inst.ClaudeSessionID = "abc-123"
	inst.ClaudeDetectedAt = time.Now()
	cmd, err := inst.Fork("forked-test", "")
	if err != nil {
		t.Errorf("Fork() failed: %v", err)
	}

	// Command should use uuidgen + --session-id pattern (instant, no API call)
	// When not explicitly configured, CLAUDE_CONFIG_DIR should NOT be set
	// (allows shell environment to take precedence)
	if strings.Contains(cmd, "CLAUDE_CONFIG_DIR=") {
		t.Errorf("Fork() should NOT set CLAUDE_CONFIG_DIR when not explicitly configured, got: %s", cmd)
	}
	// Step 1: Pre-generate UUID with uuidgen
	if !strings.Contains(cmd, "uuidgen") {
		t.Errorf("Fork() should use uuidgen for session ID, got: %s", cmd)
	}
	// Should NOT use -p "." or jq (old capture-resume pattern)
	if strings.Contains(cmd, `-p "."`) {
		t.Errorf("Fork() should NOT use -p \".\" (old pattern), got: %s", cmd)
	}
	if strings.Contains(cmd, "jq") {
		t.Errorf("Fork() should NOT use jq (old pattern), got: %s", cmd)
	}
	// Step 2: Use --session-id flag with pre-generated UUID
	if !strings.Contains(cmd, `--session-id "$session_id"`) {
		t.Errorf("Fork() should use --session-id flag, got: %s", cmd)
	}
	// Step 3: Include --resume with parent ID and --fork-session
	if !strings.Contains(cmd, "--resume abc-123 --fork-session") {
		t.Errorf("Fork() should include resume and fork-session flags, got: %s", cmd)
	}
	// Step 4: Store session ID in tmux environment
	if !strings.Contains(cmd, "tmux set-environment CLAUDE_SESSION_ID") {
		t.Errorf("Fork() should store session ID in tmux env, got: %s", cmd)
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

	// Should use pre-generated UUID pattern with uuidgen
	if !strings.Contains(cmd, "uuidgen") {
		t.Errorf("Should use uuidgen for UUID generation, got: %s", cmd)
	}

	// Should store session ID in tmux environment
	if !strings.Contains(cmd, "tmux set-environment CLAUDE_SESSION_ID") {
		t.Errorf("Should store session ID in tmux env, got: %s", cmd)
	}

	// Should use --session-id flag for new sessions
	if !strings.Contains(cmd, `--session-id "$session_id"`) {
		t.Errorf("Should use --session-id flag for new sessions, got: %s", cmd)
	}

	// Should NOT use capture-resume pattern anymore
	if strings.Contains(cmd, `-p "."`) {
		t.Errorf("Should NOT use -p \".\" capture pattern anymore, got: %s", cmd)
	}
	if strings.Contains(cmd, "--output-format json") {
		t.Errorf("Should NOT use --output-format json anymore, got: %s", cmd)
	}
	if strings.Contains(cmd, "jq") {
		t.Errorf("Should NOT use jq anymore, got: %s", cmd)
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

	// Should use --session-id pattern with explicit config
	if !strings.Contains(cmd, `--session-id "$session_id"`) {
		t.Errorf("Should use --session-id flag with explicit config, got: %s", cmd)
	}
	if !strings.Contains(cmd, "uuidgen") {
		t.Errorf("Should use uuidgen with explicit config, got: %s", cmd)
	}
}

// TestBuildClaudeCommand_CustomAlias tests that capture-resume commands always use
// "claude" binary + CLAUDE_CONFIG_DIR, NOT the custom alias (aliases don't work in bash -c)
func TestBuildClaudeCommand_CustomAlias(t *testing.T) {
	// Create temp config with custom command
	origHome := os.Getenv("HOME")
	tmpDir := t.TempDir()
	os.Setenv("HOME", tmpDir)

	// Create ~/.agent-deck/config.toml with custom command
	configDir := filepath.Join(tmpDir, ".agent-deck")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}
	configContent := `[claude]
command = "cdw"
config_dir = "~/.claude-work"
`
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	ClearUserConfigCache()
	defer func() {
		os.Setenv("HOME", origHome)
		ClearUserConfigCache()
	}()

	inst := NewInstanceWithTool("test", "/tmp/test", "claude")
	cmd := inst.buildClaudeCommand("claude")

	// Should use "claude" binary (NOT "cdw" alias) for capture-resume commands
	// Reason: Commands with $(...) get wrapped in `bash -c` for fish compatibility (#47),
	// and shell aliases are not available in non-interactive bash shells
	if strings.Contains(cmd, "cdw") {
		t.Errorf("Should NOT use alias 'cdw' in capture-resume command (aliases don't work in bash -c), got: %s", cmd)
	}

	// Should include CLAUDE_CONFIG_DIR since config_dir is explicitly set
	if !strings.Contains(cmd, "CLAUDE_CONFIG_DIR=") {
		t.Errorf("Should include CLAUDE_CONFIG_DIR for capture-resume commands, got: %s", cmd)
	}

	// Should use --session-id pattern (pre-generated UUID, instant start)
	if !strings.Contains(cmd, `--session-id "$session_id"`) {
		t.Errorf("Should use --session-id flag for instant start pattern, got: %s", cmd)
	}
}

// TestBuildClaudeCommand_SubagentAddDir tests that subagents get --add-dir
// for access to parent's project directory (for worktrees, etc.)
func TestBuildClaudeCommand_SubagentAddDir(t *testing.T) {
	// Isolate from user's environment
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

	// Create a subagent with parent project path
	inst := NewInstanceWithTool("subagent", "/tmp/subagent-workdir", "claude")
	inst.SetParentWithPath("parent-id-123", "/home/user/projects/main-project")

	cmd := inst.buildClaudeCommand("claude")

	// Should contain --add-dir with parent's project path
	if !strings.Contains(cmd, "--add-dir /home/user/projects/main-project") {
		t.Errorf("Subagent command should contain --add-dir with parent path, got: %s", cmd)
	}

	// Without parent, should NOT have --add-dir
	instNoParent := NewInstanceWithTool("standalone", "/tmp/standalone", "claude")
	cmdNoParent := instNoParent.buildClaudeCommand("claude")

	if strings.Contains(cmdNoParent, "--add-dir") {
		t.Errorf("Standalone agent should NOT have --add-dir, got: %s", cmdNoParent)
	}
}

// TestCreateForkedInstance_SessionIDPattern tests that forked sessions
// use pre-generated UUID + --session-id pattern for instant start
func TestCreateForkedInstance_SessionIDPattern(t *testing.T) {
	inst := NewInstance("original", "/tmp/test")
	inst.ClaudeSessionID = "parent-abc-123"
	inst.ClaudeDetectedAt = time.Now()

	forked, cmd, err := inst.CreateForkedInstance("forked", "")
	if err != nil {
		t.Fatalf("CreateForkedInstance() failed: %v", err)
	}

	// Command SHOULD use uuidgen + --session-id pattern (instant, no API call)
	// Step 1: Pre-generate UUID with uuidgen
	if !strings.Contains(cmd, "uuidgen") {
		t.Errorf("Fork command should use uuidgen for session ID, got: %s", cmd)
	}
	// Should NOT use -p "." or jq (old capture-resume pattern)
	if strings.Contains(cmd, `-p "."`) {
		t.Errorf("Fork command should NOT use -p \".\" (old pattern), got: %s", cmd)
	}
	if strings.Contains(cmd, "jq") {
		t.Errorf("Fork command should NOT use jq (old pattern), got: %s", cmd)
	}
	// Step 2: Use --session-id flag with pre-generated UUID
	if !strings.Contains(cmd, `--session-id "$session_id"`) {
		t.Errorf("Fork command should use --session-id flag, got: %s", cmd)
	}
	// Step 3: Include --resume with parent ID and --fork-session
	if !strings.Contains(cmd, "--resume parent-abc-123 --fork-session") {
		t.Errorf("Fork command should contain --resume with parent ID and --fork-session, got: %s", cmd)
	}
	// Step 4: Store session ID in tmux environment
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
	skipIfNoTmuxServer(t)

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
	skipIfNoTmuxServer(t)

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

// TestSyncClaudeSessionFromDisk_PicksUpNewerSession verifies that when a newer
// session file appears on disk (e.g., after /clear), syncClaudeSessionFromDisk
// updates the instance's ClaudeSessionID.
func TestSyncClaudeSessionFromDisk_PicksUpNewerSession(t *testing.T) {
	configDir := t.TempDir()
	origConfigDir := os.Getenv("CLAUDE_CONFIG_DIR")
	os.Setenv("CLAUDE_CONFIG_DIR", configDir)
	defer func() {
		if origConfigDir != "" {
			os.Setenv("CLAUDE_CONFIG_DIR", origConfigDir)
		} else {
			os.Unsetenv("CLAUDE_CONFIG_DIR")
		}
	}()

	projectPath := "/Users/test/sync-project"
	projectDirName := ConvertToClaudeDirName(projectPath)
	projectDir := filepath.Join(configDir, "projects", projectDirName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatal(err)
	}

	oldSessionID := "11111111-1111-1111-1111-111111111111"
	newSessionID := "22222222-2222-2222-2222-222222222222"

	oldPath := filepath.Join(projectDir, oldSessionID+".jsonl")
	if err := os.WriteFile(oldPath, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	pastTime := time.Now().Add(-30 * time.Second)
	if err := os.Chtimes(oldPath, pastTime, pastTime); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(projectDir, newSessionID+".jsonl"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	inst := NewInstanceWithTool("sync-test", projectPath, "claude")
	inst.ClaudeSessionID = oldSessionID
	inst.ClaudeDetectedAt = time.Now().Add(-1 * time.Minute)

	inst.syncClaudeSessionFromDisk()

	if inst.ClaudeSessionID != newSessionID {
		t.Errorf("ClaudeSessionID = %q, want %q (newer session from disk)", inst.ClaudeSessionID, newSessionID)
	}
	if inst.ClaudeDetectedAt.IsZero() {
		t.Error("ClaudeDetectedAt should be set after sync")
	}
}

// TestSyncClaudeSessionFromDisk_NoChangeWhenCurrent verifies no update when
// the current session is already the most recent file on disk.
func TestSyncClaudeSessionFromDisk_NoChangeWhenCurrent(t *testing.T) {
	configDir := t.TempDir()
	origConfigDir := os.Getenv("CLAUDE_CONFIG_DIR")
	os.Setenv("CLAUDE_CONFIG_DIR", configDir)
	defer func() {
		if origConfigDir != "" {
			os.Setenv("CLAUDE_CONFIG_DIR", origConfigDir)
		} else {
			os.Unsetenv("CLAUDE_CONFIG_DIR")
		}
	}()

	projectPath := "/Users/test/nochange-project"
	projectDirName := ConvertToClaudeDirName(projectPath)
	projectDir := filepath.Join(configDir, "projects", projectDirName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatal(err)
	}

	currentID := "33333333-3333-3333-3333-333333333333"
	if err := os.WriteFile(filepath.Join(projectDir, currentID+".jsonl"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	originalDetectedAt := time.Now().Add(-1 * time.Minute)
	inst := NewInstanceWithTool("nochange-test", projectPath, "claude")
	inst.ClaudeSessionID = currentID
	inst.ClaudeDetectedAt = originalDetectedAt

	inst.syncClaudeSessionFromDisk()

	if inst.ClaudeSessionID != currentID {
		t.Errorf("ClaudeSessionID changed to %q, should remain %q", inst.ClaudeSessionID, currentID)
	}
	if inst.ClaudeDetectedAt != originalDetectedAt {
		t.Error("ClaudeDetectedAt should not change when session is already current")
	}
}

// TestSyncClaudeSessionFromDisk_IgnoresAgentFiles verifies that agent-*.jsonl files
// are not picked up as the active session.
func TestSyncClaudeSessionFromDisk_IgnoresAgentFiles(t *testing.T) {
	configDir := t.TempDir()
	origConfigDir := os.Getenv("CLAUDE_CONFIG_DIR")
	os.Setenv("CLAUDE_CONFIG_DIR", configDir)
	defer func() {
		if origConfigDir != "" {
			os.Setenv("CLAUDE_CONFIG_DIR", origConfigDir)
		} else {
			os.Unsetenv("CLAUDE_CONFIG_DIR")
		}
	}()

	projectPath := "/Users/test/agent-files-project"
	projectDirName := ConvertToClaudeDirName(projectPath)
	projectDir := filepath.Join(configDir, "projects", projectDirName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatal(err)
	}

	realSession := "abcd1234-abcd-abcd-abcd-abcdabcdabcd"
	agentSession := "agent-eeee5555-eeee-eeee-eeee-eeeeeeeeeeee"

	realPath := filepath.Join(projectDir, realSession+".jsonl")
	if err := os.WriteFile(realPath, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(realPath, time.Now().Add(-10*time.Second), time.Now().Add(-10*time.Second)); err != nil {
		t.Fatal(err)
	}

	agentPath := filepath.Join(projectDir, agentSession+".jsonl")
	if err := os.WriteFile(agentPath, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	inst := NewInstanceWithTool("agent-files-test", projectPath, "claude")
	inst.ClaudeSessionID = realSession
	inst.ClaudeDetectedAt = time.Now().Add(-1 * time.Minute)

	inst.syncClaudeSessionFromDisk()

	if inst.ClaudeSessionID != realSession {
		t.Errorf("ClaudeSessionID = %q, want %q (agent files should be ignored)", inst.ClaudeSessionID, realSession)
	}
}

// TestSyncClaudeSessionFromDisk_SkipsNonClaude verifies non-claude tools are no-ops.
func TestSyncClaudeSessionFromDisk_SkipsNonClaude(t *testing.T) {
	inst := NewInstanceWithTool("shell-test", "/tmp", "shell")
	inst.ClaudeSessionID = "should-not-change"
	inst.syncClaudeSessionFromDisk()
	if inst.ClaudeSessionID != "should-not-change" {
		t.Error("syncClaudeSessionFromDisk should be a no-op for non-claude tools")
	}
}

// TestInstance_UpdateGeminiSession_UsesLatestFromFilesystem verifies that
// UpdateGeminiSession ALWAYS scans filesystem for the most recent session,
// even if we already have a cached session ID.
// This is the Krudony fix: prevents stale session resume when user starts a NEW session.
func TestInstance_UpdateGeminiSession_UsesLatestFromFilesystem(t *testing.T) {
	// Create temp directory and redirect Gemini config
	tmpDir := t.TempDir()
	geminiConfigDirOverride = tmpDir
	defer func() { geminiConfigDirOverride = "" }()

	// Use a stable project path
	projectPath := "/Users/test/my-project"

	// Create instance with known session ID (simulating loaded from storage)
	inst := NewInstanceWithTool("latest-gemini-test", projectPath, "gemini")
	existingID := "old-cached-session-id"
	inst.GeminiSessionID = existingID
	oldDetectedAt := time.Now().Add(-10 * time.Minute)
	inst.GeminiDetectedAt = oldDetectedAt

	// Call UpdateGeminiSession - no sessions on filesystem, should keep existing
	inst.UpdateGeminiSession(nil)

	// With no sessions on filesystem, existing ID is preserved as fallback
	if inst.GeminiSessionID != existingID {
		t.Errorf("GeminiSessionID should preserve cached ID when no sessions on filesystem, got %q", inst.GeminiSessionID)
	}

	// Now create a "newer" session file on filesystem using correct directory structure
	sessionsDir := GetGeminiSessionsDir(projectPath)
	if err := os.MkdirAll(sessionsDir, 0755); err != nil {
		t.Fatalf("Failed to create sessions dir: %v", err)
	}

	newSessionID := "new-sess-from-filesystem-abc123"
	sessionFile := filepath.Join(sessionsDir, "session-2025-01-25T10-00-"+newSessionID[:8]+".json")
	sessionContent := fmt.Sprintf(`{
		"sessionId": %q,
		"startTime": "2025-01-25T10:00:00.000Z",
		"lastUpdated": "2025-01-25T10:30:00.000Z",
		"messages": [{"id": "1", "type": "user", "content": "hello"}]
	}`, newSessionID)
	if err := os.WriteFile(sessionFile, []byte(sessionContent), 0644); err != nil {
		t.Fatalf("Failed to write session file: %v", err)
	}

	// Call UpdateGeminiSession again - should pick up the new session
	inst.UpdateGeminiSession(nil)

	// Krudony fix: filesystem session should override cached ID
	if inst.GeminiSessionID != newSessionID {
		t.Errorf("GeminiSessionID should be updated to filesystem session %q, got %q", newSessionID, inst.GeminiSessionID)
	}

	// Timestamp should be updated
	if !inst.GeminiDetectedAt.After(oldDetectedAt) {
		t.Error("GeminiDetectedAt should be updated when new session found")
	}
}

func TestInstance_Restart_ResumesClaudeSession(t *testing.T) {
	skipIfNoTmuxServer(t)

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
	skipIfNoTmuxServer(t)
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

	// Without session ID, should start Gemini fresh (no capture-resume for Gemini)
	// NOTE: Gemini does NOT use capture-resume for new sessions because
	// "gemini --output-format json ." would hang processing the prompt
	cmd := inst.buildGeminiCommand("gemini")

	// Should set YOLO mode env and start fresh
	if !strings.Contains(cmd, "GEMINI_YOLO_MODE") {
		t.Error("Should set GEMINI_YOLO_MODE env var")
	}
	if !strings.Contains(cmd, "gemini") {
		t.Errorf("Should start gemini fresh for new session, got %q", cmd)
	}
	// Should NOT use capture-resume pattern for new sessions
	if strings.Contains(cmd, "--output-format json") {
		t.Error("Should NOT use capture-resume pattern for new Gemini sessions")
	}
	if strings.Contains(cmd, "--resume") && inst.GeminiSessionID == "" {
		t.Error("Should NOT use --resume for new sessions without session ID")
	}

	// With session ID, should use simple resume with YOLO env var
	inst.GeminiSessionID = "abc-123-def"
	cmd = inst.buildGeminiCommand("gemini")
	if !strings.Contains(cmd, "gemini --resume abc-123-def") {
		t.Errorf("buildGeminiCommand('gemini') should contain resume command, got %q", cmd)
	}
	if !strings.Contains(cmd, "GEMINI_YOLO_MODE") {
		t.Errorf("buildGeminiCommand('gemini') should set GEMINI_YOLO_MODE env, got %q", cmd)
	}
	// Resume should set GEMINI_SESSION_ID in tmux env
	if !strings.Contains(cmd, "tmux set-environment GEMINI_SESSION_ID abc-123-def") {
		t.Errorf("buildGeminiCommand('gemini') should set GEMINI_SESSION_ID in tmux env on resume, got %q", cmd)
	}

	// With explicit model set, should include --model flag
	inst.GeminiModel = "gemini-2.5-pro"
	cmd = inst.buildGeminiCommand("gemini")
	if !strings.Contains(cmd, "--model gemini-2.5-pro") {
		t.Errorf("buildGeminiCommand('gemini') should include --model flag, got %q", cmd)
	}

	// Without session ID but with model, should include --model flag
	inst.GeminiSessionID = ""
	cmd = inst.buildGeminiCommand("gemini")
	if !strings.Contains(cmd, "--model gemini-2.5-pro") {
		t.Errorf("buildGeminiCommand('gemini') with model should include --model flag, got %q", cmd)
	}
	if !strings.Contains(cmd, "gemini") {
		t.Errorf("buildGeminiCommand('gemini') without session ID should start fresh, got %q", cmd)
	}

	// Custom commands should pass through (e.g., existing --resume commands)
	customCmd := "gemini --some-flag"
	cmd = inst.buildGeminiCommand(customCmd)
	if !strings.Contains(cmd, customCmd) {
		t.Errorf("buildGeminiCommand(custom) should contain %q, got %q", customCmd, cmd)
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
	inst := NewInstanceWithTool("test", "/tmp/test", "gemini")
	inst.GeminiSessionID = "abc-123-def"
	inst.GeminiDetectedAt = time.Now()

	if inst.CanFork() {
		t.Error("CanFork() should be false for Gemini (not supported by Gemini CLI)")
	}

	inst.ClaudeSessionID = "claude-session-xyz"
	inst.ClaudeDetectedAt = time.Now()

	if inst.CanFork() {
		t.Error("CanFork() should be false for Gemini tool even with ClaudeSessionID set")
	}
}

func TestInstance_CanFork_OpenCode(t *testing.T) {
	inst := NewInstanceWithTool("test", "/tmp/test", "opencode")

	if inst.CanFork() {
		t.Error("CanFork() should be false without OpenCodeSessionID")
	}

	inst.OpenCodeSessionID = "ses_abc123def456"
	inst.OpenCodeDetectedAt = time.Now()
	if !inst.CanFork() {
		t.Error("CanFork() should be true with recent OpenCodeSessionID")
	}

	inst.OpenCodeDetectedAt = time.Now().Add(-10 * time.Minute)
	if inst.CanFork() {
		t.Error("CanFork() should be false with stale OpenCodeSessionID")
	}
}

func TestInstance_ForkOpenCode(t *testing.T) {
	inst := NewInstanceWithTool("test", "/tmp/test", "opencode")

	_, err := inst.ForkOpenCode("forked-test", "")
	if err == nil {
		t.Error("ForkOpenCode() should fail without OpenCodeSessionID")
	}

	inst.OpenCodeSessionID = "ses_abc123def456ffe1234567890abcd"
	inst.OpenCodeDetectedAt = time.Now()
	cmd, err := inst.ForkOpenCode("forked-test", "")
	if err != nil {
		t.Errorf("ForkOpenCode() failed: %v", err)
	}

	// cmd is "bash '<script_path>'" - extract and read the script file
	if !strings.HasPrefix(cmd, "bash '") {
		t.Fatalf("ForkOpenCode() should return bash command, got: %s", cmd)
	}
	scriptPath := strings.TrimPrefix(cmd, "bash '")
	scriptPath = strings.TrimSuffix(scriptPath, "'")
	scriptContent, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("Failed to read fork script at %s: %v", scriptPath, err)
	}
	script := string(scriptContent)

	if !strings.Contains(script, "opencode export") {
		t.Errorf("Fork script should use opencode export, got: %s", script)
	}
	if !strings.Contains(script, "opencode import") {
		t.Errorf("Fork script should use opencode import, got: %s", script)
	}
	if !strings.Contains(script, "ses_abc123def456ffe1234567890abcd") {
		t.Errorf("Fork script should include original session ID, got: %s", script)
	}
	if !strings.Contains(script, "tmux set-environment OPENCODE_SESSION_ID") {
		t.Errorf("Fork script should set tmux environment, got: %s", script)
	}
}

func TestInstance_CreateForkedOpenCodeInstance(t *testing.T) {
	inst := NewInstanceWithTool("test", "/tmp/test", "opencode")
	inst.OpenCodeSessionID = "ses_abc123def456ffe1234567890abcd"
	inst.OpenCodeDetectedAt = time.Now()
	inst.GroupPath = "projects/ai"

	forked, cmd, err := inst.CreateForkedOpenCodeInstance("forked-test", "")
	if err != nil {
		t.Fatalf("CreateForkedOpenCodeInstance() failed: %v", err)
	}

	if forked.Title != "forked-test" {
		t.Errorf("Forked instance title = %q, want %q", forked.Title, "forked-test")
	}
	if forked.Tool != "opencode" {
		t.Errorf("Forked instance tool = %q, want %q", forked.Tool, "opencode")
	}
	if forked.GroupPath != "projects/ai" {
		t.Errorf("Forked instance GroupPath = %q, want %q", forked.GroupPath, "projects/ai")
	}
	if forked.ProjectPath != "/tmp/test" {
		t.Errorf("Forked instance ProjectPath = %q, want %q", forked.ProjectPath, "/tmp/test")
	}
	if cmd == "" {
		t.Error("CreateForkedOpenCodeInstance() returned empty command")
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
	skipIfNoTmuxServer(t)

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
		ID:               "test-123",
		Title:            "test-session",
		ProjectPath:      "/tmp/Test Path With Spaces",
		Tool:             "claude",
		ClaudeSessionID:  "session-abc-123",
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
		ID:               "test-456",
		Title:            "test-session",
		ProjectPath:      "/tmp/test",
		Tool:             "claude",
		ClaudeSessionID:  "session-xyz-789",
		ClaudeDetectedAt: time.Now(),
	}

	// Test with dangerous_mode = false
	t.Run("dangerous_mode=false", func(t *testing.T) {
		// Set up config with dangerous_mode = false
		dangerousModeFalse := false
		userConfigCacheMu.Lock()
		userConfigCache = &UserConfig{
			Claude: ClaudeSettings{
				DangerousMode: &dangerousModeFalse,
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
		dangerousModeTrue := true
		userConfigCacheMu.Lock()
		userConfigCache = &UserConfig{
			Claude: ClaudeSettings{
				DangerousMode: &dangerousModeTrue,
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
		projectDirName := ConvertToClaudeDirName(resolvedPath)
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

func TestSessionHasConversationData(t *testing.T) {
	// Create temp directory structure
	tmpDir := t.TempDir()
	projectPath := "/test/project"
	encodedPath := "-test-project"

	projectsDir := filepath.Join(tmpDir, "projects", encodedPath)
	_ = os.MkdirAll(projectsDir, 0755)

	// Override config dir for test
	origConfigDir := os.Getenv("CLAUDE_CONFIG_DIR")
	os.Setenv("CLAUDE_CONFIG_DIR", tmpDir)
	defer os.Setenv("CLAUDE_CONFIG_DIR", origConfigDir)
	ClearUserConfigCache()
	defer ClearUserConfigCache()

	t.Run("file with sessionId returns true", func(t *testing.T) {
		sessionID := "has-session-id"
		filePath := filepath.Join(projectsDir, sessionID+".jsonl")
		content := `{"type":"summary","leafUuid":"abc"}
{"type":"queue-operation","sessionId":"has-session-id","timestamp":"2026-01-01"}
{"type":"user","sessionId":"has-session-id","text":"hello"}`
		_ = os.WriteFile(filePath, []byte(content), 0644)

		if !sessionHasConversationData(sessionID, projectPath) {
			t.Error("Expected true for file with sessionId")
		}
	})

	t.Run("file without sessionId returns false", func(t *testing.T) {
		sessionID := "no-session-id"
		filePath := filepath.Join(projectsDir, sessionID+".jsonl")
		content := `{"type":"summary","leafUuid":"abc"}
{"type":"summary","leafUuid":"def"}`
		_ = os.WriteFile(filePath, []byte(content), 0644)

		if sessionHasConversationData(sessionID, projectPath) {
			t.Error("Expected false for file without sessionId")
		}
	})

	t.Run("missing file returns false (use --session-id)", func(t *testing.T) {
		if sessionHasConversationData("nonexistent-file", projectPath) {
			t.Error("Expected false for missing file (nothing to resume)")
		}
	})
}

// TestRegenerate_MCPConfig_InvalidatesCache verifies that regenerateMCPConfig()
// clears the MCP cache before reading, so externally-modified .mcp.json files
// are picked up instead of stale cached data (fixes #97).
func TestRegenerate_MCPConfig_InvalidatesCache(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agentdeck-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)
	defer ClearMCPCache(tmpDir) // Clean up cache after test

	mcpPath := filepath.Join(tmpDir, ".mcp.json")

	// Step 1: Write initial .mcp.json with one MCP
	initialJSON := `{"mcpServers":{"mcp-a":{"command":"echo","args":["a"]}}}`
	if err := os.WriteFile(mcpPath, []byte(initialJSON), 0644); err != nil {
		t.Fatalf("failed to write initial .mcp.json: %v", err)
	}

	// Step 2: Prime the cache by calling GetMCPInfo
	info1 := GetMCPInfo(tmpDir)
	if info1 == nil {
		t.Fatal("expected non-nil MCPInfo after priming cache")
	}
	localNames1 := info1.Local()
	if len(localNames1) != 1 || localNames1[0] != "mcp-a" {
		t.Fatalf("expected cache to contain [mcp-a], got %v", localNames1)
	}

	// Step 3: Externally modify .mcp.json to add a second MCP (within 30s cache window)
	updatedJSON := `{"mcpServers":{"mcp-a":{"command":"echo","args":["a"]},"mcp-b":{"command":"echo","args":["b"]}}}`
	if err := os.WriteFile(mcpPath, []byte(updatedJSON), 0644); err != nil {
		t.Fatalf("failed to write updated .mcp.json: %v", err)
	}

	// Step 4: Call regenerateMCPConfig (should clear cache before GetMCPInfo)
	inst := &Instance{
		ID:          "test-cache-invalidation",
		Title:       "Cache Test",
		ProjectPath: tmpDir,
		Tool:        "claude",
	}
	_ = inst.regenerateMCPConfig()

	// Step 5: Verify the cache was refreshed with disk data during regeneration.
	// GetMCPInfo returns the cache populated inside regenerateMCPConfig,
	// which read fresh data after clearing the cache.
	info2 := GetMCPInfo(tmpDir)
	if info2 == nil {
		t.Fatal("expected non-nil MCPInfo after regeneration")
	}
	localNames2 := info2.Local()

	// With the fix: cache was cleared, so regenerateMCPConfig read both mcp-a and mcp-b
	// Without the fix: cache still has stale data with only mcp-a
	foundB := false
	for _, name := range localNames2 {
		if name == "mcp-b" {
			foundB = true
			break
		}
	}
	if !foundB {
		t.Errorf("expected cache to contain 'mcp-b' after regeneration "+
			"(cache should have been invalidated), got: %v", localNames2)
	}
}

func TestBuildClaudeExtraFlags_DangerousMode(t *testing.T) {
	inst := &Instance{Tool: "claude"}
	opts := &ClaudeOptions{SkipPermissions: true}
	flags := inst.buildClaudeExtraFlags(opts)

	if !strings.Contains(flags, "--dangerously-skip-permissions") {
		t.Errorf("expected --dangerously-skip-permissions, got %q", flags)
	}
}

func TestBuildClaudeExtraFlags_AllowDangerousMode(t *testing.T) {
	inst := &Instance{Tool: "claude"}
	opts := &ClaudeOptions{SkipPermissions: false, AllowSkipPermissions: true}
	flags := inst.buildClaudeExtraFlags(opts)

	if !strings.Contains(flags, "--allow-dangerously-skip-permissions") {
		t.Errorf("expected --allow-dangerously-skip-permissions, got %q", flags)
	}
	if strings.Contains(flags, " --dangerously-skip-permissions") {
		t.Errorf("should not contain --dangerously-skip-permissions, got %q", flags)
	}
}

func TestBuildClaudeExtraFlags_DangerousWinsOverAllow(t *testing.T) {
	inst := &Instance{Tool: "claude"}
	opts := &ClaudeOptions{SkipPermissions: true, AllowSkipPermissions: true}
	flags := inst.buildClaudeExtraFlags(opts)

	if !strings.Contains(flags, "--dangerously-skip-permissions") {
		t.Errorf("expected --dangerously-skip-permissions, got %q", flags)
	}
	if strings.Contains(flags, "--allow-dangerously-skip-permissions") {
		t.Errorf("dangerous_mode should take precedence, got %q", flags)
	}
}

func TestBuildClaudeExtraFlags_NilOpts(t *testing.T) {
	inst := &Instance{Tool: "claude"}
	flags := inst.buildClaudeExtraFlags(nil)

	// With nil opts, no permission flags should be added
	if strings.Contains(flags, "--dangerously-skip-permissions") {
		t.Errorf("nil opts should not add permission flags, got %q", flags)
	}
	if strings.Contains(flags, "--allow-dangerously-skip-permissions") {
		t.Errorf("nil opts should not add permission flags, got %q", flags)
	}
}
