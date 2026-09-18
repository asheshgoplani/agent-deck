package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const issue2299OldReply = "OLD SESSION RESPONSE"

type issue2299Fixture struct {
	projectPath string
	oldID       string
	newID       string
	old         *Instance
}

func setupIssue2299(t *testing.T) issue2299Fixture {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })

	projectPath := filepath.Join(tmpHome, "shared-project")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir projectPath: %v", err)
	}
	resolvedProject := projectPath
	if r, err := filepath.EvalSymlinks(projectPath); err == nil {
		resolvedProject = r
	}
	projectsDir := filepath.Join(tmpHome, ".claude", "projects", ConvertToClaudeDirName(resolvedProject))
	if err := os.MkdirAll(projectsDir, 0o755); err != nil {
		t.Fatalf("mkdir projects dir: %v", err)
	}

	const (
		oldID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		newID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	)
	oldBody := fmt.Sprintf(
		`{"type":"user","cwd":%q,"sessionId":%q,"message":{"role":"user","content":"hi"}}`+"\n"+
			`{"type":"assistant","cwd":%q,"sessionId":%q,"timestamp":"2026-05-31T22:05:00Z","message":{"role":"assistant","content":[{"type":"text","text":%q}]}}`+"\n",
		resolvedProject, oldID, resolvedProject, oldID, issue2299OldReply,
	)
	writeTranscript(t, projectsDir, oldID, oldBody)

	old := NewInstance("old-session", projectPath)
	old.Tool = "claude"
	old.ClaudeSessionID = oldID
	old.Status = StatusRunning

	return issue2299Fixture{
		projectPath: projectPath,
		oldID:       oldID,
		newID:       newID,
		old:         old,
	}
}

func assertIssue2299NoSiblingLeak(t *testing.T, target *Instance, peers []*Instance, wantID string) {
	t.Helper()
	resp, err := target.GetLastResponseBestEffortChecked(peers)
	if err != nil {
		t.Fatalf("GetLastResponseBestEffortChecked returned error: %v", err)
	}
	if resp == nil {
		t.Fatal("GetLastResponseBestEffortChecked returned nil response")
	}
	if resp.Content == issue2299OldReply {
		t.Fatalf("session output returned sibling transcript %q; a session with no conversation of its own must not fall back to another session's JSONL", resp.Content)
	}
	if resp.Content != "" {
		t.Fatalf("Content = %q, want empty (no conversation yet)", resp.Content)
	}
	if target.ClaudeSessionID != wantID {
		t.Fatalf("target ClaudeSessionID mutated to %q; must not adopt sibling id", target.ClaudeSessionID)
	}
}

// Issue #2299: `agent-deck session output <id> -q` can return conversation
// text that never happened in that session. A brand-new Claude instance with
// no transcript of its own shares a project path with an older transcript;
// GetLastResponseBestEffort (the `session output` read path) then disk-scans
// that directory by mtime and silently returns — and adopts — the sibling.
func TestIssue2299_SessionOutputDoesNotReturnSiblingTranscript(t *testing.T) {
	t.Run("bound_id_missing_file", func(t *testing.T) {
		fx := setupIssue2299(t)
		target := NewInstance("new-session", fx.projectPath)
		target.Tool = "claude"
		target.ClaudeSessionID = fx.newID
		target.Status = StatusRunning
		target.tmuxSession = nil
		assertIssue2299NoSiblingLeak(t, target, []*Instance{fx.old, target}, fx.newID)
	})

	t.Run("empty_id", func(t *testing.T) {
		fx := setupIssue2299(t)
		target := NewInstance("new-session-empty", fx.projectPath)
		target.Tool = "claude"
		target.ClaudeSessionID = ""
		target.Status = StatusRunning
		target.tmuxSession = nil
		assertIssue2299NoSiblingLeak(t, target, []*Instance{fx.old, target}, "")
	})
}
