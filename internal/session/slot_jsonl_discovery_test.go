package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// discoverLatestClaudeJSONL read the process-wide Claude config dir, so a
// session on an account slot with an empty session id adopted the newest
// transcript of the default dir: the wrong conversation. These tests pin that
// the start and restart discovery look in the instance's own dir, and that a
// session without a slot is unchanged.

const slotDiscoveryConfig = `
[profiles.slotx.claude]
config_dir = "~/.claude-slotx"
`

const (
	slotDiscoveryDefaultID = "dddddddd-dddd-dddd-dddd-dddddddddddd"
	slotDiscoverySlotID    = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
)

func slotDiscoveryStage(t *testing.T, configDir, projectPath, id string, mod time.Time) {
	t.Helper()
	p := issue2769WriteTranscript(t, configDir, projectPath, id)
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

func slotDiscoveryInstance(t *testing.T, account string) (*Instance, string) {
	t.Helper()
	home := withTempAgentDeckHome(t, slotDiscoveryConfig)
	inst := issue2769Instance(t, home, "")
	inst.Account = account
	inst.ClaudeDetectedAt = time.Now().Add(-time.Hour)
	return inst, home
}

func TestSlotJSONLDiscovery_StartNeverAdoptsDefaultDirTranscript(t *testing.T) {
	inst, home := slotDiscoveryInstance(t, "slotx")
	now := time.Now()
	// The default dir's file is the newest one on disk.
	slotDiscoveryStage(t, filepath.Join(home, ".claude-slotx"), inst.ProjectPath, slotDiscoverySlotID, now.Add(-time.Hour))
	slotDiscoveryStage(t, filepath.Join(home, ".claude"), inst.ProjectPath, slotDiscoveryDefaultID, now)

	inst.ensureClaudeSessionIDFromDisk()
	if inst.ClaudeSessionID != slotDiscoverySlotID {
		t.Fatalf("start adopted %q, want the slot's own transcript %q", inst.ClaudeSessionID, slotDiscoverySlotID)
	}
}

func TestSlotJSONLDiscovery_RestartNeverAdoptsDefaultDirTranscript(t *testing.T) {
	inst, home := slotDiscoveryInstance(t, "slotx")
	now := time.Now()
	slotDiscoveryStage(t, filepath.Join(home, ".claude-slotx"), inst.ProjectPath, slotDiscoverySlotID, now.Add(-time.Hour))
	slotDiscoveryStage(t, filepath.Join(home, ".claude"), inst.ProjectPath, slotDiscoveryDefaultID, now)

	inst.ensureClaudeSessionIDFromDiskForRestart()
	if inst.ClaudeSessionID != slotDiscoverySlotID {
		t.Fatalf("restart adopted %q, want the slot's own transcript %q", inst.ClaudeSessionID, slotDiscoverySlotID)
	}
}

func TestSlotJSONLDiscovery_SlotWithNoOwnTranscriptAdoptsNothing(t *testing.T) {
	inst, home := slotDiscoveryInstance(t, "slotx")
	slotDiscoveryStage(t, filepath.Join(home, ".claude"), inst.ProjectPath, slotDiscoveryDefaultID, time.Now())

	inst.ensureClaudeSessionIDFromDisk()
	inst.ensureClaudeSessionIDFromDiskForRestart()
	if inst.ClaudeSessionID != "" {
		t.Fatalf("slot session with no transcript of its own adopted %q from the default dir", inst.ClaudeSessionID)
	}
}

func TestSlotJSONLDiscovery_SessionWithoutSlotUnchanged(t *testing.T) {
	inst, home := slotDiscoveryInstance(t, "")
	now := time.Now()
	slotDiscoveryStage(t, filepath.Join(home, ".claude-slotx"), inst.ProjectPath, slotDiscoverySlotID, now)
	slotDiscoveryStage(t, filepath.Join(home, ".claude"), inst.ProjectPath, slotDiscoveryDefaultID, now.Add(-time.Hour))

	inst.ensureClaudeSessionIDFromDisk()
	if inst.ClaudeSessionID != slotDiscoveryDefaultID {
		t.Fatalf("no-slot session adopted %q, want the process-wide dir's %q", inst.ClaudeSessionID, slotDiscoveryDefaultID)
	}
	inst.ClaudeSessionID = ""
	inst.ensureClaudeSessionIDFromDiskForRestart()
	if inst.ClaudeSessionID != slotDiscoveryDefaultID {
		t.Fatalf("no-slot restart adopted %q, want %q", inst.ClaudeSessionID, slotDiscoveryDefaultID)
	}
}
