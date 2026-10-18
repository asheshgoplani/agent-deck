package session

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Review blocker B1. The child's turn is text X ("Let me run the tests"),
// a tool call, then the final text Y. Stop fires before Y reaches the
// transcript (#1186), so the classifier walks past the tool records and
// keys the completion record on X. A parent that peeked mid-turn with
// `session output` was also shown X. That read must not withdraw the
// completion record: the parent must be woken and the drain must deliver it.

func TestClassifyTranscriptTail_ToolAfterTextMarksFinalTextPending(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  bool
	}{
		{"text then tool call", []string{fxHuman("u0", "run the tests"), fxAssistantText("X", "Let me run the tests"), fxAssistantToolUse("t1")}, true},
		{"text, tool call, tool result", []string{fxHuman("u0", "run the tests"), fxAssistantText("X", "Let me run the tests"), fxAssistantToolUse("t1"), fxToolResult("r1")}, true},
		{"final text after the tool", []string{fxHuman("u0", "run the tests"), fxAssistantText("X", "Let me run the tests"), fxAssistantToolUse("t1"), fxToolResult("r1"), fxAssistantText("Y", "All 12 tests pass.")}, false},
		{"plain reply", []string{fxHuman("u0", "hi"), fxAssistantText("Y", "Hello.")}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := classifyTranscriptTail(tc.lines)
			if f.FinalTextPending != tc.want {
				t.Fatalf("FinalTextPending = %v, want %v (facts %+v)", f.FinalTextPending, tc.want, f)
			}
		})
	}
}

// The exact scenario end to end through the daemon: X -> tool -> (Y not
// flushed), a mid-turn peek that showed X, then Stop.
func TestResultRead_MidTurnPeekAtInterimTextStillWakesParent(t *testing.T) {
	f := newPR5Fixture(t)
	// Every recorded turn wakes, so the wake shows whether the record was
	// withdrawn rather than whether its tier wakes.
	inboxConfigOverride = &InboxConfig{WakeOn: []string{"urgent", "info"}}
	t.Cleanup(func() { inboxConfigOverride = nil })
	f.appendTurn(t, fxHuman("u0", "run the tests and report"), fxAssistantText("uuid-X", "Let me run the tests"))
	peek := time.Now()
	writeResultReadAtForTest(t, f.parent.ID, f.child.ID, "uuid-X", peek) // mid-turn `session output`
	f.appendTurn(t, fxAssistantToolUse("uuid-tool"), fxToolResult("uuid-result"))

	stop := peek.Add(2 * time.Second)
	f.d.emitTurn("default", f.child, f.byID, "running", "waiting", stop, true)

	recs := childRecords(t, f)
	if len(recs) != 1 {
		t.Fatalf("want one completion record, got %+v", recs)
	}
	if recs[0].TurnUUID != "uuid-X" || !recs[0].TurnTextInterim {
		t.Fatalf("record must name the interim text and be marked interim: %+v", recs[0])
	}
	if f.woken[f.parent.ID] == 0 {
		t.Fatal("the parent was not woken: a mid-turn peek withdrew the completion")
	}
	if !InboxHasUrgentPending(f.parent.ID) && !InboxHasPending(f.parent.ID) {
		t.Fatal("the completion is not pending for the parent")
	}
	delivered, err := DrainInboxForParent(f.parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 1 || delivered[0].ChildSessionID != f.child.ID {
		t.Fatalf("drain delivered %v, want the child's completion", childIDs(delivered))
	}
}

// Guard (a) alone: an interim record is never withdrawn, even by a read of
// that uuid made after the record's time.
func TestResultRead_InterimRecordIsNeverWithdrawn(t *testing.T) {
	f := newResultReadFixture(t, 1)
	child := f.children[0]
	ev := TransitionNotificationEvent{ChildSessionID: child.ID, ChildTitle: child.Title, Profile: "_test-result-read",
		DoneStatus: "ok", DoneSummary: "interim", Timestamp: f.finished, TurnUUID: "uuid-X", TurnTextInterim: true,
		Tier: TurnTierUrgent, LastOutputHash: "turn:uuid-X"}
	writeResultReadAtForTest(t, f.parentID, child.ID, "uuid-X", time.Now())
	if res := f.n.NotifyFinished(ev); res.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("commit = %q", res.DeliveryResult)
	}
	if f.wakes != 1 {
		t.Fatalf("wakes = %d, want 1", f.wakes)
	}
	if delivered, _ := DrainInboxForParent(f.parentID); len(delivered) != 1 {
		t.Fatalf("drain delivered %v, want the interim record", childIDs(delivered))
	}
}

// Guard (b) alone: a read of the record's own (final) uuid made before the
// turn ended does not withdraw it; the same read after the turn ended does.
func TestResultRead_OnlyAReadAfterTheTurnEndedWithdraws(t *testing.T) {
	f := newResultReadFixture(t, 2)
	early, late := f.children[0], f.children[1]
	writeResultReadAtForTest(t, f.parentID, early.ID, turnOf(early), f.finished.Add(-5*time.Second))
	writeResultReadAtForTest(t, f.parentID, late.ID, turnOf(late), f.finished.Add(5*time.Second))
	f.finish(t, early, "early read")
	f.finish(t, late, "late read")
	if f.wakes != 1 || !strings.Contains(strings.Join(f.wakeFor, "\n"), early.Title) {
		t.Fatalf("wakes = %d %q, want one, for %s", f.wakes, f.wakeFor, early.Title)
	}
	delivered, withdrawn, err := DrainInboxForParentReport(f.parentID)
	if err != nil {
		t.Fatal(err)
	}
	if got := childIDs(delivered); len(got) != 1 || got[0] != early.ID {
		t.Fatalf("delivered %v, want only %s", got, early.ID)
	}
	if got := childIDs(withdrawn); len(got) != 1 || got[0] != late.ID {
		t.Fatalf("withdrawn %v, want only %s", got, late.ID)
	}
}

// A read file from before read_at existed withdraws nothing.
func TestResultRead_LegacyReadFileWithdrawsNothing(t *testing.T) {
	f := newResultReadFixture(t, 1)
	child := f.children[0]
	path := resultReadsPath(f.parentID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"`+child.ID+`":{"turns":["`+turnOf(child)+`"],"at":`+strconv.FormatInt(time.Now().UnixMilli(), 10)+`}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f.finish(t, child, "done")
	if delivered, _ := DrainInboxForParent(f.parentID); len(delivered) != 1 {
		t.Fatalf("legacy read file withdrew %v", childIDs(delivered))
	}
}

// The conductor heartbeat counts only records still to be delivered: a
// record whose result the conductor already read is not pending.
func TestInboxSnapshot_ExcludesWithdrawnRecords(t *testing.T) {
	f := newResultReadFixture(t, 2)
	read, unread := f.children[0], f.children[1]
	f.finish(t, read, "alpha")
	f.finish(t, unread, "beta")
	n, digest, err := InboxSnapshot(f.parentID)
	if err != nil || n != 2 || digest == "" {
		t.Fatalf("snapshot = %d %q %v, want 2 pending", n, digest, err)
	}
	writeResultReadForTest(t, f.parentID, read.ID, turnOf(read))
	if n, _, err := InboxSnapshot(f.parentID); err != nil || n != 1 {
		t.Fatalf("snapshot after reading one = %d %v, want 1", n, err)
	}
	writeResultReadForTest(t, f.parentID, unread.ID, turnOf(unread))
	if n, digest, err := InboxSnapshot(f.parentID); err != nil || n != 0 || digest != "" {
		t.Fatalf("snapshot after reading both = %d %q %v, want 0", n, digest, err)
	}
}

// Removing a reader session removes its result-read file.
func TestSweepInboxes_RemovesReaderResultReads(t *testing.T) {
	inboxTestHome(t)
	if err := NoteResultRead("gone-parent", "some-child", "uuid-1", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(resultReadsPath("gone-parent")); err != nil {
		t.Fatalf("read file not written: %v", err)
	}
	if _, err := SweepInboxesForChildSession("gone-parent"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(resultReadsPath("gone-parent")); !os.IsNotExist(err) {
		t.Fatalf("result-read file survived the removed session: %v", err)
	}
}
