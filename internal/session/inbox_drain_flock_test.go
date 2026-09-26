package session

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Messaging audit P1-3: producers (CommitToInbox / WriteInboxEventIfNew) took
// the cross-process flock on the inbox file and rewrote it by rename, but the
// consumer (DrainInboxForParent: stage + finalize) and the sweeps held only
// the in-process mutexes. The daemon, the parent's Stop hook and `inbox
// drain` are three processes, so a drain could read N lines while the daemon
// renamed an N+1 line file into place and then remove that file: a committed
// record lost. The drain and every inbox rewrite now take the same flock.
//
// Lock order (outermost first): inbox flock → consumedTurnsMu → inboxWriteMu.

const inboxFlockHelperEnv = "AGENTDECK_TEST_INBOX_FLOCK_HELPER"

// runInboxFlockHelper is the subprocess side, dispatched from TestMain BEFORE
// the test HOME isolation so it reaches the same inbox directory the parent
// test process passed in its environment. Modes:
//
//	hold:    take the inbox flock for the parent, touch the marker, sleep.
//	produce: commit N finished events for distinct children to the parent.
func runInboxFlockHelper(mode string) int {
	parent := os.Getenv("AGENTDECK_TEST_INBOX_PARENT")
	switch mode {
	case "hold":
		lock, err := AcquireConfigFileLock(InboxPathFor(parent))
		if err != nil {
			fmt.Fprintln(os.Stderr, "hold:", err)
			return 1
		}
		defer lock.Release()
		if err := os.WriteFile(os.Getenv("AGENTDECK_TEST_INBOX_MARKER"), []byte("held"), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, "hold marker:", err)
			return 1
		}
		time.Sleep(3 * time.Second)
	case "produce":
		n, _ := strconv.Atoi(os.Getenv("AGENTDECK_TEST_INBOX_COUNT"))
		tag := os.Getenv("AGENTDECK_TEST_INBOX_TAG")
		for i := 0; i < n; i++ {
			ev := TransitionNotificationEvent{
				Kind: transitionKindFinished, Profile: "default",
				ChildSessionID: fmt.Sprintf("child-%s-%d", tag, i), ChildTitle: "w",
				DoneStatus: "ok", DoneSummary: fmt.Sprintf("run %s %d", tag, i),
				Timestamp: time.Now(),
			}
			if err := CommitToInbox(parent, ev); err != nil {
				fmt.Fprintln(os.Stderr, "produce:", err)
				return 1
			}
		}
	}
	return 0
}

func startInboxFlockHelper(t *testing.T, mode string, extraEnv ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), inboxFlockHelperEnv+"="+mode)
	cmd.Env = append(cmd.Env, extraEnv...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper %s: %v", mode, err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd
}

func waitForMarker(t *testing.T, marker string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("helper never took the inbox flock")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// holdInboxFlockInSubprocess makes another process hold the inbox flock for
// parent and returns once it does.
func holdInboxFlockInSubprocess(t *testing.T, parent string) {
	t.Helper()
	if err := os.MkdirAll(InboxDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "held.marker")
	startInboxFlockHelper(t, "hold", "AGENTDECK_TEST_INBOX_PARENT="+parent, "AGENTDECK_TEST_INBOX_MARKER="+marker)
	waitForMarker(t, marker)
}

func shortInboxLockWait(t *testing.T) {
	t.Helper()
	prev := inboxLockWait
	inboxLockWait = 300 * time.Millisecond
	t.Cleanup(func() { inboxLockWait = prev })
}

func TestDrainInboxForParent_TakesInboxFlock(t *testing.T) {
	inboxTestHome(t)
	shortInboxLockWait(t)
	parent := "parent-flock-drain"
	ev := TransitionNotificationEvent{
		Kind: transitionKindFinished, Profile: "default", ChildSessionID: "c1",
		DoneStatus: "ok", DoneSummary: "x", Timestamp: time.Now(),
	}
	if err := CommitToInbox(parent, ev); err != nil {
		t.Fatal(err)
	}
	holdInboxFlockInSubprocess(t, parent)

	start := time.Now()
	events, err := DrainInboxForParent(parent)
	if !errors.Is(err, ErrConfigLockBusy) {
		t.Fatalf("drain must wait on the producers' flock and report busy after the bounded wait; got events=%d err=%v", len(events), err)
	}
	if waited := time.Since(start); waited < inboxLockWait {
		t.Fatalf("drain returned after %v, before the bounded wait of %v", waited, inboxLockWait)
	}
	// Nothing was consumed while the lock was held elsewhere.
	if !InboxHasPending(parent) {
		t.Fatal("a busy drain must leave the inbox untouched")
	}
}

func TestInboxSweeps_TakeInboxFlock(t *testing.T) {
	inboxTestHome(t)
	shortInboxLockWait(t)
	parent := "parent-flock-sweep"
	old := TransitionNotificationEvent{
		Profile: "default", ChildSessionID: "c-old", FromStatus: "running", ToStatus: "waiting",
		Timestamp: time.Now().Add(-48 * time.Hour),
	}
	if err := WriteInboxEvent(parent, old); err != nil {
		t.Fatal(err)
	}
	holdInboxFlockInSubprocess(t, parent)

	if _, err := SweepInboxByTuple(parent, "c-old", "running", "waiting"); !errors.Is(err, ErrConfigLockBusy) {
		t.Fatalf("tuple sweep must take the inbox flock: err=%v", err)
	}
	if _, err := SweepInboxByTTL(time.Hour); !errors.Is(err, ErrConfigLockBusy) {
		t.Fatalf("TTL sweep must take the inbox flock: err=%v", err)
	}
	if got := readInboxLines(t, parent); len(got) != 1 {
		t.Fatalf("a busy sweep must leave the inbox untouched, got %d records", len(got))
	}
}

// TestInboxDrain_ConcurrentProducersLoseNothing is the live-shaped proof: two
// producer processes commit while this process drains continuously. Every
// committed record must be drained exactly once.
func TestInboxDrain_ConcurrentProducersLoseNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("multi-process stress")
	}
	inboxTestHome(t)
	parent := "parent-flock-stress"
	const perProducer = 120
	if err := os.MkdirAll(InboxDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	producers := []*exec.Cmd{
		startInboxFlockHelper(t, "produce", "AGENTDECK_TEST_INBOX_PARENT="+parent, "AGENTDECK_TEST_INBOX_COUNT="+strconv.Itoa(perProducer), "AGENTDECK_TEST_INBOX_TAG=a"),
		startInboxFlockHelper(t, "produce", "AGENTDECK_TEST_INBOX_PARENT="+parent, "AGENTDECK_TEST_INBOX_COUNT="+strconv.Itoa(perProducer), "AGENTDECK_TEST_INBOX_TAG=b"),
	}

	seen := map[string]int{}
	deadline := time.Now().Add(60 * time.Second)
	done := make(chan struct{})
	go func() {
		for _, p := range producers {
			_ = p.Wait()
		}
		close(done)
	}()
	// Registered after the helpers' cleanups, so it runs first: on an early
	// t.Fatalf the helpers' own cmd.Wait must not run concurrently with the
	// goroutine's Wait above (a data race in the test that makes the
	// release gate abort instead of rerunning the failure).
	t.Cleanup(func() {
		for _, p := range producers {
			_ = p.Process.Kill()
		}
		<-done
	})
	finished := false
	for !finished || InboxHasPending(parent) {
		select {
		case <-done:
			finished = true
		default:
		}
		events, err := DrainInboxForParent(parent)
		if err != nil {
			t.Fatalf("drain: %v", err)
		}
		for _, ev := range events {
			seen[ev.ChildSessionID]++
		}
		if time.Now().After(deadline) {
			t.Fatalf("producers did not finish in time; drained %d so far", len(seen))
		}
		if !finished {
			time.Sleep(2 * time.Millisecond)
		}
	}
	// One more drain after the producers exited catches a final commit.
	events, err := DrainInboxForParent(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		seen[ev.ChildSessionID]++
	}

	if len(seen) != 2*perProducer {
		t.Fatalf("lost records: drained %d distinct children, want %d", len(seen), 2*perProducer)
	}
	for child, n := range seen {
		if n != 1 {
			t.Fatalf("child %s drained %d times, want exactly once", child, n)
		}
	}
	if _, err := os.Stat(inboxInflightPathFor(parent)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("in-flight WAL must be dropped after a finalized drain: %v", err)
	}
}
