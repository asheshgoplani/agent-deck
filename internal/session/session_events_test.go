package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/health"
)

func readJournal(t *testing.T, profile string) []health.Event {
	t.Helper()
	dir, err := HealthLogDir(profile)
	if err != nil {
		t.Fatal(err)
	}
	events, incomplete, err := health.ReadEvents(dir, time.Time{}, time.Now().Add(time.Hour))
	if err != nil || incomplete {
		t.Fatalf("read journal: %v incomplete=%v", err, incomplete)
	}
	return events
}

func TestSessionEventsKillSwitch(t *testing.T) {
	var settings HealthSettings
	if !settings.SessionEventsEnabled() {
		t.Fatal("session events must default on")
	}
	off := false
	settings.SessionEvents = &off
	if settings.SessionEventsEnabled() {
		t.Fatal("session_events = false must disable the journal")
	}
	settings = HealthSettings{Enabled: &off}
	if settings.SessionEventsEnabled() {
		t.Fatal("health disabled must disable the journal too")
	}

	const profile = "_test_events_killswitch"
	bootstrapDaemonProfile(t, profile)
	if SessionEventJournal(profile) == nil {
		t.Fatal("journal nil with default config")
	}
	configPath, _ := GetUserConfigPath()
	os.MkdirAll(filepath.Dir(configPath), 0o700)
	if err := os.WriteFile(configPath, []byte("[health]\nsession_events = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ClearUserConfigCache()
	if SessionEventJournal(profile) != nil {
		t.Fatal("journal not nil with session_events = false")
	}
}

// A daemon pass that observes a status change writes one status event, on that
// pass, from what it already observed: the first pass seeds and writes nothing.
func TestDaemonPassJournalsStatusChange(t *testing.T) {
	const profile = "_test_events_daemon"
	d, storage := bootstrapDaemonProfile(t, profile)
	inst := &Instance{ID: "events-child-1", Title: "worker", ProjectPath: "/tmp/events-child-1", GroupPath: DefaultGroupPath, Tool: "claude", Status: StatusRunning, CreatedAt: time.Now()}
	if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatal(err)
	}
	db := storage.GetDB()
	if err := db.RegisterInstance(false); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteStatus(inst.ID, "running", inst.Tool); err != nil {
		t.Fatal(err)
	}
	d.syncProfile(profile)
	if events := readJournal(t, profile); len(events) != 0 {
		t.Fatalf("first pass must seed silently, wrote %+v", events)
	}
	if err := db.WriteStatus(inst.ID, "waiting", inst.Tool); err != nil {
		t.Fatal(err)
	}
	d.syncProfile(profile)
	d.syncProfile(profile) // unchanged status: no second line
	d.Flush()              // journal writes are async now; wait for them to land
	events := readJournal(t, profile)
	if len(events) != 1 {
		t.Fatalf("want one status event, got %+v", events)
	}
	e := events[0]
	if e.Kind != health.KindStatus || e.SessionID != inst.ID || e.From != "running" || e.To != "waiting" {
		t.Fatalf("event: %+v", e)
	}
	if _, ok := e.Detail["substate"]; ok {
		t.Fatalf("no substate observed but one was written: %+v", e)
	}
	m := health.ComputeSessionMetrics(inst.ID, events, time.Now().Add(-time.Hour), time.Now())
	if m.Turns.Count != 1 {
		t.Fatalf("one running->waiting edge must be one turn: %+v", m.Turns)
	}
}

// blockingAppender simulates a slow or wedged health volume: every Append
// waits on block, which the test controls.
type blockingAppender struct{ block chan struct{} }

func (b *blockingAppender) Append(health.Event) error {
	<-b.block
	return nil
}

// The daemon's single-threaded loop must never stall on the journal write:
// swap in a writer whose underlying Append blocks forever and confirm
// syncProfile still returns promptly.
func TestDaemonPassJournalDoesNotBlockOnWedgedWriter(t *testing.T) {
	const profile = "_test_events_wedged_writer"
	d, storage := bootstrapDaemonProfile(t, profile)
	inst := &Instance{ID: "events-child-wedged", Title: "worker", ProjectPath: "/tmp/events-child-wedged", GroupPath: DefaultGroupPath, Tool: "claude", Status: StatusRunning, CreatedAt: time.Now()}
	if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatal(err)
	}
	db := storage.GetDB()
	if err := db.RegisterInstance(false); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteStatus(inst.ID, "running", inst.Tool); err != nil {
		t.Fatal(err)
	}
	// Seed the baseline first (also resolves the real per-profile journal),
	// then replace the writer with one wrapping a wedged appender.
	d.syncProfile(profile)

	block := make(chan struct{}) // never closed: the appender blocks forever
	t.Cleanup(func() { close(block) })
	d.journalWriters[profile] = health.NewAsyncWriter(&blockingAppender{block: block}, health.DefaultJournalQueueSize)

	if err := db.WriteStatus(inst.ID, "waiting", inst.Tool); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	d.syncProfile(profile)
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("syncProfile blocked on a wedged journal writer: took %v", elapsed)
	}
}

func TestDaemonPassRespectsKillSwitch(t *testing.T) {
	const profile = "_test_events_daemon_off"
	d, storage := bootstrapDaemonProfile(t, profile)
	configPath, _ := GetUserConfigPath()
	os.MkdirAll(filepath.Dir(configPath), 0o700)
	os.WriteFile(configPath, []byte("[health]\nsession_events = false\n"), 0o600)
	ClearUserConfigCache()
	inst := &Instance{ID: "events-child-off", Title: "worker", ProjectPath: "/tmp/events-child-off", GroupPath: DefaultGroupPath, Tool: "claude", Status: StatusRunning, CreatedAt: time.Now()}
	storage.SaveWithGroups([]*Instance{inst}, nil)
	db := storage.GetDB()
	db.RegisterInstance(false)
	db.WriteStatus(inst.ID, "running", inst.Tool)
	d.syncProfile(profile)
	db.WriteStatus(inst.ID, "waiting", inst.Tool)
	d.syncProfile(profile)
	if events := readJournal(t, profile); len(events) != 0 {
		t.Fatalf("kill switch ignored: %+v", events)
	}
}

func TestRunTaskWorkerJournalsWorkerDoneWithClaimTime(t *testing.T) {
	profile := "_test_events_worker"
	_, childID := seedDoneParentChild(t, profile)
	cmd := exec.Command("sh", "-c", "sleep 0.2; echo '===AGENTDECK_DONE=== status=ok summary=built'; exit 0")
	rec, err := RunTaskWorker(childID, profile, "worker", cmd)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.FinishedAt.After(rec.CreatedAt.Add(150 * time.Millisecond)) {
		t.Fatalf("created_at must be the claim time, not the finish time: %+v", rec)
	}
	events := readJournal(t, profile)
	if len(events) != 1 || events[0].Kind != health.KindWorkerDone || events[0].SessionID != childID {
		t.Fatalf("worker_done: %+v", events)
	}
	d, _ := events[0].Detail["duration_ms"].(float64)
	if d < 150 || events[0].Detail["status"] != "ok" {
		t.Fatalf("worker_done detail: %+v", events[0].Detail)
	}
}
