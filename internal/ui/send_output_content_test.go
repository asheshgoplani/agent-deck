package ui

// Regression tests for issue #598 — cross-session `x` (send output) transferred
// unpredictable content because getSessionContent read from a stale
// ClaudeSessionID. These tests lock in the fix: the live CLAUDE_SESSION_ID
// from tmux env must take precedence over the stored ID when reading the
// last assistant response.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// setupClaudeConfigWithTwoJSONLs writes two JSONL files (stale + fresh) into
// a fake CLAUDE_CONFIG_DIR keyed to the given project path, returning a
// cleanup func that restores env + cache.
func setupClaudeConfigWithTwoJSONLs(t *testing.T, projectPath, staleID, staleText, freshID, freshText string) func() {
	t.Helper()

	tempDir := t.TempDir()
	claudeDir := filepath.Join(tempDir, ".claude")

	resolvedProject := projectPath
	if r, err := filepath.EvalSymlinks(projectPath); err == nil {
		resolvedProject = r
	}
	projectDirName := session.ConvertToClaudeDirName(resolvedProject)
	projectDir := filepath.Join(claudeDir, "projects", projectDirName)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Two minimal JSONL docs: each ending in an assistant message with the given text.
	write := func(id, text string) {
		line := `{"sessionId":"` + id + `","type":"assistant","timestamp":"2026-04-17T12:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":"` + text + `"}]}}` + "\n"
		f := filepath.Join(projectDir, id+".jsonl")
		if err := os.WriteFile(f, []byte(line), 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	write(staleID, staleText)
	write(freshID, freshText)

	resolvedClaudeDir := claudeDir
	if r, err := filepath.EvalSymlinks(claudeDir); err == nil {
		resolvedClaudeDir = r
	}

	oldEnv := os.Getenv("CLAUDE_CONFIG_DIR")
	os.Setenv("CLAUDE_CONFIG_DIR", resolvedClaudeDir)
	session.ClearUserConfigCache()

	return func() {
		os.Setenv("CLAUDE_CONFIG_DIR", oldEnv)
		session.ClearUserConfigCache()
	}
}

// TestGetSessionContentWithLive_PrefersFreshIDOverStoredStaleID is the primary
// regression test for #598. Pre-fix, getSessionContentWithLive does not exist
// and getSessionContent reads whatever JSONL the stale ClaudeSessionID points
// to. Post-fix, the live ID from tmux env wins.
func TestGetSessionContentWithLive_PrefersFreshIDOverStoredStaleID(t *testing.T) {
	tempProject := t.TempDir()
	cleanup := setupClaudeConfigWithTwoJSONLs(t, tempProject,
		"stale-uuid", "OLD_CONTENT_FROM_PRIOR_SESSION",
		"fresh-uuid", "FRESH_CONTENT_FROM_CURRENT_TURN")
	defer cleanup()

	inst := session.NewInstance("sess-A", tempProject)
	inst.Tool = "claude"
	inst.ClaudeSessionID = "stale-uuid" // stored ID is stale (post-resume scenario)

	content, err := getSessionContentWithLive(inst, "fresh-uuid")
	if err != nil {
		t.Fatalf("getSessionContentWithLive: unexpected error: %v", err)
	}
	if !strings.Contains(content, "FRESH_CONTENT_FROM_CURRENT_TURN") {
		t.Errorf("expected fresh content, got: %q", content)
	}
	if strings.Contains(content, "OLD_CONTENT_FROM_PRIOR_SESSION") {
		t.Errorf("leaked stale content from prior session: %q", content)
	}
	if inst.ClaudeSessionID != "fresh-uuid" {
		t.Errorf("ClaudeSessionID should be updated to fresh-uuid, got %q", inst.ClaudeSessionID)
	}
}

// TestGetSessionContentWithLive_KeepsStoredIDWhenLiveEmpty guards the
// back-compat path: if tmux env has no live ID (e.g. non-Claude tool, or
// env not yet populated), fall through to stored ID.
func TestGetSessionContentWithLive_KeepsStoredIDWhenLiveEmpty(t *testing.T) {
	tempProject := t.TempDir()
	cleanup := setupClaudeConfigWithTwoJSONLs(t, tempProject,
		"stored-uuid", "STORED_CONTENT",
		"other-uuid", "OTHER_CONTENT")
	defer cleanup()

	inst := session.NewInstance("sess-A", tempProject)
	inst.Tool = "claude"
	inst.ClaudeSessionID = "stored-uuid"

	content, err := getSessionContentWithLive(inst, "")
	if err != nil {
		t.Fatalf("getSessionContentWithLive: unexpected error: %v", err)
	}
	if !strings.Contains(content, "STORED_CONTENT") {
		t.Errorf("expected stored content, got: %q", content)
	}
	if inst.ClaudeSessionID != "stored-uuid" {
		t.Errorf("ClaudeSessionID mutated when live ID empty: got %q", inst.ClaudeSessionID)
	}
}

// TestGetSessionContentWithLive_NoOpForNonClaudeTool ensures non-Claude tools
// don't have their (unrelated) ClaudeSessionID changed and that a missing
// tmux session surfaces the canonical error.
func TestGetSessionContentWithLive_NoOpForNonClaudeTool(t *testing.T) {
	inst := session.NewInstance("sess-shell", t.TempDir())
	inst.Tool = "shell"
	inst.ClaudeSessionID = "unused-id"

	// tmuxSession is nil here; fallback path should yield the canonical error.
	_, err := getSessionContentWithLive(inst, "irrelevant-live-id")
	if err == nil {
		t.Fatalf("expected error when tool=shell and tmuxSession=nil, got nil")
	}
	if inst.ClaudeSessionID != "unused-id" {
		t.Errorf("ClaudeSessionID mutated for non-claude tool: got %q", inst.ClaudeSessionID)
	}
}
