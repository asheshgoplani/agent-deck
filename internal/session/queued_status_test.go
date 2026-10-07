package session

import (
	"testing"
	"time"
)

// TestUpdateStatusPreservesQueued: a queued session with no tmux pane stays
// queued across fresh/old, nil/non-nil tmux handle and new/reloaded instances,
// and is never sampled as a live pane (issue #2526).
func TestUpdateStatusPreservesQueued(t *testing.T) {
	for _, fresh := range []bool{true, false} {
		for _, handle := range []bool{true, false} {
			for _, reloaded := range []bool{true, false} {
				name := "old"
				if fresh {
					name = "fresh"
				}
				if handle {
					name += "/tmux-handle"
				} else {
					name += "/nil-handle"
				}
				if reloaded {
					name += "/reloaded"
				} else {
					name += "/new"
				}
				t.Run(name, func(t *testing.T) {
					inst := NewInstanceWithTool("queued-test", t.TempDir(), "shell")
					inst.Status = StatusQueued
					if !fresh {
						inst.CreatedAt = time.Now().Add(-time.Minute)
					}
					if !handle {
						inst.tmuxSession = nil
					}
					if reloaded {
						inst.addedThisProcess = false
					}
					if err := inst.UpdateStatus(); err != nil {
						t.Fatal(err)
					}
					if got := inst.GetStatusThreadSafe(); got != StatusQueued {
						t.Fatalf("queued became %s", got)
					}
					if _, _, sampled := inst.LiveStatusPrior(); sampled {
						t.Fatal("queued was sampled as a live pane")
					}
				})
			}
		}
	}
}

// TestQueuedSessionStartsWhenReleased: once Start creates the pane, the next
// status pass leaves the queue and does not report an error.
func TestQueuedSessionStartsWhenReleased(t *testing.T) {
	skipIfNoTmuxBinary(t)
	inst := NewInstanceWithTool("queued-release", t.TempDir(), "shell")
	inst.Status = StatusQueued
	if err := inst.Start(); err != nil {
		t.Fatal(err)
	}
	defer inst.Kill()
	if err := inst.UpdateStatus(); err != nil {
		t.Fatal(err)
	}
	switch got := inst.GetStatusThreadSafe(); got {
	case StatusQueued:
		t.Fatal("Start did not release queued state")
	case StatusError:
		t.Fatal("released queued session reported error")
	}
	if !inst.Exists() {
		t.Fatal("released queued session has no tmux pane")
	}
}

// TestDaemonPreservesQueuedOverLivePrior: with no live TUI, the transition
// daemon must not let a carried running prior overwrite a persisted queued
// status, in the database or in its own observed status (issue #2526).
func TestDaemonPreservesQueuedOverLivePrior(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	storage, err := NewStorageWithProfile("default")
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	inst := NewInstanceWithTool("queued-prior", t.TempDir(), "shell")
	inst.CreatedAt = time.Now().Add(-time.Minute)
	inst.Status = StatusQueued
	if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatal(err)
	}
	d := NewTransitionDaemon()
	d.storages["default"] = storage
	d.livePrior["default"] = map[string]liveStatusPrior{inst.ID: {status: StatusRunning}}
	defer d.Flush()
	d.syncProfile("default")
	rows, err := storage.GetDB().ReadAllStatuses()
	if err != nil {
		t.Fatal(err)
	}
	if got := rows[inst.ID].Status; got != string(StatusQueued) {
		t.Fatalf("persisted queued overwritten by live prior: got %s", got)
	}
	if got := d.lastStatus["default"][inst.ID]; got != string(StatusQueued) {
		t.Fatalf("daemon observed %s, want queued", got)
	}
}
