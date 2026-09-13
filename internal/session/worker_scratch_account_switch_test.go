package session

import (
	"os"
	"path/filepath"
	"testing"
)

// Account-switch staleness (#924 follow-up, found live 2026-06-11):
// EnsureWorkerScratchConfigDir is idempotent per instance ID, but the
// symlink mirror was skip-if-exists. After `session set <s> account <b>` /
// `session switch-account`, the respawn reused the scratch dir whose
// symlinks still pointed at the OLD account's profile — so the switch
// silently didn't apply (conversation, auth, projects all stayed on the
// previous account). The mirror must repoint stale symlinks to the new
// source and sweep entries the new source doesn't have.

// scratchSwitchInstance returns a worker instance that needs a scratch dir.
func scratchSwitchInstance(t *testing.T, id string) *Instance {
	t.Helper()
	withTelegramConductorPresent(t)
	return &Instance{
		ID:    id,
		Title: "scratch-switch-worker",
		Tool:  "claude",
	}
}

// makeProfileDir creates a fake Claude profile with the given top-level
// entries (as real files) plus a settings.json.
func makeProfileDir(t *testing.T, entries ...string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range entries {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestEnsureWorkerScratchConfigDir_RepointsSymlinksOnSourceChange(t *testing.T) {
	inst := scratchSwitchInstance(t, "scratch-switch-1")
	srcA := makeProfileDir(t, ".claude.json", "projects", "only-in-a")
	srcB := makeProfileDir(t, ".claude.json", "projects", "only-in-b")

	scratch, err := inst.EnsureWorkerScratchConfigDir(srcA)
	if err != nil {
		t.Fatalf("seed from srcA: %v", err)
	}
	t.Cleanup(inst.CleanupWorkerScratchConfigDir)

	// Simulate the account switch: same instance respawns from srcB.
	scratch2, err := inst.EnsureWorkerScratchConfigDir(srcB)
	if err != nil {
		t.Fatalf("reseed from srcB: %v", err)
	}
	if scratch2 != scratch {
		t.Fatalf("scratch dir should be stable per instance: %q vs %q", scratch, scratch2)
	}

	for _, name := range []string{".claude.json", "projects"} {
		target, rerr := os.Readlink(filepath.Join(scratch, name))
		if rerr != nil {
			t.Fatalf("readlink %s: %v", name, rerr)
		}
		if want := filepath.Join(srcB, name); target != want {
			t.Errorf("%s still points at the old profile: %s (want %s)", name, target, want)
		}
	}

	// Entry only in B must be linked in.
	if target, rerr := os.Readlink(filepath.Join(scratch, "only-in-b")); rerr != nil || target != filepath.Join(srcB, "only-in-b") {
		t.Errorf("only-in-b not linked to new source (target=%q err=%v)", target, rerr)
	}

	// Leftover symlink to the old profile must be swept — a dangling-but-
	// valid link into srcA would silently expose the old account's state.
	if _, lerr := os.Lstat(filepath.Join(scratch, "only-in-a")); !os.IsNotExist(lerr) {
		t.Errorf("only-in-a leftover from old profile not removed (err=%v)", lerr)
	}
}

// TestWorkerScratchGeneration_DelayedOldProcessWriteStaysInOriginalAccount
// models Restart's ordering: the replacement generation is prepared before
// RespawnPane kills the old pane. A delayed write through the old process's
// CLAUDE_CONFIG_DIR must remain in account A rather than following a repointed
// symlink into account B.
func TestWorkerScratchGeneration_DelayedOldProcessWriteStaysInOriginalAccount(t *testing.T) {
	withTelegramConductorPresent(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg-data"))

	srcA := filepath.Join(home, "account-a")
	srcB := filepath.Join(home, "account-b")
	for _, source := range []string{srcA, srcB} {
		if err := os.MkdirAll(filepath.Join(source, "projects"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, "settings.json"), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	inst := &Instance{ID: "scratch-delayed-flush", Title: "worker", Tool: "claude"}
	oldGeneration, err := inst.EnsureWorkerScratchConfigDir(srcA)
	if err != nil {
		t.Fatalf("seed account A generation: %v", err)
	}
	// This is the lifecycle-only path used by prepareWorkerScratchConfigDirForSpawn.
	newGeneration, err := inst.ensureWorkerScratchConfigDir(srcB, true)
	if err != nil {
		t.Fatalf("prepare account B generation: %v", err)
	}
	if oldGeneration == newGeneration {
		t.Fatalf("restart reused scratch generation %q", oldGeneration)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(oldGeneration)) })

	oldProjects := filepath.Join(oldGeneration, "projects")
	if target, err := os.Readlink(oldProjects); err != nil || target != filepath.Join(srcA, "projects") {
		t.Fatalf("old generation projects link = %q, %v; want account A", target, err)
	}
	if target, err := os.Readlink(filepath.Join(newGeneration, "projects")); err != nil || target != filepath.Join(srcB, "projects") {
		t.Fatalf("new generation projects link = %q, %v; want account B", target, err)
	}

	// Simulate the old process flushing only after the replacement generation
	// has been created. The write must still land under its original account.
	const flushName = "delayed-old-process.jsonl"
	if err := os.WriteFile(filepath.Join(oldProjects, flushName), []byte("old-process-flush"), 0o600); err != nil {
		t.Fatalf("delayed old-process flush: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(srcA, "projects", flushName)); err != nil || string(got) != "old-process-flush" {
		t.Fatalf("account A did not retain delayed flush: got %q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(srcB, "projects", flushName)); !os.IsNotExist(err) {
		t.Fatalf("delayed old-process flush leaked into account B: %v", err)
	}
}

func TestEnsureWorkerScratchConfigDir_SameSourceLeavesRealFilesAlone(t *testing.T) {
	inst := scratchSwitchInstance(t, "scratch-switch-2")
	src := makeProfileDir(t, ".claude.json")

	scratch, err := inst.EnsureWorkerScratchConfigDir(src)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Cleanup(inst.CleanupWorkerScratchConfigDir)

	// A REAL file in scratch (e.g. state claude wrote locally) must survive a
	// same-source respawn — the sweep may only touch symlinks.
	realFile := filepath.Join(scratch, "scratch-local-state")
	if err := os.WriteFile(realFile, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := inst.EnsureWorkerScratchConfigDir(src); err != nil {
		t.Fatalf("respawn: %v", err)
	}
	if b, err := os.ReadFile(realFile); err != nil || string(b) != "keep me" {
		t.Errorf("real scratch-local file was disturbed (err=%v)", err)
	}
}
