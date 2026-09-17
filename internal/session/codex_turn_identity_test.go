package session

import (
	"testing"
	"time"
)

func TestCodexDistinctCompletedTurnsReachInboxWithinShortWindow(t *testing.T) {
	inboxTestHome(t)
	profile := "_test-codex-turn-identity"
	now := time.Unix(1_780_000_000, 0)
	parentID := "parent-codex-turns"
	child := &Instance{
		ID:              "child-codex-turns",
		Title:           "codex-worker",
		ProjectPath:     "/tmp/codex-worker",
		GroupPath:       DefaultGroupPath,
		ParentSessionID: parentID,
		Tool:            "codex",
		Status:          StatusWaiting,
		CreatedAt:       now,
		CodexSessionID:  "thread-1",
	}
	parent := &Instance{
		ID:          parentID,
		Title:       "orchestrator",
		ProjectPath: "/tmp/parent",
		GroupPath:   DefaultGroupPath,
		Tool:        "claude",
		Status:      StatusRunning,
		CreatedAt:   now,
	}
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer storage.Close()
	if err := storage.SaveWithGroups([]*Instance{child, parent}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}

	d := NewTransitionDaemon()
	d.turnLiveCheck = func(*Instance) bool { return true }
	d.notifier.wake = nil

	setCompletedCodexTurnForTest(child, "thread-1", "turn-a")
	d.recordTerminalTurns(profile, map[string]*Instance{child.ID: child}, map[string]string{child.ID: "waiting"}, nil)
	first, err := DrainInboxForParent(parentID)
	if err != nil {
		t.Fatalf("drain turn A: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("turn A delivered %d records, want 1", len(first))
	}
	if first[0].LastOutputHash != "codex:thread-1:turn-a" {
		t.Fatalf("turn A identity = %q, want %q", first[0].LastOutputHash, "codex:thread-1:turn-a")
	}

	// Turn B has the same child, status, and user-visible transition as turn A,
	// and completes well inside the legacy 90-second window. Its validated Codex
	// turn id is the only reliable evidence that this is a new completion.
	setCompletedCodexTurnForTest(child, "thread-1", "turn-b")
	d.recordTerminalTurns(profile, map[string]*Instance{child.ID: child}, map[string]string{child.ID: "waiting"}, nil)
	second, err := DrainInboxForParent(parentID)
	if err != nil {
		t.Fatalf("drain turn B: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("distinct turn B inside 90 seconds delivered %d records, want 1", len(second))
	}
	if second[0].LastOutputHash != "codex:thread-1:turn-b" {
		t.Fatalf("turn B identity = %q, want %q", second[0].LastOutputHash, "codex:thread-1:turn-b")
	}

	// A daemon restart must not replay turn B. The notifier reloads its durable
	// state and still suppresses a same-turn retry after the short window.
	restarted := NewTransitionNotifier()
	restarted.wake = nil
	retry := TransitionNotificationEvent{
		ChildSessionID: child.ID,
		ChildTitle:     child.Title,
		Profile:        profile,
		FromStatus:     "running",
		ToStatus:       "waiting",
		Timestamp:      second[0].Timestamp.Add(91 * time.Second),
		LastOutputHash: transitionEventOutputHash(child),
	}
	got := restarted.NotifyTransition(retry)
	if got.DeliveryResult != transitionDeliveryDropped {
		t.Fatalf("same turn after daemon restart = %q, want deduped drop", got.DeliveryResult)
	}
	third, err := DrainInboxForParent(parentID)
	if err != nil {
		t.Fatalf("drain retried turn B: %v", err)
	}
	if len(third) != 0 {
		t.Fatalf("same turn B replay delivered %d records after restart, want 0", len(third))
	}

	// Once the producer's output-hash TTL expires it intentionally re-commits a
	// liveness ping. The durable consumer ledger must still recognize the same
	// turn fingerprint and prevent a second parent effect.
	lateRetry := retry
	lateRetry.Timestamp = second[0].Timestamp.Add(defaultOutputHashDedupTTL + time.Second)
	late := restarted.NotifyTransition(lateRetry)
	if late.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("same turn after producer TTL = %q, want committed liveness ping", late.DeliveryResult)
	}
	fourth, err := DrainInboxForParent(parentID)
	if err != nil {
		t.Fatalf("drain late turn B retry: %v", err)
	}
	if len(fourth) != 0 {
		t.Fatalf("consumer replayed consumed turn B after producer TTL: %d records", len(fourth))
	}
}

func TestCodexTurnIdentityRequiresConvergedBoundEvidence(t *testing.T) {
	tests := []struct {
		name                                                 string
		tool, bound, started, completed, startedSID, doneSID string
		want                                                 string
	}{
		{name: "matching", tool: "codex", bound: "thread-1", started: "thread-1:turn-a", completed: "thread-1:turn-a", startedSID: "thread-1", doneSID: "thread-1", want: "codex:thread-1:turn-a"},
		{name: "missing completion", tool: "codex", bound: "thread-1", started: "thread-1:turn-a", startedSID: "thread-1"},
		{name: "mismatched generation", tool: "codex", bound: "thread-1", started: "thread-1:turn-b", completed: "thread-1:turn-a", startedSID: "thread-1", doneSID: "thread-1"},
		{name: "wrong bound session", tool: "codex", bound: "thread-2", started: "thread-1:turn-a", completed: "thread-1:turn-a", startedSID: "thread-1", doneSID: "thread-1"},
		{name: "mismatched evidence sessions", tool: "codex", bound: "thread-1", started: "thread-1:turn-a", completed: "thread-1:turn-a", startedSID: "thread-1", doneSID: "thread-2"},
		{name: "legacy tool", tool: "shell", bound: "thread-1", started: "thread-1:turn-a", completed: "thread-1:turn-a", startedSID: "thread-1", doneSID: "thread-1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inst := &Instance{
				Tool:                        tc.tool,
				CodexSessionID:              tc.bound,
				codexStartedGeneration:      tc.started,
				codexCompletedGeneration:    tc.completed,
				codexStartedSessionID:       tc.startedSID,
				codexCompletedSessionID:     tc.doneSID,
				codexInvalidatingGeneration: "",
			}
			if got := transitionEventOutputHash(inst); got != tc.want {
				t.Fatalf("transitionEventOutputHash() = %q, want %q", got, tc.want)
			}
		})
	}

	invalidated := &Instance{
		Tool:                        "codex",
		CodexSessionID:              "thread-1",
		codexStartedGeneration:      "thread-1:turn-a",
		codexCompletedGeneration:    "thread-1:turn-a",
		codexStartedSessionID:       "thread-1",
		codexCompletedSessionID:     "thread-1",
		codexInvalidatingGeneration: "thread-1:turn-a",
	}
	if got := transitionEventOutputHash(invalidated); got != "" {
		t.Fatalf("invalidated completion identity = %q, want empty", got)
	}
}

func TestShortWindowDedupRemainsForNonCodexSignals(t *testing.T) {
	base := time.Unix(1_780_000_000, 0)
	for _, tc := range []struct {
		name, first, second string
	}{
		{name: "legacy empty signal"},
		{name: "claude transcript signal", first: "jsonl:100", second: "jsonl:200"},
		{name: "untrusted lookalike", first: "codexish:thread-1:turn-a", second: "codexish:thread-1:turn-b"},
		{name: "upgrade boundary is conservative", second: "codex:thread-1:turn-b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := &TransitionNotifier{state: transitionNotifyState{Records: map[string]transitionNotifyRecord{
				"child": {From: "running", To: "waiting", At: base.Unix(), OutputHash: tc.first},
			}}}
			event := TransitionNotificationEvent{
				ChildSessionID: "child",
				FromStatus:     "running",
				ToStatus:       "waiting",
				Timestamp:      base.Add(time.Second),
				LastOutputHash: tc.second,
			}
			if !n.isDuplicate(event) {
				t.Fatal("non-Codex signal bypassed the legacy short-window dedup")
			}
		})
	}
}

func TestShortWindowAllowsDistinctTrustedCodexTurns(t *testing.T) {
	base := time.Unix(1_780_000_000, 0)
	n := &TransitionNotifier{state: transitionNotifyState{Records: map[string]transitionNotifyRecord{
		"child": {From: "running", To: "waiting", At: base.Unix(), OutputHash: "codex:thread-1:turn-a"},
	}}}
	distinctTurn := TransitionNotificationEvent{
		ChildSessionID: "child",
		FromStatus:     "running",
		ToStatus:       "waiting",
		Timestamp:      base.Add(time.Second),
		LastOutputHash: "codex:thread-1:turn-b",
	}
	if n.isDuplicate(distinctTurn) {
		t.Fatal("distinct validated Codex turn was suppressed by the legacy short window")
	}

	sameTurn := distinctTurn
	sameTurn.LastOutputHash = "codex:thread-1:turn-a"
	if !n.isDuplicate(sameTurn) {
		t.Fatal("same validated Codex turn was not deduplicated")
	}
}

func setCompletedCodexTurnForTest(inst *Instance, sessionID, turnID string) {
	generation := sessionID + ":" + turnID
	inst.mu.Lock()
	defer inst.mu.Unlock()
	inst.CodexSessionID = sessionID
	inst.codexStartedGeneration = generation
	inst.codexCompletedGeneration = generation
	inst.codexStartedSessionID = sessionID
	inst.codexCompletedSessionID = sessionID
	inst.codexInvalidatingGeneration = ""
}
