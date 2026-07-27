package session

import (
	"os"
	"path/filepath"
	"strings"
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

	if _, err := inst.EnsureWorkerScratchConfigDir(srcA); err != nil {
		t.Fatalf("seed from srcA: %v", err)
	}
	t.Cleanup(inst.CleanupWorkerScratchConfigDir)

	// Simulate the account switch: same instance respawns from srcB.
	scratch2, err := inst.EnsureWorkerScratchConfigDir(srcB)
	if err != nil {
		t.Fatalf("reseed from srcB: %v", err)
	}

	// Asserted on the EFFECTIVE scratch dir rather than on a fixed path.
	// Since workerScratchDirFor became content-keyed, a source change lands
	// on a different dir instead of repointing one in place — the #924
	// safety property (no old-account state reachable after a switch) is the
	// same, but the mechanism is structural. Under the pre-#924 bug this
	// still fails: that code reused the dir with links into srcA.
	for _, name := range []string{".claude.json", "projects"} {
		target, rerr := os.Readlink(filepath.Join(scratch2, name))
		if rerr != nil {
			t.Fatalf("readlink %s: %v", name, rerr)
		}
		if want := filepath.Join(srcB, name); target != want {
			t.Errorf("%s still points at the old profile: %s (want %s)", name, target, want)
		}
	}

	// Entry only in B must be linked in.
	if target, rerr := os.Readlink(filepath.Join(scratch2, "only-in-b")); rerr != nil || target != filepath.Join(srcB, "only-in-b") {
		t.Errorf("only-in-b not linked to new source (target=%q err=%v)", target, rerr)
	}

	// No entry may resolve into the OLD profile — a dangling-but-valid link
	// into srcA would silently expose the old account's state.
	assertNoEntriesPointInto(t, scratch2, srcA)
}

// assertNoEntriesPointInto fails if any symlink in dir resolves inside the
// given profile directory.
func assertNoEntriesPointInto(t *testing.T, dir, profile string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read scratch: %v", err)
	}
	for _, e := range entries {
		target, rerr := os.Readlink(filepath.Join(dir, e.Name()))
		if rerr != nil {
			continue // not a symlink
		}
		if strings.HasPrefix(target, profile+string(filepath.Separator)) {
			t.Errorf("%s still resolves into the old profile: %s", e.Name(), target)
		}
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
