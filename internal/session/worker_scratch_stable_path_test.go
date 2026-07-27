// Package session — tests for content-keyed worker-scratch dir paths.
//
// The scratch dir used to be keyed by instance ID, giving every session a
// unique CLAUDE_CONFIG_DIR path. On macOS that silently breaks
// authentication: Claude Code keys its Keychain OAuth entry by the literal
// config-dir path, so a per-session path never has a credential entry and
// the session comes up "Not logged in". This is only observable once an
// explicit config_dir is set (a per-group Claude account), because
// buildBashExportPrefix only exports CLAUDE_CONFIG_DIR in that case.
//
// The scratch dir's contents are a pure function of the source profile and
// the session's plugin topology (deny ∪ allow ∪ channel-allow over the
// catalog). Keying the path by those inputs instead of the instance ID
// makes it stable: one login per (profile, topology) covers every session
// that shares it, and concurrent seeders converge on identical bytes
// rather than racing. Sessions with different topology — or a different
// source profile, i.e. a different Claude account — still get separate
// dirs, so isolation is preserved.
package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func scratchTestHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg-data"))
}

// Two different sessions sharing a source profile and plugin topology must
// resolve to the SAME scratch dir, so a single `claude` login against that
// path authenticates all of them.
func TestEnsureWorkerScratchConfigDir_StableAcrossInstancesWithSameTopology(t *testing.T) {
	withTelegramConductorPresent(t)
	scratchTestHome(t)
	source := makeProfileDir(t)

	first := &Instance{ID: "00000000-0000-0000-0000-00000000aaaa", Tool: "claude", Title: "worker-a"}
	second := &Instance{ID: "00000000-0000-0000-0000-00000000bbbb", Tool: "claude", Title: "worker-b"}

	dirA, err := first.EnsureWorkerScratchConfigDir(source)
	if err != nil {
		t.Fatalf("ensure first: %v", err)
	}
	dirB, err := second.EnsureWorkerScratchConfigDir(source)
	if err != nil {
		t.Fatalf("ensure second: %v", err)
	}

	if dirA != dirB {
		t.Errorf("scratch dir must be stable across sessions with identical topology "+
			"(one Keychain login must cover all of them)\n first:  %s\n second: %s", dirA, dirB)
	}
	if strings.Contains(dirA, first.ID) {
		t.Errorf("scratch path must not embed the instance ID (that is what makes it per-session): %s", dirA)
	}
}

// A different source profile is a different Claude account. Those must
// never share a scratch dir, or the accounts would cross-contaminate.
func TestEnsureWorkerScratchConfigDir_DiffersWhenSourceProfileDiffers(t *testing.T) {
	withTelegramConductorPresent(t)
	scratchTestHome(t)
	personal := makeProfileDir(t)
	team := makeProfileDir(t)

	inst := &Instance{ID: "00000000-0000-0000-0000-00000000cccc", Tool: "claude", Title: "worker"}

	dirPersonal, err := inst.EnsureWorkerScratchConfigDir(personal)
	if err != nil {
		t.Fatalf("ensure personal: %v", err)
	}
	dirTeam, err := inst.EnsureWorkerScratchConfigDir(team)
	if err != nil {
		t.Fatalf("ensure team: %v", err)
	}

	if dirPersonal == dirTeam {
		t.Errorf("scratch dirs for different source profiles must not collide: both %s", dirPersonal)
	}
}

// Sessions with different plugin topology produce different scratch
// settings.json, so they must not share a dir — otherwise the last spawn
// to seed would silently rewrite a running session's plugin set.
func TestEnsureWorkerScratchConfigDir_DiffersWhenPluginTopologyDiffers(t *testing.T) {
	withTelegramConductorPresent(t)
	home := withTempHome(t)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg-data"))
	// computeAllowList resolves Instance.Plugins through the config.toml
	// catalog; without an entry the name resolves to nothing and both
	// instances would legitimately share a topology.
	writeConfig(t, home, `
[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"
`)
	source := makeProfileDir(t)

	plain := &Instance{ID: "00000000-0000-0000-0000-00000000dddd", Tool: "claude", Title: "worker"}
	withPlugins := &Instance{
		ID:      "00000000-0000-0000-0000-00000000eeee",
		Tool:    "claude",
		Title:   "worker",
		Plugins: []string{"octopus"},
	}

	dirPlain, err := plain.EnsureWorkerScratchConfigDir(source)
	if err != nil {
		t.Fatalf("ensure plain: %v", err)
	}
	dirPlugins, err := withPlugins.EnsureWorkerScratchConfigDir(source)
	if err != nil {
		t.Fatalf("ensure with plugins: %v", err)
	}

	if dirPlain == dirPlugins {
		t.Errorf("scratch dirs must differ when plugin topology differs: both %s", dirPlain)
	}
}

// Scratch dirs are now shared caches, not per-session state. Tearing one
// session down must not delete a directory other live sessions are using
// as their CLAUDE_CONFIG_DIR.
func TestCleanupWorkerScratchConfigDir_LeavesSharedDirIntact(t *testing.T) {
	withTelegramConductorPresent(t)
	scratchTestHome(t)
	source := makeProfileDir(t)

	stopping := &Instance{ID: "00000000-0000-0000-0000-000000000f01", Tool: "claude", Title: "worker-a"}
	running := &Instance{ID: "00000000-0000-0000-0000-000000000f02", Tool: "claude", Title: "worker-b"}

	shared, err := stopping.EnsureWorkerScratchConfigDir(source)
	if err != nil {
		t.Fatalf("ensure stopping: %v", err)
	}
	stopping.WorkerScratchConfigDir = shared
	stillUsed, err := running.EnsureWorkerScratchConfigDir(source)
	if err != nil {
		t.Fatalf("ensure running: %v", err)
	}
	running.WorkerScratchConfigDir = stillUsed

	stopping.CleanupWorkerScratchConfigDir()

	if _, err := os.Stat(filepath.Join(stillUsed, "settings.json")); err != nil {
		t.Errorf("cleanup of one session destroyed the shared scratch dir still in use by another: %v", err)
	}
	if stopping.WorkerScratchConfigDir != "" {
		t.Errorf("cleanup must still detach the stopping session from the scratch dir, got %q",
			stopping.WorkerScratchConfigDir)
	}
}
