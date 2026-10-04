package session

// Incremental remote talkback, review round 4: the journal render replays the
// producer's notifier. A repeat line the notifier dropped as a duplicate (the
// snapshot edge of a flip recordTerminalTurns already emitted) never crosses,
// so one turn arrives once and a background (info) turn never wakes a
// cross-host conductor; a stale repeat past the dedup TTL still crosses as its
// own turn, also at the journal's trim boundary. The fixture's child is under
// the cross-host conductor, so its producer commits to _unowned.

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// wakeCountingDeps drains this host into conductor-x and counts wakes.
func wakeCountingDeps(t *testing.T, wakes *int) RemoteTalkbackDeps {
	t.Helper()
	cond := NewInstanceWithTool("conductor-x", t.TempDir(), "claude")
	cond.ID = "conductor-x"
	var fetches []RemoteCursor
	deps := localTalkbackDeps(&fetches)
	deps.Parent = func() (*Instance, string) { return cond, "default" }
	deps.Wake = func(*Instance, string, TransitionNotificationEvent) { *wakes++ }
	return deps
}

// One daemon pass that sees running->waiting: recordTerminalTurns emits the
// turn, then the snapshot loop emits the same edge. Since issue #2481 the
// edge of a turn already journaled in this run is noise, so the journal holds
// one line (journals written before that hold a dropped repeat line, covered
// by the render tests below); the cursor export must ship the turn once.
func TestIssue2469PR3R4_SnapshotEdgeRepeatShipsOnce(t *testing.T) {
	f := newTurnTestFixture(t)
	parentOnOtherHost(t, f)
	f.appendTurn(t, fxHuman("u0", "merge lane A"), fxAssistantText("a0", "Lane A merged."))
	statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)

	local := unownedRecords(t)
	journal, err := ReadTurnJournal(f.child.ID, 0)
	if err != nil || len(journal) != 1 || len(local) != 1 {
		t.Fatalf("setup: want 1 local record and 1 journal line, got %d and %d (%v)", len(local), len(journal), err)
	}
	exp, err := ExportRecordsAfter(RemoteCursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(exp.Records) != 1 || exp.Records[0].Seq != 1 || exp.Records[0].OutputHashStale {
		t.Fatalf("the dropped repeat crossed: want only seq 1, got %+v", exp.Records)
	}
	if exp.CursorNext.Seqs[f.child.ID] != 1 {
		t.Fatalf("the cursor must move past the shipped line: %+v", exp.CursorNext)
	}
	wakes := 0
	res, err := RunRemoteTalkback(context.Background(), "boxd", "conductor-x", wakeCountingDeps(t, &wakes))
	if err != nil {
		t.Fatal(err)
	}
	if res.Written != 1 || wakes != 0 {
		t.Fatalf("one info turn (a plain reply): want written=1 wakes=0, got written=%d wakes=%d", res.Written, wakes)
	}
}

// The turn is drained and consumed first; the snapshot edge of the same flip
// lands afterwards. The next drain must not bring the consumed turn back.
func TestIssue2469PR3R4_SnapshotEdgeRepeatOfConsumedTurnStaysHome(t *testing.T) {
	f := newTurnTestFixture(t)
	parentOnOtherHost(t, f)
	f.appendTurn(t, fxHuman("u0", "merge lane A"), fxAssistantText("a0", "Lane A merged."))
	statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)

	wakes := 0
	deps := wakeCountingDeps(t, &wakes)
	if res, err := RunRemoteTalkback(context.Background(), "boxd", "conductor-x", deps); err != nil || res.Written != 1 {
		t.Fatalf("drain 1: %+v %v", res, err)
	}
	if _, err := DrainInboxForParent("conductor-x"); err != nil {
		t.Fatal(err)
	}
	wakes = 0
	time.Sleep(10 * time.Millisecond)
	f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
	if got := unownedRecords(t); len(got) != 1 {
		t.Fatalf("the local producer must drop the repeat: %d records", len(got))
	}
	res, err := RunRemoteTalkback(context.Background(), "boxd", "conductor-x", deps)
	if err != nil {
		t.Fatal(err)
	}
	if res.Written != 0 || wakes != 0 {
		t.Fatalf("a consumed turn came back: written=%d wakes=%d", res.Written, wakes)
	}
}

// A background turn is info and must never wake. Its snapshot-edge repeat is
// not journaled again (issue #2481: it used to be journaled as urgent); it
// must not cross and wake the cross-host conductor.
func TestIssue2469PR3R4_InfoTurnSnapshotRepeatDoesNotWake(t *testing.T) {
	f := newTurnTestFixture(t)
	parentOnOtherHost(t, f)
	f.appendTurn(t, fxHuman("u0", "run the board"), fxAssistantText("a0", "Starting lanes."))
	statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)

	wakes := 0
	deps := wakeCountingDeps(t, &wakes)
	if _, err := RunRemoteTalkback(context.Background(), "boxd", "conductor-x", deps); err != nil {
		t.Fatal(err)
	}
	if _, err := DrainInboxForParent("conductor-x"); err != nil {
		t.Fatal(err)
	}
	wakes = 0
	f.appendTurn(t, fxTaskNotification("u1"), fxAssistantText("a1", "Lane C merged; verifier running."))
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
	journal, _ := ReadTurnJournal(f.child.ID, 0)
	if len(journal) != 2 || journal[1].Tier != TurnTierInfo || journal[1].UUID != "a1" {
		t.Fatalf("setup: want the info line and no urgent repeat, got %+v", journal)
	}
	res, err := RunRemoteTalkback(context.Background(), "boxd", "conductor-x", deps)
	if err != nil {
		t.Fatal(err)
	}
	if res.Written != 1 || wakes != 0 || len(res.Stored) != 1 || res.Stored[0].Tier != TurnTierInfo {
		t.Fatalf("background turn: want one info record and no wake, got written=%d wakes=%d stored=%+v", res.Written, wakes, res.Stored)
	}
}

// A repeat past the dedup TTL was committed stale (#2184) and crosses as its
// own turn, while a repeat inside the TTL in the same journal stays home.
func TestIssue2469PR3R4_StaleRepeatPastTTLCrossesDroppedRepeatDoesNot(t *testing.T) {
	cursorTestHome(t)
	base := time.Now().Add(-5 * time.Hour)
	line := func(uuid string, at time.Time) TurnJournalEntry {
		l, err := AppendTurnJournal(TurnJournalEntry{TS: at, Child: "wr", Profile: "default", Status: "waiting",
			Tier: TurnTierUrgent, UUID: uuid, TextHash: "h-" + uuid, Text: "t-" + uuid}, 0)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	line("U1", base)
	line("U1", base.Add(time.Second))           // dropped duplicate
	line("U1", base.Add(3*time.Hour))           // stale: past the TTL of the last commit
	line("U1", base.Add(3*time.Hour+time.Hour)) // dropped: within the TTL of the stale commit
	exp, err := ExportRecordsAfter(RemoteCursor{})
	if err != nil {
		t.Fatal(err)
	}
	if got := exportSeqs(exp); len(got) != 2 || got[0] != 1 || got[1] != 3 || !exp.Records[1].OutputHashStale {
		t.Fatalf("want seq 1 and stale seq 3, got %v %+v", got, exp.Records)
	}
}

// R3-2: the first retained journal line is a stale repeat of a trimmed line.
// The render recovers the producer's last commit from _unowned, so both the
// trimmed turn and its stale repeat cross.
func TestIssue2469PR3R4_StaleRepeatAtTrimBoundaryCrosses(t *testing.T) {
	cursorTestHome(t)
	base := time.Now().Add(-5 * time.Hour)
	add := func(uuid string, at time.Time, stale bool) {
		l, err := AppendTurnJournal(TurnJournalEntry{TS: at, Child: "wb", Profile: "default", Status: "waiting",
			Tier: TurnTierUrgent, UUID: uuid, TextHash: "h-" + uuid, Text: "t-" + uuid}, 4)
		if err != nil {
			t.Fatal(err)
		}
		recordUnownedCopy(t, l, stale)
	}
	add("u1", base, false)
	first, err := ExportRecordsAfter(RemoteCursor{})
	if err != nil || len(first.Records) != 1 {
		t.Fatalf("drain 1: %v %v", first.Records, err)
	}
	add("u2", base.Add(time.Minute), false)
	add("u2", base.Add(3*time.Hour), true) // #2184 stale repeat of u2, committed past the TTL
	for i := 4; i <= 6; i++ {
		add(fmt.Sprintf("u%d", i), base.Add(3*time.Hour+time.Duration(i)*time.Minute), false)
	}
	second, err := ExportRecordsAfter(first.CursorNext)
	if err != nil {
		t.Fatal(err)
	}
	if got := exportSeqs(second); len(got) != 5 {
		t.Fatalf("want turns 2..6 (5), got %v", got)
	}
	third, err := ExportRecordsAfter(second.CursorNext)
	if err != nil || len(third.Records) != 0 {
		t.Fatalf("the boundary turns must cross once: %v %v", exportSeqs(third), err)
	}
}

// The first retained line is a repeat the producer dropped (no _unowned copy)
// of a trimmed info turn: the info turn crosses from _unowned as info and the
// forced-urgent repeat does not cross, so nothing wakes.
func TestIssue2469PR3R4_DroppedRepeatAtTrimBoundaryStaysHome(t *testing.T) {
	cursorTestHome(t)
	base := time.Now().Add(-time.Hour)
	add := func(uuid, tier string, at time.Time, committed bool) {
		l, err := AppendTurnJournal(TurnJournalEntry{TS: at, Child: "wd", Profile: "default", Status: "waiting",
			Tier: tier, UUID: uuid, TextHash: "h-" + uuid, Text: "t-" + uuid}, 3)
		if err != nil {
			t.Fatal(err)
		}
		if committed {
			recordUnownedCopy(t, l, false)
		}
	}
	add("u1", TurnTierUrgent, base, true)
	first, err := ExportRecordsAfter(RemoteCursor{})
	if err != nil || len(first.Records) != 1 {
		t.Fatalf("drain 1: %v %v", first.Records, err)
	}
	add("u2", TurnTierInfo, base.Add(time.Minute), true)
	add("u2", TurnTierUrgent, base.Add(time.Minute+time.Second), false) // the snapshot edge's repeat
	add("u3", TurnTierInfo, base.Add(2*time.Minute), true)
	add("u4", TurnTierInfo, base.Add(3*time.Minute), true) // trims seqs 1 and 2
	second, err := ExportRecordsAfter(first.CursorNext)
	if err != nil {
		t.Fatal(err)
	}
	got := exportSeqs(second)
	if len(got) != 3 || got[0] != 2 || got[1] != 4 || got[2] != 5 {
		t.Fatalf("want seqs 2, 4, 5 (the dropped repeat seq 3 stays home), got %v", got)
	}
	for _, ev := range second.Records {
		if ev.Tier != TurnTierInfo {
			t.Fatalf("an info turn crossed as %s: %+v", ev.Tier, ev)
		}
	}
}

// R3-2, no seed: the trimmed predecessor's _unowned copy is gone (swept), so
// the render cannot tell the first retained line is a stale repeat. The
// producer's stale record of it differs from the render and still crosses.
func TestIssue2469PR3R4_StaleUnownedCopyCrossesWhenRenderCannotTell(t *testing.T) {
	cursorTestHome(t)
	base := time.Now().Add(-5 * time.Hour)
	add := func(uuid string, at time.Time) TurnJournalEntry {
		l, err := AppendTurnJournal(TurnJournalEntry{TS: at, Child: "wn", Profile: "default", Status: "waiting",
			Tier: TurnTierUrgent, UUID: uuid, TextHash: "h-" + uuid, Text: "t-" + uuid}, 2)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	add("u1", base) // its _unowned copy was swept
	recordUnownedCopy(t, add("u1", base.Add(3*time.Hour)), true)
	recordUnownedCopy(t, add("u3", base.Add(3*time.Hour+time.Minute)), false)
	exp, err := ExportRecordsAfter(RemoteCursor{})
	if err != nil {
		t.Fatal(err)
	}
	stale := 0
	for _, ev := range exp.Records {
		if ev.Seq == 2 && ev.OutputHashStale {
			stale++
		}
	}
	if stale != 1 {
		t.Fatalf("the producer's stale record of seq 2 must cross once, got %+v", exp.Records)
	}
}
