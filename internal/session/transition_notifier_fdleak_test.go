package session

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestPrepareDispatch_NoFDLeak and TestLiveTargetAvailability_NoFDLeak guard
// the P0 leak where prepareDispatch and liveTargetAvailability each opened a
// Storage via NewStorageWithProfile and never called Close(). On a long-running
// notify-daemon this leaked one SQLite file descriptor per dispatch (~34/min
// observed in production, 1117 open FDs to state.db after 2h40m before WAL
// contention wedged the daemon).
//
// The check counts /proc/self/fd entries before and after N calls. Linux-only
// because /proc/self/fd is the most reliable per-process FD snapshot; on
// macOS/BSD the equivalent (/dev/fd) lists the test runner's handles too and
// is noisier. The leak reproduces identically on Linux production hosts so
// gating coverage on GOOS=linux is acceptable.

func countOpenFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("read /proc/self/fd: %v", err)
	}
	return len(entries)
}

func setupNotifierLeakFixture(t *testing.T) (*TransitionNotifier, TransitionNotificationEvent, string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("FD-leak check uses /proc/self/fd; linux-only")
	}

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	if err := os.MkdirAll(filepath.Join(tmpHome, ".agent-deck"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	profile := "_test-fdleak"
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	now := time.Now()
	child := &Instance{
		ID:              "child-fdleak",
		Title:           "worker",
		ProjectPath:     "/tmp/child",
		GroupPath:       DefaultGroupPath,
		ParentSessionID: "parent-fdleak",
		Tool:            "shell",
		Status:          StatusWaiting,
		CreatedAt:       now,
	}
	parent := &Instance{
		ID:          "parent-fdleak",
		Title:       "orchestrator",
		ProjectPath: "/tmp/parent",
		GroupPath:   DefaultGroupPath,
		Tool:        "shell",
		Status:      StatusWaiting,
		CreatedAt:   now,
	}
	if err := storage.SaveWithGroups([]*Instance{child, parent}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("storage.Close: %v", err)
	}

	dir := t.TempDir()
	notifier := &TransitionNotifier{
		statePath:    filepath.Join(dir, "state.json"),
		logPath:      filepath.Join(dir, "transition-notifier.log"),
		missedPath:   filepath.Join(dir, "notifier-missed.log"),
		queuePath:    filepath.Join(dir, "queue.json"),
		orphanPath:   filepath.Join(dir, "orphans.log"),
		sender:       func(profile, targetID, message string) error { return nil },
		sendTimeout:  200 * time.Millisecond,
		targetSlots:  map[string]chan struct{}{},
		orphanWarned: map[string]bool{},
		stopCh:       make(chan struct{}),
		state: transitionNotifyState{
			Records: map[string]transitionNotifyRecord{},
		},
	}

	event := TransitionNotificationEvent{
		ChildSessionID: child.ID,
		ChildTitle:     child.Title,
		Profile:        profile,
		FromStatus:     "running",
		ToStatus:       "waiting",
		Timestamp:      now,
	}
	return notifier, event, profile
}

func TestPrepareDispatch_NoFDLeak(t *testing.T) {
	notifier, event, _ := setupNotifierLeakFixture(t)

	const iterations = 200

	// Warm-up: first call may initialize lazy package-level state.
	plan := notifier.prepareDispatch(event)
	if plan.finalized {
		t.Fatalf("prepareDispatch warm-up finalized unexpectedly: %+v", plan.event)
	}

	before := countOpenFDs(t)
	for i := 0; i < iterations; i++ {
		notifier.prepareDispatch(event)
	}
	after := countOpenFDs(t)

	growth := after - before
	// Allow a tiny slack for unrelated runtime allocations (epoll, etc.).
	// The leak grows linearly with iterations — pre-fix this was ~iterations.
	if growth > 5 {
		t.Fatalf("FD leak in prepareDispatch: %d open FDs grew to %d after %d calls (delta=%d)",
			before, after, iterations, growth)
	}
}

func TestLiveTargetAvailability_NoFDLeak(t *testing.T) {
	notifier, event, profile := setupNotifierLeakFixture(t)

	const iterations = 200

	// Warm-up.
	_ = notifier.liveTargetAvailability(profile, event.ChildSessionID)

	before := countOpenFDs(t)
	for i := 0; i < iterations; i++ {
		notifier.liveTargetAvailability(profile, "parent-fdleak")
	}
	after := countOpenFDs(t)

	growth := after - before
	if growth > 5 {
		t.Fatalf("FD leak in liveTargetAvailability: %d open FDs grew to %d after %d calls (delta=%d)",
			before, after, iterations, growth)
	}
}
