package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// These tests pin the notify-daemon runtime-hang fix. The daemon's Run loop is
// single-threaded (SyncOnce → syncProfile → UpdateStatus per instance, in the
// no-live-TUI branch). Before the fix a status probe that blocks — a wedged
// tmux pane, a stuck tmux server, a query that never returns, or lock
// contention on inst.mu — froze the entire delivery loop and muted every
// profile until launchctl kickstart. The fix bounds each probe
// (statusProbeBudget) and the whole pass (syncPassBudget) so one hung probe is
// skipped and the loop keeps delivering.
//
// The status probe is exercised through the updateInstanceStatus seam so the
// block is deterministic (a channel, not a real hung tmux that would be
// timing-flaky).

// withBlockingProbe installs an updateInstanceStatus seam where instances whose
// IDs are in blockIDs hang on the returned channel, while all other instances
// record that they were probed. The returned release() unblocks the hung probes
// and restores the original seam. It also shrinks the budgets so the test runs
// fast. blockedProbed counts how many distinct non-blocked instances were
// probed.
func withBlockingProbe(t *testing.T, blockIDs ...string) (block chan struct{}, otherProbed *atomic.Int32, release func()) {
	t.Helper()
	block = make(chan struct{})
	otherProbed = &atomic.Int32{}
	blocked := map[string]bool{}
	for _, id := range blockIDs {
		blocked[id] = true
	}

	origSeam := updateInstanceStatus.Load().(statusProbeFunc)
	updateInstanceStatus.Store(statusProbeFunc(func(inst *Instance) error {
		if blocked[inst.ID] {
			<-block // model a status probe that never returns
			return nil
		}
		otherProbed.Add(1)
		return nil
	}))

	origBudget := statusProbeBudget
	origPass := syncPassBudget
	statusProbeBudget = 200 * time.Millisecond
	syncPassBudget = 5 * time.Second

	var released atomic.Bool
	release = func() {
		if released.Swap(true) {
			return
		}
		updateInstanceStatus.Store(origSeam)
		statusProbeBudget = origBudget
		syncPassBudget = origPass
		close(block) // let any detached probe goroutines exit
	}
	t.Cleanup(release)
	return block, otherProbed, release
}

// saveInstances persists bare instances (no tmux session attached) for a
// daemon-backed profile.
func saveInstances(t *testing.T, storage *Storage, ids ...string) {
	t.Helper()
	insts := make([]*Instance, 0, len(ids))
	for _, id := range ids {
		pp := filepath.Join(os.Getenv("HOME"), "proj-"+id)
		if err := os.MkdirAll(pp, 0o755); err != nil {
			t.Fatalf("mkdir project %s: %v", id, err)
		}
		insts = append(insts, &Instance{
			ID:          id,
			Title:       id,
			ProjectPath: pp,
			GroupPath:   DefaultGroupPath,
			Tool:        "claude",
			Status:      StatusRunning,
			CreatedAt:   time.Now().Add(-time.Hour), // past the tmux init grace window
		})
	}
	if err := storage.SaveWithGroups(insts, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
}

// runSyncProfileWithDeadline runs d.syncProfile(profile) in a goroutine and
// fails the test if it does not return within deadline. This is the
// reproduction assertion: on the unbounded (pre-fix) loop, a blocked probe
// makes syncProfile never return and this fatals — the freeze. With the fix it
// returns promptly.
func runSyncProfileWithDeadline(t *testing.T, d *TransitionDaemon, profile string, deadline time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		d.syncProfile(profile)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(deadline):
		t.Fatalf("syncProfile did not return within %s: a hung status probe froze the whole pass (the notify-daemon runtime hang)", deadline)
	}
}

// TestSyncProfile_HungProbeDoesNotFreezeLoop is the primary reproduction +
// regression test. One instance's status probe blocks forever; the pass must
// still complete within the budget and the healthy instance must still be
// probed. On the pre-fix unbounded loop syncProfile never returns and this
// test fatals via the deadline.
func TestSyncProfile_HungProbeDoesNotFreezeLoop(t *testing.T) {
	const profile = "_test_notify_hang_primary"
	d, storage := bootstrapDaemonProfile(t, profile)

	_, otherProbed, _ := withBlockingProbe(t, "hung")
	saveInstances(t, storage, "hung", "healthy")

	// Prime baseline (initialized=true) so the second pass exercises the full
	// transition-detection path, not just the init shortcut.
	runSyncProfileWithDeadline(t, d, profile, 3*time.Second)
	runSyncProfileWithDeadline(t, d, profile, 3*time.Second)

	if otherProbed.Load() == 0 {
		t.Fatal("the healthy instance was never probed — the hung probe blocked the loop")
	}
}

// TestSyncProfile_TimedOutProbeIsSkippedOthersProcessed asserts the timeout
// path: a probe that exceeds statusProbeBudget is skipped (the instance keeps
// its last-known status, taken without touching inst.mu) while every other
// instance is processed normally and its real transition is observed.
func TestSyncProfile_TimedOutProbeIsSkippedOthersProcessed(t *testing.T) {
	const profile = "_test_notify_hang_skip"
	d, storage := bootstrapDaemonProfile(t, profile)

	block, otherProbed, _ := withBlockingProbe(t, "hung")
	saveInstances(t, storage, "hung", "h1", "h2", "h3")

	runSyncProfileWithDeadline(t, d, profile, 3*time.Second)

	// All three healthy instances were probed despite "hung" never returning.
	if got := otherProbed.Load(); got != 3 {
		t.Fatalf("expected 3 healthy instances probed, got %d (hung probe leaked back-pressure onto the loop)", got)
	}

	// The hung instance kept its last-known status (running), recorded without
	// blocking on its held lock.
	if got := d.lastStatus[profile]["hung"]; got != string(StatusRunning) {
		t.Fatalf("hung instance status: want %q (last-known), got %q", StatusRunning, got)
	}

	// Sanity: the loop never read the hung instance's lock-guarded state.
	select {
	case <-block:
		t.Fatal("block channel was closed early")
	default:
	}
}

// TestSyncProfile_ProbeStallBreadcrumb asserts the watchdog breadcrumb: a
// timed-out probe writes a diagnostic line to notifier-probe-stalls.log so a
// future hang is visible and the self-recovery is auditable.
func TestSyncProfile_ProbeStallBreadcrumb(t *testing.T) {
	const profile = "_test_notify_hang_breadcrumb"
	d, storage := bootstrapDaemonProfile(t, profile)

	withBlockingProbe(t, "hung")
	saveInstances(t, storage, "hung", "healthy")

	runSyncProfileWithDeadline(t, d, profile, 3*time.Second)

	path := notifierProbeStallLogPath()
	entries := readProbeStallLog(t, path)
	if len(entries) == 0 {
		t.Fatalf("no probe-stall breadcrumb written to %s", path)
	}
	var found bool
	for _, e := range entries {
		if e["instance"] == "hung" && e["reason"] == "probe_budget" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a probe_budget breadcrumb for instance \"hung\", got %+v", entries)
	}
}

// TestLogProbeStall_RateLimited asserts the breadcrumb is rate-limited per
// (profile,instance,reason) so a permanently wedged instance — which times out
// every poll — logs at most once per probeStallLogInterval instead of flooding.
func TestLogProbeStall_RateLimited(t *testing.T) {
	const profile = "_test_notify_hang_ratelimit"
	d, _ := bootstrapDaemonProfile(t, profile)

	path := notifierProbeStallLogPath()
	// Truncate before writing so entries from earlier tests don't inflate the count.
	if err := os.Truncate(path, 0); err != nil && !os.IsNotExist(err) {
		t.Fatalf("truncate probe-stall log: %v", err)
	}
	for i := 0; i < 5; i++ {
		d.logProbeStall(profile, "hung", "probe_budget")
	}
	if got := len(readProbeStallLog(t, path)); got != 1 {
		t.Fatalf("expected 1 rate-limited breadcrumb, got %d", got)
	}
}

func readProbeStallLog(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("open probe-stall log: %v", err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e map[string]any
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out
}
