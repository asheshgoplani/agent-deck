package session

import (
	"os"
	"path/filepath"
	"testing"
)

// SpawnClaudeConfigDirForInstance answers "which config dir is this process
// actually reading", which is a different question from
// GetClaudeConfigDirForInstance's "which account does this session belong to".
// The two diverge on every session agent-deck hands a worker-scratch home, and
// the divergence is not cosmetic: a live /context capture on 2026-07-29 showed
// the harness loading ~/.agent-deck/worker-scratch/<id>/CLAUDE.md while the
// context panel listed two files from the profile dir that were not in the
// window at all.

// TestSpawnConfigDirPrefersThePreparedScratchHome is the case the capture
// exposed.
func TestSpawnConfigDirPrefersThePreparedScratchHome(t *testing.T) {
	tmpHome := setupConductorTest(t)
	writeConductorConfig(t, tmpHome, `
[groups."buddii".claude]
config_dir = "`+filepath.Join(tmpHome, ".claude-buddii")+`"
`)
	scratch := filepath.Join(tmpHome, ".agent-deck", "worker-scratch", "b81d755d")
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		t.Fatalf("mkdir scratch: %v", err)
	}

	inst := NewInstanceWithGroupAndTool("worker", tmpHome, "buddii", "claude")
	inst.WorkerScratchConfigDir = scratch

	path, source := SpawnClaudeConfigDirForInstance(inst)
	if path != scratch {
		t.Errorf("spawn config dir = %q, want the prepared scratch home %q: the profile dir's CLAUDE.md is not the one this process reads", path, scratch)
	}
	if source != ClaudeConfigSourceWorkerScratch {
		t.Errorf("source = %q, want %q", source, ClaudeConfigSourceWorkerScratch)
	}

	// The account-resolution answer must be unchanged. It has other callers —
	// credential mirroring among them — and quietly redirecting it at a scratch
	// mirror is the nested-scratch credential bug, not a fix.
	if got := GetClaudeConfigDirForInstance(inst); got == scratch {
		t.Error("GetClaudeConfigDirForInstance must keep resolving the account profile, not the ephemeral scratch mirror")
	}
}

// TestSpawnConfigDirIgnoresAStaleScratchRow covers a stopped session:
// CleanupWorkerScratchConfigDir removed the directory, but a persisted row can
// still name it. A path that is gone is not where anything is being read from.
func TestSpawnConfigDirIgnoresAStaleScratchRow(t *testing.T) {
	tmpHome := setupConductorTest(t)
	profile := filepath.Join(tmpHome, ".claude-buddii")
	writeConductorConfig(t, tmpHome, `
[groups."buddii".claude]
config_dir = "`+profile+`"
`)

	inst := NewInstanceWithGroupAndTool("worker", tmpHome, "buddii", "claude")
	inst.WorkerScratchConfigDir = filepath.Join(tmpHome, ".agent-deck", "worker-scratch", "deleted")

	path, source := SpawnClaudeConfigDirForInstance(inst)
	if path != profile {
		t.Errorf("spawn config dir = %q, want the resolved profile %q when the scratch dir no longer exists", path, profile)
	}
	if source == ClaudeConfigSourceWorkerScratch {
		t.Error("a scratch dir that is not on disk must not be reported as the source")
	}
}

// TestSpawnConfigDirReportsADormantScratchAsAmbient reproduces the issue #949
// gate. When nothing explicit resolves, the spawn builders export no
// CLAUDE_CONFIG_DIR at all, so a prepared scratch stays dormant and the process
// inherits whatever its shell had. Reporting the scratch there would name a
// directory the harness never opened.
func TestSpawnConfigDirReportsADormantScratchAsAmbient(t *testing.T) {
	tmpHome := setupConductorTest(t)
	scratch := filepath.Join(tmpHome, ".agent-deck", "worker-scratch", "dormant")
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		t.Fatalf("mkdir scratch: %v", err)
	}

	inst := NewInstanceWithGroupAndTool("worker", tmpHome, "", "claude")
	inst.WorkerScratchConfigDir = scratch

	path, source := SpawnClaudeConfigDirForInstance(inst)
	if source != ClaudeConfigSourceAmbient {
		t.Fatalf("source = %q, want %q: with the config-dir gate closed the scratch is dormant", source, ClaudeConfigSourceAmbient)
	}
	if path == scratch {
		t.Error("a dormant scratch must not be presented as the dir the session loads from")
	}
	if path != filepath.Join(tmpHome, ".claude") {
		t.Errorf("ambient path = %q, want the harness default %q", path, filepath.Join(tmpHome, ".claude"))
	}
}

// TestSpawnConfigDirWithoutScratchMatchesTheResolver keeps the ordinary case
// honest: no scratch means the two functions agree, and the source label is the
// resolver's own.
func TestSpawnConfigDirWithoutScratchMatchesTheResolver(t *testing.T) {
	tmpHome := setupConductorTest(t)
	profile := filepath.Join(tmpHome, ".claude-work")
	writeConductorConfig(t, tmpHome, `
[groups."work".claude]
config_dir = "`+profile+`"
`)

	inst := NewInstanceWithGroupAndTool("worker", tmpHome, "work", "claude")

	path, source := SpawnClaudeConfigDirForInstance(inst)
	if path != profile {
		t.Errorf("spawn config dir = %q, want %q", path, profile)
	}
	if source != "group" {
		t.Errorf("source = %q, want %q", source, "group")
	}
	if got := GetClaudeConfigDirForInstance(inst); got != path {
		t.Errorf("with no scratch the two resolvers must agree: spawn=%q account=%q", path, got)
	}
}

// TestSpawnConfigDirHandlesANilInstance keeps the panel from panicking on a
// session row it could not load.
func TestSpawnConfigDirHandlesANilInstance(t *testing.T) {
	path, source := SpawnClaudeConfigDirForInstance(nil)
	if path != "" || source != "" {
		t.Fatalf("nil instance = (%q, %q), want empty: there is nothing to resolve", path, source)
	}
}

// TestSpawnConfigDirRecoversAnUnpersistedScratchDir is the case that decides
// whether the fix works at all in the CLI.
//
// Instance.WorkerScratchConfigDir is not a column in the instances table: it is
// set by the process that prepared the scratch and is lost the moment another
// process loads the same session from state.db. Resolving only the in-memory
// field would fix the TUI and leave `agent-deck session context` reporting the
// profile dir — files the session is not reading — which is the defect this
// change exists to remove.
func TestSpawnConfigDirRecoversAnUnpersistedScratchDir(t *testing.T) {
	tmpHome := setupConductorTest(t)
	writeConductorConfig(t, tmpHome, `
[groups."buddii".claude]
config_dir = "`+filepath.Join(tmpHome, ".claude-buddii")+`"
`)

	inst := NewInstanceWithGroupAndTool("worker", tmpHome, "buddii", "claude")
	// Exactly what a fresh CLI process holds: an id, and nothing about scratch.
	inst.WorkerScratchConfigDir = ""

	derived := WorkerScratchConfigDirFor(inst.ID)
	if derived == "" {
		t.Fatal("the scratch path must be derivable from the instance id alone")
	}
	if err := os.MkdirAll(derived, 0o700); err != nil {
		t.Fatalf("mkdir scratch: %v", err)
	}

	path, source := SpawnClaudeConfigDirForInstance(inst)
	if path != derived {
		t.Errorf("spawn config dir = %q, want the on-disk scratch home %q recovered from the id", path, derived)
	}
	if source != ClaudeConfigSourceWorkerScratch {
		t.Errorf("source = %q, want %q", source, ClaudeConfigSourceWorkerScratch)
	}
}

// TestWorkerScratchConfigDirForRejectsAnEmptyID keeps the derivation from
// returning the scratch ROOT, which would make every session appear to load
// from a directory that holds every other session's mirror.
func TestWorkerScratchConfigDirForRejectsAnEmptyID(t *testing.T) {
	if got := WorkerScratchConfigDirFor("  "); got != "" {
		t.Fatalf("WorkerScratchConfigDirFor(blank) = %q, want empty", got)
	}
}
