package ui

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Pressing `u` (mark unread) clears the acknowledged flag with a targeted
// write, flips the in-memory status to waiting, and saves.
//
// The targeted write moves last_modified. The save's external-change guard
// compares last_modified against the value captured at load, so without
// adoptOwnWrite the guard fires on this TUI's OWN bump: the save aborts and
// schedules a reload instead, the reload rebuilds acknowledged from the stored
// status -- still "idle", because the aborted save was the one that would have
// written "waiting" -- and the row goes straight back to gray. The key flashes
// yellow and reverts, however many times it is pressed.
//
// This exercises the same sequence as the `u` case in updateInner, minus the
// tmux session it needs to reach that code.
func TestMarkUnread_SaveIsNotAbortedByItsOwnWrite(t *testing.T) {
	h, storage := newHeadlessHomeForTest(t, "_test_mark_unread_save")

	inst := &session.Instance{
		ID:          "mark-unread-001",
		Title:       "worker",
		ProjectPath: "/tmp/mark-unread-proj",
		GroupPath:   session.DefaultGroupPath,
		Command:     "claude",
		Tool:        "claude",
		Status:      session.StatusIdle,
		CreatedAt:   time.Now(),
	}
	all := []*session.Instance{inst}
	if err := storage.SaveWithGroups(all, session.NewGroupTree(all)); err != nil {
		t.Fatalf("seed SaveWithGroups: %v", err)
	}
	h.instances = all
	h.instanceByID[inst.ID] = inst
	h.groupTree = session.NewGroupTree(all)

	// The TUI's freshness marker as of its last load.
	loaded, err := storage.GetFileMtime()
	if err != nil {
		t.Fatalf("GetFileMtime: %v", err)
	}
	h.lastLoadMtime = loaded

	// --- what the `u` key does ---
	stamps, err := storage.GetDB().SetAcknowledgedStamped(inst.ID, false)
	if err != nil {
		t.Fatalf("SetAcknowledgedStamped: %v", err)
	}
	if !h.adoptOwnWrite(stamps, "mark_unread") {
		t.Fatal("adoptOwnWrite refused our own write: nothing else touched this database")
	}
	inst.Status = session.StatusWaiting
	h.saveInstances()
	// --- end ---

	rows, err := storage.GetDB().LoadInstances()
	if err != nil {
		t.Fatalf("LoadInstances: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("LoadInstances returned %d rows, want 1", len(rows))
	}
	if rows[0].Status != string(session.StatusWaiting) {
		t.Errorf("stored status = %q, want %q: the save aborted on this TUI's own acknowledged "+
			"write, so a reload restores the session as acknowledged (gray) and mark-unread "+
			"appears to do nothing", rows[0].Status, session.StatusWaiting)
	}
}

// The guard must still fire for a real external write. Adopting unconditionally
// would trade a lost mark-unread for a lost update: this TUI would overwrite
// whatever the other process changed while it was stale.
func TestMarkUnread_ForeignWriteStillAbortsTheSave(t *testing.T) {
	h, storage := newHeadlessHomeForTest(t, "_test_mark_unread_foreign")

	inst := &session.Instance{
		ID:          "mark-unread-002",
		Title:       "worker",
		ProjectPath: "/tmp/mark-unread-proj",
		GroupPath:   session.DefaultGroupPath,
		Command:     "claude",
		Tool:        "claude",
		Status:      session.StatusIdle,
		CreatedAt:   time.Now(),
	}
	all := []*session.Instance{inst}
	if err := storage.SaveWithGroups(all, session.NewGroupTree(all)); err != nil {
		t.Fatalf("seed SaveWithGroups: %v", err)
	}
	h.instances = all
	h.instanceByID[inst.ID] = inst
	h.groupTree = session.NewGroupTree(all)

	loaded, err := storage.GetFileMtime()
	if err != nil {
		t.Fatalf("GetFileMtime: %v", err)
	}
	h.lastLoadMtime = loaded

	// Another process writes after we loaded.
	if err := storage.GetDB().Touch(); err != nil {
		t.Fatalf("Touch: %v", err)
	}

	stamps, err := storage.GetDB().SetAcknowledgedStamped(inst.ID, false)
	if err != nil {
		t.Fatalf("SetAcknowledgedStamped: %v", err)
	}
	if h.adoptOwnWrite(stamps, "mark_unread") {
		t.Error("adopted the marker past another process's write: the next save would revert " +
			"whatever that process changed")
	}
}
