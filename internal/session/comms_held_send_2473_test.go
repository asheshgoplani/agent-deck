package session

import (
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/comms"
)

// Issue #2473 with the Comms Ledger on (docs/comms.md): the Claude Stop hook
// spools the held send turn like any other turn, but the inbox does not record
// it; the reply goes with the task turn that settles the work. The ledger must
// keep both turns (none lost, none doubled) and answer the sender once, on the
// settling turn, exactly as the inbox does.

const heldLedgerResult = "comms-followon-round3 finished: 5/5 lanes merged."

// fxRawWorkflowNotification is fxWorkflowNotification as Claude Code writes
// it: raw angle brackets (json.Marshal escapes them as \u003c, which the
// background-work scan, like the real transcript, does not contain).
func fxRawWorkflowNotification(uuid, taskID string) string {
	return strings.NewReplacer(`\u003c`, "<", `\u003e`, ">").Replace(fxWorkflowNotification(uuid, taskID))
}

// spoolClaudeStop spools the Stop hook payload for the child's current turn.
func spoolClaudeStop(t *testing.T, f *pr5Fixture, text string) {
	t.Helper()
	spoolTurn(t, CommsSpoolEntry{Harness: "claude", Event: "Stop", Instance: f.child.ID, SessionID: f.child.ClaudeSessionID,
		Text: text, TranscriptPath: f.transcript, TSignal: time.Now().UnixMilli()})
}

func childLedgerTurns(t *testing.T, f *pr5Fixture) []comms.Record {
	t.Helper()
	bus, err := comms.OpenReader("default")
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer bus.Close()
	recs, _, err := comms.ReadAfter(bus, 0, 0)
	if err != nil {
		t.Fatalf("ReadAfter: %v", err)
	}
	var out []comms.Record
	for _, r := range recs {
		if r.Kind == comms.KindTurn && r.From == f.child.ID {
			out = append(out, r)
		}
	}
	return out
}

func newHeldLedgerFixture(t *testing.T) (*pr5Fixture, *Instance, map[string]string, map[string]string) {
	t.Helper()
	f := newPR5Fixture(t)
	t.Cleanup(SetCommsLedgerForTest(true))
	t.Cleanup(f.d.closeCommsLedgers)
	sib := pr5Sibling(f.child.ProjectPath)
	f.saveRegistry(t, sib)
	running := map[string]string{f.child.ID: "running", f.parent.ID: "waiting", sib.ID: "idle"}
	waiting := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting", sib.ID: "idle"}
	return f, sib, running, waiting
}

// heldLedgerLaunch is heldSendLaunch with the Stop hook's spool entry and the
// daemon's ledger ingest in production order (inbox path, then ingest, then
// hook candidates).
func heldLedgerLaunch(t *testing.T, f *pr5Fixture, fromID string, running map[string]string) {
	t.Helper()
	f.appendTurn(t, fxHuman("u0", SendEnvelope(fromID)+"\nrun the follow-on workflow and tell me the result"))
	f.appendTurn(t, fxWorkflowLaunch("wqphbmkuj", "comms-followon-round3")...)
	const launched = "Launched comms-followon-round3 in the background."
	f.appendTurn(t, fxAssistantText("a0", launched), fxTurnDuration(1))
	spoolClaudeStop(t, f, launched)
	stop := map[string]hookTransitionCandidate{f.child.ID: {ToStatus: "waiting", Timestamp: time.Now(), Event: "Stop"}}
	for i := 0; i < 5; i++ {
		f.d.recordTerminalTurns("default", f.byID, running, nil)
		f.d.ingestCommsSpool("default", f.byID)
		f.d.emitHookTransitionCandidates("default", f.byID, running, running, stop)
	}
}

func assertLedgerAnsweredOnSettlingTurn(t *testing.T, f *pr5Fixture, senderID string) {
	t.Helper()
	turns := childLedgerTurns(t, f)
	if len(turns) != 2 {
		t.Fatalf("ledger must keep the held send turn and the task turn, got %d: %+v", len(turns), turns)
	}
	held, task := turns[0], turns[1]
	if held.Trigger != TurnTriggerSend || held.ReplyTo != "" || len(held.To) != 1 || held.To[0] != f.parent.ID {
		t.Fatalf("held send turn must not reply (the inbox does not record it): %+v", held)
	}
	if task.Trigger != TurnTriggerTask || task.ReplyTo != senderID || task.Text != heldLedgerResult ||
		len(task.To) != 2 || task.To[0] != f.parent.ID || task.To[1] != senderID {
		t.Fatalf("settling task turn must reply to the held send's sender: %+v", task)
	}
	if got, _ := ReadCommsSpool(f.child.ID); len(got) != 0 {
		t.Fatalf("spool not drained: %+v", got)
	}
}

// The inbox records the settling turn first (same pass, before the ingest),
// which clears the inbox's held send record: the ledger answers from its own
// owed sender.
func TestCommsLedger2473_HeldSendRepliesOnceOnTheSettlingTurn(t *testing.T) {
	f, sib, running, waiting := newHeldLedgerFixture(t)
	heldLedgerLaunch(t, f, sib.ID, running)

	f.appendTurn(t, fxRawWorkflowNotification("u1", "wqphbmkuj"), fxAssistantText("a1", heldLedgerResult), fxTurnDuration(0))
	spoolClaudeStop(t, f, heldLedgerResult)
	stop := map[string]hookTransitionCandidate{f.child.ID: {ToStatus: "waiting", Timestamp: time.Now(), Event: "Stop"}}
	for i := 0; i < 5; i++ {
		f.d.recordTerminalTurns("default", f.byID, waiting, nil)
		f.d.ingestCommsSpool("default", f.byID)
		f.d.emitHookTransitionCandidates("default", f.byID, running, waiting, stop)
	}

	assertHeldSendAnswered(t, f, sib.ID, heldLedgerResult)
	assertLedgerAnsweredOnSettlingTurn(t, f, sib.ID)
}

// The ledger sees the settling turn's Stop before the inbox records it: the
// held send record is still there and the work no longer holds the turn.
func TestCommsLedger2473_LedgerIngestBeforeInboxStillRepliesOnce(t *testing.T) {
	f, sib, running, waiting := newHeldLedgerFixture(t)
	heldLedgerLaunch(t, f, sib.ID, running)

	f.appendTurn(t, fxRawWorkflowNotification("u1", "wqphbmkuj"), fxAssistantText("a1", heldLedgerResult), fxTurnDuration(0))
	spoolClaudeStop(t, f, heldLedgerResult)
	if backgroundWorkHoldsTurn(f.child) {
		t.Fatal("the workflow reported back; the work must no longer hold the turn")
	}
	f.d.ingestCommsSpool("default", f.byID)
	if loadHeldSend(f.child.ID) == nil {
		t.Fatal("the ledger must never clear the inbox's held send record")
	}
	stop := map[string]hookTransitionCandidate{f.child.ID: {ToStatus: "waiting", Timestamp: time.Now(), Event: "Stop"}}
	for i := 0; i < 5; i++ {
		f.d.recordTerminalTurns("default", f.byID, waiting, nil)
		f.d.ingestCommsSpool("default", f.byID)
		f.d.emitHookTransitionCandidates("default", f.byID, running, waiting, stop)
	}

	assertHeldSendAnswered(t, f, sib.ID, heldLedgerResult)
	assertLedgerAnsweredOnSettlingTurn(t, f, sib.ID)
}

// An intermediate task turn while the work still holds the session is not
// the settling turn: the ledger records it without the sender.
func TestCommsLedger2473_IntermediateTaskTurnCarriesNoSender(t *testing.T) {
	f, sib, running, _ := newHeldLedgerFixture(t)
	heldLedgerLaunch(t, f, sib.ID, running)

	// A second workflow is launched from a background turn; the first one's
	// notification turn ends while the second still runs.
	f.appendTurn(t, fxRawWorkflowNotification("u1", "wqphbmkuj"))
	f.appendTurn(t, fxWorkflowLaunch("second", "comms-followon-round4")...)
	const interim = "Round 3 done; launched round 4."
	f.appendTurn(t, fxAssistantText("a1", interim), fxTurnDuration(1))
	spoolClaudeStop(t, f, interim)
	f.d.recordTerminalTurns("default", f.byID, running, nil)
	f.d.ingestCommsSpool("default", f.byID)

	turns := childLedgerTurns(t, f)
	if len(turns) != 2 || turns[1].Trigger != TurnTriggerTask || turns[1].ReplyTo != "" {
		t.Fatalf("intermediate task turn must carry no sender: %+v", turns)
	}
	if held := loadHeldSend(f.child.ID); held == nil || held.FromID != sib.ID {
		t.Fatalf("the sender is still owed the result: %+v", held)
	}
	if owed := loadLedgerOwedSender(f.child.ID); owed != sib.ID {
		t.Fatalf("the ledger still owes the sender the result: %q", owed)
	}
}

func ledgerRepliesTo(t *testing.T, f *pr5Fixture, sender string) int {
	t.Helper()
	n := 0
	for _, r := range childLedgerTurns(t, f) {
		if r.ReplyTo == sender {
			n++
		}
	}
	return n
}

// A permission menu while the work runs: the inbox answers the sender on the
// held send turn and clears its held record. The ledger committed that turn
// while it was held, so it must still answer once, on the settling turn.
func TestCommsLedger2473_MenuInterludeLedgerRepliesOnce(t *testing.T) {
	f, sib, running, waiting := newHeldLedgerFixture(t)
	heldLedgerLaunch(t, f, sib.ID, running)
	for i := 0; i < 2; i++ {
		f.d.recordTerminalTurns("default", f.byID, waiting, nil)
		f.d.ingestCommsSpool("default", f.byID)
	}
	for i := 0; i < 3; i++ {
		f.d.recordTerminalTurns("default", f.byID, running, nil)
		f.d.ingestCommsSpool("default", f.byID)
	}
	f.appendTurn(t, fxRawWorkflowNotification("u1", "wqphbmkuj"), fxAssistantText("a1", heldLedgerResult), fxTurnDuration(0))
	spoolClaudeStop(t, f, heldLedgerResult)
	stop := map[string]hookTransitionCandidate{f.child.ID: {ToStatus: "waiting", Timestamp: time.Now(), Event: "Stop"}}
	for i := 0; i < 5; i++ {
		f.d.recordTerminalTurns("default", f.byID, waiting, nil)
		f.d.ingestCommsSpool("default", f.byID)
		f.d.emitHookTransitionCandidates("default", f.byID, running, waiting, stop)
	}
	if inbox := len(readSenderRecords(t, sib.ID)); inbox != 1 {
		t.Fatalf("inbox must answer the sender once, got %d", inbox)
	}
	assertLedgerAnsweredOnSettlingTurn(t, f, sib.ID)
	if owed := loadLedgerOwedSender(f.child.ID); owed != "" {
		t.Fatalf("the paid sender must be cleared, got %q", owed)
	}
}

// A child with transition notifications off: the inbox never remembers the
// held send, but the ledger records every turn and must answer the sender once.
func TestCommsLedger2473_NoTransitionNotifyLedgerRepliesOnce(t *testing.T) {
	f, sib, running, waiting := newHeldLedgerFixture(t)
	f.child.NoTransitionNotify = true
	heldLedgerLaunch(t, f, sib.ID, running)
	if loadHeldSend(f.child.ID) != nil {
		t.Fatal("precondition: the inbox remembers nothing for a child with notifications off")
	}
	f.appendTurn(t, fxRawWorkflowNotification("u1", "wqphbmkuj"), fxAssistantText("a1", heldLedgerResult), fxTurnDuration(0))
	spoolClaudeStop(t, f, heldLedgerResult)
	for i := 0; i < 3; i++ {
		f.d.recordTerminalTurns("default", f.byID, waiting, nil)
		f.d.ingestCommsSpool("default", f.byID)
	}
	assertLedgerAnsweredOnSettlingTurn(t, f, sib.ID)
}

// The ledger could not ingest during the whole hold and drains the backlog
// after the work settled: the send turn is committed unheld with its own
// reply and owes nothing, so the task turn must not answer a second time.
func TestCommsLedger2473_LateDrainRepliesOnce(t *testing.T) {
	f, sib, running, waiting := newHeldLedgerFixture(t)
	env := SendEnvelope(sib.ID) + "\nrun the follow-on workflow and tell me the result"
	spoolTurn(t, CommsSpoolEntry{Harness: "claude", Edge: CommsEdgePromptStart, Event: "UserPromptSubmit", Instance: f.child.ID,
		SessionID: f.child.ClaudeSessionID, Prompt: env, TSignal: time.Now().UnixMilli()})
	f.appendTurn(t, fxHuman("u0", env))
	f.appendTurn(t, fxWorkflowLaunch("wqphbmkuj", "comms-followon-round3")...)
	const launched = "Launched comms-followon-round3 in the background."
	f.appendTurn(t, fxAssistantText("a0", launched), fxTurnDuration(1))
	spoolClaudeStop(t, f, launched)
	stop := map[string]hookTransitionCandidate{f.child.ID: {ToStatus: "waiting", Timestamp: time.Now(), Event: "Stop"}}
	for i := 0; i < 3; i++ {
		f.d.recordTerminalTurns("default", f.byID, running, nil)
		f.d.emitHookTransitionCandidates("default", f.byID, running, running, stop)
	}
	f.appendTurn(t, fxRawWorkflowNotification("u1", "wqphbmkuj"), fxAssistantText("a1", heldLedgerResult), fxTurnDuration(0))
	spoolClaudeStop(t, f, heldLedgerResult)
	f.d.recordTerminalTurns("default", f.byID, waiting, nil)
	f.d.ingestCommsSpool("default", f.byID)

	turns := childLedgerTurns(t, f)
	if len(turns) != 2 {
		t.Fatalf("want both turns in the ledger, got %+v", turns)
	}
	if n := ledgerRepliesTo(t, f, sib.ID); n != 1 {
		t.Fatalf("late drain: ledger replies %d, want 1: %+v", n, turns)
	}
	if loadLedgerOwedSender(f.child.ID) != "" {
		t.Fatal("an unheld send turn owes nothing")
	}
}
