package session

import (
	"os"
	"strings"
	"testing"
	"time"
)

// Regression coverage for the running-session variant of issue #962
// reported by @seanyoungberg after PR #992 closed the removed-session
// angle. When a conductor delivers a transition event but the target is
// busy (StatusRunning), the notifier persists the event to the
// per-conductor inbox JSONL with delivery_result=deferred_target_busy.
// Pre-fix, these entries were never cleaned up — neither when the target
// became available and was successfully reached again, nor when they
// aged past a sensible TTL. Conductor-A on the reporter's machine had 15
// entries spanning 4 days (7 unique children) growing unbounded on every
// busy day.

// TestTransitionNotifier_TargetBusyEntries_CleanedOnSuccess_RegressionFor962Variant
// asserts: an inbox entry persisted for (child, from, to) is removed
// when a later successful delivery covers the same tuple. The hook is
// SweepInboxByTuple, invoked from the success paths in the notifier.
func TestTransitionNotifier_TargetBusyEntries_CleanedOnSuccess_RegressionFor962Variant(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	ResetInboxFingerprintCacheForTest()
	t.Cleanup(func() {
		ClearUserConfigCache()
		ResetInboxFingerprintCacheForTest()
	})

	parent := "parent-962-variant-success"
	// Simulate the exhausted-busy-retries history: an earlier transition
	// of the same (child, from, to) already landed in the inbox.
	earlier := TransitionNotificationEvent{
		ChildSessionID:  "pepper",
		ChildTitle:      "pepper",
		Profile:         "_test",
		FromStatus:      "running",
		ToStatus:        "waiting",
		Timestamp:       time.Now().Add(-2 * time.Hour),
		TargetSessionID: parent,
		TargetKind:      "parent",
		DeliveryResult:  transitionDeliveryDeferred,
	}
	if err := WriteInboxEvent(parent, earlier); err != nil {
		t.Fatalf("WriteInboxEvent earlier: %v", err)
	}

	// An unrelated entry must survive the tuple-targeted sweep.
	other := earlier
	other.ChildSessionID = "garlic"
	other.Timestamp = time.Now().Add(-1 * time.Hour)
	if err := WriteInboxEvent(parent, other); err != nil {
		t.Fatalf("WriteInboxEvent other: %v", err)
	}

	// A new live transition for the same (child=pepper, running→waiting)
	// successfully reaches the now-idle target. The notifier must sweep
	// the inbox for entries matching this tuple as part of the success
	// path.
	n := newInboxTestNotifier(t, home)
	n.busyBackoff = []time.Duration{5 * time.Millisecond}
	n.availability = func(profile, targetID string) bool { return true }
	n.sender = func(profile, targetID, message string) error { return nil }

	live := earlier
	live.Timestamp = time.Now() // fresh fingerprint, not "terminated"
	n.scheduleBusyRetry(live)
	n.Flush()

	remaining, err := ReadAndTruncateInbox(parent)
	if err != nil {
		t.Fatalf("ReadAndTruncateInbox: %v", err)
	}
	for _, ev := range remaining {
		if ev.ChildSessionID == "pepper" && ev.FromStatus == "running" && ev.ToStatus == "waiting" {
			t.Fatalf("expected inbox entry for (pepper, running, waiting) removed after successful redelivery; still present: %+v", ev)
		}
	}
	// The unrelated (garlic) entry must survive.
	var survived bool
	for _, ev := range remaining {
		if ev.ChildSessionID == "garlic" {
			survived = true
			break
		}
	}
	if !survived {
		t.Fatalf("tuple sweep must not drop unrelated inbox entries; got %d entries: %+v", len(remaining), remaining)
	}
}

// TestTransitionNotifier_TargetBusyEntries_TTLEnforced_RegressionFor962Variant
// asserts: inbox entries persisted longer ago than the configured TTL
// are removed by the TTL sweep. Without this, even a fixed cleanup-on-
// success path is not enough — entries for children that never see
// another transition (e.g. a worker that completed once and stayed gone)
// accumulate forever.
func TestTransitionNotifier_TargetBusyEntries_TTLEnforced_RegressionFor962Variant(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	ResetInboxFingerprintCacheForTest()
	t.Cleanup(func() {
		ClearUserConfigCache()
		ResetInboxFingerprintCacheForTest()
	})

	parent := "parent-962-variant-ttl"

	// One entry well past the 7-day TTL — must be swept.
	stale := TransitionNotificationEvent{
		ChildSessionID:  "ancient-worker",
		ChildTitle:      "ancient",
		Profile:         "_test",
		FromStatus:      "running",
		ToStatus:        "waiting",
		Timestamp:       time.Now().Add(-10 * 24 * time.Hour),
		TargetSessionID: parent,
		TargetKind:      "parent",
		DeliveryResult:  transitionDeliveryDeferred,
	}
	if err := WriteInboxEvent(parent, stale); err != nil {
		t.Fatalf("WriteInboxEvent stale: %v", err)
	}

	// One entry within the TTL — must survive.
	fresh := stale
	fresh.ChildSessionID = "recent-worker"
	fresh.Timestamp = time.Now().Add(-1 * time.Hour)
	if err := WriteInboxEvent(parent, fresh); err != nil {
		t.Fatalf("WriteInboxEvent fresh: %v", err)
	}

	dropped, err := SweepInboxByTTL(7 * 24 * time.Hour)
	if err != nil {
		t.Fatalf("SweepInboxByTTL: %v", err)
	}
	if dropped != 1 {
		t.Fatalf("expected exactly 1 stale entry dropped, got %d", dropped)
	}

	remaining, err := ReadAndTruncateInbox(parent)
	if err != nil {
		t.Fatalf("ReadAndTruncateInbox: %v", err)
	}
	if len(remaining) != 1 {
		t.Fatalf("expected 1 surviving inbox entry, got %d: %+v", len(remaining), remaining)
	}
	if remaining[0].ChildSessionID != "recent-worker" {
		t.Fatalf("TTL sweep dropped the wrong entry: %+v", remaining[0])
	}
}

// TestTransitionNotifier_SweepInboxByTuple_DropsMatching is a direct
// unit test for the public sweep helper. Guards against accidental
// matches on similar tuples (e.g. same child, different status flip).
func TestTransitionNotifier_SweepInboxByTuple_DropsMatching(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	ResetInboxFingerprintCacheForTest()
	t.Cleanup(func() {
		ClearUserConfigCache()
		ResetInboxFingerprintCacheForTest()
	})

	parent := "parent-962-tuple"
	mk := func(child, from, to string, ago time.Duration) TransitionNotificationEvent {
		return TransitionNotificationEvent{
			ChildSessionID:  child,
			Profile:         "_test",
			FromStatus:      from,
			ToStatus:        to,
			Timestamp:       time.Now().Add(-ago),
			TargetSessionID: parent,
			TargetKind:      "parent",
			DeliveryResult:  transitionDeliveryDeferred,
		}
	}
	rows := []TransitionNotificationEvent{
		mk("pepper", "running", "waiting", 3*time.Hour),
		mk("pepper", "running", "waiting", 2*time.Hour), // duplicate tuple, different ts
		mk("pepper", "waiting", "running", 1*time.Hour), // SAME child, different tuple
		mk("garlic", "running", "waiting", 1*time.Hour), // different child
	}
	for i, ev := range rows {
		if err := WriteInboxEvent(parent, ev); err != nil {
			t.Fatalf("WriteInboxEvent row %d: %v", i, err)
		}
	}

	dropped, err := SweepInboxByTuple(parent, "pepper", "running", "waiting")
	if err != nil {
		t.Fatalf("SweepInboxByTuple: %v", err)
	}
	if dropped != 2 {
		t.Fatalf("expected 2 matching entries dropped, got %d", dropped)
	}

	remaining, err := ReadAndTruncateInbox(parent)
	if err != nil {
		t.Fatalf("ReadAndTruncateInbox: %v", err)
	}
	if len(remaining) != 2 {
		t.Fatalf("expected 2 surviving entries, got %d: %+v", len(remaining), remaining)
	}
	for _, ev := range remaining {
		if ev.ChildSessionID == "pepper" && ev.FromStatus == "running" && ev.ToStatus == "waiting" {
			t.Fatalf("matching entry survived sweep: %+v", ev)
		}
	}
}

// TestTransitionNotifier_SweepInboxByTTL_EnvVarOverride verifies that
// the daemon-facing TTL helper honors AGENT_DECK_INBOX_TTL when caller
// resolves the duration via InboxTTL().
func TestTransitionNotifier_SweepInboxByTTL_EnvVarOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	t.Setenv("AGENT_DECK_INBOX_TTL", "1h")
	ClearUserConfigCache()
	ResetInboxFingerprintCacheForTest()
	t.Cleanup(func() {
		ClearUserConfigCache()
		ResetInboxFingerprintCacheForTest()
	})

	ttl := InboxTTL()
	if ttl != time.Hour {
		t.Fatalf("AGENT_DECK_INBOX_TTL=1h must yield 1h, got %s", ttl)
	}

	// Default (env unset) must return the 7-day default.
	t.Setenv("AGENT_DECK_INBOX_TTL", "")
	if got := InboxTTL(); got != 7*24*time.Hour {
		t.Fatalf("InboxTTL default must be 7 days, got %s", got)
	}

	// Restore for the rest of the test.
	t.Setenv("AGENT_DECK_INBOX_TTL", "1h")

	parent := "parent-962-ttl-env"
	old := TransitionNotificationEvent{
		ChildSessionID:  "stale",
		Profile:         "_test",
		FromStatus:      "running",
		ToStatus:        "waiting",
		Timestamp:       time.Now().Add(-2 * time.Hour),
		TargetSessionID: parent,
		DeliveryResult:  transitionDeliveryDeferred,
	}
	if err := WriteInboxEvent(parent, old); err != nil {
		t.Fatalf("WriteInboxEvent: %v", err)
	}

	dropped, err := SweepInboxByTTL(InboxTTL())
	if err != nil {
		t.Fatalf("SweepInboxByTTL: %v", err)
	}
	if dropped != 1 {
		t.Fatalf("expected 1 entry dropped under 1h TTL, got %d", dropped)
	}
	if _, err := os.Stat(InboxPathFor(parent)); err == nil {
		// File still exists — must contain no surviving entries.
		remaining, err := ReadAndTruncateInbox(parent)
		if err != nil {
			t.Fatalf("ReadAndTruncateInbox: %v", err)
		}
		if len(remaining) != 0 {
			t.Fatalf("expected inbox emptied by TTL sweep, got %d entries", len(remaining))
		}
	}
	_ = strings.TrimSpace // keep imports stable across test edits
}
