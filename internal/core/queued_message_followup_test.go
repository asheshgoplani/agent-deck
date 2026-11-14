package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/sendqueue"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// quiesceBackgroundWriters keeps the writers a queued start leaves running
// out of t.TempDir's cleanup ("TempDir RemoveAll cleanup: ... directory not
// empty"). Call it right after HOME is set to a t.TempDir: its cleanup then
// runs after every cleanup registered later (the tmux kills) and before HOME
// is removed. It joins the spawn goroutines of every session the test started
// (ownership attribution and the fast-death watcher write under the data dir
// for up to the spawn window, and the instance that spawned is often one the
// test never holds), and turns the events bus off, as the sendqueue tests do:
// the send-queue enqueue publishes to it, and its writer appends from a
// background goroutine after the test returns.
func quiesceBackgroundWriters(t *testing.T) {
	t.Helper()
	t.Setenv("AGENTDECK_EVENTS_BUS", "off")
	t.Cleanup(session.RetireSpawnWatchersForTest)
}

// stubSendWorkers records the send workers started instead of spawning them.
func stubSendWorkers(t *testing.T) *[]string {
	t.Helper()
	var ids []string
	prev := spawnSendWorker
	spawnSendWorker = func(_, id string) error { ids = append(ids, id); return nil }
	t.Cleanup(func() { spawnSendWorker = prev })
	return &ids
}

func queuedSends(t *testing.T, profile, id string) []*sendqueue.Record {
	t.Helper()
	s := openStorage(t, profile)
	defer s.Close()
	recs, err := sendqueue.List(sendqueue.Dir(filepath.Dir(s.Path())), id)
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

// A queued session whose spawn fails keeps its -m message: it is held on the
// send queue for the session (delivered if it is started within the retry
// budget) and the drain says so.
func TestDrainSpawnFailureHoldsQueuedMessage(t *testing.T) {
	profile := seedQueued(t, "brief")
	groups := []*session.GroupData{{Name: "serial", Path: "serial", MaxConcurrent: 1}}
	instances := loadStore(t, profile)
	storage := openStorage(t, profile)
	defer storage.Close()
	workers := stubSendWorkers(t)
	t.Cleanup(session.SetQueuedSpawnForTest(func(*session.Instance, string) error { return errors.New("spawn failed") }))

	rs := &runState{}
	ctx := withRunState(context.Background(), rs)
	if got := drainGroupQueue(ctx, storage, "serial", instances, groups); got != nil {
		t.Fatalf("drained %s although its spawn failed", got.Title)
	}
	next := findByTitle(instances, "next")
	recs := queuedSends(t, profile, next.ID)
	if len(recs) != 1 || recs[0].Message != "brief" || len(*workers) != 1 {
		t.Fatalf("queued sends %+v, workers %q; want \"brief\" held for next", recs, *workers)
	}
	if w := strings.Join(rs.warnings, "\n"); !strings.Contains(w, "queued -m message held for next") || !strings.Contains(w, "within 30m0s") || !strings.Contains(w, `"brief"`) {
		t.Errorf("warnings = %q, want the held message named with its 30m window", w)
	}
}

// When the message cannot be queued at all, the drain says it was NOT
// delivered, with its first line, and does not put it back on the row.
func TestDrainUnqueueableMessageWarnsNotDelivered(t *testing.T) {
	profile := seedQueued(t, "brief line one\nsecond line")
	groups := []*session.GroupData{{Name: "serial", Path: "serial", MaxConcurrent: 1}}
	instances := loadStore(t, profile)
	storage := openStorage(t, profile)
	defer storage.Close()
	stubSendWorkers(t)
	// The send queue's directory is a file: the enqueue fails.
	if err := os.WriteFile(sendqueue.Dir(filepath.Dir(storage.Path())), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.SetQueuedSpawnForTest(func(i *session.Instance, _ string) error {
		i.Status = session.StatusStarting
		return nil
	}))

	rs := &runState{}
	if got := drainGroupQueue(withRunState(context.Background(), rs), storage, "serial", instances, groups); got == nil {
		t.Fatal("drained nothing")
	}
	if w := strings.Join(rs.warnings, "\n"); !strings.Contains(w, "queued -m message NOT delivered for next") || !strings.Contains(w, `"brief line one"`) {
		t.Errorf("warnings = %q, want NOT delivered with the first line", w)
	}
	if n := findByTitle(loadStore(t, profile), "next"); n == nil || n.QueuedMessage != "" {
		t.Errorf("stored row = %+v, want the message not put back", n)
	}
}

// A drainer whose claim on the oldest queued session is lost to another
// drainer moves on to the next queued one: its stop freed a slot too.
func TestDrainClaimLoserStartsTheNextQueued(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	quiesceBackgroundWriters(t)
	profile := fmt.Sprintf("_core_drain_next_%d", time.Now().UnixNano())
	groups := []*session.GroupData{{Name: "serial", Path: "serial", MaxConcurrent: 2}}
	older := session.NewInstanceWithGroup("older", t.TempDir(), "serial")
	older.MarkQueued("")
	older.SetTmuxSessionForTest(nil)
	newer := session.NewInstanceWithGroup("newer", t.TempDir(), "serial")
	newer.MarkQueued("")
	newer.CreatedAt = older.CreatedAt.Add(time.Second)
	newer.SetTmuxSessionForTest(nil)
	seedStore(t, profile, groups, older, newer)
	first, second := loadStore(t, profile), loadStore(t, profile)
	a, b := openStorage(t, profile), openStorage(t, profile)
	defer a.Close()
	defer b.Close()
	t.Cleanup(session.SetQueuedSpawnForTest(func(i *session.Instance, _ string) error {
		i.Status = session.StatusStarting
		return nil
	}))

	if got := drainGroupQueue(context.Background(), a, "serial", first, groups); got == nil || got.Title != "older" {
		t.Fatalf("first drainer started %v, want older", got)
	}
	if got := drainGroupQueue(context.Background(), b, "serial", second, groups); got == nil || got.Title != "newer" {
		t.Fatalf("second drainer started %v, want newer", got)
	}
}

// A queued session started directly (below the cap again) delivers its queued
// -m message through the send queue instead of dropping it.
func TestSessionStartOfQueuedSessionDeliversQueuedMessage(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	quiesceBackgroundWriters(t)
	profile := fmt.Sprintf("_core_start_queued_msg_%d", time.Now().UnixNano())
	inst := session.NewInstanceWithGroup(fmt.Sprintf("queued-%d", time.Now().UnixNano()), t.TempDir(), "serial")
	inst.Command = ""
	inst.MarkQueued("brief")
	seedStore(t, profile, []*session.GroupData{{Name: "serial", Path: "serial", MaxConcurrent: 1}}, inst)
	workers := stubSendWorkers(t)

	_, res := Invoke[SessionStartOut](context.Background(), testRegistry(t, Deps{}), IDSessionStart, SessionStartIn{Profile: profile, Session: inst.Title, NoWait: true})
	t.Cleanup(func() {
		if n := findByTitle(loadStore(t, profile), inst.Title); n != nil {
			_ = n.Kill()
		}
	})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	recs := queuedSends(t, profile, inst.ID)
	if len(recs) != 1 || recs[0].Message != "brief" || len(*workers) != 1 {
		t.Fatalf("queued sends %+v, workers %q; want the queued \"brief\" handed to the send queue", recs, *workers)
	}
	if n := findByTitle(loadStore(t, profile), inst.Title); n == nil || n.QueuedMessage != "" {
		t.Errorf("stored row still holds the queued message: %+v", n)
	}
}

// The drainer that lost its claim must not overrun the cap: the winner holds
// the only slot of a max_concurrent=1 group with the session it started.
func TestDrainClaimLoserRespectsTheCap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	quiesceBackgroundWriters(t)
	profile := fmt.Sprintf("_core_drain_cap_%d", time.Now().UnixNano())
	groups := []*session.GroupData{{Name: "serial", Path: "serial", MaxConcurrent: 1}}
	older := session.NewInstanceWithGroup("older", t.TempDir(), "serial")
	older.MarkQueued("")
	older.SetTmuxSessionForTest(nil)
	newer := session.NewInstanceWithGroup("newer", t.TempDir(), "serial")
	newer.MarkQueued("")
	newer.CreatedAt = older.CreatedAt.Add(time.Second)
	newer.SetTmuxSessionForTest(nil)
	seedStore(t, profile, groups, older, newer)
	first, second := loadStore(t, profile), loadStore(t, profile)
	a, b := openStorage(t, profile), openStorage(t, profile)
	defer a.Close()
	defer b.Close()
	t.Cleanup(session.SetQueuedSpawnForTest(func(i *session.Instance, _ string) error {
		i.Status = session.StatusStarting
		return nil
	}))

	if got := drainGroupQueue(context.Background(), a, "serial", first, groups); got == nil || got.Title != "older" {
		t.Fatalf("first drainer started %v, want older", got)
	}
	if got := drainGroupQueue(context.Background(), b, "serial", second, groups); got != nil {
		t.Fatalf("second drainer started %s past the cap of 1", got.Title)
	}
	if n := findByTitle(loadStore(t, profile), "newer"); n == nil || n.Status != session.StatusQueued {
		t.Errorf("newer = %v, want still queued", n)
	}
}

// A failed direct start of a queued session must not queue its brief: the
// row stays queued with it, and the drain that later starts the session
// queues it exactly once (a failed start used to queue one copy per attempt).
func TestFailedDirectStartThenDrainQueuesTheBriefOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	quiesceBackgroundWriters(t)
	profile := fmt.Sprintf("_core_failed_start_%d", time.Now().UnixNano())
	groups := []*session.GroupData{{Name: "serial", Path: "serial", MaxConcurrent: 1}}
	next := session.NewInstanceWithGroup("next", t.TempDir(), "serial")
	next.Tool = "claude"
	next.Account = "no-such-account" // the start fails in ValidateAccount
	next.MarkQueued("brief")
	next.SetTmuxSessionForTest(nil)
	seedStore(t, profile, groups, next)
	stubSendWorkers(t)

	for range 2 {
		if _, res := Invoke[SessionStartOut](context.Background(), testRegistry(t, Deps{}), IDSessionStart, SessionStartIn{Profile: profile, Session: "next", NoWait: true}); res.Err == nil {
			t.Fatal("start with an unknown account succeeded")
		}
	}
	t.Cleanup(session.SetQueuedSpawnForTest(func(i *session.Instance, _ string) error {
		i.Status = session.StatusStarting
		return nil
	}))
	storage := openStorage(t, profile)
	defer storage.Close()
	if got := drainGroupQueue(context.Background(), storage, "serial", loadStore(t, profile), groups); got == nil {
		t.Fatal("drain started nothing")
	}
	if recs := queuedSends(t, profile, next.ID); len(recs) != 1 || recs[0].Message != "brief" {
		t.Fatalf("queued sends = %+v, want \"brief\" exactly once", recs)
	}
}

// Restarting a queued session takes it off the queue: its brief is delivered
// now, once, and is not left on the row for a later start to type into a
// conversation that has moved on.
func TestRestartOfQueuedSessionDeliversTheBriefOnce(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	quiesceBackgroundWriters(t)
	profile := fmt.Sprintf("_core_restart_queued_%d", time.Now().UnixNano())
	inst := session.NewInstanceWithGroup(fmt.Sprintf("queued-%d", time.Now().UnixNano()), t.TempDir(), "serial")
	inst.Command = ""
	inst.MarkQueued("brief")
	seedStore(t, profile, []*session.GroupData{{Name: "serial", Path: "serial", MaxConcurrent: 1}}, inst)
	stubSendWorkers(t)
	t.Cleanup(func() {
		if n := findByTitle(loadStore(t, profile), inst.Title); n != nil {
			_ = n.Kill()
		}
	})

	if _, res := Invoke[SessionRestartOut](context.Background(), testRegistry(t, Deps{}), IDSessionRestart, SessionRestartIn{Profile: profile, Session: inst.Title, Force: true}); res.Err != nil {
		t.Fatal(res.Err)
	}
	if n := findByTitle(loadStore(t, profile), inst.Title); n == nil || n.QueuedMessage != "" {
		t.Fatalf("row after restart = %+v, want the brief taken off", n)
	}
	// A later stop and start must not deliver it again.
	if n := findByTitle(loadStore(t, profile), inst.Title); n != nil {
		_ = n.Kill()
	}
	if _, res := Invoke[SessionStartOut](context.Background(), testRegistry(t, Deps{}), IDSessionStart, SessionStartIn{Profile: profile, Session: inst.Title, NoWait: true}); res.Err != nil {
		t.Fatal(res.Err)
	}
	if recs := queuedSends(t, profile, inst.ID); len(recs) != 1 || recs[0].Message != "brief" {
		t.Fatalf("queued sends = %+v, want \"brief\" exactly once", recs)
	}
}

// A drain claims only a row that is still queued. A row another process has
// started meanwhile, even one still carrying a queued message (left by an
// older binary), is not started a second time from a stale queued copy.
func TestDrainDoesNotStartARowThatLeftTheQueue(t *testing.T) {
	profile := seedQueued(t, "brief")
	groups := []*session.GroupData{{Name: "serial", Path: "serial", MaxConcurrent: 1}}
	stale := loadStore(t, profile)
	storage := openStorage(t, profile)
	defer storage.Close()
	next := findByTitle(stale, "next")
	if err := storage.GetDB().WriteStatus(next.ID, string(session.StatusIdle), next.Tool); err != nil {
		t.Fatal(err)
	}
	stubSendWorkers(t)
	spawns := 0
	t.Cleanup(session.SetQueuedSpawnForTest(func(i *session.Instance, _ string) error {
		spawns++
		i.Status = session.StatusStarting
		return nil
	}))
	if got := drainGroupQueue(context.Background(), storage, "serial", stale, groups); got != nil || spawns != 0 {
		t.Fatalf("drained %v (%d spawns) from a stale queued copy of a started row", got, spawns)
	}
}

// The drain delivers the message the row holds when it takes it, not the one
// in its own stale copy: another process may have queued the session again
// with a new brief since this drain loaded it.
func TestDrainDeliversTheRowsMessageNotAStaleCopy(t *testing.T) {
	profile := seedQueued(t, "old brief")
	groups := []*session.GroupData{{Name: "serial", Path: "serial", MaxConcurrent: 1}}
	stale := loadStore(t, profile)
	fresh := loadStore(t, profile)
	n := findByTitle(fresh, "next")
	n.MarkQueued("new brief")
	storage := openStorage(t, profile)
	defer storage.Close()
	if err := storage.SaveWithGroups([]*session.Instance{n}, nil); err != nil {
		t.Fatal(err)
	}
	stubSendWorkers(t)
	t.Cleanup(session.SetQueuedSpawnForTest(func(i *session.Instance, _ string) error {
		i.Status = session.StatusStarting
		return nil
	}))
	if got := drainGroupQueue(context.Background(), storage, "serial", stale, groups); got == nil {
		t.Fatal("drained nothing")
	}
	if recs := queuedSends(t, profile, n.ID); len(recs) != 1 || recs[0].Message != "new brief" {
		t.Fatalf("queued sends = %+v, want the row's \"new brief\"", recs)
	}
}
