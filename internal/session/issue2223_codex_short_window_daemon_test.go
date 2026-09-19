package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestIssue2223_CodexTwoCompletionsWithinShortWindowBothDelivered closes the
// gap the #2223 verification flagged: the existing tests cover
// codexTurnSignal()/TurnFingerprint()/CommitToInbox() directly, but nothing
// drove the full TransitionNotifier polling contract — two real *Instance
// status flips (running->waiting) for the same Codex child, spaced under
// shortWindowDedupSeconds (90s) apart — through NotifyTransition (the
// notifier's transition-detection entry point) and the daemon's actual
// isDuplicate/outputHashIsStale path together.
//
// Bug reproduced by the issue: turn B, completed <90s after turn A, used to
// be dropped because TurnFingerprint fell back to the legacy
// "flip|running>waiting" signal (identical for both turns) since Codex has
// no JSONL transcript for the transcript-size fallback. The fix routes
// Codex through codexTurnSignal (its persisted hook-watcher generation),
// which is a stable per-turn signal independent of wall-clock spacing.
func TestIssue2223_CodexTwoCompletionsWithinShortWindowBothDelivered(t *testing.T) {
	inboxTestHome(t)
	profile := "_test-2223-codex"
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	now := time.Now()
	parentID := "codex-parent-2223"
	child := &Instance{
		ID:              "codex-child-2223",
		Title:           "codex-worker",
		ProjectPath:     "/tmp/codex-child",
		GroupPath:       DefaultGroupPath,
		ParentSessionID: parentID,
		Tool:            "codex",
		Status:          StatusRunning,
		CreatedAt:       now,
	}
	parent := &Instance{
		ID:          parentID,
		Title:       "conductor-2223",
		ProjectPath: "/tmp/codex-parent",
		GroupPath:   DefaultGroupPath,
		Tool:        "claude",
		Status:      StatusIdle,
		CreatedAt:   now,
	}
	if err := storage.SaveWithGroups([]*Instance{child, parent}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}

	if err := os.MkdirAll(GetHooksDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCodexHook := func(generation string, sequence uint64) {
		t.Helper()
		fields := map[string]any{
			"status":                     "waiting",
			"ts":                         time.Now().Unix(),
			"codex_started_generation":   generation,
			"codex_completed_generation": generation,
			"codex_started_sequence":     sequence,
			"codex_completed_sequence":   sequence,
		}
		b, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(GetHooksDir(), child.ID+".json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	n := NewTransitionNotifier()
	n.wake = &wakeNudgeWiring{
		nudger: NewWakeNudger(0),
		now:    time.Now,
		isIdle: func(*Instance) bool { return true },
		send:   func(*Instance, string) error { return nil },
	}

	t0 := now.Add(-4 * time.Hour)

	// Turn A: Codex thread completes generation "thread:turn-a".
	writeCodexHook("thread:turn-a", 1)
	first := n.NotifyTransition(TransitionNotificationEvent{
		ChildSessionID: child.ID,
		ChildTitle:     child.Title,
		Profile:        profile,
		FromStatus:     "running",
		ToStatus:       "waiting",
		Timestamp:      t0,
		LastOutputHash: transitionEventOutputHash(child),
	})
	if first.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("turn A = %q, want committed", first.DeliveryResult)
	}

	// Turn B: a DISTINCT completion 30s later — well inside
	// shortWindowDedupSeconds (90s) — with its own generation.
	writeCodexHook("thread:turn-b", 2)
	second := n.NotifyTransition(TransitionNotificationEvent{
		ChildSessionID: child.ID,
		ChildTitle:     child.Title,
		Profile:        profile,
		FromStatus:     "running",
		ToStatus:       "waiting",
		Timestamp:      t0.Add(30 * time.Second),
		LastOutputHash: transitionEventOutputHash(child),
	})
	if second.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("turn B (30s after turn A) = %q, want committed -- this is the #2223 drop", second.DeliveryResult)
	}

	drained, err := DrainInboxForParent(parentID)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(drained) != 2 {
		t.Fatalf("expected 2 distinct completions delivered, got %d: %+v", len(drained), drained)
	}
	if drained[0].TurnFingerprint == "" || drained[1].TurnFingerprint == "" {
		t.Fatalf("both records must carry a turn_fingerprint: %+v", drained)
	}
	if drained[0].TurnFingerprint == drained[1].TurnFingerprint {
		t.Fatalf("turn A and turn B must not collapse to the same turn_fingerprint: %+v", drained)
	}
}
