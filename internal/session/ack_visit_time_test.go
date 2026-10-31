package session

import (
	"testing"
	"time"
)

// clearHookCacheForColdRead drops the in-memory hook state the way the TUI's
// detach refresh does, so the next UpdateStatus reads the hook file cold.
func clearHookCacheForColdRead(inst *Instance) {
	inst.mu.Lock()
	inst.hookStatus = ""
	inst.hookEvent = ""
	inst.hookLastUpdate = time.Time{}
	inst.mu.Unlock()
}

// #2950 review item 1: load with an old visit, a Stop arrives, the user opens
// the session (attach ack), then the detach refresh reads the hook cold. The
// Stop is older than the new visit, so the session stays seen.
func TestAcknowledgeVisit_ColdReadAfterAttachStaysSeen(t *testing.T) {
	inst := newIssue2361Instance(t, "test-ack-visit-attach")
	inst.tmuxSession.Acknowledge()
	inst.acknowledgedAt = time.Now().Add(-10 * time.Minute)

	stopAt := time.Unix(time.Now().Add(-time.Minute).Unix(), 0)
	writeIssue2361HookFile(t, inst.ID, "waiting", "Stop", "owner-session", inst.ProjectPath, stopAt)
	if err := inst.UpdateStatus(); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if inst.tmuxSession.IsAcknowledged() {
		t.Fatal("precondition: a Stop newer than the loaded visit is unseen")
	}

	before := time.Now()
	inst.AcknowledgeVisit()
	if got := inst.AcknowledgedAt(); got.Before(before) {
		t.Fatalf("AcknowledgedAt = %v after AcknowledgeVisit, want >= %v", got, before)
	}

	clearHookCacheForColdRead(inst)
	if err := inst.UpdateStatus(); err != nil {
		t.Fatalf("UpdateStatus after detach: %v", err)
	}
	if !inst.tmuxSession.IsAcknowledged() {
		t.Fatal("cold read after the attach ack reset it: the visit time stayed at its load value")
	}
}

// An ack synced from another TUI carries that TUI's visit time, so the same
// cold read here also sees the Stop as already seen. An older shared time
// never moves the visit back.
func TestSetAcknowledgedFromSharedAt_RecordsVisitTime(t *testing.T) {
	inst := newIssue2361Instance(t, "test-ack-visit-shared")
	inst.Status = StatusWaiting
	inst.acknowledgedAt = time.Now().Add(-10 * time.Minute)

	shared := time.Now().Add(-time.Second).Truncate(time.Millisecond)
	inst.SetAcknowledgedFromSharedAt(shared)
	if !inst.tmuxSession.IsAcknowledged() {
		t.Fatal("shared ack not applied")
	}
	if got := inst.AcknowledgedAt(); !got.Equal(shared) {
		t.Fatalf("AcknowledgedAt = %v, want the shared visit %v", got, shared)
	}

	inst.SetAcknowledgedFromSharedAt(shared.Add(-time.Hour))
	if got := inst.AcknowledgedAt(); !got.Equal(shared) {
		t.Fatalf("an older shared visit moved AcknowledgedAt back to %v", got)
	}

	// Running is authoritative: no ack, no visit time.
	running := newIssue2361Instance(t, "test-ack-visit-shared-running")
	running.Status = StatusRunning
	running.SetAcknowledgedFromSharedAt(shared)
	if !running.AcknowledgedAt().IsZero() {
		t.Fatal("a shared ack refused for a running session still set the visit time")
	}
}

// loadAckedSession saves one session with the given stored status, marks it
// seen in SQLite and reloads it.
func loadAckedSession(t *testing.T, status Status) *Instance {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	storage, err := NewStorageWithProfile("_ack_visit_time")
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	dir := t.TempDir()
	inst := NewInstanceWithTool("ack-visit-load", dir, "claude")
	inst.Status = status
	insts := []*Instance{inst}
	if err := storage.SaveWithGroups(insts, NewGroupTree(insts)); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}
	if err := storage.GetDB().SetAcknowledged(inst.ID, true); err != nil {
		t.Fatalf("SetAcknowledged: %v", err)
	}
	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	for _, in := range loaded {
		if in.ID == inst.ID && in.GetTmuxSession() != nil {
			return in
		}
	}
	t.Fatal("instance or its tmux session missing after reload")
	return nil
}

// The load wires the stored visit time into the instance.
func TestLoadWithGroups_RestoresVisitTime(t *testing.T) {
	before := time.Now().Add(-time.Second)
	got := loadAckedSession(t, StatusWaiting)
	if at := got.AcknowledgedAt(); at.Before(before) || at.After(time.Now().Add(time.Second)) {
		t.Fatalf("AcknowledgedAt after load = %v, want close to now", at)
	}
}

// The load-time ack follows the same rule as the shared-ack sync: a session
// stored as running or starting is not forced to seen.
func TestLoadWithGroups_StoredRunningIsNotAcknowledged(t *testing.T) {
	for _, status := range []Status{StatusRunning, StatusStarting} {
		t.Run(string(status), func(t *testing.T) {
			got := loadAckedSession(t, status)
			if got.GetTmuxSession().IsAcknowledged() {
				t.Fatalf("stored %s session rebuilt as seen", status)
			}
		})
	}
}
