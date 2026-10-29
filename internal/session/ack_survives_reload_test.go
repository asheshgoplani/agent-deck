package session

import (
	"encoding/json"
	"os"
	"path/filepath"
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

// writeAckHookFile writes a Stop hook record with the given whole-second
// "ts" and, when receivedAt is set, the handler's sub-second received_at_ns.
func writeAckHookFile(t *testing.T, inst *Instance, ts, receivedAt time.Time) {
	t.Helper()
	record := map[string]any{
		"status":     "waiting",
		"session_id": "owner-session",
		"event":      "Stop",
		"cwd":        inst.ProjectPath,
		"ts":         ts.Unix(),
	}
	if !receivedAt.IsZero() {
		record["received_at_ns"] = receivedAt.UnixNano()
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal hook record: %v", err)
	}
	path := filepath.Join(GetHooksDir(), inst.ID+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir hooks dir: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write hook file: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
}

// The hook "ts" is whole seconds but the visit time is milliseconds. A Stop
// later in the same second as the visit is a turn the user has not seen: the
// handler's received time orders the two.
func TestColdHookRead_SameSecondStopAfterVisitClearsAcknowledgment(t *testing.T) {
	inst := newIssue2361Instance(t, "test-ack-cold-same-second")
	second := time.Unix(time.Now().Add(-time.Minute).Unix(), 0)
	inst.tmuxSession.Acknowledge()
	inst.acknowledgedAt = second.Add(200 * time.Millisecond)

	writeAckHookFile(t, inst, second, second.Add(700*time.Millisecond))

	if err := inst.UpdateStatus(); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if inst.tmuxSession.IsAcknowledged() {
		t.Fatal("a Stop received after the visit, in the same second, kept the acknowledgment")
	}
}

// Without a received time, a Stop in the same second as the visit cannot be
// ordered. It counts as unseen: a seen turn shown as new beats a new turn
// shown as seen.
func TestColdHookRead_SameSecondStopWithoutReceivedTimeClearsAcknowledgment(t *testing.T) {
	inst := newIssue2361Instance(t, "test-ack-cold-same-second-legacy")
	second := time.Unix(time.Now().Add(-time.Minute).Unix(), 0)
	inst.tmuxSession.Acknowledge()
	inst.acknowledgedAt = second.Add(200 * time.Millisecond)

	writeAckHookFile(t, inst, second, time.Time{})

	if err := inst.UpdateStatus(); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if inst.tmuxSession.IsAcknowledged() {
		t.Fatal("a same-second Stop with no received time kept the acknowledgment")
	}
}

// The Codex fast path turns a fresh "waiting" hook into waiting and clears
// the acknowledgment. A completion the user already saw (older than the
// visit) must not, or a rebuilt seen session shows waiting for the fast-path
// window.
func TestCodexWaitingFastPath_HookOlderThanVisitKeepsSeen(t *testing.T) {
	skipIfNoTmuxBinary(t)
	t.Setenv("HOME", t.TempDir())

	inst := NewInstanceWithTool("codex-ack-older", t.TempDir(), "codex")
	if err := inst.tmuxSession.Start("sleep 300"); err != nil {
		t.Fatalf("tmux start: %v", err)
	}
	t.Cleanup(func() { _ = inst.tmuxSession.Kill() })
	inst.tmuxSession.Acknowledge()
	inst.acknowledgedAt = time.Now()

	inst.hookStatus = "waiting"
	inst.hookEvent = "Stop"
	inst.hookLastUpdate = time.Now().Add(-2 * time.Second)

	if err := inst.UpdateStatus(); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if !inst.tmuxSession.IsAcknowledged() {
		t.Fatal("Codex fast path cleared the acknowledgment for a completion older than the visit")
	}
	if got := inst.GetStatusThreadSafe(); got == StatusWaiting {
		t.Fatalf("status = %q for a completion the user already saw, want not waiting", got)
	}
}
