package session

import (
	"os"
	"testing"
	"time"
)

// Regression tests for issue #1142 — agent-deck status-transition notifier
// emits `[EVENT] Child '<id>' is waiting.` repeatedly for the same dormant
// child. One observed run produced 47 identical [EVENT] notifications for the
// same child (`adeck-supervisor-drain`) over 15.5 hours (mean interval
// ~20 min). The parent conductor burned context re-checking the same session
// on every fire.
//
// Root cause: isDuplicate only suppressed identical (from→to) within 90s.
// A child that flickers running→waiting every 20+ minutes is always outside
// the window, so each fire re-emits even though the child is dormant and the
// pane output is unchanged.
//
// Fix shape: dedup on (child_id, to_status, last_output_hash) within a long
// TTL (default 2 hours). If the child re-emits the same transition with the
// same output hash inside the TTL, suppress. If the output hash changes (real
// progress), re-emit. If the to_status changes (waiting → error), re-emit.
// If the TTL elapses, re-emit as a liveness ping so the operator still gets
// periodic confirmation the child is alive.
//
// Tests exercise the dedup boundary directly via isDuplicate + markNotified.
// That avoids the full storage / parent-resolution / tmux dispatch path while
// still pinning the behavior the user observes: how many [EVENT] lines land
// in the conductor pane.

func setupNotifierForDedupTest(t *testing.T) *TransitionNotifier {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	t.Cleanup(func() {
		ClearUserConfigCache()
		ResetInboxFingerprintCacheForTest()
	})
	if err := os.MkdirAll(home+"/.agent-deck", 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	n := NewTransitionNotifier()
	t.Cleanup(n.Close)
	return n
}

// countEmitted walks a sequence of events through the dedup boundary the same
// way NotifyTransition does: check isDuplicate, and if not duplicate, persist
// via markNotified and count as emitted. This pins the user-visible behavior
// (how many [EVENT] lines reach the parent pane) without depending on the
// full dispatch path.
func countEmitted(n *TransitionNotifier, events []TransitionNotificationEvent) int {
	emitted := 0
	for _, ev := range events {
		if n.isDuplicate(ev) {
			continue
		}
		n.markNotified(ev)
		emitted++
	}
	return emitted
}

// Test 1: 5 events for the same child with the same to_status and the same
// output hash collapse to a single emission. This is the literal bug from
// #1142: a dormant child re-emitting the same (running→waiting) with no new
// pane content should not flood the parent.
func TestIssue1142_SameStatusSameHash_EmitsOnce(t *testing.T) {
	n := setupNotifierForDedupTest(t)

	base := time.Unix(1747800000, 0).UTC() // 2025-05-21-ish
	const childID = "child-dormant-1142"
	const hash = "sha1:dormant-output-bytes"

	events := make([]TransitionNotificationEvent, 5)
	for i := 0; i < 5; i++ {
		events[i] = TransitionNotificationEvent{
			ChildSessionID: childID,
			Profile:        "_test-1142",
			FromStatus:     "running",
			ToStatus:       "waiting",
			LastOutputHash: hash,
			// Spread 20 minutes apart so the existing 90s short-window
			// dedup is NOT what catches them — only the new output-hash
			// dedup can.
			Timestamp: base.Add(time.Duration(i) * 20 * time.Minute),
		}
	}

	got := countEmitted(n, events)
	if got != 1 {
		t.Fatalf("issue #1142: expected 1 emit for 5 identical (status+hash) events spaced 20m apart, got %d", got)
	}
}

// Test 2: 5 events with the same child + same to_status but DIFFERENT output
// hashes are all real progress and must all emit. A child writing new content
// on every transition is doing work and the operator wants every signal.
func TestIssue1142_SameStatusDifferentHash_EmitsEachTime(t *testing.T) {
	n := setupNotifierForDedupTest(t)

	base := time.Unix(1747800000, 0).UTC()
	const childID = "child-progressing-1142"

	events := make([]TransitionNotificationEvent, 5)
	for i := 0; i < 5; i++ {
		events[i] = TransitionNotificationEvent{
			ChildSessionID: childID,
			Profile:        "_test-1142",
			FromStatus:     "running",
			ToStatus:       "waiting",
			LastOutputHash: "sha1:progress-step-" + time.Duration(i).String(),
			// Spread 10 minutes apart — well outside the 90s window, so
			// pre-fix this passes too. The new logic must NOT over-dedup.
			Timestamp: base.Add(time.Duration(i) * 10 * time.Minute),
		}
	}

	got := countEmitted(n, events)
	if got != 5 {
		t.Fatalf("issue #1142: expected 5 emits for 5 progressing (different hashes), got %d", got)
	}
}

// Test 3: same child re-emitting with a DIFFERENT to_status (waiting → error)
// must emit each time. Status oscillation is a real state change the operator
// needs to see — dedup must key on to_status, not just child_id+hash.
func TestIssue1142_DifferentStatus_EmitsEachTime(t *testing.T) {
	n := setupNotifierForDedupTest(t)

	base := time.Unix(1747800000, 0).UTC()
	const childID = "child-oscillating-1142"
	const hash = "sha1:same-pane-content"

	statuses := []string{"waiting", "error", "waiting", "idle", "waiting"}
	events := make([]TransitionNotificationEvent, len(statuses))
	for i, s := range statuses {
		events[i] = TransitionNotificationEvent{
			ChildSessionID: childID,
			Profile:        "_test-1142",
			FromStatus:     "running",
			ToStatus:       s,
			// Same hash across all events — the output didn't change,
			// only the status classification did. We still want each
			// distinct status to emit so the operator sees the
			// transition.
			LastOutputHash: hash,
			Timestamp:      base.Add(time.Duration(i) * 5 * time.Minute),
		}
	}

	got := countEmitted(n, events)
	if got != len(statuses) {
		t.Fatalf("issue #1142: expected %d emits for %d distinct statuses, got %d",
			len(statuses), len(statuses), got)
	}
}

// Test 4: after the TTL elapses, the same (child, status, hash) emits again
// as a liveness ping. Without this the operator could think a child died
// silently after one initial event hours ago; with this they get periodic
// confirmation the child is still in the waiting state.
//
// We drive elapsed time via event.Timestamp rather than wall-clock sleep so
// the test is fast and deterministic. The TTL is the notifier's
// outputHashDedupTTL (default 2 hours).
func TestIssue1142_AfterTTL_LivenessPingReEmits(t *testing.T) {
	n := setupNotifierForDedupTest(t)

	base := time.Unix(1747800000, 0).UTC()
	const childID = "child-liveness-1142"
	const hash = "sha1:still-dormant"

	// First event: emit.
	ev1 := TransitionNotificationEvent{
		ChildSessionID: childID,
		Profile:        "_test-1142",
		FromStatus:     "running",
		ToStatus:       "waiting",
		LastOutputHash: hash,
		Timestamp:      base,
	}

	// Second event: same child, same status, same hash, BEFORE the TTL
	// elapses. Must dedup.
	ev2 := ev1
	ev2.Timestamp = base.Add(90 * time.Minute) // < default 2h TTL

	// Third event: same child, same status, same hash, AFTER the TTL
	// elapses. Must re-emit as a liveness ping.
	ev3 := ev1
	ev3.Timestamp = base.Add(150 * time.Minute) // > default 2h TTL

	events := []TransitionNotificationEvent{ev1, ev2, ev3}

	got := countEmitted(n, events)
	if got != 2 {
		t.Fatalf("issue #1142: expected 2 emits (initial + liveness ping after TTL), got %d", got)
	}
}

// Boundary case: an event with no output hash must still benefit from the
// existing 90s short-window dedup (back-compat with any caller that hasn't
// been updated to populate the new field). Five rapid fires within 90s with
// no hash collapse to one. Outside 90s with no hash, every fire emits — the
// new TTL only kicks in when a hash is present.
func TestIssue1142_NoHash_FallsBackToLegacyWindow(t *testing.T) {
	n := setupNotifierForDedupTest(t)

	base := time.Unix(1747800000, 0).UTC()
	const childID = "child-no-hash-1142"

	// Five fires within 90s with empty hash: legacy dedup catches them.
	rapid := make([]TransitionNotificationEvent, 5)
	for i := 0; i < 5; i++ {
		rapid[i] = TransitionNotificationEvent{
			ChildSessionID: childID,
			Profile:        "_test-1142",
			FromStatus:     "running",
			ToStatus:       "waiting",
			LastOutputHash: "",
			Timestamp:      base.Add(time.Duration(i*10) * time.Second),
		}
	}
	if got := countEmitted(n, rapid); got != 1 {
		t.Fatalf("legacy 90s dedup: expected 1 emit for 5 rapid fires, got %d", got)
	}

	// Two fires spaced 10 minutes apart with empty hash: legacy dedup does
	// NOT catch them, so both emit. (Pre-fix behavior — the bug.) The new
	// output-hash dedup is opt-in via the hash being populated.
	n2 := setupNotifierForDedupTest(t)
	spaced := []TransitionNotificationEvent{
		{
			ChildSessionID: childID,
			Profile:        "_test-1142",
			FromStatus:     "running",
			ToStatus:       "waiting",
			Timestamp:      base,
		},
		{
			ChildSessionID: childID,
			Profile:        "_test-1142",
			FromStatus:     "running",
			ToStatus:       "waiting",
			Timestamp:      base.Add(10 * time.Minute),
		},
	}
	if got := countEmitted(n2, spaced); got != 2 {
		t.Fatalf("no-hash spaced fires: expected 2 emits (legacy behavior), got %d", got)
	}
}
