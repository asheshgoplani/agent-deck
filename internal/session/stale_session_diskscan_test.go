package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestGetLastResponseBestEffort_RecoversFromStaleSessionIDViaDiskScan locks the
// fix for the conductor "raw JSON in chat" leak.
//
// Repro: a conductor's Claude session rolls over (/clear or compaction starts a
// NEW transcript), but the tmux env var CLAUDE_SESSION_ID — fixed at launch —
// still points at the OLD, now-empty transcript. The stored ClaudeSessionID
// matches that stale env value. getClaudeLastResponse then reads the empty file,
// finds no assistant message, errors, and GetLastResponseBestEffort falls back
// to raw tmux-pane parsing (which includes tool output like `list --json`).
//
// Without tmux available (as in this hermetic test), the old behavior returns an
// empty response. The fix adds a disk-scan fallback — mirroring the Gemini
// syncGeminiSessionFromDisk path — that locates the newest transcript on disk
// carrying a real assistant message and returns its clean text.
//
// RED before the disk-scan fallback exists; GREEN after.
func TestGetLastResponseBestEffort_RecoversFromStaleSessionIDViaDiskScan(t *testing.T) {
	tmpHome := t.TempDir()

	origHome := os.Getenv("HOME")
	origConfigDir := os.Getenv("CLAUDE_CONFIG_DIR")
	_ = os.Setenv("HOME", tmpHome)
	_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
	t.Cleanup(func() {
		_ = os.Setenv("HOME", origHome)
		if origConfigDir != "" {
			_ = os.Setenv("CLAUDE_CONFIG_DIR", origConfigDir)
		} else {
			_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
		}
		ClearUserConfigCache()
	})
	ClearUserConfigCache()

	configDir := filepath.Join(tmpHome, ".claude")
	projectPath := filepath.Join(tmpHome, "project")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir projectPath: %v", err)
	}
	// Resolve symlinks the same way the read path does (macOS t.TempDir() lives
	// under /var -> /private/var); production ProjectPaths have no symlink
	// component so this is a test-environment alignment, not a code concern.
	resolvedProject := projectPath
	if r, err := filepath.EvalSymlinks(projectPath); err == nil {
		resolvedProject = r
	}
	encoded := ConvertToClaudeDirName(resolvedProject)
	projectsDir := filepath.Join(configDir, "projects", encoded)
	if err := os.MkdirAll(projectsDir, 0o755); err != nil {
		t.Fatalf("mkdir projects dir: %v", err)
	}

	// Stale transcript: user record only, no assistant reply (mimics the empty
	// 7-line file the launch-time env var points at).
	staleID := "11111111-1111-1111-1111-111111111111"
	staleBody := fmt.Sprintf(`{"type":"user","sessionId":%q,"message":{"role":"user","content":"hi"}}`+"\n", staleID)
	writeTranscript(t, projectsDir, staleID, staleBody)

	// A subagent SIDECHAIN transcript, newest by mtime, with an assistant reply.
	// A naive newest-mtime scan would wrongly pick this; the scan must skip it.
	sideID := "22222222-2222-2222-2222-222222222222"
	sideBody := fmt.Sprintf(`{"type":"assistant","isSidechain":true,"sessionId":%q,"timestamp":"2026-05-31T22:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":"SUBAGENT NOISE"}]}}`+"\n", sideID)

	// Real, current transcript: a clean assistant reply (no tool output).
	realID := "33333333-3333-3333-3333-333333333333"
	realBody := fmt.Sprintf(`{"type":"assistant","sessionId":%q,"timestamp":"2026-05-31T22:05:00Z","message":{"role":"assistant","content":[{"type":"text","text":"CLEAN CONDUCTOR REPLY"}]}}`+"\n", realID)
	writeTranscript(t, projectsDir, realID, realBody)
	writeTranscript(t, projectsDir, sideID, sideBody)

	// Force mtime ordering: sidechain newest, then real, then stale oldest.
	base := time.Now()
	mustChtime(t, filepath.Join(projectsDir, staleID+".jsonl"), base.Add(-2*time.Minute))
	mustChtime(t, filepath.Join(projectsDir, realID+".jsonl"), base.Add(-1*time.Minute))
	mustChtime(t, filepath.Join(projectsDir, sideID+".jsonl"), base)

	inst := NewInstance("conductor-work", projectPath)
	inst.Tool = "claude"
	inst.ClaudeSessionID = staleID // stale: points at the empty transcript
	// No tmuxSession: the only viable recovery is the disk scan.

	resp, err := inst.GetLastResponseBestEffort()
	if err != nil {
		t.Fatalf("GetLastResponseBestEffort returned error: %v", err)
	}
	if resp == nil {
		t.Fatal("GetLastResponseBestEffort returned nil response")
	}
	if got := resp.Content; got != "CLEAN CONDUCTOR REPLY" {
		t.Errorf("expected clean reply from newest real transcript, got %q", got)
	}
}

func writeTranscript(t *testing.T, dir, id, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatalf("write transcript %s: %v", id, err)
	}
}

func mustChtime(t *testing.T, path string, mod time.Time) {
	t.Helper()
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}
