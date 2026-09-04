package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Field failure 2026-08-20 (Mac↔agentbox, reproduced on g14): an ordinary claude
// session completed a turn and NOTHING was recorded. The loopback harness passed
// throughout, because it never exercised an ordinary no-sentinel session — the
// only shape the product actually runs most of the time.
//
// These tests are written against that shape specifically. The previous suite's
// failure was not a missing assertion, it was a missing CASE.

// withFieldSandbox points every data path at a throwaway HOME so these tests
// never read or write the real profile.
func withFieldSandbox(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
}

func fieldDaemon(t *testing.T) *TransitionDaemon {
	t.Helper()
	return &TransitionDaemon{
		notifier:    NewTransitionNotifier(),
		lastStatus:  map[string]map[string]string{},
		initialized: map[string]bool{},
		lastDone:    map[string]map[string]DoneSignal{},
		lastTurn:    map[string]map[string]string{},
		// These instances have no tmux session; the real probe would reject them.
		turnLiveCheck: func(*Instance) bool { return true },
	}
}

// The headline case: no sentinel and no parent. It must be recorded as an
// actionable transition, without fabricating a completion-ledger entry.
func TestFieldTalkback_OrdinarySessionWithNoSentinelIsRecorded(t *testing.T) {
	storage := reviewTestHome(t, "default")
	d := fieldDaemon(t)

	inst := NewInstanceWithTool("ordinary", t.TempDir(), "claude")
	inst.ID = "ord-1"
	inst.ParentSessionID = "conductor-on-another-host"
	if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}
	byID := map[string]*Instance{inst.ID: inst}
	statuses := map[string]string{inst.ID: string(StatusWaiting)}

	d.recordTerminalTurns("default", byID, statuses, nil)

	events, err := ReadInboxEvents(UnownedInboxID)
	if err != nil || len(events) != 1 || events[0].ToStatus != string(StatusWaiting) {
		t.Fatalf("ordinary waiting turn must produce one transition: events=%+v err=%v", events, err)
	}
	dir, _ := CompletionLedgerDir()
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("ordinary turn must not claim a sentinel completion; got %d ledger entries", len(entries))
	}
}

// The first-scan race that lost field round 3: the session is already parked at a
// terminal status the first time the daemon ever sees it, so there is no edge to
// fire on. Recording must not depend on having observed it mid-`running`.
func TestFieldTalkback_FirstScanRecordsAnAlreadyParkedSession(t *testing.T) {
	storage := reviewTestHome(t, "default")
	d := fieldDaemon(t)

	inst := NewInstanceWithTool("parked", t.TempDir(), "claude")
	inst.ID = "parked-1"
	inst.ParentSessionID = "conductor-on-another-host"
	if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}
	byID := map[string]*Instance{inst.ID: inst}
	statuses := map[string]string{inst.ID: string(StatusWaiting)}

	// No prior status map at all — this is the daemon's very first pass.
	d.recordTerminalTurns("default", byID, statuses, nil)

	if events, _ := ReadInboxEvents(UnownedInboxID); len(events) == 0 {
		t.Fatal("a session already parked at waiting on the first scan must still be recorded; suppressing it is exactly the field round-3 loss")
	}
}

// Repeated polls of a session sitting still must not republish. The turn key is
// status + transcript signal, so a parked session records once.
func TestFieldTalkback_ParkedSessionRecordsOnlyOnce(t *testing.T) {
	storage := reviewTestHome(t, "default")
	d := fieldDaemon(t)

	inst := NewInstanceWithTool("quiet", t.TempDir(), "claude")
	inst.ID = "quiet-1"
	inst.ParentSessionID = "conductor-on-another-host"
	if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}
	byID := map[string]*Instance{inst.ID: inst}
	statuses := map[string]string{inst.ID: string(StatusWaiting)}

	for i := 0; i < 5; i++ {
		d.recordTerminalTurns("default", byID, statuses, nil)
	}
	events, _ := ReadInboxEvents(UnownedInboxID)
	if len(events) != 1 {
		t.Fatalf("five polls of an unchanged session must produce one entry, got %d", len(events))
	}
}

// A stopped session did not finish a turn — it was shut down. Recording that as
// a completion tells a conductor something untrue, and on a real profile it
// published 34 long-dead sessions the first time this ran.
func TestFieldTalkback_StoppedIsNotACompletedTurn(t *testing.T) {
	if isRecordableTurnStatus(string(StatusStopped)) {
		t.Error("`stopped` must not count as a completed turn")
	}
	for _, s := range []Status{StatusWaiting, StatusIdle, StatusError} {
		if !isRecordableTurnStatus(string(s)) {
			t.Errorf("%q must count as a completed turn", s)
		}
	}
}

// FINDING A: an empty drain is only good news if something was watching.
func TestFieldTalkback_WriterStatusDistinguishesAbsentFromQuiet(t *testing.T) {
	withFieldSandbox(t)

	if st := ReadWriterStatus(); st.Running {
		t.Error("with no heartbeat ever stamped, the writer must report NOT running")
	} else if st.Detail == "" {
		t.Error("a not-running verdict must carry a reason the operator can act on")
	}

	WriteNotifyHeartbeat()
	if st := ReadWriterStatus(); !st.Running {
		t.Errorf("a freshly stamped heartbeat must report running: %+v", st)
	}

	// A daemon that died leaves its last stamp behind; stale must read as absent.
	path, err := notifyHeartbeatPath()
	if err != nil {
		t.Fatalf("heartbeat path: %v", err)
	}
	old := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339Nano)
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatalf("write stale heartbeat: %v", err)
	}
	if st := ReadWriterStatus(); st.Running {
		t.Error("a heartbeat 10 minutes old means nothing is recording; it must not read as running")
	}
}

func TestIssue1952_WriterStatusReadFailureIsUnknown(t *testing.T) {
	withFieldSandbox(t)
	path, err := notifyHeartbeatPath()
	if err != nil {
		t.Fatalf("heartbeat path: %v", err)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("make unreadable heartbeat stand-in: %v", err)
	}
	st := ReadWriterStatus()
	if !strings.Contains(st.Detail, "unreadable") || strings.Contains(st.Detail, "never stamped") {
		t.Fatalf("I/O failure must be unknown, not absent: %+v", st)
	}
}
