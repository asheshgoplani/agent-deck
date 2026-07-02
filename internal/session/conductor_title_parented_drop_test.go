package session

// Regression for the conductor-title silent-drop bug.
//
// transition_notifier.go's NotifyTransition / NotifyFinished (and
// taskworker.go's DeliverCompletion) used to pre-filter on
// isConductorSessionTitle(event.ChildTitle) and drop UNCONDITIONALLY. That keys
// on the child's TITLE PREFIX alone, so a legitimate child whose title merely
// starts with "conductor-" (e.g. a wrap-up worker named conductor-wrapup-design)
// but which HAS a real parent never notified its parent — silently (no commit,
// no dead-letter, no breadcrumb). Proven by a live A/B: a parented child titled
// conductor-reprotest emitted the sentinel and wrote its status .json, but the
// daemon dropped it, while an identical normal-reprotest delivered
// committed_inbox.
//
// The correct, ParentSessionID-qualified suppression already lives downstream in
// resolveParentIDForInbox (inbox_outbox.go): only a TOP-LEVEL/self-pointing
// conductor (empty/self parent) is dropped, silently (deadLetterReasonSelfConductor).
// The fix removed the buggy title-only pre-filters and delegates to that check.
//
// These tests are NON-VACUOUS: the "committed" assertions FAIL on the pre-fix
// code (which returned dropped), and the "dropped + silent" assertions ensure
// the legitimate top-level-conductor suppression was NOT thrown out with the bug.

import (
	"os"
	"strings"
	"testing"
	"time"
)

// seedNotifyInstances sets up an isolated HOME + profile storage with the given
// instances persisted, mirroring the setup used by the existing notifier inbox
// tests (seedDoneParentChild et al.).
func seedNotifyInstances(t *testing.T, profile string, insts ...*Instance) {
	t.Helper()
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
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	defer storage.Close()
	if err := storage.SaveWithGroups(insts, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}
}

// conductorParent returns a live (idle) conductor parent, matching the proven
// seedDoneParentChild pattern (resolveParentNotificationTarget resolves an idle
// conductor in the test env without tmux).
func conductorParent(id string) *Instance {
	return &Instance{
		ID:          id,
		Title:       "conductor-harness-advisor",
		ProjectPath: "/tmp/parent",
		GroupPath:   DefaultGroupPath,
		Tool:        "claude",
		Status:      StatusIdle,
		CreatedAt:   time.Now(),
	}
}

// --- (a) parented conductor-TITLED child must be DELIVERED, not dropped -------

// TestConductorTitledChild_Transition_ParentedIsCommitted: a child titled
// conductor-* WITH a real parent must commit its running→waiting transition to
// the parent's durable inbox. This is the core regression: pre-fix this returned
// dropped_no_target with no inbox record.
func TestConductorTitledChild_Transition_ParentedIsCommitted(t *testing.T) {
	profile := "_test-condtitle-transition-parented"
	parent := conductorParent("parent-conductor-tx")
	child := &Instance{
		ID:              "child-conductor-titled-tx",
		Title:           "conductor-wrapup-design", // title prefix that USED to trigger the drop
		ProjectPath:     "/tmp/child",
		GroupPath:       DefaultGroupPath,
		ParentSessionID: parent.ID, // but it has a REAL parent — must deliver
		Tool:            "claude",
		Status:          StatusWaiting,
		CreatedAt:       time.Now(),
	}
	seedNotifyInstances(t, profile, parent, child)

	n := NewTransitionNotifier()
	t.Cleanup(n.Close)

	now := time.Now()
	res := n.NotifyTransition(TransitionNotificationEvent{
		ChildSessionID: child.ID,
		ChildTitle:     child.Title,
		Profile:        profile,
		FromStatus:     "running",
		ToStatus:       "waiting",
		Timestamp:      now,
	})
	if res.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("parented conductor-titled child: want %q, got %q (reason=%q) — title-prefix drop regressed",
			transitionDeliveryCommitted, res.DeliveryResult, res.DeadLetterReason)
	}

	inbox := readInboxLines(t, parent.ID)
	if len(inbox) != 1 {
		t.Fatalf("parent inbox has %d records, want exactly 1: %+v", len(inbox), inbox)
	}
	if inbox[0].ChildSessionID != child.ID || inbox[0].TargetSessionID != parent.ID {
		t.Fatalf("inbox record mis-targeted: child=%q target=%q", inbox[0].ChildSessionID, inbox[0].TargetSessionID)
	}
}

// TestConductorTitledChild_Finished_ParentedIsCommitted: the one-shot/sentinel
// "finished" path (NotifyFinished) must likewise deliver a parented
// conductor-titled child's completion.
func TestConductorTitledChild_Finished_ParentedIsCommitted(t *testing.T) {
	profile := "_test-condtitle-finished-parented"
	parent := conductorParent("parent-conductor-fin")
	child := &Instance{
		ID:              "child-conductor-titled-fin",
		Title:           "conductor-reprotest",
		ProjectPath:     "/tmp/child",
		GroupPath:       DefaultGroupPath,
		ParentSessionID: parent.ID,
		Tool:            "claude",
		Status:          StatusWaiting,
		CreatedAt:       time.Now(),
	}
	seedNotifyInstances(t, profile, parent, child)

	n := NewTransitionNotifier()
	t.Cleanup(n.Close)

	res := n.NotifyFinished(TransitionNotificationEvent{
		ChildSessionID: child.ID,
		ChildTitle:     child.Title,
		Profile:        profile,
		DoneStatus:     "ok",
		DoneSummary:    "design complete",
		Timestamp:      time.Now(),
	})
	if res.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("parented conductor-titled finished: want %q, got %q (reason=%q)",
			transitionDeliveryCommitted, res.DeliveryResult, res.DeadLetterReason)
	}

	inbox := readInboxLines(t, parent.ID)
	if len(inbox) != 1 {
		t.Fatalf("parent inbox has %d records, want exactly 1: %+v", len(inbox), inbox)
	}
	if inbox[0].Kind != transitionKindFinished || inbox[0].DoneStatus != "ok" {
		t.Fatalf("finished record missing outcome: kind=%q status=%q", inbox[0].Kind, inbox[0].DoneStatus)
	}
}

// TestConductorTitledChild_DeliverCompletion_ParentedIsCommitted: the durable
// one-shot worker completion path (DeliverCompletion, taskworker.go) had the
// SAME title-only pre-filter and must also deliver a parented conductor-titled
// worker.
func TestConductorTitledChild_DeliverCompletion_ParentedIsCommitted(t *testing.T) {
	profile := "_test-condtitle-deliver-parented"
	parent := conductorParent("parent-conductor-dc")
	child := &Instance{
		ID:              "child-conductor-titled-dc",
		Title:           "conductor-named-worker",
		ProjectPath:     "/tmp/child",
		GroupPath:       DefaultGroupPath,
		ParentSessionID: parent.ID,
		Tool:            "claude",
		Status:          StatusWaiting,
		CreatedAt:       time.Now(),
	}
	seedNotifyInstances(t, profile, parent, child)

	n := NewTransitionNotifier()
	t.Cleanup(n.Close)

	if !n.DeliverCompletion(CompletionRecord{
		ChildID: child.ID,
		Profile: profile,
		Title:   child.Title,
		Status:  "ok",
		Summary: "task done",
	}) {
		t.Fatalf("parented conductor-titled worker: DeliverCompletion returned not-committed (title-prefix drop regressed)")
	}
	inbox := readInboxLines(t, parent.ID)
	if len(inbox) != 1 || inbox[0].ChildSessionID != child.ID {
		t.Fatalf("parent inbox has %d records, want exactly 1 for child: %+v", len(inbox), inbox)
	}
}

// --- (b) top-level / self conductor must STILL be dropped, and SILENTLY -------

// TestTopLevelConductor_Transition_StillDroppedSilently: a genuine top-level
// conductor (empty parent) titled conductor-* still self-suppresses with
// dropped_no_target AND silently — no dead-letter record, no missed-log line.
// This guards against the fix over-correcting and delivering a root conductor's
// own transitions to nothing.
func TestTopLevelConductor_Transition_StillDroppedSilently(t *testing.T) {
	profile := "_test-condtitle-toplevel-tx"
	cond := &Instance{
		ID:              "conductor-top-tx",
		Title:           "conductor-foo",
		ProjectPath:     "/tmp/cond",
		GroupPath:       DefaultGroupPath,
		ParentSessionID: "", // TOP-LEVEL — no upstream to notify
		Tool:            "claude",
		Status:          StatusWaiting,
		CreatedAt:       time.Now(),
	}
	seedNotifyInstances(t, profile, cond)

	n := NewTransitionNotifier()
	t.Cleanup(n.Close)

	res := n.NotifyTransition(TransitionNotificationEvent{
		ChildSessionID: cond.ID,
		ChildTitle:     cond.Title,
		Profile:        profile,
		FromStatus:     "running",
		ToStatus:       "waiting",
		Timestamp:      time.Now(),
	})
	if res.DeliveryResult != transitionDeliveryDropped {
		t.Fatalf("top-level conductor must self-suppress (dropped), got %q", res.DeliveryResult)
	}
	if res.DeadLetterReason != deadLetterReasonSelfConductor {
		t.Fatalf("top-level conductor drop must be reason %q, got %q", deadLetterReasonSelfConductor, res.DeadLetterReason)
	}
	assertSilentDrop(t, cond.ID)
}

// TestTopLevelConductor_Finished_StillDroppedSilently: same guard on the
// NotifyFinished path.
func TestTopLevelConductor_Finished_StillDroppedSilently(t *testing.T) {
	profile := "_test-condtitle-toplevel-fin"
	cond := &Instance{
		ID:              "conductor-top-fin",
		Title:           "conductor-foo",
		ProjectPath:     "/tmp/cond",
		GroupPath:       DefaultGroupPath,
		ParentSessionID: "",
		Tool:            "claude",
		Status:          StatusWaiting,
		CreatedAt:       time.Now(),
	}
	seedNotifyInstances(t, profile, cond)

	n := NewTransitionNotifier()
	t.Cleanup(n.Close)

	res := n.NotifyFinished(TransitionNotificationEvent{
		ChildSessionID: cond.ID,
		ChildTitle:     cond.Title,
		Profile:        profile,
		DoneStatus:     "ok",
		Timestamp:      time.Now(),
	})
	if res.DeliveryResult != transitionDeliveryDropped {
		t.Fatalf("top-level conductor finished must self-suppress (dropped), got %q", res.DeliveryResult)
	}
	if res.DeadLetterReason != deadLetterReasonSelfConductor {
		t.Fatalf("top-level conductor finished drop must be reason %q, got %q", deadLetterReasonSelfConductor, res.DeadLetterReason)
	}
	assertSilentDrop(t, cond.ID)
}

// TestSelfPointingConductor_Transition_StillDropped: a conductor whose
// ParentSessionID points at ITSELF (self-loop) is also treated as a root and
// suppressed — mirroring inbox_outbox.go:368.
func TestSelfPointingConductor_Transition_StillDropped(t *testing.T) {
	profile := "_test-condtitle-selfloop-tx"
	cond := &Instance{
		ID:              "conductor-self-tx",
		Title:           "conductor-foo",
		ProjectPath:     "/tmp/cond",
		GroupPath:       DefaultGroupPath,
		ParentSessionID: "conductor-self-tx", // self-pointing == still a root
		Tool:            "claude",
		Status:          StatusWaiting,
		CreatedAt:       time.Now(),
	}
	seedNotifyInstances(t, profile, cond)

	n := NewTransitionNotifier()
	t.Cleanup(n.Close)

	res := n.NotifyTransition(TransitionNotificationEvent{
		ChildSessionID: cond.ID,
		ChildTitle:     cond.Title,
		Profile:        profile,
		FromStatus:     "running",
		ToStatus:       "waiting",
		Timestamp:      time.Now(),
	})
	if res.DeliveryResult != transitionDeliveryDropped {
		t.Fatalf("self-pointing conductor must be dropped, got %q", res.DeliveryResult)
	}
	if res.DeadLetterReason != deadLetterReasonSelfConductor {
		t.Fatalf("self-pointing conductor drop must be reason %q, got %q", deadLetterReasonSelfConductor, res.DeadLetterReason)
	}
	assertSilentDrop(t, cond.ID)
}

// TestTopLevelConductor_DeliverCompletion_StillDropped: DeliverCompletion must
// return not-committed for a top-level/self conductor.
func TestTopLevelConductor_DeliverCompletion_StillDropped(t *testing.T) {
	profile := "_test-condtitle-toplevel-dc"
	cond := &Instance{
		ID:              "conductor-top-dc",
		Title:           "conductor-foo",
		ProjectPath:     "/tmp/cond",
		GroupPath:       DefaultGroupPath,
		ParentSessionID: "",
		Tool:            "claude",
		Status:          StatusWaiting,
		CreatedAt:       time.Now(),
	}
	seedNotifyInstances(t, profile, cond)

	n := NewTransitionNotifier()
	t.Cleanup(n.Close)

	if n.DeliverCompletion(CompletionRecord{
		ChildID: cond.ID,
		Profile: profile,
		Title:   cond.Title,
		Status:  "ok",
	}) {
		t.Fatalf("top-level conductor DeliverCompletion must NOT commit")
	}
}

// assertSilentDrop verifies a self_conductor suppression left NO operator-visible
// trail: no dead-letter record and no missed-log line for the child. (terminalDrop
// returns early for deadLetterReasonSelfConductor, so the drop must be silent.)
func assertSilentDrop(t *testing.T, childID string) {
	t.Helper()
	recs, err := ReadDeadLetter(childID)
	if err != nil {
		t.Fatalf("ReadDeadLetter: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("self_conductor drop must be SILENT (no dead-letter), got %d records", len(recs))
	}
	missed, err := os.ReadFile(transitionNotifierMissedPath())
	if err != nil {
		if os.IsNotExist(err) {
			return // no missed log at all — silent, good
		}
		t.Fatalf("read missed log: %v", err)
	}
	if strings.Contains(string(missed), childID) {
		t.Fatalf("self_conductor drop must be SILENT (no missed-log line), got: %s", missed)
	}
}
