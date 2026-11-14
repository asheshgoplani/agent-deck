package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// `session output` read the last reply from the process-wide Claude config dir,
// so a session started on an account slot (its own config_dir) never found its
// own transcript and fell back to scraping the pane. These tests pin that the
// last-response readers resolve the config dir per instance, and never read
// another dir's transcript when the slot's own is missing.

const slotLastResponseConfig = `
[profiles.slotx.claude]
config_dir = "~/.claude-slotx"

[profiles.sloty.claude]
config_dir = "~/.claude-sloty"
`

func slotLastResponseWrite(t *testing.T, configDir string, inst *Instance, sessionID, reply string, mod time.Time) string {
	t.Helper()
	resolved := inst.ProjectPath
	if r, err := filepath.EvalSymlinks(resolved); err == nil {
		resolved = r
	}
	dir := filepath.Join(configDir, "projects", ConvertToClaudeDirName(resolved))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, sessionID+".jsonl")
	body := fmt.Sprintf(
		`{"type":"user","sessionId":%q,"message":{"role":"user","content":"count to 3"},"timestamp":"2026-01-01T00:00:00Z","uuid":"u1"}`+"\n"+
			`{"type":"assistant","sessionId":%q,"message":{"role":"assistant","content":[{"type":"text","text":%q}]},"timestamp":"2026-01-01T00:00:01Z","uuid":"a1"}`+"\n",
		sessionID, sessionID, reply)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	if !mod.IsZero() {
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}
	return path
}

func slotLastResponseInstance(t *testing.T, home, sessionID, account string) *Instance {
	t.Helper()
	inst := issue2769Instance(t, home, sessionID)
	inst.Account = account
	return inst
}

func TestSlotLastResponse_ReadsTranscriptFromSlotConfigDir(t *testing.T) {
	home := withTempAgentDeckHome(t, slotLastResponseConfig)
	inst := slotLastResponseInstance(t, home, "slot-own-id", "slotx")
	slotLastResponseWrite(t, filepath.Join(home, ".claude-slotx"), inst, inst.ClaudeSessionID, "1 2 3", time.Time{})

	resp, err := inst.getClaudeLastResponse()
	if err != nil {
		t.Fatalf("getClaudeLastResponse: %v", err)
	}
	if resp.Content != "1 2 3" {
		t.Fatalf("Content = %q, want the slot's reply %q", resp.Content, "1 2 3")
	}
}

func TestSlotLastResponse_SlotWinsOverDefaultDirWithSameID(t *testing.T) {
	home := withTempAgentDeckHome(t, slotLastResponseConfig)
	inst := slotLastResponseInstance(t, home, "slot-shared-id", "slotx")
	slotLastResponseWrite(t, filepath.Join(home, ".claude-slotx"), inst, inst.ClaudeSessionID, "from slot", time.Time{})
	slotLastResponseWrite(t, filepath.Join(home, ".claude"), inst, inst.ClaudeSessionID, "from default", time.Time{})

	resp, err := inst.getClaudeLastResponse()
	if err != nil {
		t.Fatalf("getClaudeLastResponse: %v", err)
	}
	if resp.Content != "from slot" {
		t.Fatalf("Content = %q, want %q", resp.Content, "from slot")
	}
}

// A wrong transcript is worse than none: a slot session whose own transcript is
// missing must not be answered from the default dir or another slot.
func TestSlotLastResponse_NeverReadsDefaultOrOtherSlotWhenOwnMissing(t *testing.T) {
	home := withTempAgentDeckHome(t, slotLastResponseConfig)
	inst := slotLastResponseInstance(t, home, "slot-missing-id", "slotx")
	slotLastResponseWrite(t, filepath.Join(home, ".claude"), inst, inst.ClaudeSessionID, "from default", time.Time{})
	slotLastResponseWrite(t, filepath.Join(home, ".claude-sloty"), inst, inst.ClaudeSessionID, "from other slot", time.Time{})

	if resp, err := inst.getClaudeLastResponse(); err == nil {
		t.Fatalf("getClaudeLastResponse returned %q, want an error so the caller falls back", resp.Content)
	}
	if id, resp := inst.findLatestClaudeTranscriptOnDisk(); resp != nil {
		t.Fatalf("findLatestClaudeTranscriptOnDisk returned id %q reply %q from a foreign dir, want none", id, resp.Content)
	}
}

func TestSlotLastResponse_DiskScanFindsRolloverInSlotDir(t *testing.T) {
	home := withTempAgentDeckHome(t, slotLastResponseConfig)
	inst := slotLastResponseInstance(t, home, "slot-old-id", "slotx")
	now := time.Now()
	slotLastResponseWrite(t, filepath.Join(home, ".claude-slotx"), inst, "slot-new-id", "after clear", now)
	// A newer file in the default dir must be ignored for a slot session.
	slotLastResponseWrite(t, filepath.Join(home, ".claude"), inst, "default-newer-id", "wrong", now.Add(time.Minute))

	id, resp := inst.findLatestClaudeTranscriptOnDisk()
	if resp == nil {
		t.Fatal("findLatestClaudeTranscriptOnDisk found nothing in the slot dir")
	}
	if id != "slot-new-id" || resp.Content != "after clear" {
		t.Fatalf("got id %q reply %q, want slot-new-id / %q", id, resp.Content, "after clear")
	}
}

// Sessions without a slot behave exactly as before: the process-wide dir.
func TestSlotLastResponse_NoSlotSessionStillUsesProcessWideDir(t *testing.T) {
	home := withTempAgentDeckHome(t, slotLastResponseConfig)
	inst := slotLastResponseInstance(t, home, "noslot-id", "")
	slotLastResponseWrite(t, filepath.Join(home, ".claude"), inst, inst.ClaudeSessionID, "from default", time.Time{})
	slotLastResponseWrite(t, filepath.Join(home, ".claude-slotx"), inst, inst.ClaudeSessionID, "from slot", time.Time{})

	resp, err := inst.getClaudeLastResponse()
	if err != nil {
		t.Fatalf("getClaudeLastResponse: %v", err)
	}
	if resp.Content != "from default" {
		t.Fatalf("Content = %q, want %q", resp.Content, "from default")
	}
	id, scan := inst.findLatestClaudeTranscriptOnDisk()
	if scan == nil || id != "noslot-id" || scan.Content != "from default" {
		t.Fatalf("disk scan = %q %+v, want noslot-id / from default", id, scan)
	}
}

// A session with no slot whose group dir changed after its transcript was
// written keeps reading that transcript from the process-wide dir.
func TestSlotLastResponse_NoSlotGroupDirFallsBackToProcessWideDir(t *testing.T) {
	home := withTempAgentDeckHome(t, `
[groups."work".claude]
config_dir = "~/.claude-groupdir"
`)
	inst := slotLastResponseInstance(t, home, "group-old-id", "")
	slotLastResponseWrite(t, filepath.Join(home, ".claude"), inst, inst.ClaudeSessionID, "older reply", time.Time{})

	resp, err := inst.getClaudeLastResponse()
	if err != nil || resp.Content != "older reply" {
		t.Fatalf("got %+v, %v; want the process-wide transcript", resp, err)
	}
}
