package session

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/comms"
)

// A ledger-consuming parent with a pi child: since #2827 the inbox keys a pi
// run pi-run:<hash of run_id>. The prompt hook retires an inbox record the
// ledger showed only when inboxTurnKey maps the record to the ledger's key, so
// the ledger must key a pi run by the same run token. One way only: a record
// marks a turn shown only when it carried the turn's text, and a pi inbox
// record does not, so a run the inbox showed first is shown again by the
// ledger. A spool entry from before the upgrade (no run token) keeps the
// session and turn id key.

// withRun is a pi spool entry carrying the extension's run token.
func withRun(e CommsSpoolEntry, run string) CommsSpoolEntry {
	e.RunID = run
	return e
}

// piLedgerFixture is a ledger parent whose child is a settles-capable pi
// session that has finished run A (its inbox record committed by the daemon).
func piLedgerFixture(t *testing.T) *deliverFixture {
	t.Helper()
	f := newDeliverFixture(t)
	piChild(f.turnTestFixture)
	w := newPiWriter(t, f.child.ID, true)
	w.TurnStart("run-A")
	f.piPoll()
	w.TurnEnd("run-A").Settle("run-A", "entry-1")
	f.piPoll()
	if !InboxHasPending(f.parent.ID) {
		t.Fatal("precondition: the run's inbox record")
	}
	return f
}

func (f *commsFixture) turnRecords(t *testing.T) []comms.Record {
	t.Helper()
	var out []comms.Record
	for _, r := range f.ledgerRecords(t) {
		if r.Kind == comms.KindTurn {
			out = append(out, r)
		}
	}
	return out
}

func TestLedgerConsumer_APiChildsRunIsShownOnce(t *testing.T) {
	f := piLedgerFixture(t)
	spoolTurn(t, withRun(piTurn(f.child, "entry-1", "Built and tested.", ""), "run-A"))
	f.d.ingestCommsSpool("default", f.byID)

	dl, ok := LedgerPromptContext(f.parent.ID, "hi")
	if !ok {
		t.Fatal("enrolled parent fell back to the inbox path")
	}
	if strings.Count(dl.Text, "Built and tested.") != 1 {
		t.Fatalf("the run's text once: %q", dl.Text)
	}
	if n := strings.Count(dl.Text, f.child.Title); n != 1 {
		t.Fatalf("the run reached the parent %d times (ledger turn plus inbox record): %q", n, dl.Text)
	}
	dl.Done(true)
	if InboxHasPending(f.parent.ID) {
		t.Fatal("the inbox twin is retired once the ledger showed the run")
	}
}

// The other direction: the inbox shows the run before the ledger holds it (the
// daemon emits inbox turns before it ingests the spool). A pi inbox record
// carries no text, so it must not stand for the ledger turn that does: the
// run is shown twice, as before, and its text is never lost.
func TestLedgerConsumer_APiRunTheInboxShowedFirstKeepsItsText(t *testing.T) {
	for _, path := range []string{"prompt", "stop"} {
		t.Run(path, func(t *testing.T) {
			f := piLedgerFixture(t)
			var dl *LedgerDelivery
			if path == "prompt" {
				dl, _ = LedgerPromptContext(f.parent.ID, "hi")
			} else {
				dl, _ = LedgerStopDecision(f.parent.ID, false)
			}
			if !strings.Contains(dl.Text, f.child.Title) {
				t.Fatalf("the inbox shows the run the ledger does not hold yet: %q", dl.Text)
			}
			dl.Done(true)
			spoolTurn(t, withRun(piTurn(f.child, "entry-1", "Built and tested.", ""), "run-A"))
			f.d.ingestCommsSpool("default", f.byID)
			dl, _ = LedgerPromptContext(f.parent.ID, "again")
			if !strings.Contains(dl.Text, "Built and tested.") {
				t.Fatalf("the run's text never reached the parent: %q", dl.Text)
			}
			dl.Done(true)
		})
	}
}

// The ledger key: by the run token when the spool entry carries one, by the
// session and turn id when it does not (an entry spooled before the upgrade,
// or a v3 extension). One entry is one key, so a run never lands under both;
// a replayed entry is a duplicate.
func TestCommsIngest_APiTurnIsKeyedByItsRunWhenTheSpoolCarriesIt(t *testing.T) {
	f, pi := newPiCommsFixture(t)
	spoolTurn(t, withRun(piTurn(pi, "entry-1", "Run one.", ""), "run-1"))
	spoolTurn(t, piTurn(pi, "entry-2", "Old entry.", ""))
	f.d.ingestCommsSpool("default", f.byID)
	spoolTurn(t, withRun(piTurn(pi, "entry-1", "Run one.", ""), "run-1")) // replayed
	f.d.ingestCommsSpool("default", f.byID)
	keys := map[string]string{}
	for _, r := range f.ledgerRecords(t) {
		if r.Kind == comms.KindTurn {
			keys[r.Text] = r.Key
		}
	}
	if len(keys) != 2 {
		t.Fatalf("two runs, two records: %+v", keys)
	}
	if keys["Run one."] != comms.Key(comms.KindTurn, pi.ID, "pi", piRunSignal("run-1")) {
		t.Fatalf("a run-carrying entry is keyed by its run: %s", keys["Run one."])
	}
	if keys["Old entry."] != comms.Key(comms.KindTurn, pi.ID, "pi", "pi-sess-1:entry-2") {
		t.Fatalf("an entry without a run keeps the session and turn id key: %s", keys["Old entry."])
	}
}

// The upgrade window: the run's spool entry was written by the handler before
// the upgrade (no run token), its inbox record by the upgraded daemon (pi-run
// key). Nothing links the two keys, so that run is shown twice, as before this
// change; it is one ledger record, under the old key. Only runs spooled before
// the upgrade and delivered after it are affected.
func TestLedgerConsumer_APiRunSpooledBeforeTheUpgradeIsShownAsBefore(t *testing.T) {
	f := piLedgerFixture(t)
	spoolTurn(t, piTurn(f.child, "entry-1", "Built and tested.", ""))
	f.d.ingestCommsSpool("default", f.byID)
	turns := f.turnRecords(t)
	if len(turns) != 1 || turns[0].Key != comms.Key(comms.KindTurn, f.child.ID, "pi", "pi-sess-1:entry-1") {
		t.Fatalf("one record under the old key: %+v", turns)
	}
	dl, _ := LedgerPromptContext(f.parent.ID, "hi")
	if strings.Count(dl.Text, "Built and tested.") != 1 || strings.Count(dl.Text, f.child.Title) != 2 {
		t.Fatalf("the ledger turn and the inbox record, as before: %q", dl.Text)
	}
	dl.Done(true)
}

// An errored pi run: the inbox delivers its error record (inboxTurnKey never
// maps an error), and the ledger holds no turn for it (an errored or aborted
// run settles without text: a status edge). The parent hears it once.
func TestLedgerConsumer_AnErroredPiRunIsTheInboxErrorOnce(t *testing.T) {
	f := newDeliverFixture(t)
	piChild(f.turnTestFixture)
	w := newPiWriter(t, f.child.ID, true)
	w.TurnStart("run-A")
	f.piPoll()
	w.TurnEnd("run-A")
	f.d.recordTerminalTurns("default", f.byID, map[string]string{f.child.ID: "error", f.parent.ID: "waiting"}, nil)
	spoolTurn(t, withRun(CommsSpoolEntry{Harness: "pi", Event: "agent_settled", Edge: CommsEdgeStatus, Instance: f.child.ID,
		SessionID: "pi-sess-1", From: "running", State: "waiting"}, "run-A"))
	f.d.ingestCommsSpool("default", f.byID)
	if turns := f.turnRecords(t); len(turns) != 0 {
		t.Fatalf("an errored run is no ledger turn: %+v", turns)
	}
	dl, _ := LedgerPromptContext(f.parent.ID, "hi")
	if n := strings.Count(dl.Text, f.child.Title); n != 1 || !strings.Contains(dl.Text, "error") {
		t.Fatalf("the error once, from the inbox: %q", dl.Text)
	}
	dl.Done(true)
}

// A pi-run signal names its run for good (TurnFingerprint keeps it past the
// 2 h hash TTL), so an inbox record the notifier flagged stale is still that
// run: the parent idle past the TTL, the daemon observing the run again.
func TestLedgerConsumer_APiRunFlaggedStaleIsShownOnce(t *testing.T) {
	f := newDeliverFixture(t)
	piChild(f.turnTestFixture)
	ev := TransitionNotificationEvent{ChildSessionID: f.child.ID, ChildTitle: f.child.Title, Profile: "default",
		FromStatus: "running", ToStatus: "waiting", Tier: TurnTierUrgent,
		LastOutputHash: piRunSignal("run-A"), OutputHashStale: true, Timestamp: f.clock}
	if committed, _, _ := f.d.notifier.commitEventToInbox(ev); !committed {
		t.Fatal("test setup: the commit")
	}
	spoolTurn(t, withRun(piTurn(f.child, "entry-1", "Built and tested.", ""), "run-A"))
	f.d.ingestCommsSpool("default", f.byID)
	dl, _ := LedgerPromptContext(f.parent.ID, "hi")
	if n := strings.Count(dl.Text, f.child.Title); n != 1 || !strings.Contains(dl.Text, "Built and tested.") {
		t.Fatalf("a stale-flagged pi run reached the parent %d times: %q", n, dl.Text)
	}
	dl.Done(true)
}

// The inbox hashes the whole run_id (the status file keeps it as sent); the
// ledger must hash the same input, however long it is, not the spool's capped
// copy.
func TestCommsIngest_APiRunIdOverTheSpoolCapKeepsTheInboxKey(t *testing.T) {
	f, pi := newPiCommsFixture(t)
	long := strings.Repeat("r", commsSpoolIDBytes+44)
	spoolTurn(t, withRun(piTurn(pi, "entry-1", "Run one.", ""), long))
	f.d.ingestCommsSpool("default", f.byID)
	turns := f.turnRecords(t)
	if len(turns) != 1 || turns[0].Key != piRunTurnKey(pi.ID, piRunSignal(long)) {
		t.Fatalf("a %d-byte run_id: %+v, want the key of the whole id", len(long), turns)
	}
}

// An error edge or a remote record that carries a run's signal is not that
// run's turn: the ledger showing the run must not retire it.
func TestLedgerConsumer_AnErrorOrRemoteRecordOfAShownPiRunIsStillShown(t *testing.T) {
	for _, kind := range []string{"error", "remote"} {
		t.Run(kind, func(t *testing.T) {
			f := newDeliverFixture(t)
			piChild(f.turnTestFixture)
			ev := TransitionNotificationEvent{ChildSessionID: f.child.ID, ChildTitle: f.child.Title, Profile: "default",
				FromStatus: "running", ToStatus: "waiting", Tier: TurnTierUrgent, LastOutputHash: piRunSignal("run-A"), Timestamp: f.clock}
			if kind == "error" {
				ev.ToStatus = "error"
			} else {
				ev.SourceRemote = "peer"
			}
			if err := WriteInboxEvent(f.parent.ID, ev); err != nil {
				t.Fatal(err)
			}
			spoolTurn(t, withRun(piTurn(f.child, "entry-1", "Built and tested.", ""), "run-A"))
			f.d.ingestCommsSpool("default", f.byID)
			dl, _ := LedgerPromptContext(f.parent.ID, "hi")
			if !strings.Contains(dl.Text, "Built and tested.") || !strings.Contains(dl.Text, "[agent-deck inbox]") {
				t.Fatalf("the %s record was retired by the ledger turn: %q", kind, dl.Text)
			}
			dl.Done(true)
		})
	}
}

// A run that never settled in time is published by the bounded fallback; its
// settle, late, then reaches the ledger. The inbox showed it first, so the
// text still arrives.
func TestLedgerConsumer_APiFallbackEmitThenALateSettleKeepsTheText(t *testing.T) {
	f := newDeliverFixture(t)
	piChild(f.turnTestFixture)
	w := newPiWriter(t, f.child.ID, true).Age(piOld)
	w.TurnStart("run-A").TurnEnd("run-A")
	f.piPoll()
	if !InboxHasPending(f.parent.ID) {
		t.Fatal("precondition: the fallback emit")
	}
	dl, _ := LedgerPromptContext(f.parent.ID, "hi")
	dl.Done(true)
	w.Settle("run-A", "entry-1")
	spoolTurn(t, withRun(piTurn(f.child, "entry-1", "Built and tested.", ""), "run-A"))
	f.piPoll()
	f.d.ingestCommsSpool("default", f.byID)
	dl, _ = LedgerPromptContext(f.parent.ID, "again")
	if !strings.Contains(dl.Text, "Built and tested.") {
		t.Fatalf("the late settle's text never reached the parent: %q", dl.Text)
	}
	dl.Done(true)
}

// A settled run the daemon never polled (the next run's turn_start overwrote
// its file) is emitted from pi_orphan_runs in the same pass that ingests its
// settle: the ledger turn shows it, and the orphan record is its twin.
func TestLedgerConsumer_AnOrphanedPiRunIsShownOnceWithItsText(t *testing.T) {
	f := newDeliverFixture(t)
	piChild(f.turnTestFixture)
	w := newPiWriter(t, f.child.ID, true).Age(piOld)
	w.TurnStart("run-A").TurnEnd("run-A").Settle("run-A", "entry-1")
	spoolTurn(t, withRun(piTurn(f.child, "entry-1", "Built and tested.", ""), "run-A"))
	w.TurnStart("run-B")
	f.piPoll()
	f.d.ingestCommsSpool("default", f.byID)
	if !InboxHasPending(f.parent.ID) {
		t.Fatal("precondition: the orphan emit")
	}
	dl, _ := LedgerPromptContext(f.parent.ID, "hi")
	if n := strings.Count(dl.Text, f.child.Title); n != 1 || !strings.Contains(dl.Text, "Built and tested.") {
		t.Fatalf("the orphaned run %d times: %q", n, dl.Text)
	}
	dl.Done(true)
}
