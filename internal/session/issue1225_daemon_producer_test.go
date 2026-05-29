package session

// Audit B13 / GAP §6 — the interactive running→waiting producer was wired but
// never exercised end-to-end through the daemon's real entry point. A refactor
// to the lastStatus tracking or the ShouldNotifyTransition guard could silently
// disconnect it and no unit test would catch it. This integration test drives
// TransitionDaemon.syncProfile (NOT NotifyTransition directly) and asserts the
// transition actually lands in the busy parent's durable inbox.
//
// It uses the status-DB (tuiAlive) path so transitions are driven purely via
// WriteStatus — no tmux, and crucially NO `claude -p` (which is banned/billed).

import (
	"testing"
	"time"
)

func TestB13_DaemonSyncProfile_InteractiveTransitionCommitsToInbox(t *testing.T) {
	inboxTestHome(t)
	profile := "_test-daemon-producer"

	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer storage.Close()

	now := time.Now()
	parentID := "parent-daemon-1"
	child := &Instance{
		ID:              "child-daemon-1",
		Title:           "worker",
		ProjectPath:     "/tmp/c",
		GroupPath:       DefaultGroupPath,
		ParentSessionID: parentID,
		Tool:            "claude",
		Status:          StatusRunning,
		CreatedAt:       now,
	}
	parent := &Instance{
		ID:          parentID,
		Title:       "orchestrator", // NOT "conductor-" prefixed → resolve skips tmux UpdateStatus
		ProjectPath: "/tmp/p",
		GroupPath:   DefaultGroupPath,
		Tool:        "claude",
		Status:      StatusRunning, // busy parent — pull model must still deliver
		CreatedAt:   now,
	}
	if err := storage.SaveWithGroups([]*Instance{child, parent}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}

	db := storage.GetDB()
	if db == nil {
		t.Fatal("nil db")
	}
	// Make the TUI "alive" so syncProfile reads statuses from the DB instead of
	// calling inst.UpdateStatus() (which would need tmux and flip to error).
	if err := db.RegisterInstance(false); err != nil {
		t.Fatalf("register instance: %v", err)
	}
	if err := db.Heartbeat(); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if err := db.WriteStatus(child.ID, "running", "claude"); err != nil {
		t.Fatalf("seed child running: %v", err)
	}
	if err := db.WriteStatus(parent.ID, "running", "claude"); err != nil {
		t.Fatalf("seed parent running: %v", err)
	}

	d := NewTransitionDaemon()

	// Pass 1: prime the baseline (running observed, no transition emitted yet).
	d.syncProfile(profile)

	// The child finishes its turn while the parent is busy: running → waiting.
	if err := db.WriteStatus(child.ID, "waiting", "claude"); err != nil {
		t.Fatalf("write waiting: %v", err)
	}

	// Pass 2: the daemon observes the transition and (pull model) commits it to
	// the busy parent's durable outbox.
	d.syncProfile(profile)

	events, err := DrainInboxForParent(parentID)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	found := false
	for _, ev := range events {
		if ev.ChildSessionID == child.ID && ev.ToStatus == "waiting" {
			found = true
		}
	}
	if !found {
		t.Fatalf("interactive running→waiting did NOT commit to the busy parent's inbox via the daemon; drained %+v", events)
	}
}
