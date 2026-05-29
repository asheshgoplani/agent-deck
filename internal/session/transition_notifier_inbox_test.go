package session

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestNotifier_OrphanChildLogsWarnOnce is the regression for cause A: a child
// born without a parent_session_id (e.g. via a worktree-setup hook that did
// not preserve $AGENTDECK_INSTANCE_ID). The notifier should:
//   - log a single WARN line to notifier-orphans.log per orphan child id
//   - drop the event (no commit attempt)
//   - NOT log the orphan again on the next transition for the same child
//
// The "once per child" property prevents log spam on long-lived orphans
// firing many transitions over their lifetime.
func TestNotifier_OrphanChildLogsWarnOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })
	if err := os.MkdirAll(home+"/.agent-deck", 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	profile := "_test-orphan"
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	defer storage.Close()

	now := time.Now()
	child := &Instance{
		ID:              "orphan-child-x",
		Title:           "worker",
		ProjectPath:     "/tmp/orphan",
		GroupPath:       DefaultGroupPath,
		ParentSessionID: "", // ORPHAN — no parent linkage at creation
		Tool:            "shell",
		Status:          StatusWaiting,
		CreatedAt:       now,
	}
	if err := storage.SaveWithGroups([]*Instance{child}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	n := NewTransitionNotifier()

	for i := 0; i < 3; i++ {
		ev := TransitionNotificationEvent{
			ChildSessionID: child.ID,
			ChildTitle:     child.Title,
			Profile:        profile,
			FromStatus:     "running",
			ToStatus:       "waiting",
			Timestamp:      now.Add(time.Duration(i+1) * 5 * time.Minute), // bypass dedup
		}
		result := n.NotifyTransition(ev)
		if result.DeliveryResult != transitionDeliveryDropped {
			t.Fatalf("orphan event #%d expected dropped, got %q", i, result.DeliveryResult)
		}
	}

	data, err := os.ReadFile(transitionNotifierOrphanLogPath())
	if err != nil {
		t.Fatalf("orphan log must exist after warn: %v", err)
	}
	count := strings.Count(string(data), child.ID)
	if count != 1 {
		t.Fatalf("expected exactly 1 WARN line per child, got %d (content=%s)", count, data)
	}
	if !strings.Contains(string(data), "orphan child detected") {
		t.Fatalf("orphan log must include actionable hint, got %q", data)
	}
}

// TestNotifier_TopLevelConductorSelfSuppress is the regression for cause C.
// A conductor session is top-level (its own ParentSessionID is empty). When
// the conductor itself transitions running→waiting, NotifyTransition must
// short-circuit (dropped) — it has no upstream to notify, so nothing should
// land in any inbox.
func TestNotifier_TopLevelConductorSelfSuppress(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })
	if err := os.MkdirAll(home+"/.agent-deck", 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	profile := "_test-self-suppress"
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	defer storage.Close()

	now := time.Now()
	conductor := &Instance{
		ID:              "conductor-top-1",
		Title:           "conductor-agent-deck", // matches isConductorSessionTitle
		ProjectPath:     "/tmp/conductor",
		GroupPath:       DefaultGroupPath,
		ParentSessionID: "", // top-level
		Tool:            "claude",
		Status:          StatusWaiting,
		CreatedAt:       now,
	}
	if err := storage.SaveWithGroups([]*Instance{conductor}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	n := NewTransitionNotifier()

	ev := TransitionNotificationEvent{
		ChildSessionID: conductor.ID,
		ChildTitle:     conductor.Title,
		Profile:        profile,
		FromStatus:     "running",
		ToStatus:       "waiting",
		Timestamp:      now,
	}
	result := n.NotifyTransition(ev)
	n.Flush()

	if result.DeliveryResult != transitionDeliveryDropped {
		t.Fatalf("top-level conductor must self-suppress with dropped, got %q", result.DeliveryResult)
	}
}

// TestNotifyTransition_BusyParentStillCommittedToInbox is the case-10 unit
// assertion for issue #1225: a parent that is StatusRunning (busy) is NO
// LONGER a reason to defer/enqueue/miss the transition. Under the pull model
// the event is committed straight to the busy parent's durable outbox, and the
// parent drains it at its own turn boundary. (This replaces the old
// TestNotifyTransition_ParentBusyEnqueuesNotMarksDone, which asserted the now-
// removed busy-defer enqueue.)
func TestNotifyTransition_BusyParentStillCommittedToInbox(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	ResetInboxFingerprintCacheForTest()
	t.Cleanup(func() {
		ClearUserConfigCache()
		ResetInboxFingerprintCacheForTest()
	})

	if err := os.MkdirAll(tmpHome+"/.agent-deck", 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	profile := "_test-busy-commit"
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	defer storage.Close()

	now := time.Now()
	child := &Instance{
		ID:              "child-busy-1",
		Title:           "worker",
		ProjectPath:     "/tmp/child",
		GroupPath:       DefaultGroupPath,
		ParentSessionID: "parent-busy-1",
		Tool:            "shell",
		Status:          StatusWaiting,
		CreatedAt:       now,
	}
	parent := &Instance{
		ID:          "parent-busy-1",
		Title:       "orchestrator",
		ProjectPath: "/tmp/parent",
		GroupPath:   DefaultGroupPath,
		Tool:        "shell",
		Status:      StatusRunning, // parent is mid-task — must NOT block delivery
		CreatedAt:   now,
	}
	if err := storage.SaveWithGroups([]*Instance{child, parent}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	n := NewTransitionNotifier()
	t.Cleanup(n.Close)
	event := TransitionNotificationEvent{
		ChildSessionID: child.ID,
		ChildTitle:     child.Title,
		Profile:        profile,
		FromStatus:     "running",
		ToStatus:       "waiting",
		Timestamp:      now,
	}

	result := n.NotifyTransition(event)
	if result.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("busy parent: expected committed_inbox (no enqueue/defer/miss), got %q", result.DeliveryResult)
	}

	inbox, err := DrainInboxForParent(parent.ID)
	if err != nil {
		t.Fatalf("DrainInboxForParent: %v", err)
	}
	if len(inbox) != 1 {
		t.Fatalf("busy parent inbox has %d records, want exactly 1", len(inbox))
	}
	if inbox[0].ChildSessionID != child.ID {
		t.Fatalf("inbox record wrong child: %+v", inbox[0])
	}
}
