package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/comms"
	"github.com/asheshgoplani/agent-deck/internal/events"
)

// The delivery pass skips a parent with nothing new since the cursor it judged
// itself. Any other consumer pass (a prompt edge's ack, a receipt, a prompt or
// Stop hook, `msg read`) also advances Through, over records it does not
// judge, so Through cannot be that cursor. Every test here runs the daemon's
// poll as it runs: the spool ingest, then delivery.

// poll is one daemon poll for the fixture's profile.
func (f *deliverFixture) poll(t *testing.T, parentStatus string) {
	t.Helper()
	f.d.ingestCommsSpool(f.profileName(), f.byID)
	f.deliver(t, parentStatus)
	f.d.notifier.Flush()
}

// settleAndCount runs idle polls for 6 min, starting the run each new doorbell
// asks for (with its receipt), and returns the doorbells.
func (w *piWakeFx) settleAndCount(t *testing.T, text string) int {
	t.Helper()
	seen := 0
	for i := 0; i < 12; i++ {
		w.poll(t, "waiting")
		for ; seen < w.ext.count(); seen++ {
			w.clock = w.clock.Add(time.Second)
			w.shownOnce(t, seen, w.piWokenRun(t, seen, true), text)
		}
		w.clock = w.clock.Add(30 * time.Second)
	}
	if typed := w.typed.all(); len(typed) != 0 {
		t.Errorf("typed into a socket-launched pi: %q", typed)
	}
	return w.ext.count()
}

// A pin, not a regression test (it passes with the fast path on Through too).
// The receipt of an injection made before the child's turn landed arrives
// while the parent is still busy (finalizeInjection, a consumer pass).
func TestCommsDeliverPin_PiReceiptWhileBusyLeavesTheUrgentTurnToTheDeliveryPass(t *testing.T) {
	w := newPiWakeFixture(t, replyWith("submitted"))
	w.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: "earlier result"})
	dl, ok := LedgerPromptContext(w.parent.ID, "carry on", "", w.parent.Tool == "pi") // the parent's own run starts
	if !ok {
		t.Fatal("no injection at the run start")
	}
	dl.Done(true)
	w.poll(t, "running")
	w.clock = w.clock.Add(time.Second)
	w.piChildUrgentTurn(t, "run-0000000e0001", "Should we ship on Friday or Monday?", false)
	w.injectedEdge(t, tokenOf(t, dl)) // the run's turn_start echoes the token
	for i := 0; i < 4; i++ {
		w.poll(t, "running")
		w.clock = w.clock.Add(10 * time.Second)
	}
	if n := w.settleAndCount(t, "Should we ship on Friday or Monday?"); n != 1 {
		t.Errorf("socket doorbells after the parent settled = %d, want exactly 1", n)
	}
}

// `agent-deck msg read` while the parent is busy: the CLI's pass (a peek that
// consumes nothing) covers the turn.
func TestCommsDeliver_PiMsgReadWhileBusyLeavesTheUrgentTurnToTheDeliveryPass(t *testing.T) {
	w := newPiWakeFixture(t, replyWith("submitted"))
	w.poll(t, "running")
	w.piChildUrgentTurn(t, "run-0000000e0002", "Should we ship on Friday or Monday?", false)
	// What msg read does without --ack: one pass that prints and acknowledges nothing.
	if _, err := w.l.Reader().Do(w.parent.ID, func(comms.Pass) ([]events.Cursor, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		w.poll(t, "running")
		w.clock = w.clock.Add(10 * time.Second)
	}
	if n := w.settleAndCount(t, "Should we ship on Friday or Monday?"); n != 1 {
		t.Errorf("socket doorbells after the parent settled = %d, want exactly 1", n)
	}
}

// A pin, not a regression test (it passes with the fast path on Through too).
// The parent's next run start injects the turn, and the extension drops it (no
// receipt). The prompt hook's pass covers the turn; once the attempt is stale
// the delivery pass rings for it.
func TestCommsDeliverPin_PiUnreceiptedInjectionLeavesTheUrgentTurnToTheDeliveryPass(t *testing.T) {
	w := newPiWakeFixture(t, replyWith("submitted"))
	w.poll(t, "running")
	w.piChildUrgentTurn(t, "run-0000000e0003", "Should we ship on Friday or Monday?", false)
	w.poll(t, "running")
	dl, ok := LedgerPromptContext(w.parent.ID, "next task", "", w.parent.Tool == "pi") // a human prompt starts a run
	if !ok || !strings.Contains(dl.Text, "Should we ship on Friday or Monday?") {
		t.Fatalf("the run start did not carry the turn (ok %v)", ok)
	}
	dl.Done(true) // printed; the extension dropped it
	for i := 0; i < 4; i++ {
		w.poll(t, "running")
		w.clock = w.clock.Add(10 * time.Second)
	}
	w.clock = w.clock.Add(2 * time.Minute) // past the attempt's stale bound
	if n := w.settleAndCount(t, "Should we ship on Friday or Monday?"); n != 1 {
		t.Errorf("socket doorbells after the dropped injection = %d, want exactly 1", n)
	}
}

// A pin, not a regression test (it passes without Judged, on main too): a
// Claude parent's Stop hook that may not block (stop_hook_active) still runs a
// pass over the ledger-only urgent record, and the delivery pass wakes for it
// once the parent is idle (the Stop pass leaves a due time).
func TestCommsDeliverPin_ClaudeStopWithoutABlockLeavesTheUrgentRecordToTheDeliveryPass(t *testing.T) {
	f := newDeliverFixture(t)
	f.poll(t, "running")
	f.commit(t, comms.Record{Kind: comms.KindDelivery, State: comms.StateFailed, To: []string{f.parent.ID},
		Tier: comms.TierUrgent, Text: "the send failed", Done: "ok", Summary: "the send failed"})
	if dl, _ := LedgerStopDecision(f.parent.ID, true); dl != nil {
		dl.Done(false)
	}
	for i := 0; i < 12; i++ {
		f.poll(t, "waiting")
		f.clock = f.clock.Add(30 * time.Second)
	}
	if len(f.sent) == 0 {
		t.Error("the ledger never woke the idle Claude parent for its urgent record")
	}
}

// A busy parent's own prompt edge is remembered by the ingest until its turn
// consumes it, and acknowledged again on every poll: a consumer pass while the
// parent is busy, over a ledger-only urgent record committed meanwhile. With
// no Stop pass after it (a harness without a Stop hook, or one that failed),
// the ledger wake is its only route, and it must not wait for the next record.
func TestCommsDeliver_ABusyParentsRememberedPromptEdgeLeavesTheUrgentRecordToTheDeliveryPass(t *testing.T) {
	f := newDeliverFixture(t)
	raw, _ := json.Marshal(map[string]any{"harness": "claude", "event": "UserPromptSubmit", "edge": CommsEdgePromptStart,
		"instance": f.parent.ID, "prompt": "run the long job", "t_signal": f.clock.UnixMilli()})
	var e CommsSpoolEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	spoolTurn(t, e) // the parent's own prompt: its turn is running
	f.poll(t, "running")
	f.clock = f.clock.Add(20 * time.Second)
	f.commit(t, comms.Record{Kind: comms.KindDelivery, State: comms.StateFailed, To: []string{f.parent.ID},
		Tier: comms.TierUrgent, Text: "the send failed", Done: "ok", Summary: "the send failed"})
	for i := 0; i < 4; i++ {
		f.poll(t, "running")
		f.clock = f.clock.Add(10 * time.Second)
	}
	for i := 0; i < 12; i++ { // settled, idle 6 min; the prompt edge is still remembered
		f.poll(t, "waiting")
		f.clock = f.clock.Add(30 * time.Second)
	}
	if len(f.sent) == 0 {
		t.Error("the ledger never woke the idle parent for its urgent record")
	}
}

// A ledger restored from an older copy starts a new epoch at a smaller cursor.
// A consumer state from before it has a Judged cursor far beyond anything the
// new epoch holds, and must still be scanned (and rebased) at the next pass.
func TestCommsDeliver_ANewLedgerEpochIsScannedWhateverTheJudgedCursor(t *testing.T) {
	f := newDeliverFixture(t)
	dir := f.l.Reader().Dir
	f.d.closeCommsLedgers() // the daemon stops; the ledger is restored under it
	rewrite := func(path string, set map[string]any) {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]any{}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		for k, v := range set {
			m[k] = v
		}
		out, _ := json.Marshal(m)
		if err := os.WriteFile(path, out, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The consumer had read 1000 records; the copy restored holds fewer.
	rewrite(comms.ConsumerPath(dir, f.parent.ID), map[string]any{"watermark": 1000, "through": 1000, "judged": 1000})
	rewrite(filepath.Join(dir, "store.json"), map[string]any{"hwm": 1000})
	if f.l = f.d.commsLedgerFor("default"); f.l == nil {
		t.Fatal("ledger did not reopen")
	}
	if st := f.l.Reader().Store; st.Epoch < 2 {
		t.Fatalf("test setup: the restored ledger kept epoch %d", st.Epoch)
	}
	f.commit(t, comms.Record{Kind: comms.KindDelivery, State: comms.StateFailed, To: []string{f.parent.ID},
		Tier: comms.TierUrgent, Text: "the send failed", Done: "ok", Summary: "the send failed"})
	if fl, found := comms.ReadFlag(dir, f.parent.ID); !found || fl.Epoch == f.state(t).Epoch {
		t.Fatalf("test setup: want a flag from the new epoch, got %+v (found %v)", fl, found)
	}
	for i := 0; i < 3; i++ {
		f.poll(t, "waiting")
		f.clock = f.clock.Add(30 * time.Second)
	}
	if len(f.sent) == 0 {
		t.Error("the delivery pass skipped the new epoch's urgent record behind the old epoch's judged cursor")
	}
}

// A log restored from a copy taken after its high-water mark keeps its epoch
// and is shorter than the consumer has read. The next consumer pass resumes
// it inside the log; the judged cursor must resume with it, or the delivery
// pass skips the records that reuse the cursors past that point (main heals
// here, since its fast path compares Through).
func TestCommsDeliver_ARestoredShorterLogLowersTheJudgedCursorWithThrough(t *testing.T) {
	f := newDeliverFixture(t)
	dir := f.l.Reader().Dir
	raw, err := os.ReadFile(comms.ConsumerPath(dir, f.parent.ID))
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["watermark"], m["through"], m["judged"] = 500, 500, 500 // read 500; the log holds fewer
	out, _ := json.Marshal(m)
	if err := os.WriteFile(comms.ConsumerPath(dir, f.parent.ID), out, 0o600); err != nil {
		t.Fatal(err)
	}
	// Any other consumer pass (here a bare one, as `msg read` runs) resumes it.
	if _, err := f.l.Reader().Do(f.parent.ID, func(comms.Pass) ([]events.Cursor, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	f.commit(t, comms.Record{Kind: comms.KindDelivery, State: comms.StateFailed, To: []string{f.parent.ID},
		Tier: comms.TierUrgent, Text: "the send failed", Done: "ok", Summary: "the send failed"})
	for i := 0; i < 12; i++ {
		f.poll(t, "waiting")
		f.clock = f.clock.Add(30 * time.Second)
	}
	if len(f.sent) == 0 {
		t.Errorf("the delivery pass skipped the restored log's new urgent record behind the old judged cursor (judged %d)", f.state(t).Judged)
	}
}

// A listed parent nothing was ever addressed to has no pending flag. It holds
// the fast path as on main: no flag is not a flag from another epoch.
func TestCommsDeliver_AConsumerNeverAddressedHoldsTheFastPath(t *testing.T) {
	f := newDeliverFixture(t)
	if _, found := comms.ReadFlag(f.l.Reader().Dir, f.parent.ID); found {
		t.Fatal("test setup: the parent already has a pending flag")
	}
	scans := 0
	prev := commsScanHook
	commsScanHook = func(id string) {
		if id == f.parent.ID {
			scans++
		}
	}
	t.Cleanup(func() { commsScanHook = prev })
	for i := 0; i < 10; i++ {
		f.clock = f.clock.Add(time.Second)
		f.poll(t, "waiting")
	}
	if scans > 1 {
		t.Errorf("delivery passes that scanned a parent with no pending flag = %d over 10 polls, want at most 1", scans)
	}
}

// A pass that only learns the cursor writes once and then holds the fast path:
// no write per poll, and a Claude parent scans at most once after a hook pass.
func TestCommsDeliver_TheJudgedCursorIsWrittenOnceThenHoldsTheFastPath(t *testing.T) {
	f := newDeliverFixture(t)
	f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: "child result"})
	dl, ok := LedgerPromptContext(f.parent.ID, "carry on", "", f.parent.Tool == "pi") // a hook-side pass
	if !ok {
		t.Fatal("no injection")
	}
	dl.Done(true)
	scans := 0
	prev := commsScanHook
	commsScanHook = func(id string) {
		if id == f.parent.ID {
			scans++
		}
	}
	t.Cleanup(func() { commsScanHook = prev })
	var updated []int64
	for i := 0; i < 5; i++ {
		f.clock = f.clock.Add(time.Second)
		f.poll(t, "waiting")
		updated = append(updated, f.state(t).Updated)
	}
	fl, _ := comms.ReadFlag(f.l.Reader().Dir, f.parent.ID)
	if st := f.state(t); st.Judged != fl.Last {
		t.Errorf("the delivery pass did not persist its judged cursor: judged %d, flag %d", st.Judged, fl.Last)
	}
	if scans > 1 {
		t.Errorf("delivery passes that scanned after one hook pass = %d, want at most 1", scans)
	}
	for i := 1; i < len(updated); i++ {
		if updated[i] != updated[0] {
			t.Errorf("the consumer file was rewritten on poll %d (updated %v): a write per poll", i+1, updated)
			break
		}
	}
	if len(f.sent) != 0 {
		t.Errorf("an info record woke the parent: %q", f.sent)
	}
}
