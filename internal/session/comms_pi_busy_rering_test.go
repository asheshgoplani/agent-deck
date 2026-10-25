package session

import (
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/comms"
)

// piChildUrgentTurn commits a pi child's urgent run the way a live one lands:
// the ledger record under the run's key and its textless inbox twin under the
// run's signal (the same key, #pilk), then the commit-time inbox doorbell, whose
// idle gate reports idle as given.
func (w *piWakeFx) piChildUrgentTurn(t *testing.T, run, text string, idleAtCommit bool) {
	t.Helper()
	w.ringInboxDoorbell(w.commitChildUrgentTurn(t, run, text), idleAtCommit)
}

// commitChildUrgentTurn commits the ledger record and its inbox twin, and
// returns the event the commit-time inbox doorbell rings for.
func (w *piWakeFx) commitChildUrgentTurn(t *testing.T, run, text string) TransitionNotificationEvent {
	t.Helper()
	w.commitLedgerTurn(t, run, text)
	return w.commitInboxTwin(t, run, text)
}

// commitLedgerTurn is the spool ingest of the child's run, keyed by the run.
func (w *piWakeFx) commitLedgerTurn(t *testing.T, run, text string) {
	t.Helper()
	w.child.Tool = "pi"
	w.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierUrgent, Tool: "pi", Text: text,
		Key: piRunTurnKey(w.child.ID, piRunSignal(run))})
}

// commitInboxTwin is the daemon's inbox record of the same run, under the
// pi-run signal (#2827), with no transcript uuid and no text: the text is the
// ledger turn's.
func (w *piWakeFx) commitInboxTwin(t *testing.T, run, text string) TransitionNotificationEvent {
	t.Helper()
	w.child.Tool = "pi"
	ev := TransitionNotificationEvent{ChildSessionID: w.child.ID, ChildTitle: "board-zero", Profile: "default", FromStatus: "running",
		ToStatus: "waiting", TargetSessionID: w.parent.ID, TargetKind: "parent", Tier: TurnTierUrgent,
		LastOutputHash: piRunSignal(run), Timestamp: time.Now()}
	if err := CommitToInbox(w.parent.ID, ev); err != nil {
		t.Fatal(err)
	}
	return ev
}

// ringInboxDoorbell fires the commit-time inbox doorbell for ev, whose idle
// gate reports idle as given.
func (w *piWakeFx) ringInboxDoorbell(ev TransitionNotificationEvent, idle bool) {
	w.d.notifier.wake.send = sendWakeNudge // the production sender: a goroutine into the leg
	w.d.notifier.wake.isIdle = func(*Instance, string) bool { return idle }
	w.d.notifier.fireWakeNudge(w.parent, ev)
	w.d.notifier.Flush()
}

// Lab L4b (real pi 1.4.0 at 5e496b22): a listed pi parent is busy when a child
// commits an urgent turn, so the commit-time inbox doorbell is skipped (it rings
// only an idle parent) and the ledger pass never rings for a child's turn (that
// is the inbox's). A Claude parent gets the turn from its own Stop hook; pi has
// no blocking Stop, so the parent's settle to idle must ring one socket doorbell,
// or an unattended pi conductor hears of it only at its next heartbeat or prompt.
func TestCommsDeliver_PiBusyAtCommitRingsOnceWhenTheParentSettles(t *testing.T) {
	w := newPiWakeFixture(t, replyWith("submitted"))
	w.piChildUrgentTurn(t, "aaaaaaaa-0000-4000-8000-0000000b0001", "which way, A or B?", false)
	w.d.deliverCommsLedger(w.profileName(), w.byID, map[string]string{w.parent.ID: "running", w.child.ID: "waiting"})
	w.d.notifier.Flush()
	if n := w.ext.count(); n != 0 {
		t.Fatalf("test setup: %d frames while the parent was busy", n)
	}
	// The parent's run settles: it is idle now, the records still pending.
	w.clock = w.clock.Add(5 * time.Second)
	w.deliver(t, "waiting")
	w.d.notifier.Flush()
	// Later passes while it stays idle ring nothing more.
	w.clock = w.clock.Add(10 * time.Second)
	w.deliver(t, "waiting")
	w.d.notifier.Flush()
	if n := w.ext.count(); n != 1 {
		t.Errorf("socket doorbells after the parent settled idle = %d, want exactly 1", n)
	}
	if typed := w.typed.all(); len(typed) != 0 {
		t.Errorf("typed into a socket-launched pi: %q", typed)
	}
}

// Control: the same turn committed while the parent is idle rings once, at
// commit, and the ledger pass that follows adds no second doorbell.
func TestCommsDeliver_PiIdleAtCommitRingsOnce(t *testing.T) {
	w := newPiWakeFixture(t, replyWith("submitted"))
	w.piChildUrgentTurn(t, "aaaaaaaa-0000-4000-8000-0000000b0002", "which way, A or B?", true)
	w.deliver(t, "waiting")
	w.d.notifier.Flush()
	if n := w.ext.count(); n != 1 {
		t.Errorf("socket doorbells for a turn committed while idle = %d, want 1", n)
	}
}

// piWokenRun is the run frame i starts: pi's before_agent_start hook injects
// the ledger context, and the extension echoes the delivery token on the next
// turn_start when receipt is set.
func (w *piWakeFx) piWokenRun(t *testing.T, i int, receipt bool) *LedgerDelivery {
	t.Helper()
	fr := w.ext.frame(t, i)
	line, _ := fr["line"].(string)
	nonce, _ := fr["id"].(string)
	dl, ok := LedgerPromptContext(w.parent.ID, line, nonce)
	if !ok {
		t.Fatalf("the run frame %d started injected nothing", i)
	}
	dl.Done(true)
	if receipt {
		w.injectedEdge(t, tokenOf(t, dl))
	}
	return dl
}

// shownOnce checks that the run frame i started showed text exactly once, in
// the doorbell's line or in the run's context.
func (w *piWakeFx) shownOnce(t *testing.T, i int, dl *LedgerDelivery, text string) {
	t.Helper()
	line, _ := w.ext.frame(t, i)["line"].(string)
	if n := strings.Count(line, text) + strings.Count(dl.Text, text); n != 1 {
		t.Fatalf("the run frame %d started showed the turn %d times, want once: line %q, context %q", i, n, line, dl.Text)
	}
}

// The inbox doorbell rings a parent idle at commit, and the run it starts lasts
// past the ledger's debounce while passes keep running, some on a stale idle
// status: the turn the run received is never rung again.
func TestCommsDeliver_PiIdleAtCommitLongRunRingsOnce(t *testing.T) {
	w := newPiWakeFixture(t, replyWith("submitted"))
	w.piChildUrgentTurn(t, "aaaaaaaa-0000-4000-8000-0000000b0003", "which way, A or B?", true)
	if n := w.ext.count(); n != 1 {
		t.Fatalf("test setup: the commit-time doorbell rang %d frames, want 1", n)
	}
	w.clock = w.clock.Add(time.Second)
	if dl := w.piWokenRun(t, 0, true); !strings.Contains(dl.Text, "which way, A or B?") {
		t.Fatalf("the woken run did not carry the turn: %q", dl.Text)
	}
	for _, status := range []string{"running", "waiting", "running", "running", "waiting", "running"} {
		w.clock = w.clock.Add(30 * time.Second) // 3 min in all, three debounces
		w.deliver(t, status)
		w.d.notifier.Flush()
	}
	for i := 0; i < 4; i++ { // the run is over
		w.clock = w.clock.Add(time.Minute)
		w.deliver(t, "waiting")
		w.d.notifier.Flush()
	}
	if n := w.ext.count(); n != 1 {
		t.Errorf("socket doorbells for one turn = %d, want exactly 1", n)
	}
	if typed := w.typed.all(); len(typed) != 0 {
		t.Errorf("typed into a socket-launched pi: %q", typed)
	}
}

// A run whose receipt never arrives leaves the turn owed: the ledger rings it
// again only within the settle retry, so the doorbells after the commit-time
// one are exactly as many as the case allows, however long the parent then
// stays idle, and never more than commsMaxWakeTries.
func TestCommsDeliver_PiTurnWithoutAReceiptRingsWithinTheSettleRetry(t *testing.T) {
	for name, c := range map[string]struct {
		runs bool
		want int
	}{
		// The ledger's own ring starts a run that carries its nonce, and that
		// run acknowledges the record whatever became of the injection.
		"each ring starts a run that prints and is never received": {true, 2},
		// The ledger's ring settles unacknowledged, and the settle retries once.
		"no ring starts a run": {false, 1 + commsMaxWakeTries},
	} {
		runs, want := c.runs, c.want
		t.Run(name, func(t *testing.T) {
			w := newPiWakeFixture(t, replyWith("submitted"))
			w.piChildUrgentTurn(t, "aaaaaaaa-0000-4000-8000-0000000b0004", "which way, A or B?", true)
			seen := 0
			for step := 0; step < 60; step++ { // 30 min of idle passes
				for ; runs && seen < w.ext.count(); seen++ {
					w.clock = w.clock.Add(time.Second)
					w.piWokenRun(t, seen, false)
				}
				w.clock = w.clock.Add(30 * time.Second)
				w.deliver(t, "waiting")
				w.d.notifier.Flush()
			}
			if n := w.ext.count(); n != want {
				t.Errorf("socket doorbells for one unreceipted turn = %d, want %d", n, want)
			}
			if typed := w.typed.all(); len(typed) != 0 {
				t.Errorf("typed into a socket-launched pi: %q", typed)
			}
		})
	}
}

// The parent turns busy between the inbox doorbell's idle gate and its frame:
// the extension refuses it busy. Once the parent settles it is rung once.
func TestCommsDeliver_PiBusyRefusedInboxDoorbellRingsOnceWhenTheParentSettles(t *testing.T) {
	w := newPiWakeFixture(t, func(n int, f map[string]any, c net.Conn) {
		if n == 1 {
			refuse(c, f, "busy")
			return
		}
		answer(c, f, "submitted")
	})
	w.piChildUrgentTurn(t, "aaaaaaaa-0000-4000-8000-0000000b0005", "which way, A or B?", true)
	if n := w.ext.count(); n != 1 {
		t.Fatalf("test setup: the commit-time doorbell rang %d frames, want 1", n)
	}
	w.clock = w.clock.Add(5 * time.Second)
	w.deliver(t, "running")
	ran := false
	for i := 0; i < 8; i++ { // settled, then idle for 4 min
		w.clock = w.clock.Add(30 * time.Second)
		w.deliver(t, "waiting")
		w.d.notifier.Flush()
		if !ran && w.ext.count() == 2 { // the ring starts a run, which receives the turn
			w.clock = w.clock.Add(time.Second)
			w.shownOnce(t, 1, w.piWokenRun(t, 1, true), "which way, A or B?")
			ran = true
		}
	}
	if n := w.ext.count(); n != 2 {
		t.Errorf("socket doorbells = %d, want the refused one and exactly one more", n)
	}
	if typed := w.typed.all(); len(typed) != 0 {
		t.Errorf("typed into a socket-launched pi: %q", typed)
	}
}

// The cap and the budget gate hold for a child turn's wake as for any other: a
// spent budget rings nothing and publishes session.budget_exceeded.
func TestCommsDeliver_PiBusyAtCommitTurnHonoursTheSendBudget(t *testing.T) {
	w := newPiWakeFixtureIn(t, uniqueProfile("turnbudget"), replyWith("submitted"))
	quietBusTeardown(t)
	t.Setenv("AGENTDECK_EVENTS_BUS", "1") // TestMain turns the bus off for the package
	spendParent(t, w)
	w.piChildUrgentTurn(t, "aaaaaaaa-0000-4000-8000-0000000b0006", "which way, A or B?", false)
	w.clock = w.clock.Add(5 * time.Second)
	w.deliver(t, "waiting")
	w.d.notifier.Flush()
	if w.ext.count() != 0 || len(w.typed.all()) != 0 {
		t.Errorf("a spent budget still woke the parent: frames %d typed %q", w.ext.count(), w.typed.all())
	}
	if st := w.state(t); len(st.Inflight) != 0 || st.AutoWakes != 0 {
		t.Errorf("a spent budget carried the turn: %+v", st)
	}
	if via := waitBudgetVia(t, os.Getenv("HOME"), w.parent.ID); len(via) != 1 || via[0] != "socket" {
		t.Errorf("session.budget_exceeded frames for the parent: via %v, want one with via socket", via)
	}
}

// A parent at the automatic wake cap is not rung for a child's turn either.
func TestCommsDeliver_PiBusyAtCommitTurnHonoursTheWakeCap(t *testing.T) {
	w := newPiWakeFixture(t, replyWith("submitted"))
	w.piChildUrgentTurn(t, "aaaaaaaa-0000-4000-8000-0000000b0007", "which way, A or B?", false)
	w.setAutoWakes(t, comms.MaxAutoWakes)
	w.clock = w.clock.Add(5 * time.Second)
	w.deliver(t, "waiting")
	w.d.notifier.Flush()
	if w.ext.count() != 0 || len(w.typed.all()) != 0 {
		t.Errorf("a capped parent was woken: frames %d typed %q", w.ext.count(), w.typed.all())
	}
}

// Claude is unchanged: a Claude parent's urgent child turn is the inbox's to
// ring (and its Stop hook's to hand over), never the ledger pass's.
func TestCommsDeliver_ClaudeParentUrgentChildTurnIsNotLedgerWoken(t *testing.T) {
	f := newDeliverFixture(t)
	f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierUrgent, Text: "which way, A or B?",
		Key: comms.Key(comms.KindTurn, f.child.ID, "aaaaaaaa-0000-4000-8000-0000000b0008")})
	for i := 0; i < 4; i++ {
		f.clock = f.clock.Add(time.Minute)
		f.deliver(t, "waiting")
	}
	if len(f.sent) != 0 {
		t.Errorf("the ledger woke a Claude parent for a child's turn: %q", f.sent)
	}
	if st := f.state(t); len(st.Inflight) != 0 || st.LastWake != 0 {
		t.Errorf("the ledger carried a Claude parent's child turn: %+v", st)
	}
}

// The ledger pass rings first: it runs between the turn's commit and the
// commit-time inbox doorbell. The doorbell then reaches a pi already running
// the woken run, which refuses it busy or hands it over queued. The one run
// injects the turn once, and nothing rings after it.
func TestCommsDeliver_PiLedgerRingsBeforeTheInboxDoorbell(t *testing.T) {
	for _, second := range []string{"busy", "queued"} {
		t.Run(second, func(t *testing.T) {
			w := newPiWakeFixture(t, func(n int, f map[string]any, c net.Conn) {
				switch {
				case n == 1:
					answer(c, f, "submitted")
				case second == "busy":
					refuse(c, f, "busy")
				default:
					answer(c, f, "queued")
				}
			})
			ev := w.commitChildUrgentTurn(t, "aaaaaaaa-0000-4000-8000-0000000b0009", "which way, A or B?")
			w.deliver(t, "waiting") // the ledger pass of the poll, ahead of the doorbell
			w.d.notifier.Flush()
			if n := w.ext.count(); n != 1 {
				t.Fatalf("the ledger pass rang %d frames, want 1", n)
			}
			w.ringInboxDoorbell(ev, true) // its idle gate read the parent a moment before the run began
			if n := w.ext.count(); n != 2 {
				t.Fatalf("frames after the inbox doorbell = %d, want 2", n)
			}
			w.clock = w.clock.Add(time.Second)
			w.shownOnce(t, 0, w.piWokenRun(t, 0, true), "which way, A or B?")
			w.deliver(t, "running")
			for i := 0; i < 6; i++ { // the run is over; idle for 3 min
				w.clock = w.clock.Add(30 * time.Second)
				w.deliver(t, "waiting")
				w.d.notifier.Flush()
			}
			if n := w.ext.count(); n != 2 {
				t.Errorf("frames = %d, want the ledger's and the inbox doorbell's only", n)
			}
			next, _ := LedgerPromptContext(w.parent.ID, "carry on", "")
			if next != nil {
				if strings.Contains(next.Text, "which way, A or B?") {
					t.Errorf("the received turn was injected again: %q", next.Text)
				}
				next.Done(true)
			}
			turns := 0
			for _, r := range w.ledgerRecords(t) {
				if r.Kind == comms.KindTurn {
					turns++
				}
			}
			if turns != 1 {
				t.Errorf("turn records in the ledger = %d, want 1", turns)
			}
			if typed := w.typed.all(); len(typed) != 0 {
				t.Errorf("typed into a socket-launched pi: %q", typed)
			}
		})
	}
}

// A pi parent of a pi child (Forge V3, Max on pilk): the daemon emits the inbox
// record and rings before it ingests the spool, so the run the doorbell starts
// can read its context, and echo its receipt, before the ledger twin exists.
// The textless inbox record shares the twin's key, but it must not mark the
// twin shown: the twin rings once more after the debounce, its doorbell
// carrying the text; then nothing rings again.
func TestCommsDeliver_PiChildTwinIngestedAfterTheInboxRunRingsOnceMore(t *testing.T) {
	w := newPiWakeFixture(t, replyWith("submitted"))
	const run, text = "run-0000000c0001", "which way, A or B?"
	w.ringInboxDoorbell(w.commitInboxTwin(t, run, text), true)
	if n := w.ext.count(); n != 1 {
		t.Fatalf("test setup: the commit-time doorbell rang %d frames, want 1", n)
	}
	w.clock = w.clock.Add(time.Second)
	if dl := w.piWokenRun(t, 0, true); strings.Contains(dl.Text, text) {
		t.Fatalf("test setup: the inbox record carries the text: %q", dl.Text)
	}
	w.commitLedgerTurn(t, run, text) // the spool ingest, after the run read its context and its receipt
	w.deliver(t, "running")
	carried := 0
	for i := 0; i < 12; i++ { // the run is over; idle for 6 min
		w.clock = w.clock.Add(30 * time.Second)
		w.deliver(t, "waiting")
		w.d.notifier.Flush()
		for ; 1+carried < w.ext.count(); carried++ {
			w.clock = w.clock.Add(time.Second)
			// The twin rides the doorbell's own line whole (the custom message pi
			// shows), so the run it starts acks it by nonce and repeats nothing.
			if line, _ := w.ext.frame(t, 1+carried)["line"].(string); !strings.Contains(line, "«"+text+"»") {
				t.Errorf("the twin's doorbell does not carry its text: %q", line)
			}
			if dl := w.piWokenRun(t, 1+carried, true); strings.Contains(dl.Text, text) {
				t.Errorf("the twin's run repeated the text its doorbell carried: %q", dl.Text)
			}
		}
	}
	if n := w.ext.count(); n != 2 {
		t.Errorf("socket doorbells = %d, want the inbox's and exactly one for the twin", n)
	}
	if next, _ := LedgerPromptContext(w.parent.ID, "carry on", ""); next != nil {
		if strings.Contains(next.Text, text) {
			t.Errorf("the turn was injected a third time: %q", next.Text)
		}
		next.Done(true)
	}
	if typed := w.typed.all(); len(typed) != 0 {
		t.Errorf("typed into a socket-launched pi: %q", typed)
	}
}

// Lab L4b at f9d80bca (real pi 1.4.0): the daemon's poll, as it runs. The busy
// parent's own run start is a prompt edge the ingest remembers until the run's
// turn consumes it, and every poll's ingest acknowledges it again: a consumer
// pass while the parent is busy. That pass covers the child's urgent turn
// without judging it, so a delivery pass that skips a parent with nothing new
// since the last pass never looks at it again. Exactly one doorbell once the
// parent settles.
func TestCommsDeliver_PiBusyAtCommitRingsOnceThroughTheDaemonPoll(t *testing.T) {
	w := newPiWakeFixture(t, replyWith("submitted"))
	poll := func(status string) {
		t.Helper()
		w.d.ingestCommsSpool(w.profileName(), w.byID)
		w.d.deliverCommsLedger(w.profileName(), w.byID, map[string]string{w.parent.ID: status, w.child.ID: "waiting"})
		w.d.notifier.Flush()
	}
	w.promptEdge(t, "Run the bash command: sleep 60", "") // the parent's own run starts
	poll("running")
	w.clock = w.clock.Add(20 * time.Second)
	w.piChildUrgentTurn(t, "run-0000000d0001", "Should we ship on Friday or Monday?", false)
	for i := 0; i < 4; i++ { // the parent's run goes on
		poll("running")
		w.clock = w.clock.Add(10 * time.Second)
	}
	// The run ends: its own turn (to nobody) consumes the prompt edge.
	spoolTurn(t, CommsSpoolEntry{Harness: "pi", Event: "turn_end", Instance: w.parent.ID, SessionID: "pi-sess-parent",
		TurnID: "parent-turn-1", Text: "BUSY DONE", TSignal: w.clock.UnixMilli()})
	ran := false
	for i := 0; i < 12; i++ { // idle for 6 min
		poll("waiting")
		if !ran && w.ext.count() == 1 { // the ring starts a run, which receives the turn
			w.clock = w.clock.Add(time.Second)
			w.shownOnce(t, 0, w.piWokenRun(t, 0, true), "Should we ship on Friday or Monday?")
			ran = true
		}
		w.clock = w.clock.Add(30 * time.Second)
	}
	if n := w.ext.count(); n != 1 {
		t.Errorf("socket doorbells after the parent settled idle = %d, want exactly 1", n)
	}
	if typed := w.typed.all(); len(typed) != 0 {
		t.Errorf("typed into a socket-launched pi: %q", typed)
	}
}

// The same through the spool alone: the parent's run start is a pi
// before_agent_start prompt edge (remembered, re-ingested every poll) and the
// child's urgent run is its agent_settled edge with the run and the text, as
// the extension writes them. No record is committed by hand. Once the parent
// settles it is rung, within the wake's retry bound.
func TestCommsDeliver_PiBusyParentIsRungForASpooledChildRunWhenItSettles(t *testing.T) {
	w := newPiWakeFixture(t, replyWith("submitted"))
	w.child.Tool = "pi"
	w.promptEdge(t, "run the long job", "")
	w.poll(t, "running")
	w.clock = w.clock.Add(5 * time.Second)
	spoolTurn(t, CommsSpoolEntry{Harness: "pi", Event: "agent_settled", Edge: CommsEdgeTurnEnd, Instance: w.child.ID,
		SessionID: "child-sess", TurnID: "t-1", RunID: "run-0000000f0001",
		Text: "Built and tested. NEED: approve the deploy?", TSignal: w.clock.UnixMilli()})
	for i := 0; i < 6; i++ {
		w.poll(t, "running")
		w.clock = w.clock.Add(10 * time.Second)
	}
	urgent := 0
	for _, r := range w.ledgerRecords(t) {
		if r.Kind == comms.KindTurn && r.IsUrgent() {
			urgent++
		}
	}
	if urgent != 1 {
		t.Fatalf("the spooled child run committed %d urgent ledger turns, want 1", urgent)
	}
	if n := w.ext.count(); n != 0 {
		t.Fatalf("socket doorbells while the parent was busy = %d, want 0", n)
	}
	for i := 0; i < 12; i++ { // idle for 6 min
		w.poll(t, "waiting")
		w.clock = w.clock.Add(30 * time.Second)
	}
	if n := w.ext.count(); n < 1 || n > 1+commsMaxWakeTries {
		t.Errorf("socket doorbells after the parent settled = %d, want 1 to %d", n, 1+commsMaxWakeTries)
	}
	if typed := w.typed.all(); len(typed) != 0 {
		t.Errorf("typed into a socket-launched pi: %q", typed)
	}
}
