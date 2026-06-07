package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMultiRepoWorktreesRoot_UnderDataDirNotTemp is the regression test for the
// Codex round-2 P1 finding: the multi-repo worktrees root is PERSISTED in
// session state (Instance.MultiRepoTempDir + worktree/symlink paths), so it
// must resolve to a stable, non-ephemeral location. The old code fell back to
// os.TempDir() on resolution failure, which a reboot or tmp cleanup would wipe,
// silently breaking active git worktrees.
//
// This asserts the resolved root lives under the XDG data dir and never under
// the OS temp dir.
func TestMultiRepoWorktreesRoot_UnderDataDirNotTemp(t *testing.T) {
	home := setXDGTestHome(t)

	root, err := multiRepoWorktreesRoot()
	if err != nil {
		t.Fatalf("multiRepoWorktreesRoot() returned error: %v", err)
	}

	// Root must resolve under the persistent XDG data dir.
	wantPrefix := filepath.Join(home, ".local", "share", "agent-deck")
	if !strings.HasPrefix(filepath.Clean(root), filepath.Clean(wantPrefix)) {
		t.Errorf("worktrees root %q is not under the data dir %q", root, wantPrefix)
	}

	// Critical: the root must NOT be the old ephemeral fallback
	// (os.TempDir()/agent-deck/multi-repo-worktrees). That bare-temp path —
	// not anchored under the data dir — would be wiped on reboot/tmp-cleanup,
	// silently breaking persisted worktrees (Codex round-2 P1). Note: the test
	// data dir itself lives under t.TempDir(), so we check for the *bare*
	// fallback shape, not merely "under /tmp".
	bareTempFallback := filepath.Join(os.TempDir(), "agent-deck", "multi-repo-worktrees")
	if filepath.Clean(root) == filepath.Clean(bareTempFallback) {
		t.Errorf("worktrees root %q is the ephemeral temp fallback; persistent worktree state would be wiped on reboot/tmp-cleanup", root)
	}

	if !strings.HasSuffix(filepath.Clean(root), filepath.Join("agent-deck", "multi-repo-worktrees")) {
		t.Errorf("worktrees root %q does not end with agent-deck/multi-repo-worktrees", root)
	}
}
