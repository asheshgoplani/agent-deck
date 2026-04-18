// Regression tests for https://github.com/asheshgoplani/agent-deck/issues/663:
// multi-repo sessions silently drop all conversation history on stop→start.
//
// Root cause per .planning/investigate-multirepo-restart/REPORT.md: the
// reload/restart paths seed tmux cwd and Claude JSONL lookup from
// Instance.ProjectPath (which, for multi-repo sessions, was rewritten at
// creation time to be a symlink inside MultiRepoTempDir) instead of
// EffectiveWorkingDir() (which returns MultiRepoTempDir for multi-repo).
// Result: tmux launches in the wrong cwd; Claude's JSONL encoded-path key
// never matches; Start() enters the fresh-session branch; prior
// conversation is silently lost.
//
// These four tests pin each of the four sites that must use
// EffectiveWorkingDir() rather than ProjectPath on the restart/resume path.
//
// Parallel-paths audit (claude-conductor invariant 7): the bug has four
// code paths, all fixed together:
//  1. storage.go convertToInstances → tmux.ReconnectSessionLazy WorkDir
//  2. instance.go recreateTmuxSession → tmux.NewSession WorkDir
//  3. instance.go ensureClaudeSessionIDFromDisk → discoverLatestClaudeJSONL
//  4. instance.go sessionHasConversationData encoded-path lookup
//
// Three RED tests below cover sites 1–3, which are 100% broken on v1.7.23.
// Site 4 is a latent bug: the wrong encoded-path primary lookup is masked
// by an existing cross-project fallback (findSessionFileInAllProjects) that
// globs every project hash dir for the session UUID and hits the correct
// JSONL anyway. We still apply the same EffectiveWorkingDir() fix at site 4
// for consistency (and to avoid the fallback's read amplification when
// projects/ contains hundreds of hash dirs), but it has no observable
// failure mode on current code, so it ships without a dedicated RED test.
package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// multiRepoFixtureDirs creates the on-disk layout that production sets up
// for a multi-repo session:
//
//	<home>/.agent-deck/multi-repo-worktrees/<id8>/       (MultiRepoTempDir, real dir)
//	<home>/.agent-deck/multi-repo-worktrees/<id8>/repo1 -> <home>/src/repo1
//	<home>/src/repo1                                     (real source repo)
//
// ProjectPath points to the symlink (the first repo inside MultiRepoTempDir).
// This mirrors home.go:7255-7364's rewrite-ProjectPath-to-parentDir-symlink step.
// Returns (multiRepoTempDir, projectPathSymlink).
func multiRepoFixtureDirs(t *testing.T, home string) (string, string) {
	t.Helper()
	srcRepo := filepath.Join(home, "src", "repo1")
	if err := os.MkdirAll(srcRepo, 0o755); err != nil {
		t.Fatalf("mkdir srcRepo: %v", err)
	}
	parent := filepath.Join(home, ".agent-deck", "multi-repo-worktrees", "abc12345")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	// Resolve symlinks on the parent dir to match production (home.go uses
	// filepath.EvalSymlinks when computing MultiRepoTempDir). On Linux the
	// temp HOME itself may include /private or /var symlinks, so normalize.
	if resolved, err := filepath.EvalSymlinks(parent); err == nil {
		parent = resolved
	}
	symlink := filepath.Join(parent, "repo1")
	if err := os.Symlink(srcRepo, symlink); err != nil {
		t.Fatalf("symlink project: %v", err)
	}
	return parent, symlink
}

// TestRestore_ReconnectSeedsWorkDirFromMultiRepoTempDir pins site 1.
//
// On reload, storage.go:730-820's convertToInstances calls
// tmux.ReconnectSessionLazy with instData.ProjectPath as the WorkDir. For
// multi-repo sessions this must be instData.MultiRepoTempDir instead —
// otherwise the first attach starts the pane inside the symlink's target
// (the original repo), not the parent worktree dir.
func TestRestore_ReconnectSeedsWorkDirFromMultiRepoTempDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })

	parent, symlink := multiRepoFixtureDirs(t, home)

	data := &StorageData{
		Instances: []*InstanceData{{
			ID:               "test-663-reconnect",
			Title:            "multirepo-reconnect",
			ProjectPath:      symlink,
			Tool:             "claude",
			Command:          "claude",
			Status:           StatusStopped,
			TmuxSession:      "agentdeck_test_663_reconnect_deadbeef",
			MultiRepoEnabled: true,
			MultiRepoTempDir: parent,
			AdditionalPaths:  []string{},
		}},
	}

	s := &Storage{}
	instances, _, err := s.convertToInstances(data)
	if err != nil {
		t.Fatalf("convertToInstances: %v", err)
	}
	if len(instances) != 1 {
		t.Fatalf("want 1 instance, got %d", len(instances))
	}

	sess := instances[0].GetTmuxSession()
	if sess == nil {
		t.Fatal("GetTmuxSession returned nil — cannot verify WorkDir")
	}
	if sess.WorkDir != parent {
		t.Fatalf("issue #663: after Load, tmux WorkDir = %q, want MultiRepoTempDir = %q. "+
			"On restart the pane will launch in the symlink's resolved target (the "+
			"original source repo) instead of the parent worktree dir, and Claude's "+
			"JSONL encoded-path key will not match the one written on the first boot.",
			sess.WorkDir, parent)
	}
}

// TestRestore_RecreateTmuxSessionUsesEffectiveWorkingDir pins site 2.
//
// recreateTmuxSession (instance.go:3318-3323) runs on the TUI restart path
// when the old tmux session is dead. Today it calls tmux.NewSession with
// i.ProjectPath; for multi-repo sessions this must use
// i.EffectiveWorkingDir() so the freshly-created session lands in
// MultiRepoTempDir.
func TestRestore_RecreateTmuxSessionUsesEffectiveWorkingDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })

	parent, symlink := multiRepoFixtureDirs(t, home)

	inst := &Instance{
		ID:               "test-663-recreate",
		Title:            "multirepo-recreate",
		ProjectPath:      symlink,
		Tool:             "claude",
		Command:          "claude",
		MultiRepoEnabled: true,
		MultiRepoTempDir: parent,
	}

	inst.recreateTmuxSession()

	sess := inst.GetTmuxSession()
	if sess == nil {
		t.Fatal("recreateTmuxSession left tmuxSession nil")
	}
	if sess.WorkDir != parent {
		t.Fatalf("issue #663: recreateTmuxSession set WorkDir = %q, want %q (MultiRepoTempDir). "+
			"A TUI restart will then spawn the replacement tmux session in the wrong cwd.",
			sess.WorkDir, parent)
	}
}

// TestRestore_DiscoverJSONLUsesMultiRepoTempDir pins site 3.
//
// ensureClaudeSessionIDFromDisk calls discoverLatestClaudeJSONL(i.ProjectPath).
// On a multi-repo session the JSONL was written under
// ~/.claude/projects/<encoded MultiRepoTempDir>/, so a ProjectPath lookup
// (which EvalSymlinks-resolves into the ORIGINAL repo path) misses. After
// the fix, the lookup uses EffectiveWorkingDir() = MultiRepoTempDir and
// hits.
func TestRestore_DiscoverJSONLUsesMultiRepoTempDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })

	parent, symlink := multiRepoFixtureDirs(t, home)

	// Seed a JSONL under the MultiRepoTempDir-encoded path (the key that
	// Claude would have written on the first boot, when cwd = parent).
	sessionUUID := "1234abcd-5678-90ab-cdef-1234567890ab"
	encoded := ConvertToClaudeDirName(parent)
	if encoded == "" {
		encoded = "-"
	}
	projectsDir := filepath.Join(home, ".claude", "projects", encoded)
	if err := os.MkdirAll(projectsDir, 0o755); err != nil {
		t.Fatalf("mkdir projects: %v", err)
	}
	jsonlPath := filepath.Join(projectsDir, sessionUUID+".jsonl")
	line, _ := json.Marshal(map[string]string{"sessionId": sessionUUID, "role": "user", "content": "hi"})
	if err := os.WriteFile(jsonlPath, append(line, '\n'), 0o644); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}

	inst := &Instance{
		ID:               "test-663-discover",
		ProjectPath:      symlink,
		Tool:             "claude",
		MultiRepoEnabled: true,
		MultiRepoTempDir: parent,
		// Required: ClaudeDetectedAt must be non-zero for the discovery
		// path (issue #608 guard: zero-time means "never started, must be
		// fresh").
		ClaudeDetectedAt: time.Now(),
	}

	inst.ensureClaudeSessionIDFromDisk()

	if inst.ClaudeSessionID != sessionUUID {
		t.Fatalf("issue #663: ensureClaudeSessionIDFromDisk did not find the JSONL under "+
			"MultiRepoTempDir encoding. Got ClaudeSessionID = %q, want %q. "+
			"Current code encodes ProjectPath (the symlink, which EvalSymlinks "+
			"resolves into the ORIGINAL repo path — a different hash than the "+
			"one Claude used when cwd = MultiRepoTempDir). JSONL at: %s",
			inst.ClaudeSessionID, sessionUUID, jsonlPath)
	}
}

// NOTE on site 4 (sessionHasConversationData):
//
// The fourth parallel path — sessionHasConversationData — is ALSO fixed to
// use EffectiveWorkingDir() for consistency (see parallel-paths audit
// comment at the top of this file), but it has no dedicated RED test.
// Reason: the helper has an existing cross-project fallback
// (findSessionFileInAllProjects at instance.go:5352) that globs every
// project hash dir for the session UUID and lands on the correct JSONL
// even when the primary lookup uses the wrong (ProjectPath-based) encoded
// path. So an end-to-end test seeded with a single JSONL at the
// MultiRepoTempDir-encoded path passes both before and after the fix.
// The fix at site 4 is still correct (eliminates a needless read
// amplification over projects/) but is not user-observable in isolation.
