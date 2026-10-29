package session

import (
	"os"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// A storage reload rebuilds every session. A session the user already looked
// at must come back as seen: the rebuild used to derive the flag from the
// stored status alone ("waiting" -> unseen) and ignore the acknowledged
// column, so the sidebar dot went back to waiting on every reload.
func TestLoadWithGroups_RebuildKeepsSavedAcknowledgment(t *testing.T) {
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", t.TempDir())
	ClearUserConfigCache()
	t.Cleanup(func() {
		os.Setenv("HOME", origHome)
		ClearUserConfigCache()
	})

	storage, err := NewStorageWithProfile("_ack_survives_reload")
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	dir := t.TempDir()
	inst := NewInstanceWithTool("seen-session", dir, "claude")
	inst.tmuxSession = tmux.NewSession(inst.Title, dir)
	inst.Status = StatusWaiting

	insts := []*Instance{inst}
	if err := storage.SaveWithGroups(insts, NewGroupTree(insts)); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}
	// The user opens the session: the TUI records the visit in SQLite before
	// its next status save has turned the stored "waiting" into "idle".
	if err := storage.GetDB().SetAcknowledged(inst.ID, true); err != nil {
		t.Fatalf("SetAcknowledged: %v", err)
	}

	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	var got *Instance
	for _, in := range loaded {
		if in.ID == inst.ID {
			got = in
		}
	}
	if got == nil || got.GetTmuxSession() == nil {
		t.Fatal("instance or its tmux session missing after reload")
	}
	if !got.GetTmuxSession().IsAcknowledged() {
		t.Fatal("rebuilt session is unseen although SQLite says acknowledged: the reload undid the user's visit")
	}
}

// After a reload the session has no hook status yet, so its first pass reads
// the hook file from disk. A Stop recorded BEFORE the user's visit is the turn
// the user already saw and must not clear the acknowledgment.
func TestColdHookRead_StopOlderThanVisitKeepsAcknowledgment(t *testing.T) {
	inst := newIssue2361Instance(t, "test-ack-cold-older-stop")
	inst.tmuxSession.Acknowledge()
	inst.acknowledgedAt = time.Now()

	stopAt := time.Unix(time.Now().Add(-10*time.Minute).Unix(), 0)
	writeIssue2361HookFile(t, inst.ID, "waiting", "Stop", "owner-session", inst.ProjectPath, stopAt)

	if err := inst.UpdateStatus(); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if !inst.tmuxSession.IsAcknowledged() {
		t.Fatal("cold hook read of a Stop older than the visit cleared the acknowledgment")
	}
}

// A Stop recorded AFTER the visit is a turn the user has not seen yet, for
// example one that finished while agent-deck was closed. It still clears the
// acknowledgment.
func TestColdHookRead_StopNewerThanVisitClearsAcknowledgment(t *testing.T) {
	inst := newIssue2361Instance(t, "test-ack-cold-newer-stop")
	inst.tmuxSession.Acknowledge()
	inst.acknowledgedAt = time.Now().Add(-10 * time.Minute)

	stopAt := time.Unix(time.Now().Add(-time.Minute).Unix(), 0)
	writeIssue2361HookFile(t, inst.ID, "waiting", "Stop", "owner-session", inst.ProjectPath, stopAt)

	if err := inst.UpdateStatus(); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if inst.tmuxSession.IsAcknowledged() {
		t.Fatal("cold hook read of a Stop newer than the visit kept the acknowledgment: an unseen turn would show as seen")
	}
}
