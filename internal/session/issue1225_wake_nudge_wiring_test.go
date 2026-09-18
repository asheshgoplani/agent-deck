package session

// Issue #1225 Tier-2 wiring — the wake-nudge was built + unit-tested
// (issue1225_wake_nudge_test.go) but NOTHING triggered it: an idle conductor
// only drained on its next heartbeat (up to ~14 min lag). These tests assert
// the producer commit chokepoint (commitEventToInbox, shared by the interactive
// running→waiting path AND the one-shot run-task completion path) fires a
// debounced, idle-only, best-effort wake-nudge to THAT parent the moment a
// completion durably lands — and that a dropped nudge is harmless because the
// durable record is still present for the next-turn drain.

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// newWakeNudgeFixture seeds a child→parent pair in a fresh profile and returns a
// notifier plus a finished event whose commit resolves to parentID. The parent
// is titled non-"conductor-" so resolveParentNotificationTarget keeps it live
// without a tmux UpdateStatus (the conductor-scoping policy is unit-tested
// separately in TestIssue1225_ParentIsNudgeableIdle).
func newWakeNudgeFixture(t *testing.T) (*TransitionNotifier, string, TransitionNotificationEvent) {
	t.Helper()
	inboxTestHome(t)
	profile := "_test-wake-nudge"
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	now := time.Now()
	parentID := "wake-parent-1"
	child := &Instance{
		ID:              "wake-child-1",
		Title:           "worker",
		ProjectPath:     "/tmp/c",
		GroupPath:       DefaultGroupPath,
		ParentSessionID: parentID,
		Tool:            "claude",
		Status:          StatusRunning,
		CreatedAt:       now,
	}
	parent := &Instance{
		ID:          parentID,
		Title:       "orchestrator",
		ProjectPath: "/tmp/p",
		GroupPath:   DefaultGroupPath,
		Tool:        "claude",
		Status:      StatusIdle,
		CreatedAt:   now,
	}
	if err := storage.SaveWithGroups([]*Instance{child, parent}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}

	event := TransitionNotificationEvent{
		ChildSessionID: child.ID,
		ChildTitle:     child.Title,
		Profile:        profile,
		DoneStatus:     "success",
		DoneSummary:    "done",
	}
	return NewTransitionNotifier(), parentID, event
}

// A successful commit fires exactly one wake-nudge, aimed at the resolved parent.
func TestIssue1225_CommitFiresWakeNudgeToParent(t *testing.T) {
	n, parentID, event := newWakeNudgeFixture(t)

	var mu sync.Mutex
	var sentTo []string
	n.wake = &wakeNudgeWiring{
		nudger: NewWakeNudger(0),
		now:    func() time.Time { return time.Unix(1000, 0) },
		isIdle: func(p *Instance) bool { return true },
		send: func(p *Instance, profile string) error {
			mu.Lock()
			sentTo = append(sentTo, p.ID)
			mu.Unlock()
			return nil
		},
	}

	res := n.NotifyFinished(event)
	if res.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("commit result = %q, want %q", res.DeliveryResult, transitionDeliveryCommitted)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sentTo) != 1 || sentTo[0] != parentID {
		t.Fatalf("wake-nudge targets = %v, want exactly [%s]", sentTo, parentID)
	}
}

// A busy (non-idle) parent is never nudged — send-keys into a running pane only
// queues the keystroke (issue #36326). The commit still succeeds.
func TestIssue1225_CommitDoesNotNudgeBusyParent(t *testing.T) {
	n, _, event := newWakeNudgeFixture(t)
	sent := 0
	n.wake = &wakeNudgeWiring{
		nudger: NewWakeNudger(0),
		now:    func() time.Time { return time.Unix(1000, 0) },
		isIdle: func(p *Instance) bool { return false },
		send:   func(p *Instance, profile string) error { sent++; return nil },
	}
	res := n.NotifyFinished(event)
	if res.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("commit must still succeed for a busy parent; got %q", res.DeliveryResult)
	}
	if sent != 0 {
		t.Fatalf("busy parent: send called %d times, want 0", sent)
	}
}

// Two completions landing in a burst collapse to ONE wake — the parent's drain
// consumes all pending records in the single woken turn, so the suppressed
// nudge loses nothing.
func TestIssue1225_RapidCommitsDebounceToOneNudge(t *testing.T) {
	n, _, event := newWakeNudgeFixture(t)
	sent := 0
	n.wake = &wakeNudgeWiring{
		nudger: NewWakeNudger(time.Minute),
		now:    func() time.Time { return time.Unix(2000, 0) },
		isIdle: func(p *Instance) bool { return true },
		send:   func(p *Instance, profile string) error { sent++; return nil },
	}
	n.NotifyFinished(event)
	n.NotifyFinished(event) // within the debounce window → suppressed
	if sent != 1 {
		t.Fatalf("rapid commits: send called %d times, want 1 (debounced)", sent)
	}
}

// A nudge that fails to send is harmless: the commit still reports committed AND
// the durable record is present for the next heartbeat/turn drain (wake ≠ deliver).
func TestIssue1225_NudgeSendErrorIsHarmless(t *testing.T) {
	n, parentID, event := newWakeNudgeFixture(t)
	n.wake = &wakeNudgeWiring{
		nudger: NewWakeNudger(0),
		now:    func() time.Time { return time.Unix(3000, 0) },
		isIdle: func(p *Instance) bool { return true },
		send:   func(p *Instance, profile string) error { return errors.New("pane gone") },
	}
	res := n.NotifyFinished(event)
	if res.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("a failed nudge must not fail the commit; got %q", res.DeliveryResult)
	}
	got := readInboxLines(t, parentID)
	if len(got) != 1 {
		t.Fatalf("durable record count = %d, want 1 (drain-on-next-turn safety net)", len(got))
	}
}

// The PRODUCTION default wiring (not a test spy) is fully populated and routes
// its idle probe through the conductor-scoped gate end-to-end: a conductor-
// prefixed title is nudgeable only when idle, a busy conductor and a
// non-conductor leaf are not. This guards against a future refactor silently
// swapping defaultWakeNudgeWiring's isIdle for an unscoped probe.
func TestIssue1225_DefaultWiringUsesConductorIdleGate(t *testing.T) {
	withNoopStatusProbe(t)
	w := defaultWakeNudgeWiring()
	if w == nil || w.nudger == nil || w.now == nil || w.isIdle == nil || w.send == nil {
		t.Fatalf("default wiring must populate every hook, got %+v", w)
	}
	if !w.isIdle(&Instance{ID: "c", Title: "conductor-x", Status: StatusIdle}) {
		t.Fatal("default wiring must nudge an idle conductor")
	}
	if w.isIdle(&Instance{ID: "c", Title: "conductor-x", Status: StatusRunning}) {
		t.Fatal("default wiring must NOT nudge a busy conductor (send-keys would only queue)")
	}
	if w.isIdle(&Instance{ID: "l", Title: "worker", Status: StatusIdle}) {
		t.Fatal("default wiring must NOT nudge a non-conductor leaf (no inbox drain → noise)")
	}
}

// The production idle-probe is conductor-scoped (only conductors drain an inbox)
// and only green when the pane is idle/waiting (not mid-turn).
func TestIssue1225_ParentIsNudgeableIdle(t *testing.T) {
	withNoopStatusProbe(t)
	cases := []struct {
		title  string
		status Status
		want   bool
	}{
		{"conductor-x", StatusIdle, true},
		{"conductor-x", StatusWaiting, true},
		{"conductor-x", StatusRunning, false}, // busy: send-keys would only queue
		{"worker", StatusIdle, false},         // non-conductor leaf: no inbox drain → noise
		{"conductor-x", StatusError, false},
	}
	for _, c := range cases {
		p := &Instance{ID: "p", Title: c.title, Status: c.status}
		if got := parentIsNudgeableIdle(p); got != c.want {
			t.Errorf("parentIsNudgeableIdle(title=%q,status=%q)=%v, want %v", c.title, c.status, got, c.want)
		}
	}
}

// withNoopStatusProbe swaps the status-probe seam for one that leaves the
// instance's persisted status untouched, so a table test can pin the gate's
// decision per status without a tmux server.
func withNoopStatusProbe(t *testing.T) {
	t.Helper()
	orig := updateInstanceStatus.Load().(statusProbeFunc)
	updateInstanceStatus.Store(statusProbeFunc(func(*Instance) error { return nil }))
	t.Cleanup(func() { updateInstanceStatus.Store(orig) })
}

// Review round 2 (P2-D): the idle gate must consult a FRESH status, not the
// registry row as last persisted. A parent whose row still says running but
// whose hook/pane state is idle is woken; a probe that overruns the daemon's
// budget is treated as not idle and the nudge is skipped.
func TestWakeNudge_IdleGateUsesFreshStatus(t *testing.T) {
	orig := updateInstanceStatus.Load().(statusProbeFunc)
	origBudget := statusProbeBudget
	t.Cleanup(func() {
		updateInstanceStatus.Store(orig)
		statusProbeBudget = origBudget
	})

	// Stale running row, fresh probe says idle: nudgeable.
	updateInstanceStatus.Store(statusProbeFunc(func(inst *Instance) error {
		inst.Status = StatusIdle
		return nil
	}))
	stale := &Instance{ID: "p", Title: "conductor-x", Status: StatusRunning}
	if !parentIsNudgeableIdle(stale) {
		t.Fatal("a stale running row must not withhold the wake when the fresh probe says idle")
	}

	// Stale idle row, fresh probe says running (mid-turn): not nudgeable.
	updateInstanceStatus.Store(statusProbeFunc(func(inst *Instance) error {
		inst.Status = StatusRunning
		return nil
	}))
	busy := &Instance{ID: "p", Title: "conductor-x", Status: StatusIdle}
	if parentIsNudgeableIdle(busy) {
		t.Fatal("a stale idle row must not send a nudge into a pane the fresh probe reports mid-turn")
	}

	// Probe overruns the budget: bounded, and not idle.
	statusProbeBudget = 50 * time.Millisecond
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	updateInstanceStatus.Store(statusProbeFunc(func(*Instance) error {
		<-block
		return nil
	}))
	hung := &Instance{ID: "p", Title: "conductor-x", Status: StatusIdle}
	start := time.Now()
	if parentIsNudgeableIdle(hung) {
		t.Fatal("a probe that overruns the budget must not report idle")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("gate must return at the probe budget, took %v", elapsed)
	}
}
