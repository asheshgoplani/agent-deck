package session

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Issue #2899: the daemon half of the Codex completion path. codex-notify now
// persists a sentinel from last-assistant-message as done_status/done_summary
// in the Codex hook status file; the daemon must read that file for a Codex
// child and commit a kind=finished record carrying the outcome to the parent,
// the same record a Claude child produces.
func TestIssue2899_CodexChildDoneFieldsEmitFinishedRecord(t *testing.T) {
	profile := "_test-2899-codex-finished"
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	ResetInboxFingerprintCacheForTest()
	t.Cleanup(func() {
		ClearUserConfigCache()
		ResetInboxFingerprintCacheForTest()
	})
	if err := os.MkdirAll(filepath.Join(home, ".agent-deck"), 0o700); err != nil {
		t.Fatal(err)
	}
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	defer storage.Close()
	now := time.Now()
	parent := &Instance{ID: "parent-2899", Title: "conductor-2899", ProjectPath: "/tmp/p2899",
		GroupPath: DefaultGroupPath, Tool: "claude", Status: StatusIdle, CreatedAt: now}
	child := &Instance{ID: "child-codex-2899", Title: "codex-worker", ProjectPath: "/tmp/c2899",
		GroupPath: DefaultGroupPath, ParentSessionID: parent.ID, Tool: "codex", Status: StatusWaiting, CreatedAt: now}
	if err := storage.SaveWithGroups([]*Instance{parent, child}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}

	// The file codex-notify writes for an agent-turn-complete with a sentinel.
	hooksDir := GetHooksDir()
	if err := os.MkdirAll(hooksDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"status":"waiting","session_id":"thr-2899","event":"agent-turn-complete","ts":` +
		strconv.FormatInt(now.Unix(), 10) + `,"done_status":"ok","done_summary":"fixed the parser"}`
	if err := os.WriteFile(filepath.Join(hooksDir, child.ID+".json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	hs := readHookStatusFile(child.ID)
	if hs == nil {
		t.Fatal("readHookStatusFile returned nil for the Codex hook file")
	}

	d := NewTransitionDaemon()
	t.Cleanup(d.notifier.Close)
	byID := map[string]*Instance{parent.ID: parent, child.ID: child}
	d.emitDoneSignals(profile, byID, map[string]*HookStatus{child.ID: hs})
	d.notifier.Flush()

	inbox := readInboxLines(t, parent.ID)
	if len(inbox) != 1 {
		t.Fatalf("parent inbox has %d records, want 1 finished record: %+v", len(inbox), inbox)
	}
	ev := inbox[0]
	if ev.Kind != transitionKindFinished || ev.DoneStatus != "ok" || ev.DoneSummary != "fixed the parser" {
		t.Fatalf("record kind=%q done=(%q, %q), want finished (ok, fixed the parser)", ev.Kind, ev.DoneStatus, ev.DoneSummary)
	}
}
