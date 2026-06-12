package session

import (
	"os"
	"path/filepath"
	"testing"
)

// Claude rewrites .claude.json via write-temp+rename, which CLOBBERS the
// scratch symlink into a real file. Found live (2026-06-11): after
// switch-account, a long-lived conductor kept a 586KB real .claude.json
// carrying the OLD account's oauthAccount/MCP state even though
// .credentials.json and projects had been repointed. On a source-profile
// change the stale real file must be replaced with a fresh symlink to the
// new source; on a same-source respawn the clobber must be left alone
// (it is claude's newest local state).

func TestEnsureWorkerScratchConfigDir_ReplacesClobberedClaudeJSONOnSourceChange(t *testing.T) {
	inst := scratchSwitchInstance(t, "scratch-clobber-1")
	srcA := makeProfileDir(t, ".claude.json")
	srcB := makeProfileDir(t, ".claude.json")

	scratch, err := inst.EnsureWorkerScratchConfigDir(srcA)
	if err != nil {
		t.Fatalf("seed from srcA: %v", err)
	}
	t.Cleanup(inst.CleanupWorkerScratchConfigDir)

	// Simulate claude's rename-on-write clobber: symlink becomes a real file
	// holding old-account state.
	clobbered := filepath.Join(scratch, ".claude.json")
	if err := os.Remove(clobbered); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clobbered, []byte(`{"oauthAccount":"old-account"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := inst.EnsureWorkerScratchConfigDir(srcB); err != nil {
		t.Fatalf("reseed from srcB: %v", err)
	}

	li, err := os.Lstat(clobbered)
	if err != nil {
		t.Fatalf("lstat .claude.json: %v", err)
	}
	if li.Mode()&os.ModeSymlink == 0 {
		t.Fatalf(".claude.json still a real file after source change — stale old-account state survives the switch")
	}
	if target, _ := os.Readlink(clobbered); target != filepath.Join(srcB, ".claude.json") {
		t.Errorf(".claude.json points at %q, want new source", target)
	}
}

func TestEnsureWorkerScratchConfigDir_KeepsClobberedClaudeJSONOnSameSource(t *testing.T) {
	inst := scratchSwitchInstance(t, "scratch-clobber-2")
	src := makeProfileDir(t, ".claude.json")

	scratch, err := inst.EnsureWorkerScratchConfigDir(src)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Cleanup(inst.CleanupWorkerScratchConfigDir)

	clobbered := filepath.Join(scratch, ".claude.json")
	if err := os.Remove(clobbered); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clobbered, []byte(`{"local":"newest state"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := inst.EnsureWorkerScratchConfigDir(src); err != nil {
		t.Fatalf("respawn: %v", err)
	}
	b, err := os.ReadFile(clobbered)
	if err != nil || string(b) != `{"local":"newest state"}` {
		t.Errorf("same-source respawn must not disturb claude's local .claude.json (err=%v got=%s)", err, b)
	}
}

// Scratches created before the source marker existed must still reset when
// the source changes — the previous source is inferred from an existing
// symlink's target (e.g. projects).
func TestEnsureWorkerScratchConfigDir_InfersPreviousSourceWithoutMarker(t *testing.T) {
	inst := scratchSwitchInstance(t, "scratch-clobber-3")
	srcA := makeProfileDir(t, ".claude.json", "projects")
	srcB := makeProfileDir(t, ".claude.json", "projects")

	scratch, err := inst.EnsureWorkerScratchConfigDir(srcA)
	if err != nil {
		t.Fatalf("seed from srcA: %v", err)
	}
	t.Cleanup(inst.CleanupWorkerScratchConfigDir)

	// Pre-marker scratch: marker absent, .claude.json clobbered to real file.
	if err := os.Remove(filepath.Join(scratch, scratchSourceMarker)); err != nil {
		t.Fatal(err)
	}
	clobbered := filepath.Join(scratch, ".claude.json")
	if err := os.Remove(clobbered); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clobbered, []byte(`{"oauthAccount":"old"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := inst.EnsureWorkerScratchConfigDir(srcB); err != nil {
		t.Fatalf("reseed from srcB: %v", err)
	}
	li, err := os.Lstat(clobbered)
	if err != nil {
		t.Fatal(err)
	}
	if li.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("marker-less scratch did not reset clobbered .claude.json on source change")
	}
}
