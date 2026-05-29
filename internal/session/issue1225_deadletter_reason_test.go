package session

// Audit B5 — a completion whose parent was removed mid-flight, or whose parent
// lives in a different profile, must NOT be silently dropped. It is terminally
// dead-lettered WITH a distinguishing reason and an operator-visible missed-log
// line. And removing a conductor/parent must sweep its OWN inbox + consumed-turn
// ledger (previously leaked).

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestB5_ParentRemovedMidFlight_DeadLettersWithReasonAndLogs(t *testing.T) {
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
	if err := os.MkdirAll(home+"/.agent-deck", 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	profile := "_test-parent-removed"
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	defer storage.Close()

	now := time.Now()
	// Child references a parent that does NOT exist in this profile's registry
	// (removed mid-flight, or cross-profile). This is terminal but must be loud.
	child := &Instance{
		ID:              "child-orphaned-parent",
		Title:           "worker",
		ProjectPath:     "/tmp/c",
		GroupPath:       DefaultGroupPath,
		ParentSessionID: "parent-that-was-removed",
		Tool:            "shell",
		Status:          StatusWaiting,
		CreatedAt:       now,
	}
	if err := storage.SaveWithGroups([]*Instance{child}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	n := NewTransitionNotifier()
	res := n.NotifyTransition(TransitionNotificationEvent{
		ChildSessionID: child.ID,
		ChildTitle:     child.Title,
		Profile:        profile,
		FromStatus:     "running",
		ToStatus:       "waiting",
		Timestamp:      now,
	})
	if res.DeliveryResult != transitionDeliveryDropped {
		t.Fatalf("expected dropped, got %q", res.DeliveryResult)
	}
	if res.DeadLetterReason != deadLetterReasonParentMissing {
		t.Fatalf("expected reason %q, got %q", deadLetterReasonParentMissing, res.DeadLetterReason)
	}

	// Dead-letter record exists with the reason.
	recs, err := ReadDeadLetter(child.ID)
	if err != nil {
		t.Fatalf("ReadDeadLetter: %v", err)
	}
	if len(recs) == 0 {
		t.Fatalf("parent-removed completion must be dead-lettered, not silently dropped")
	}
	if recs[0].DeadLetterReason != deadLetterReasonParentMissing {
		t.Fatalf("dead-letter record missing reason, got %q", recs[0].DeadLetterReason)
	}

	// Operator-visible missed-log line carries the reason.
	missed, err := os.ReadFile(transitionNotifierMissedPath())
	if err != nil {
		t.Fatalf("missed log must exist: %v", err)
	}
	if !strings.Contains(string(missed), deadLetterReasonParentMissing) {
		t.Fatalf("missed log must name the reason, got %q", missed)
	}
}

func TestB5_RemovingParentSweepsItsOwnInboxAndLedger(t *testing.T) {
	inboxTestHome(t)
	parent := "conductor-parent-1777500000"

	// Parent accumulated a pending child completion and a consumed-turn ledger.
	commitForStop(t, parent, "some-child", "turn-1")
	if _, err := DrainInboxForParent(parent); err != nil {
		t.Fatalf("seed drain (writes consumed ledger): %v", err)
	}
	// Re-commit so the parent inbox is non-empty at removal time too.
	commitForStop(t, parent, "another-child", "turn-2")

	inboxPath := InboxPathFor(parent)
	ledgerPath := consumedTurnsPathFor(parent)
	if _, err := os.Stat(inboxPath); err != nil {
		t.Fatalf("precondition: parent inbox should exist: %v", err)
	}
	if _, err := os.Stat(ledgerPath); err != nil {
		t.Fatalf("precondition: consumed ledger should exist: %v", err)
	}

	if _, err := SweepInboxesForChildSession(parent); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if _, err := os.Stat(inboxPath); !os.IsNotExist(err) {
		t.Fatalf("parent's own inbox not swept on removal (err=%v)", err)
	}
	if _, err := os.Stat(ledgerPath); !os.IsNotExist(err) {
		t.Fatalf("parent's own consumed-turn ledger not swept on removal (err=%v)", err)
	}
}
