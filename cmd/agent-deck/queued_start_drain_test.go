package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/core"
	"github.com/asheshgoplani/agent-deck/internal/sendqueue"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// `session stop` drains the group queue through drainGroupQueue. The drained
// claude session must get the launch path's folder-trust seed, and its queued
// -m message must go to the send queue, once.
func TestDrainGroupQueue_SeedsTrustAndDeliversQueuedMessageOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	projectDir := t.TempDir()
	queued := session.NewInstanceWithGroup("worker", projectDir, "workers")
	queued.Tool = "claude"
	queued.MarkQueued("brief")
	var sent []string
	t.Cleanup(session.SetQueuedSpawnForTest(func(i *session.Instance, message string) error {
		sent = append(sent, message)
		i.Status = session.StatusStarting
		return nil
	}))

	storage, err := session.NewStorageWithProfile(fmt.Sprintf("_cmd_queue_drain_%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	instances := []*session.Instance{queued}
	groups := []*session.GroupData{{Name: "workers", Path: "workers", MaxConcurrent: 1}}
	if err := saveSessionData(storage, instances, groups); err != nil {
		t.Fatal(err)
	}
	var workers []string
	t.Cleanup(core.SetSendWorkerSpawnForTest(func(_, id string) error { workers = append(workers, id); return nil }))
	if got := drainGroupQueue(storage, "workers", instances, groups); got != queued {
		t.Fatalf("drained %v, want the queued worker", got)
	}
	if got := drainGroupQueue(storage, "workers", instances, groups); got != nil {
		t.Fatalf("second drain started %v", got.Title)
	}
	if len(sent) != 1 || sent[0] != "" || queued.QueuedMessage != "" {
		t.Fatalf("spawns %q, stored %q; want one spawn typing nothing, message cleared", sent, queued.QueuedMessage)
	}
	recs, err := sendqueue.List(sendQueueDir(storage), queued.ID)
	if err != nil || len(recs) != 1 || recs[0].Message != "brief" || len(workers) != 1 || workers[0] != queued.ID {
		t.Fatalf("queued sends %+v (err %v), workers %q; want one \"brief\" and one worker", recs, err, workers)
	}
	row, err := storage.GetDB().LoadInstanceByID(queued.ID)
	if err != nil || row == nil || row.Status != string(session.StatusStarting) || session.ReadQueuedMessageFromToolData(row.ToolData) != "" {
		t.Fatalf("drained row not persisted as starting with the message cleared: %+v %v", row, err)
	}
	assertLaunchTrustSeeded(t, filepath.Join(home, ".claude.json"), projectDir)
}

// `session stop` saves before the drain: a restart of the stopped session by
// another process during the drained agent's readiness wait must survive, and
// the stop must already be on disk when the spawn starts.
func TestStopInstanceAndSave_DrainKeepsConcurrentRestart(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	storage, err := session.NewStorageWithProfile(fmt.Sprintf("_cmd_stop_drain_%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()

	busy := session.NewInstanceWithGroup(fmt.Sprintf("busy-%d", time.Now().UnixNano()), t.TempDir(), "workers")
	busy.Command = ""
	if err := busy.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = busy.Kill() })
	for deadline := time.Now().Add(3 * time.Second); !busy.Exists() && time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
	}
	busy.Status = session.StatusRunning
	next := session.NewInstanceWithGroup("next", t.TempDir(), "workers")
	next.MarkQueued("brief")
	next.SetTmuxSessionForTest(nil)
	instances := []*session.Instance{busy, next}
	groups := []*session.GroupData{{Name: "workers", Path: "workers", MaxConcurrent: 1}}
	if err := saveSessionData(storage, instances, groups); err != nil {
		t.Fatal(err)
	}

	peer, err := session.NewStorageWithProfile(storage.Profile())
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	var busyAtSpawn string
	t.Cleanup(session.SetQueuedSpawnForTest(func(i *session.Instance, _ string) error {
		if row, err := peer.GetDB().LoadInstanceByID(busy.ID); err == nil && row != nil {
			busyAtSpawn = row.Status
		}
		if err := peer.GetDB().WriteStatus(busy.ID, string(session.StatusRunning), busy.Tool); err != nil {
			t.Errorf("concurrent restart: %v", err)
		}
		i.Status = session.StatusStarting
		return nil
	}))

	t.Cleanup(core.SetSendWorkerSpawnForTest(func(string, string) error { return nil }))

	drained, err := stopInstanceAndSave(storage, busy, instances, groups)
	if err != nil || drained != next {
		t.Fatalf("stop: drained %v, err %v", drained, err)
	}
	if busyAtSpawn != string(session.StatusStopped) {
		t.Errorf("stopped session's row during the spawn = %q, want stopped", busyAtSpawn)
	}
	row, err := peer.GetDB().LoadInstanceByID(busy.ID)
	if err != nil || row == nil {
		t.Fatalf("load stopped session: %v", err)
	}
	if row.Status != string(session.StatusRunning) {
		t.Errorf("restart during the drain was overwritten with %q", row.Status)
	}
}

// A `session stop` whose claim on the oldest queued session is lost to
// another drainer starts the next queued one instead, while the group is
// below its cap.
func TestDrainGroupQueue_ClaimLoserStartsTheNextQueued(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storage, err := session.NewStorageWithProfile(fmt.Sprintf("_cmd_drain_next_%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	older := session.NewInstanceWithGroup("older", t.TempDir(), "workers")
	older.MarkQueued("")
	newer := session.NewInstanceWithGroup("newer", t.TempDir(), "workers")
	newer.MarkQueued("")
	newer.CreatedAt = older.CreatedAt.Add(time.Second)
	instances := []*session.Instance{older, newer}
	groups := []*session.GroupData{{Name: "workers", Path: "workers", MaxConcurrent: 2}}
	if err := saveSessionData(storage, instances, groups); err != nil {
		t.Fatal(err)
	}
	// Another process claims the older one first.
	if _, err := storage.GetDB().WriteStatusIfCurrent(older.ID, string(session.StatusQueued), string(session.StatusStarting), older.Tool); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.SetQueuedSpawnForTest(func(i *session.Instance, _ string) error {
		i.Status = session.StatusStarting
		return nil
	}))
	if got := drainGroupQueue(storage, "workers", instances, groups); got != newer {
		t.Fatalf("drained %v, want newer", got)
	}
}

// ... but not past the cap: with max_concurrent=1 the session another drainer
// claimed holds the only slot, so nothing else is started.
func TestDrainGroupQueue_ClaimLoserRespectsTheCap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storage, err := session.NewStorageWithProfile(fmt.Sprintf("_cmd_drain_cap_%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	older := session.NewInstanceWithGroup("older", t.TempDir(), "workers")
	older.MarkQueued("")
	newer := session.NewInstanceWithGroup("newer", t.TempDir(), "workers")
	newer.MarkQueued("")
	newer.CreatedAt = older.CreatedAt.Add(time.Second)
	instances := []*session.Instance{older, newer}
	groups := []*session.GroupData{{Name: "workers", Path: "workers", MaxConcurrent: 1}}
	if err := saveSessionData(storage, instances, groups); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.GetDB().WriteStatusIfCurrent(older.ID, string(session.StatusQueued), string(session.StatusStarting), older.Tool); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.SetQueuedSpawnForTest(func(i *session.Instance, _ string) error {
		i.Status = session.StatusStarting
		return nil
	}))
	if got := drainGroupQueue(storage, "workers", instances, groups); got != nil {
		t.Fatalf("drained %s past the cap of 1", got.Title)
	}
}
