package session

import (
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// TestTransitionNotifier_MuteFlagRespectedOnReplay_RegressionFor962V3 pins the
// third variant of issue #962 reported by @seanyoungberg: the per-session
// `NoTransitionNotify` mute flag is checked at NEW event emission time
// (transition_daemon.go:210 and :375) but NOT during inbox / deferred-queue
// replay (DrainRetryQueueWithResolver). Once an event is sitting in the
// deferred queue, toggling `agent-deck session set-transition-notify <child>
// off` does not stop further re-deliveries — the queue keeps firing
// "[EVENT] Child '<child>' is waiting" into the conductor pane on every
// poll until the queue drains by some other means.
//
// Fix shape: generalize the existing childPresence resolver (from variant 1,
// PR #992) into an eventDeliverable resolver that checks BOTH registry
// presence AND the child's current NoTransitionNotify flag. Same boundary,
// same test seam, broader predicate. Centralizes "is this event still
// deliverable to this session?" at the replay-dispatch site so future
// variants (session-paused, conductor-stopped, etc.) plug in here too.
//
// Setup mirrors TestTransitionNotifier_SkipsRemovedSessions_RegressionFor962:
// build storage, save parent+child, enqueue deferred events, mutate state
// between enqueue and drain, then assert zero sends.
func TestTransitionNotifier_MuteFlagRespectedOnReplay_RegressionFor962V3(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })
	if err := os.MkdirAll(home+"/.agent-deck", 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	profile := "_test-962-v3-mute-replay"
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	defer storage.Close()

	now := time.Now()
	parent := &Instance{
		ID:          "parent-conductor-962v3",
		Title:       "conductor-962v3",
		ProjectPath: "/tmp/p962v3",
		GroupPath:   DefaultGroupPath,
		Tool:        "claude",
		Status:      StatusIdle,
		CreatedAt:   now,
	}
	child := &Instance{
		ID:                 "child-muted-962v3",
		Title:              "noisy-worker",
		ProjectPath:        "/tmp/c962v3",
		GroupPath:          DefaultGroupPath,
		ParentSessionID:    parent.ID,
		Tool:               "shell",
		Status:             StatusWaiting,
		CreatedAt:          now,
		NoTransitionNotify: false, // mute not yet set at enqueue time
	}
	if err := storage.SaveWithGroups([]*Instance{parent, child}, nil); err != nil {
		t.Fatalf("save initial: %v", err)
	}

	n := NewTransitionNotifier()
	var sent atomic.Int32
	n.sender = func(profile, targetID, message string) error {
		sent.Add(1)
		return nil
	}
	t.Cleanup(n.Close)

	// Production trace from seanyoungberg: a single child transition gets
	// 4-5 re-fires from accumulated queue. Reproduce with 5 deferred events
	// for the same child against the same parent — distinct from→to tuples
	// so the dedup ledger doesn't collapse them.
	tuples := []struct{ from, to string }{
		{"running", "waiting"},
		{"waiting", "running"},
		{"running", "error"},
		{"error", "waiting"},
		{"waiting", "idle"},
	}
	for i, tup := range tuples {
		event := TransitionNotificationEvent{
			ChildSessionID:  child.ID,
			ChildTitle:      child.Title,
			Profile:         profile,
			FromStatus:      tup.from,
			ToStatus:        tup.to,
			Timestamp:       now.Add(time.Duration(i) * time.Second),
			TargetSessionID: parent.ID,
			TargetKind:      "parent",
		}
		n.EnqueueDeferred(event)
	}

	// Simulate the user running `agent-deck session set-transition-notify
	// <child> off` AFTER the events are queued. The mutator writes
	// no_transition_notify=1 to the DB row; the queue file is untouched.
	child.NoTransitionNotify = true
	if err := storage.SaveWithGroups([]*Instance{parent, child}, nil); err != nil {
		t.Fatalf("save muted: %v", err)
	}

	// Drain with availability always true: target IS free. On current main
	// the queue ignores the mute flag and dispatches all 5 events. The fix
	// is to consult the child's current NoTransitionNotify before dispatch.
	n.DrainRetryQueueWithResolver(profile, func(p, id string) bool { return true })
	n.Flush()

	if got := sent.Load(); got != 0 {
		t.Fatalf("issue #962 v3: notifier dispatched %d events for a muted child; expected 0", got)
	}

	if entries := n.snapshotQueueForTest(); len(entries) != 0 {
		t.Fatalf("issue #962 v3: queue still holds %d entries for muted child; expected drained", len(entries))
	}
}
