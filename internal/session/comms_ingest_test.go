package session

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/comms"
)

// Comms Ledger ingest (docs/comms.md): the daemon drains the producers'
// spool and commits one record per turn, next to the inbox, with the same
// classification for Claude and a prompt-derived one for every other
// harness. These tests drive ingestCommsSpool with the same fixture the
// issue #2469 tests use, so the ledger and the inbox can be compared.

type commsFixture struct {
	*turnTestFixture
	codex *Instance
	shell *Instance
}

func newCommsFixture(t *testing.T) *commsFixture {
	t.Helper()
	f := newTurnTestFixture(t)
	t.Cleanup(SetCommsLedgerForTest(true))
	codex := NewInstanceWithTool("codex-worker", t.TempDir(), "codex")
	codex.ID = "codex-2470"
	codex.ParentSessionID = f.parent.ID
	codex.Status = StatusWaiting
	shell := NewInstanceWithTool("shell-worker", t.TempDir(), "shell")
	shell.ID = "shell-2470"
	shell.ParentSessionID = f.parent.ID
	shell.Status = StatusWaiting
	f.byID[codex.ID] = codex
	f.byID[shell.ID] = shell
	t.Cleanup(f.d.closeCommsLedgers)
	return &commsFixture{turnTestFixture: f, codex: codex, shell: shell}
}

func (f *commsFixture) ledgerRecords(t *testing.T) []comms.Record {
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
	return recs
}

func spoolTurn(t *testing.T, e CommsSpoolEntry) {
	t.Helper()
	if e.Edge == "" {
		e.Edge = CommsEdgeTurnEnd
	}
	if err := WriteCommsSpool(e); err != nil {
		t.Fatalf("WriteCommsSpool: %v", err)
	}
}

func TestCommsIngest_ClaudeTurnMatchesTheInboxClassification(t *testing.T) {
	f := newCommsFixture(t)
	f.appendTurn(t, fxHuman("u0", "run the board"), fxAssistantText("a0", "Starting 13 lanes."))
	spoolTurn(t, CommsSpoolEntry{Harness: "claude", Event: "Stop", Instance: f.child.ID, SessionID: f.child.ClaudeSessionID,
		Text: "Starting 13 lanes.", TranscriptPath: f.transcript, TSignal: time.Now().UnixMilli() - 20})

	f.d.ingestCommsSpool("default", f.byID)

	recs := f.ledgerRecords(t)
	if len(recs) != 1 {
		t.Fatalf("records: %+v", recs)
	}
	r := recs[0]
	if r.Kind != comms.KindTurn || r.From != f.child.ID || r.Tool != "claude" || r.Tier != comms.TierUrgent ||
		r.Trigger != TurnTriggerHuman || r.Text != "Starting 13 lanes." || r.Profile != "default" || r.Seq != 1 {
		t.Fatalf("claude record: %+v", r)
	}
	if len(r.To) != 1 || r.To[0] != f.parent.ID {
		t.Fatalf("to: %v", r.To)
	}
	if r.Key != comms.Key(comms.KindTurn, f.child.ID, "a0") {
		t.Fatalf("key must be the transcript uuid: %s", r.Key)
	}
	if r.LatencyMS < 20 || r.TRecord == 0 || r.TSignal == 0 || r.Bytes != len(r.Text) || r.TH == "" {
		t.Fatalf("measurement fields: %+v", r)
	}
	if got, _ := ReadCommsSpool(f.child.ID); len(got) != 0 {
		t.Fatalf("spool not drained: %+v", got)
	}

	// The same Stop observed again (hook re-fire) is one record, not two.
	spoolTurn(t, CommsSpoolEntry{Harness: "claude", Event: "Stop", Instance: f.child.ID, Text: "Starting 13 lanes.", TranscriptPath: f.transcript})
	f.d.ingestCommsSpool("default", f.byID)
	if got := f.ledgerRecords(t); len(got) != 1 {
		t.Fatalf("re-fire produced %d records", len(got))
	}

	// A background turn: info, carried with its text, in the same ledger.
	f.appendTurn(t, fxTaskNotification("u1"), fxAssistantText("a2", "Lane C merged; verifier running."))
	spoolTurn(t, CommsSpoolEntry{Harness: "claude", Event: "Stop", Instance: f.child.ID, Text: "Lane C merged; verifier running.", TranscriptPath: f.transcript})
	f.d.ingestCommsSpool("default", f.byID)
	got := f.ledgerRecords(t)
	if len(got) != 2 || got[1].Tier != comms.TierInfo || got[1].Trigger != TurnTriggerTask || got[1].Seq != 2 {
		t.Fatalf("background turn: %+v", got)
	}
}

func TestCommsIngest_CodexTurnTakesTriggerFromThePromptEdge(t *testing.T) {
	f := newCommsFixture(t)
	// A tagged send started the turn; the codex notify carries the reply.
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "agent-turn-complete", Instance: f.codex.ID, SessionID: "thread-1", TurnID: "turn-1",
		Text: "Done: tests green.", Prompt: "[agent-deck from:" + f.parent.ID + "] run the tests"})
	f.d.ingestCommsSpool("default", f.byID)
	recs := f.ledgerRecords(t)
	if len(recs) != 1 {
		t.Fatalf("records: %+v", recs)
	}
	r := recs[0]
	if r.Tool != "codex" || r.Trigger != TurnTriggerSend || r.ReplyTo != f.parent.ID || r.Tier != comms.TierUrgent || r.Text != "Done: tests green." {
		t.Fatalf("codex send reply: %+v", r)
	}
	if r.Key != comms.Key(comms.KindTurn, f.codex.ID, "codex", "turn-1") {
		t.Fatalf("codex key must use the turn id: %s", r.Key)
	}

	// A prompt-start edge followed by a turn end (Gemini-style pairing).
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "x", Edge: CommsEdgePromptStart, Instance: f.codex.ID, Prompt: "[HEARTBEAT] anything new?"})
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "agent-turn-complete", Instance: f.codex.ID, SessionID: "thread-1", TurnID: "turn-2", Text: "Nothing new."})
	f.d.ingestCommsSpool("default", f.byID)
	recs = f.ledgerRecords(t)
	if len(recs) != 2 || recs[1].Trigger != TurnTriggerInbox || recs[1].Tier != comms.TierInfo {
		t.Fatalf("heartbeat-triggered turn: %+v", recs)
	}

	// No prompt seen: unknown, tiers urgent (louder, not lossy).
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "agent-turn-complete", Instance: f.codex.ID, SessionID: "thread-1", TurnID: "turn-3", Text: "Something happened."})
	f.d.ingestCommsSpool("default", f.byID)
	recs = f.ledgerRecords(t)
	if len(recs) != 3 || recs[2].Trigger != TurnTriggerUnknown || recs[2].Tier != comms.TierUrgent {
		t.Fatalf("unknown-trigger turn: %+v", recs[len(recs)-1])
	}

	// Repeated background text is noise: stored, tiered noise, countable.
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "x", Edge: CommsEdgePromptStart, Instance: f.codex.ID, Prompt: "[HEARTBEAT] again"})
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "agent-turn-complete", Instance: f.codex.ID, SessionID: "thread-1", TurnID: "turn-4", Text: "Something happened."})
	f.d.ingestCommsSpool("default", f.byID)
	recs = f.ledgerRecords(t)
	if len(recs) != 4 || recs[3].Tier != comms.TierNoise {
		t.Fatalf("repeated background text: %+v", recs[len(recs)-1])
	}

	// A completion sentinel and a question are urgent whatever started them.
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "x", Edge: CommsEdgePromptStart, Instance: f.codex.ID, Prompt: "[HEARTBEAT] again"})
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "agent-turn-complete", Instance: f.codex.ID, SessionID: "thread-1", TurnID: "turn-5",
		Text: "All done.\n===AGENTDECK_DONE=== status=ok summary=shipped it"})
	f.d.ingestCommsSpool("default", f.byID)
	recs = f.ledgerRecords(t)
	last := recs[len(recs)-1]
	if len(recs) != 5 || last.Tier != comms.TierUrgent || last.Done != "ok" || last.Summary != "shipped it" {
		t.Fatalf("sentinel turn: %+v", last)
	}
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "x", Edge: CommsEdgePromptStart, Instance: f.codex.ID, Prompt: "<task-notification>bg</task-notification>"})
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "agent-turn-complete", Instance: f.codex.ID, SessionID: "thread-1", TurnID: "turn-6", Text: "Should I retry?"})
	f.d.ingestCommsSpool("default", f.byID)
	recs = f.ledgerRecords(t)
	last = recs[len(recs)-1]
	if len(recs) != 6 || last.Tier != comms.TierUrgent || !last.Q || last.Trigger != TurnTriggerTask {
		t.Fatalf("question turn: %+v", last)
	}
	if got, _ := ReadCommsSpool(f.codex.ID); len(got) != 0 {
		t.Fatalf("spool not drained: %+v", got)
	}
}

func TestCommsIngest_OffByDefaultWritesNothing(t *testing.T) {
	f := newCommsFixture(t)
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "agent-turn-complete", Instance: f.codex.ID, Text: "hello"})
	restore := SetCommsLedgerForTest(false)
	defer restore()
	f.d.ingestCommsSpool("default", f.byID)
	f.d.commsStatusRecord("default", f.shell, "waiting", time.Now())
	if comms.Exists("default") {
		t.Fatal("ledger directory created with [comms] ledger off")
	}
	if got, _ := ReadCommsSpool(f.codex.ID); len(got) != 1 {
		t.Fatalf("spool must be left alone with the ledger off: %+v", got)
	}
}

func TestCommsIngest_UnknownInstancesAreLeftForTheirProfile(t *testing.T) {
	f := newCommsFixture(t)
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "agent-turn-complete", Instance: "someone-elses-child", Text: "hello"})
	f.d.ingestCommsSpool("default", f.byID)
	if got, _ := ReadCommsSpool("someone-elses-child"); len(got) != 1 {
		t.Fatalf("foreign spool consumed: %+v", got)
	}
	if recs := f.ledgerRecords(t); len(recs) != 0 {
		t.Fatalf("foreign spool committed: %+v", recs)
	}
}

func TestCommsIngest_ShellToolGetsAStatusRecordOnce(t *testing.T) {
	f := newCommsFixture(t)
	at := time.Now()
	f.d.commsStatusRecord("default", f.shell, "waiting", at)
	f.d.commsStatusRecord("default", f.shell, "waiting", at.Add(time.Second)) // same window: duplicate
	f.d.commsStatusRecord("default", f.codex, "waiting", at)                  // codex has a text producer: no status record
	recs := f.ledgerRecords(t)
	if len(recs) != 1 || recs[0].Kind != comms.KindStatus || recs[0].From != f.shell.ID || recs[0].State != "waiting" || recs[0].Tool != "shell" {
		t.Fatalf("status records: %+v", recs)
	}
	if !recs[0].IsUrgent() {
		t.Fatal("a status-only record has no tier and must count as urgent")
	}
}

func TestCommsIngest_RecordsSurviveADaemonRestart(t *testing.T) {
	f := newCommsFixture(t)
	spoolTurn(t, CommsSpoolEntry{Harness: "gemini", Event: "AfterAgent", Instance: f.codex.ID, Text: "one", Prompt: "hi"})
	f.d.ingestCommsSpool("default", f.byID)
	f.d.closeCommsLedgers()
	if len(f.d.ledgers) != 0 {
		t.Fatal("ledgers not released")
	}
	// A fresh daemon: same key is still a duplicate, sequence continues.
	d2 := &TransitionDaemon{}
	t.Cleanup(d2.closeCommsLedgers)
	spoolTurn(t, CommsSpoolEntry{Harness: "gemini", Event: "AfterAgent", Instance: f.codex.ID, Text: "one", Prompt: "hi"})
	spoolTurn(t, CommsSpoolEntry{Harness: "gemini", Event: "AfterAgent", Instance: f.codex.ID, Text: "two", Prompt: "hi again"})
	d2.ingestCommsSpool("default", f.byID)
	recs := f.ledgerRecords(t)
	if len(recs) != 2 || recs[1].Text != "two" || recs[1].Seq != 2 {
		t.Fatalf("after restart: %+v", recs)
	}
	if entries, _ := os.ReadDir(commsSpoolInstanceDir(f.codex.ID)); len(entries) != 0 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("spool left: %s", strings.Join(names, ","))
	}
}
