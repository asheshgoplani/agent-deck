package session

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/comms"
)

// Pi turns reach the ledger like Codex turns: one spooled turn_end per run,
// identified by session and turn id, with the prompt the extension carried.
// A run that ended without text is one status record instead.

func newPiCommsFixture(t *testing.T) (*commsFixture, *Instance) {
	t.Helper()
	f := newCommsFixture(t)
	pi := NewInstanceWithTool("pi-worker", t.TempDir(), "pi")
	pi.ID = "pi-2470"
	pi.ParentSessionID = f.parent.ID
	pi.Status = StatusWaiting
	f.byID[pi.ID] = pi
	saveCommsFixtureInstances(t, pi)
	return f, pi
}

func piTurn(pi *Instance, turnID, text, prompt string) CommsSpoolEntry {
	return CommsSpoolEntry{Harness: "pi", Event: "turn_end", Instance: pi.ID, SessionID: "pi-sess-1", TurnID: turnID, Text: text, Prompt: prompt}
}

func TestCommsIngest_PiTurnIsKeyedBySessionAndTurnAndReplaysAsDuplicate(t *testing.T) {
	f, pi := newPiCommsFixture(t)
	e := piTurn(pi, "entry-1", "Built and tested.", "<task-notification>bg</task-notification>")
	spoolTurn(t, e)
	f.d.ingestCommsSpool("default", f.byID)
	recs := f.ledgerRecords(t)
	if len(recs) != 1 {
		t.Fatalf("records: %+v", recs)
	}
	r := recs[0]
	if r.Kind != comms.KindTurn || r.Tool != "pi" || r.Text != "Built and tested." {
		t.Fatalf("pi turn: %+v", r)
	}
	if r.Key != comms.Key(comms.KindTurn, pi.ID, "pi", "pi-sess-1:entry-1") {
		t.Fatalf("pi key must use the session and turn id: %s", r.Key)
	}

	// The same run delivered again (a replayed spool entry) adds nothing.
	spoolTurn(t, e)
	f.d.ingestCommsSpool("default", f.byID)
	if got := f.ledgerRecords(t); len(got) != 1 {
		t.Fatalf("replay produced %d records", len(got))
	}
	if got, _ := ReadCommsSpool(pi.ID); len(got) != 0 {
		t.Fatalf("spool not drained: %+v", got)
	}

	// A second run is a second record.
	spoolTurn(t, piTurn(pi, "entry-2", "Second answer.", "<task-notification>bg</task-notification>"))
	f.d.ingestCommsSpool("default", f.byID)
	if got := f.ledgerRecords(t); len(got) != 2 {
		t.Fatalf("second run: %+v", got)
	}
}

func TestCommsIngest_PiTurnTiersFromThePromptAndSentinel(t *testing.T) {
	f, pi := newPiCommsFixture(t)
	const sender = "conductor-9"
	spoolTurn(t, piTurn(pi, "entry-1", "Built and tested.\n===AGENTDECK_DONE=== status=ok summary=matrix",
		"[agent-deck from:"+sender+"] build it"))
	f.d.ingestCommsSpool("default", f.byID)
	recs := f.ledgerRecords(t)
	if len(recs) != 1 {
		t.Fatalf("records: %+v", recs)
	}
	r := recs[0]
	if r.Tier != comms.TierUrgent || r.Trigger != TurnTriggerSend || r.Done != "ok" || r.ReplyTo != sender {
		t.Fatalf("sentinel reply to a send: %+v", r)
	}
	hasSender := false
	for _, to := range r.To {
		hasSender = hasSender || to == sender
	}
	if !hasSender {
		t.Fatalf("To must include the sender %q: %v", sender, r.To)
	}

	// The same text again is news when a send started the run and noise when a
	// background notification did; ingest must not dedup on the text.
	spoolTurn(t, piTurn(pi, "entry-2", "Built and tested.", "[agent-deck from:"+sender+"] build it again"))
	f.d.ingestCommsSpool("default", f.byID)
	recs = f.ledgerRecords(t)
	if len(recs) != 2 || recs[1].Tier != comms.TierInfo || recs[1].Trigger != TurnTriggerSend {
		t.Fatalf("repeated text after a send: %+v", recs)
	}

	spoolTurn(t, piTurn(pi, "entry-3", "Built and tested.", "<task-notification>bg</task-notification>"))
	f.d.ingestCommsSpool("default", f.byID)
	recs = f.ledgerRecords(t)
	if len(recs) != 3 || recs[2].Tier != comms.TierNoise || recs[2].Trigger != TurnTriggerTask {
		t.Fatalf("repeated text after a background notification: %+v", recs)
	}
}

func TestCommsIngest_PiToolOnlyRunIsOneStatusRecordAndNoTurn(t *testing.T) {
	f, pi := newPiCommsFixture(t)
	spoolTurn(t, CommsSpoolEntry{Harness: "pi", Event: "agent_settled", Edge: CommsEdgeStatus, Instance: pi.ID, From: "running", State: "waiting"})
	f.d.ingestCommsSpool("default", f.byID)
	recs := f.ledgerRecords(t)
	turns, statuses := 0, 0
	for _, r := range recs {
		switch r.Kind {
		case comms.KindTurn:
			turns++
		case comms.KindStatus:
			statuses++
		}
	}
	if turns != 0 || statuses != 1 {
		t.Fatalf("tool-only run: %d turn and %d status records: %+v", turns, statuses, recs)
	}
	if recs[0].From != pi.ID || recs[0].State != "waiting" || recs[0].Tool != "pi" {
		t.Fatalf("status record: %+v", recs[0])
	}
}

// A run that spooled its text must not also get a status record from the
// daemon's own transition path.
func TestCommsIngest_PiTextRunAddsNoStatusRecordFromTheLedgerPath(t *testing.T) {
	f, pi := newPiCommsFixture(t)
	spoolTurn(t, piTurn(pi, "entry-1", "Built and tested.", "<task-notification>bg</task-notification>"))
	f.d.commsStatusEdge(pi, "running", "waiting", time.Now())
	f.d.ingestCommsSpool("default", f.byID)
	recs := f.ledgerRecords(t)
	if len(recs) != 1 || recs[0].Kind != comms.KindTurn {
		t.Fatalf("text run records: %+v", recs)
	}
}
