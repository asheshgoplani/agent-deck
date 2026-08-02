package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDiskScanRecovery_SharedPrefixDoesNotCrossRoute pins the output-routing fix
// for sessions whose project paths share a prefix.
//
// ConvertToClaudeDirName maps every non-alphanumeric byte to a hyphen, so two
// distinct project paths that differ only at a separator — /<tmp>/a/b and
// /<tmp>/a-b, which share the prefix /<tmp>/a — encode to the SAME projects/
// subdirectory. Both sessions' transcripts then live side by side in that one
// directory.
//
// The stale-session disk-scan recovery used to pick the newest transcript by
// mtime alone. With a shared encoded dir that returns whichever SIBLING wrote
// last, so `session output` for session A could return session B's reply (and
// rebind A's ClaudeSessionID to B's transcript). The fix reads the cwd Claude
// records in each transcript and skips any whose cwd is not this instance's
// project path.
//
// RED before the cwd filter (returns the newer sibling's reply); GREEN after.
func TestDiskScanRecovery_SharedPrefixDoesNotCrossRoute(t *testing.T) {
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

	// Two sibling project paths sharing the prefix <tmpHome>/a. Because "/" and
	// "-" both encode to "-", they collide on one projects/ subdir.
	ownPath := filepath.Join(tmpHome, "a", "b")  // /<tmp>/a/b
	siblingPath := filepath.Join(tmpHome, "a-b") // /<tmp>/a-b
	for _, p := range []string{ownPath, siblingPath} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
	}

	resolvedOwn := ownPath
	if r, err := filepath.EvalSymlinks(ownPath); err == nil {
		resolvedOwn = r
	}
	resolvedSibling := siblingPath
	if r, err := filepath.EvalSymlinks(siblingPath); err == nil {
		resolvedSibling = r
	}

	encOwn := ConvertToClaudeDirName(resolvedOwn)
	encSibling := ConvertToClaudeDirName(resolvedSibling)
	if encOwn != encSibling {
		t.Fatalf("test precondition: expected shared encoded dir, got %q vs %q", encOwn, encSibling)
	}

	projectsDir := filepath.Join(configDir, "projects", encOwn)
	if err := os.MkdirAll(projectsDir, 0o755); err != nil {
		t.Fatalf("mkdir projects dir: %v", err)
	}

	writeCWDTranscript := func(id, cwd, text, ts string) {
		body := fmt.Sprintf(
			`{"type":"assistant","cwd":%q,"sessionId":%q,"timestamp":%q,"message":{"role":"assistant","content":[{"type":"text","text":%q}]}}`+"\n",
			cwd, id, ts, text)
		writeTranscript(t, projectsDir, id, body)
	}

	// This instance's own transcript.
	ownID := "11111111-1111-1111-1111-111111111111"
	writeCWDTranscript(ownID, ownPath, "OWN SESSION REPLY", "2026-05-31T22:00:00Z")

	// Sibling session's transcript in the same encoded dir, written LATER so a
	// naive newest-mtime scan would pick it.
	siblingID := "22222222-2222-2222-2222-222222222222"
	writeCWDTranscript(siblingID, siblingPath, "SIBLING SESSION REPLY", "2026-05-31T22:05:00Z")

	base := time.Now()
	mustChtime(t, filepath.Join(projectsDir, ownID+".jsonl"), base.Add(-1*time.Minute))
	mustChtime(t, filepath.Join(projectsDir, siblingID+".jsonl"), base) // newest

	inst := NewInstance("shared-prefix-own", ownPath)
	inst.Tool = "claude"
	inst.ClaudeSessionID = "99999999-9999-9999-9999-999999999999" // stale: no such file
	// No tmuxSession: the only viable recovery is the disk scan.

	id, resp := inst.findLatestClaudeTranscriptOnDisk()
	if resp == nil {
		t.Fatal("expected to recover the instance's own transcript, got nil")
	}
	if resp.Content != "OWN SESSION REPLY" {
		t.Fatalf("cross-route: expected own reply, got %q (id %s)", resp.Content, id)
	}
	if id != ownID {
		t.Fatalf("recovered wrong transcript id: got %s want %s", id, ownID)
	}
}

// TestDiskScanRecovery_NoCWDStillRecovers guards against over-filtering: a
// transcript with no recorded cwd (older/truncated) must still be recoverable
// when it is the only viable candidate, so the fix does not regress the
// single-session /clear-rollover recovery.
func TestDiskScanRecovery_NoCWDStillRecovers(t *testing.T) {
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
	resolved := projectPath
	if r, err := filepath.EvalSymlinks(projectPath); err == nil {
		resolved = r
	}
	projectsDir := filepath.Join(configDir, "projects", ConvertToClaudeDirName(resolved))
	if err := os.MkdirAll(projectsDir, 0o755); err != nil {
		t.Fatalf("mkdir projects dir: %v", err)
	}

	// No cwd field on the record — mimics an older transcript.
	id := "44444444-4444-4444-4444-444444444444"
	body := fmt.Sprintf(
		`{"type":"assistant","sessionId":%q,"timestamp":"2026-05-31T22:05:00Z","message":{"role":"assistant","content":[{"type":"text","text":"LEGACY REPLY"}]}}`+"\n", id)
	writeTranscript(t, projectsDir, id, body)

	inst := NewInstance("legacy-single", projectPath)
	inst.Tool = "claude"
	inst.ClaudeSessionID = "99999999-9999-9999-9999-999999999999"

	gotID, resp := inst.findLatestClaudeTranscriptOnDisk()
	if resp == nil || resp.Content != "LEGACY REPLY" {
		t.Fatalf("expected legacy reply recovered, got %+v (id %s)", resp, gotID)
	}
}
