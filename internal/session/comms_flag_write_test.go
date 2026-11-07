package session

import (
	"os"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/comms"
)

// A pending flag write is best effort (comms.Ledger raiseFlags). When the
// write for a new record fails, the flag on disk still holds an older
// cursor that the parent's last pass already covered. The daemon commits
// every record itself, so its delivery pass must not let that stale file
// hide the record: an idle parent is woken for its urgent record at the
// next poll, not at the next flag write that happens to succeed (#3026
// item 2).
func TestCommsDeliver_AFailedFlagWriteDoesNotHideAnUrgentRecord(t *testing.T) {
	f := newDeliverFixture(t)
	dir := f.l.Reader().Dir
	f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: "fyi"})
	f.deliver(t, "waiting") // judges the info record: Through covers the flag
	before, ok := comms.ReadFlag(dir, f.parent.ID)
	if st := f.state(t); !ok || st.Through < before.Last || st.NextDue != 0 {
		t.Fatalf("test setup: flag %+v (found %v), state %+v", before, ok, st)
	}
	// The next flag write fails: its temp file cannot be created.
	if err := os.Mkdir(comms.FlagPath(dir, f.parent.ID)+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	f.commit(t, comms.Record{Kind: comms.KindDelivery, State: comms.StateFailed, To: []string{f.parent.ID},
		Tier: comms.TierUrgent, Text: "the send failed", Done: "ok", Summary: "the send failed"})
	if after, _ := comms.ReadFlag(dir, f.parent.ID); after.Last != before.Last {
		t.Fatalf("test setup: the flag write did not fail (flag %+v, was %+v)", after, before)
	}
	for i := 0; i < 3; i++ {
		f.deliver(t, "waiting")
		f.clock = f.clock.Add(10 * time.Second)
	}
	if len(f.sent) == 0 {
		t.Error("an idle parent was never woken for an urgent record whose pending flag write failed")
	}
}

// The same when the parent's first flag write fails: no flag file reads as
// "nothing was ever addressed to it", and the fast path skips. The daemon's
// own copy of the flag says otherwise.
func TestCommsDeliver_AFailedFirstFlagWriteDoesNotHideAnUrgentRecord(t *testing.T) {
	f := newDeliverFixture(t)
	dir := f.l.Reader().Dir
	if _, ok := comms.ReadFlag(dir, f.parent.ID); ok {
		t.Fatal("test setup: the parent already has a flag")
	}
	if _, found, _ := comms.ReadConsumer(dir, f.parent.ID); !found {
		t.Fatal("test setup: the enrolled parent has no consumer state")
	}
	if err := os.MkdirAll(comms.FlagPath(dir, f.parent.ID)+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	f.commit(t, comms.Record{Kind: comms.KindDelivery, State: comms.StateFailed, To: []string{f.parent.ID},
		Tier: comms.TierUrgent, Text: "the send failed", Done: "ok", Summary: "the send failed"})
	if _, ok := comms.ReadFlag(dir, f.parent.ID); ok {
		t.Fatal("test setup: the flag write did not fail")
	}
	for i := 0; i < 3; i++ {
		f.deliver(t, "waiting")
		f.clock = f.clock.Add(10 * time.Second)
	}
	if len(f.sent) == 0 {
		t.Error("an idle parent was never woken for an urgent record whose first pending flag write failed")
	}
}
