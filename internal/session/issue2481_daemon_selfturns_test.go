package session

import (
	"os"
	"strings"
	"testing"
	"time"
)

// Issue #2481 item 3: the transition daemon spent ~25% of a core (and 8 to
// 12 CPU hours a day on a remote host) on frames it was always going to drop.
// 42% of all transition frames were a top-level conductor's OWN turns: each
// was classified from the transcript, journaled, counted in the _unowned
// stats and resolved against a fresh registry load, only to be dropped as
// self_conductor. And a flapping child with no turn signal re-committed the
// same turn_fingerprint every ~1.7 min (one fingerprint 136 times), although
// the parent's drain drops every one of those copies as already consumed.

// selfConductorFixture turns the 2469 fixture's child into a top-level
// conductor (no parent, conductor-* title).
func selfConductorFixture(t *testing.T, tool string) *turnTestFixture {
	t.Helper()
	f := newTurnTestFixture(t)
	f.child.Title = "conductor-ops"
	f.child.ParentSessionID = ""
	f.child.Tool = tool
	storage, err := NewStorageWithProfile("default")
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer storage.Close()
	if err := storage.SaveWithGroups([]*Instance{f.parent, f.child}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}
	return f
}

// classifiedPasses is how many times emitTurn classified a turn for this
// stats bucket: every pass bumps exactly one of these counters.
func classifiedPasses(t *testing.T, parentID string) int64 {
	t.Helper()
	s, err := ReadInboxStats(parentID)
	if err != nil {
		t.Fatalf("ReadInboxStats: %v", err)
	}
	return s.RecordsUrgent + s.RecordsInfo + s.RecordsLegacy + s.NoiseSuppressed + s.DedupSuppressed
}

func TestIssue2481_SelfConductorTurnSkippedBeforeClassification(t *testing.T) {
	for _, tc := range []struct{ name, tool string }{
		{"transcript", "claude"}, // classified path: transcript facts, tier, journal
		{"legacy", "shell"},      // no readable transcript: legacy signal path
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := selfConductorFixture(t, tc.tool)
			f.appendTurn(t, fxHuman("u0", "status?"), fxAssistantText("a0", "All lanes green."))

			// The conductor flips running->waiting over and over, as the
			// live one did ~3,100 times a day; every observation path fires.
			statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}
			for i := 0; i < 5; i++ {
				ev, ok := f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now().Add(time.Duration(i)*time.Hour), true)
				if !ok {
					t.Fatalf("pass %d: a self turn must be handled (not retried), got pending/failed", i)
				}
				if ev.DeliveryResult != transitionDeliveryDropped || ev.DeadLetterReason != deadLetterReasonSelfConductor {
					t.Fatalf("pass %d: result=%q reason=%q, want dropped/self_conductor", i, ev.DeliveryResult, ev.DeadLetterReason)
				}
				f.d.recordTerminalTurns("default", f.byID, statuses, nil)
			}

			if n := classifiedPasses(t, UnownedInboxID); n != 0 {
				t.Fatalf("a self turn was classified and counted %d times; it must be skipped before classification", n)
			}
			if _, err := os.Stat(TurnJournalPath(f.child.ID)); !os.IsNotExist(err) {
				lines, _ := ReadTurnJournal(f.child.ID, 0)
				t.Fatalf("a self turn must not be journaled (stat err %v, %d lines)", err, len(lines))
			}
			if InboxHasPending(f.child.ID) || InboxHasPending(UnownedInboxID) {
				t.Fatal("a self turn must not land in any inbox")
			}
		})
	}
}

// A self turn that answers a tagged send still takes the full path: the
// asker gets its reply (TestPR5_TopLevelConductorReplyReachesAskingChild
// covers the reply itself; this pins that the early skip does not eat it).
func TestIssue2481_SelfConductorAnsweringSendIsNotSkipped(t *testing.T) {
	f := selfConductorFixture(t, "claude")
	asker := NewInstanceWithTool("asker", f.child.ProjectPath, "claude")
	asker.ID = "asker-2481"
	asker.ParentSessionID = f.child.ID
	asker.Status = StatusIdle
	storage, err := NewStorageWithProfile("default")
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	if err := storage.SaveWithGroups([]*Instance{f.parent, f.child, asker}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}
	storage.Close()
	f.byID[asker.ID] = asker

	f.appendTurn(t, fxHuman("u0", SendEnvelope(asker.ID)+"\nwhich port?"), fxAssistantText("a0", "8443."))
	f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)

	replies, err := ReadInboxEvents(asker.ID)
	if err != nil {
		t.Fatalf("ReadInboxEvents: %v", err)
	}
	if len(replies) != 1 || replies[0].TargetKind != InboxTargetKindReply || replies[0].Text != "8443." {
		t.Fatalf("the asker must get the conductor's answer: %+v", replies)
	}
}

// A child with no turn signal (hash-less: the fingerprint is the constant
// flip|running>waiting) flaps running->waiting every ~100 s, past the 90 s
// short window. Once the parent has consumed that fingerprint, every further
// copy is dropped by the drain, so writing it is pure waste: no inbox record,
// no transition-log line, no wake.
func TestIssue2481_ConsumedFingerprintIsNotRewritten(t *testing.T) {
	n, parentID, base := newWakeNudgeFixture(t)
	sent := 0
	n.wake = &wakeNudgeWiring{
		nudger: NewWakeNudger(0),
		now:    time.Now,
		isIdle: func(*Instance, string) bool { return true },
		send:   func(*Instance, string, string) error { sent++; return nil },
	}
	logPath := t.TempDir() + "/transition-notifier.log"
	n.logPath = logPath

	start := time.Now().Add(-time.Hour)
	flap := func(i int) TransitionNotificationEvent {
		ev := base
		ev.DoneStatus, ev.DoneSummary = "", ""
		ev.FromStatus, ev.ToStatus = "running", "waiting"
		ev.Substate = "running"
		ev.Timestamp = start.Add(time.Duration(i) * 100 * time.Second)
		return n.NotifyTransition(ev)
	}

	if res := flap(0); res.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("first flap = %q, want committed", res.DeliveryResult)
	}
	delivered, err := DrainInboxForParent(parentID)
	if err != nil || len(delivered) != 1 {
		t.Fatalf("drain: delivered=%d err=%v", len(delivered), err)
	}
	fp := delivered[0].TurnFingerprint

	for i := 1; i <= 10; i++ {
		res := flap(i)
		if res.DeliveryResult != transitionDeliveryCommitted {
			t.Fatalf("flap %d = %q: the turn is already delivered, report it committed", i, res.DeliveryResult)
		}
	}
	if recs, _ := ReadInboxEvents(parentID); len(recs) != 0 {
		t.Fatalf("wrote %d duplicate records for consumed fingerprint %s: %+v", len(recs), fp, recs)
	}
	raw, _ := os.ReadFile(logPath)
	if got := strings.Count(string(raw), fp); got != 1 {
		t.Fatalf("transition log holds fingerprint %s %d times, want 1", fp, got)
	}
	if sent != 1 {
		t.Fatalf("wake nudges = %d, want 1", sent)
	}
	if delivered, _ := DrainInboxForParent(parentID); len(delivered) != 0 {
		t.Fatalf("consumed-turn semantics changed: drain delivered %+v", delivered)
	}
}
