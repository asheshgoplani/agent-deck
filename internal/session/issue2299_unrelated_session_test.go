package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestGetLastResponseBestEffort_NewSessionNeverLeaksUnrelatedTranscript locks
// the fix for issue #2299: `session output` on a brand-new session with an
// empty pane and no transcript of its own returned a DIFFERENT, older
// session's reply, because it shared the same project directory.
//
// Repro (from the issue): a new session is spawned (so it has a freshly
// assigned ClaudeSessionID and a real LastStartedAt), no message is ever sent
// to it, and no transcript exists on disk for its own session id. The
// project's Claude projects/ dir already holds an unrelated transcript from a
// session created and last modified days earlier. `session output` must
// report "no output yet" for the new session, never the unrelated session's
// content.
//
// RED before the fix: findLatestClaudeTranscriptOnDisk's newest-mtime scan
// picks the older, unrelated transcript (same project path passes the cwd
// check) and GetLastResponseBestEffort returns its content.
// GREEN after the fix: the scan excludes any transcript last modified before
// this instance's own LastStartedAt, so a session that has never produced a
// transcript of its own gets an empty ("no output yet") response instead.
func TestGetLastResponseBestEffort_NewSessionNeverLeaksUnrelatedTranscript(t *testing.T) {
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
	resolvedProject := projectPath
	if r, err := filepath.EvalSymlinks(projectPath); err == nil {
		resolvedProject = r
	}
	encoded := ConvertToClaudeDirName(resolvedProject)
	projectsDir := filepath.Join(configDir, "projects", encoded)
	if err := os.MkdirAll(projectsDir, 0o755); err != nil {
		t.Fatalf("mkdir projects dir: %v", err)
	}

	// Unrelated, older session in the SAME project directory: created and
	// last modified 5 days ago, with a real assistant reply. This is the
	// "different, older session" from the issue.
	unrelatedID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	unrelatedBody := fmt.Sprintf(
		`{"type":"assistant","sessionId":%q,"cwd":%q,"timestamp":"2026-09-12T10:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":"UNRELATED OLDER SESSION REPLY"}]}}`+"\n",
		unrelatedID, resolvedProject)
	writeTranscript(t, projectsDir, unrelatedID, unrelatedBody)
	mustChtime(t, filepath.Join(projectsDir, unrelatedID+".jsonl"), time.Now().Add(-5*24*time.Hour))

	// The new session: spawned just now (LastStartedAt = now), assigned its
	// own fresh session id, but no message was ever sent so no transcript
	// exists for its id yet.
	inst := NewInstance("no1", projectPath)
	inst.Tool = "claude"
	inst.ClaudeSessionID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	inst.LastStartedAt = time.Now()
	// No tmuxSession: mirrors "empty pane, nothing captured" from the issue.

	resp, err := inst.GetLastResponseBestEffort()
	if err != nil {
		t.Fatalf("GetLastResponseBestEffort returned error: %v", err)
	}
	if resp == nil {
		t.Fatal("GetLastResponseBestEffort returned nil response")
	}
	if resp.Content != "" {
		t.Fatalf("leaked unrelated session's content into new session's output: got %q", resp.Content)
	}
	if inst.ClaudeSessionID != "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb" {
		t.Fatalf("must not adopt the unrelated session's id; got %q", inst.ClaudeSessionID)
	}
}

// TestFindLatestClaudeTranscriptOnDisk_RolloverStillRecovers guards against
// over-fixing #2299: a session that DID start and roll over (/clear or
// compaction) mid-conversation must still recover its own new transcript via
// the disk scan, even though LastStartedAt was stamped before that new
// transcript was written.
func TestFindLatestClaudeTranscriptOnDisk_RolloverStillRecovers(t *testing.T) {
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
	resolvedProject := projectPath
	if r, err := filepath.EvalSymlinks(projectPath); err == nil {
		resolvedProject = r
	}
	encoded := ConvertToClaudeDirName(resolvedProject)
	projectsDir := filepath.Join(configDir, "projects", encoded)
	if err := os.MkdirAll(projectsDir, 0o755); err != nil {
		t.Fatalf("mkdir projects dir: %v", err)
	}

	inst := NewInstance("conductor-rollover", projectPath)
	inst.Tool = "claude"
	staleID := "cccccccc-cccc-cccc-cccc-cccccccccccc"
	inst.ClaudeSessionID = staleID
	// The session started 10 minutes ago; the rollover transcript below is
	// written AFTER that, while the session is live.
	inst.LastStartedAt = time.Now().Add(-10 * time.Minute)

	rolledID := "dddddddd-dddd-dddd-dddd-dddddddddddd"
	rolledBody := fmt.Sprintf(
		`{"type":"assistant","sessionId":%q,"cwd":%q,"timestamp":"2026-09-17T10:05:00Z","message":{"role":"assistant","content":[{"type":"text","text":"POST ROLLOVER REPLY"}]}}`+"\n",
		rolledID, resolvedProject)
	writeTranscript(t, projectsDir, rolledID, rolledBody)
	// Written a minute ago: after LastStartedAt, well before "now".
	mustChtime(t, filepath.Join(projectsDir, rolledID+".jsonl"), time.Now().Add(-1*time.Minute))

	resp, err := inst.GetLastResponseBestEffort()
	if err != nil {
		t.Fatalf("GetLastResponseBestEffort returned error: %v", err)
	}
	if resp == nil || resp.Content != "POST ROLLOVER REPLY" {
		t.Fatalf("expected rollover recovery to still work, got %+v", resp)
	}
}
