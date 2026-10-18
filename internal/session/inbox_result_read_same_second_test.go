package session

import (
	"testing"
	"time"
)

// Follow-up to review B1: a hook-driven record is stamped with the hook's
// whole second. If Stop fires after the interim text X is written but
// before the tool record, the classifier sees no tool record (so the record
// is not interim) and keys it on X. A peek at X in that same second is
// later than the stamp yet came before the turn ended: it must not
// withdraw. A read a full second after the stamp does.
func TestResultRead_SameSecondPeekAtWholeSecondStampDoesNotWithdraw(t *testing.T) {
	f := newResultReadFixture(t, 2)
	peeked, readLater := f.children[0], f.children[1]
	stamp := time.Unix(time.Now().Add(-time.Minute).Unix(), 0) // whole-second hook stamp
	writeResultReadAtForTest(t, f.parentID, peeked.ID, turnOf(peeked), stamp.Add(400*time.Millisecond))
	writeResultReadAtForTest(t, f.parentID, readLater.ID, turnOf(readLater), stamp.Add(time.Second))
	f.finishAt(t, peeked, "Let me run the tests", turnOf(peeked), stamp)
	f.finishAt(t, readLater, "All tests pass.", turnOf(readLater), stamp)

	if f.wakes != 1 {
		t.Fatalf("wakes = %d %q, want one, for %s", f.wakes, f.wakeFor, peeked.Title)
	}
	delivered, withdrawn, err := DrainInboxForParentReport(f.parentID)
	if err != nil {
		t.Fatal(err)
	}
	if got := childIDs(delivered); len(got) != 1 || got[0] != peeked.ID {
		t.Fatalf("delivered %v, want only %s (the same-second peek)", got, peeked.ID)
	}
	if got := childIDs(withdrawn); len(got) != 1 || got[0] != readLater.ID {
		t.Fatalf("withdrawn %v, want only %s", got, readLater.ID)
	}
}

// A sub-second stamp (the daemon's own observation time) is used as is.
func TestTurnEndedByMillis(t *testing.T) {
	whole := time.Unix(1_700_000_000, 0)
	if got := turnEndedByMillis(whole); got != whole.Add(time.Second).UnixMilli() {
		t.Fatalf("whole-second stamp: %d, want stamp+1s", got)
	}
	sub := time.Unix(1_700_000_000, 250_000_000)
	if got := turnEndedByMillis(sub); got != sub.UnixMilli() {
		t.Fatalf("sub-second stamp: %d, want the stamp", got)
	}
}
