package session

import (
	"os"
	"path/filepath"
	"testing"
)

// Cross-project --resume regression suite.
//
// Symptom (reported against v1.10.11): a session whose ProjectPath is
// /Users/<u>/projects/devops/ansible carried a ClaudeSessionID whose jsonl
// actually lived under the encoding of the PARENT dir
// (~/.claude/projects/-Users-<u>-projects-devops/<id>.jsonl), because the
// conversation had originally been started from the parent. Every restart
// emitted `claude --resume <id>` from the ansible cwd, Claude answered
//
//	No conversation found with session ID: <id>
//
// and exited within ~1s — the session flapped straight to `error` and no
// amount of retrying could recover it.
//
// Root cause: sessionHasConversationData falls back to
// findSessionFileInAllProjects, which globs EVERY project dir under the
// config dir. Finding the jsonl *somewhere* was treated as proof that
// `--resume` would work. It is not: `claude --resume` only consults the
// project dir derived from its own cwd. The fallback therefore promised a
// resume Claude could not honor.
//
// The fallback still has a legitimate job — rescuing an encoding mismatch
// that denotes the SAME real directory (symlinked project paths, where the
// primary lookup encodes the resolved path but Claude filed under the
// unresolved one). So the fix is not to delete the fallback but to accept it
// only when the directory it found is a plausible encoding of this
// instance's own working dir.
//
// Test 1 (RED before the fix) pins the bug.
// Test 2 guards the symlink case the fallback exists for.

// TestCrossProjectJSONL_DoesNotJustifyResume is the RED test. A jsonl filed
// under a genuinely different project directory must NOT route the restart to
// --resume, because Claude will not find it and will exit immediately.
// Returning false routes to --session-id, which starts cleanly in the correct
// project dir (verified against the real Claude CLI: reusing a session id
// whose jsonl lives only under another project dir is NOT rejected).
func TestCrossProjectJSONL_DoesNotJustifyResume(t *testing.T) {
	tmpDir := t.TempDir()
	withIsolatedClaudeConfigDir(t, tmpDir)

	// Real, existing dirs so EvalSymlinks behaves deterministically.
	parentDir := filepath.Join(tmpDir, "work", "devops")
	childDir := filepath.Join(parentDir, "ansible")
	if err := os.MkdirAll(childDir, 0o755); err != nil {
		t.Fatalf("mkdir child dir: %v", err)
	}

	sessionID := "d046b632-3294-4c9c-accf-249386e68e61"

	// File the transcript under the PARENT's encoding — a different project
	// dir than the instance's own, exactly as in the field report.
	resolvedParent := mustEvalSymlinks(t, parentDir)
	foreignDir := filepath.Join(tmpDir, "projects", ConvertToClaudeDirName(resolvedParent))
	if err := os.MkdirAll(foreignDir, 0o755); err != nil {
		t.Fatalf("mkdir foreign projects dir: %v", err)
	}
	body := `{"type":"user","sessionId":"` + sessionID + `","text":"hi"}` + "\n"
	if err := os.WriteFile(filepath.Join(foreignDir, sessionID+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}

	inst := NewInstance("cross-project", childDir)
	inst.Tool = "claude"

	if sessionHasConversationData(inst, sessionID) {
		t.Fatalf("jsonl for %s lives only under %q, but the instance works in %q; "+
			"`claude --resume` would fail with \"No conversation found\" and exit. "+
			"sessionHasConversationData must return false so the restart uses --session-id.",
			sessionID, foreignDir, childDir)
	}
}

// TestSymlinkedProjectPath_StillResumesViaFallback guards the case the
// cross-project fallback was actually added for: the SAME real directory
// reachable under two spellings. The primary lookup encodes the resolved
// path; if Claude filed the transcript under the unresolved (symlinked)
// spelling, the fallback must still rescue it — Claude, running with that
// cwd, does find its own file.
func TestSymlinkedProjectPath_StillResumesViaFallback(t *testing.T) {
	tmpDir := t.TempDir()
	withIsolatedClaudeConfigDir(t, tmpDir)

	realDir := filepath.Join(tmpDir, "real-project")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatalf("mkdir real dir: %v", err)
	}
	linkDir := filepath.Join(tmpDir, "linked-project")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	sessionID := "22222222-3333-4444-5555-666666666666"

	// Claude filed under the UNRESOLVED spelling; the primary lookup will
	// encode the RESOLVED one and miss.
	unresolvedEncoded := ConvertToClaudeDirName(linkDir)
	resolvedEncoded := ConvertToClaudeDirName(mustEvalSymlinks(t, linkDir))
	if unresolvedEncoded == resolvedEncoded {
		t.Skip("symlink did not produce a distinct encoding on this platform")
	}

	dir := filepath.Join(tmpDir, "projects", unresolvedEncoded)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir projects dir: %v", err)
	}
	body := `{"type":"user","sessionId":"` + sessionID + `","text":"hi"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}

	inst := NewInstance("symlinked", linkDir)
	inst.Tool = "claude"

	if !sessionHasConversationData(inst, sessionID) {
		t.Fatalf("jsonl at %q denotes the same real dir as the instance path %q; "+
			"the fallback must still accept it so restart uses --resume", dir, linkDir)
	}
}

// withIsolatedClaudeConfigDir points CLAUDE_CONFIG_DIR at dir for the test and
// restores the previous value (and the config cache) afterwards.
func withIsolatedClaudeConfigDir(t *testing.T, dir string) {
	t.Helper()
	orig, had := os.LookupEnv("CLAUDE_CONFIG_DIR")
	if err := os.Setenv("CLAUDE_CONFIG_DIR", dir); err != nil {
		t.Fatalf("set CLAUDE_CONFIG_DIR: %v", err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("CLAUDE_CONFIG_DIR", orig)
		} else {
			_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
		}
		ClearUserConfigCache()
	})
	ClearUserConfigCache()
}

func mustEvalSymlinks(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", path, err)
	}
	return resolved
}
