package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func seedCwdSyncInstance(t *testing.T, profile string, inst *session.Instance) *session.Storage {
	t.Helper()
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("new storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	if err := storage.Save([]*session.Instance{inst}); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	return storage
}

func loadCwdSyncInstance(t *testing.T, storage *session.Storage, id string) *session.Instance {
	t.Helper()
	loaded, err := storage.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	for _, i := range loaded {
		if i.ID == id {
			return i
		}
	}
	t.Fatalf("instance %s disappeared", id)
	return nil
}

func TestApplyClaudeCwdSync_UpdatesProjectPathAndPreservesWorktreeFields(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENTDECK_PROFILE", "cwd_sync_update")

	oldPath := filepath.Join(home, "repos", "a")
	newPath := filepath.Join(home, "repos", "b")
	for _, p := range []string{oldPath, newPath} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	storage := seedCwdSyncInstance(t, "cwd_sync_update", &session.Instance{
		ID:               "inst-1",
		Title:            "s1",
		Tool:             "claude",
		ProjectPath:      oldPath,
		Command:          "claude",
		WorktreePath:     "/repo/worktrees/feature-x",
		WorktreeRepoRoot: "/repo",
		WorktreeBranch:   "feature-x",
	})

	applyClaudeCwdSync("inst-1", newPath)

	got := loadCwdSyncInstance(t, storage, "inst-1")
	if got.ProjectPath != newPath {
		t.Errorf("ProjectPath = %q, want %q", got.ProjectPath, newPath)
	}
	if got.WorktreePath != "/repo/worktrees/feature-x" {
		t.Errorf("WorktreePath changed: got %q", got.WorktreePath)
	}
	if got.WorktreeRepoRoot != "/repo" {
		t.Errorf("WorktreeRepoRoot changed: got %q", got.WorktreeRepoRoot)
	}
	if got.WorktreeBranch != "feature-x" {
		t.Errorf("WorktreeBranch changed: got %q", got.WorktreeBranch)
	}
}

// applyClaudeCwdSync fires from the LOCAL hook handler when Claude Code emits
// a hook. Remote sessions run Claude on the remote host, so their hooks fire
// on the remote agent-deck binary (not this local one) and never reach this
// code path — nothing to cover here.
func TestApplyClaudeCwdSync_SkipsRemoteSessions(t *testing.T) {
	t.Skip("RemoteSession out of scope: hooks fire on the remote host, not the local hook handler")
}

func TestApplyClaudeCwdSync_NoopWhenSame(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENTDECK_PROFILE", "cwd_sync_noop")

	sameDir := filepath.Join(home, "repo")
	if err := os.MkdirAll(sameDir, 0o755); err != nil {
		t.Fatal(err)
	}

	storage := seedCwdSyncInstance(t, "cwd_sync_noop", &session.Instance{
		ID:              "inst-2",
		Title:           "s2",
		Tool:            "claude",
		ProjectPath:     sameDir,
		AdditionalPaths: []string{"/keep/this/entry"},
		Command:         "claude",
	})

	applyClaudeCwdSync("inst-2", sameDir)

	got := loadCwdSyncInstance(t, storage, "inst-2")
	if got.ProjectPath != sameDir {
		t.Errorf("ProjectPath changed on no-op sync: got %q, want %q", got.ProjectPath, sameDir)
	}
	if len(got.AdditionalPaths) != 1 || got.AdditionalPaths[0] != "/keep/this/entry" {
		t.Errorf("AdditionalPaths mutated on no-op sync: got %v", got.AdditionalPaths)
	}
}

func TestApplyClaudeCwdSync_SwapsMultiRepo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENTDECK_PROFILE", "cwd_sync_swap")

	primary := filepath.Join(home, "repos", "primary")
	extraB := filepath.Join(home, "repos", "b")
	extraC := filepath.Join(home, "repos", "c")
	for _, p := range []string{primary, extraB, extraC} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	storage := seedCwdSyncInstance(t, "cwd_sync_swap", &session.Instance{
		ID:              "inst-3",
		Title:           "s3",
		Tool:            "claude",
		ProjectPath:     primary,
		AdditionalPaths: []string{extraB, extraC},
		Command:         "claude",
	})

	applyClaudeCwdSync("inst-3", extraB)

	got := loadCwdSyncInstance(t, storage, "inst-3")
	if got.ProjectPath != extraB {
		t.Errorf("ProjectPath = %q, want %q", got.ProjectPath, extraB)
	}
	if len(got.AdditionalPaths) != 2 {
		t.Fatalf("AdditionalPaths len = %d, want 2 (paths=%v)", len(got.AdditionalPaths), got.AdditionalPaths)
	}
	if got.AdditionalPaths[0] != primary {
		t.Errorf("AdditionalPaths[0] = %q, want swapped-in old primary %q", got.AdditionalPaths[0], primary)
	}
	if got.AdditionalPaths[1] != extraC {
		t.Errorf("AdditionalPaths[1] = %q, want untouched %q", got.AdditionalPaths[1], extraC)
	}
}

func TestApplyClaudeCwdSync_NoopWhenCwdEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENTDECK_PROFILE", "cwd_sync_empty")

	dir := filepath.Join(home, "repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	storage := seedCwdSyncInstance(t, "cwd_sync_empty", &session.Instance{
		ID:          "inst-4",
		Title:       "s4",
		Tool:        "claude",
		ProjectPath: dir,
		Command:     "claude",
	})

	applyClaudeCwdSync("inst-4", "")

	got := loadCwdSyncInstance(t, storage, "inst-4")
	if got.ProjectPath != dir {
		t.Errorf("ProjectPath changed on empty cwd: got %q", got.ProjectPath)
	}
}
