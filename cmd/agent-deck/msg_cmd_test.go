package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/comms"
	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// `agent-deck msg` verbs against a real ledger in a throwaway HOME: peek
// consumes nothing, read prints urgent first within the budget and
// acknowledges what it printed, ack names records by id tail or cursor,
// export is the stable --json contract a test rig reads, stats measures.

func msgTestLedger(t *testing.T) *comms.Ledger {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", "")
	l, err := comms.Open(events.CurrentProfile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func runMsgJSON(t *testing.T, out any, args ...string) {
	t.Helper()
	var buf bytes.Buffer
	if err := runMsg(&buf, args, ""); err != nil {
		t.Fatalf("msg %v: %v", args, err)
	}
	if err := json.Unmarshal(buf.Bytes(), out); err != nil {
		t.Fatalf("msg %v: bad JSON %q: %v", args, buf.String(), err)
	}
}

func TestMsgReadPeekAckExportAndStats(t *testing.T) {
	l := msgTestLedger(t)
	info, _, err := l.Commit(comms.Record{Kind: comms.KindTurn, From: "c1", To: []string{"human:ops"}, Tier: comms.TierInfo, Text: "progress"})
	if err != nil {
		t.Fatal(err)
	}
	urgent, _, err := l.Commit(comms.Record{Kind: comms.KindTurn, From: "c2", To: []string{"human:ops"}, Tier: comms.TierUrgent, Text: "done", Done: "ok", Summary: "shipped"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.Commit(comms.Record{Kind: comms.KindTurn, From: "c3", To: []string{"someone-else"}, Tier: comms.TierUrgent, Text: "not yours"}); err != nil {
		t.Fatal(err)
	}

	var peek msgReadOutput
	runMsgJSON(t, &peek, "peek", "--for", "human:ops", "--json")
	if peek.Acked || len(peek.Records) != 2 || peek.Records[0].Record.ID != info.ID || peek.Records[1].Record.ID != urgent.ID || peek.Left != 0 {
		t.Fatalf("peek: %+v", peek)
	}
	var read msgReadOutput
	runMsgJSON(t, &read, "read", "--for", "human:ops", "--last", "1", "--json")
	if !read.Acked || len(read.Records) != 1 || read.Records[0].Record.ID != urgent.ID || read.Left != 1 {
		t.Fatalf("read --last 1 must take the urgent record first: %+v", read)
	}
	var again msgReadOutput
	runMsgJSON(t, &again, "peek", "--for", "human:ops", "--json")
	if len(again.Records) != 1 || again.Records[0].Record.ID != info.ID {
		t.Fatalf("after reading the urgent record the info record is still pending: %+v", again)
	}
	var ack msgAckOutput
	runMsgJSON(t, &ack, "ack", "--for", "human:ops", comms.ShortID(info.ID), "--json")
	if len(ack.Acked) != 1 || ack.Left != 0 || ack.Watermark != l.Cursor() {
		t.Fatalf("ack by id tail: %+v (cursor %d)", ack, l.Cursor())
	}
	var buf bytes.Buffer
	if err := runMsg(&buf, []string{"ack", "--for", "human:ops", "ZZZZZZ"}, ""); err == nil || !strings.Contains(err.Error(), "not pending") {
		t.Fatalf("acking an unknown record must fail and acknowledge nothing: %v", err)
	}
	buf.Reset()
	if err := runMsg(&buf, []string{"read", "--for", "human:ops"}, ""); err != nil || !strings.Contains(buf.String(), "nothing pending") {
		t.Fatalf("text read of an empty queue: %q %v", buf.String(), err)
	}

	var exp msgExportOutput
	runMsgJSON(t, &exp, "export", "--json")
	if exp.V != 1 || !exp.Ledger || exp.Store != l.Store().ID || len(exp.Records) != 3 || exp.Through != l.Cursor() || exp.More || exp.NowMS == 0 {
		t.Fatalf("export: %+v", exp)
	}
	var page msgExportOutput
	runMsgJSON(t, &page, "export", "--limit", "1", "--json")
	if len(page.Records) != 1 || !page.More || page.Through != 1 {
		t.Fatalf("export --limit 1: %+v", page)
	}
	var next msgExportOutput
	runMsgJSON(t, &next, "export", "--after", "1", "--for", "c2", "--json")
	if len(next.Records) != 1 || next.Records[0].Record.From != "c2" {
		t.Fatalf("export --after 1 --for c2: %+v", next)
	}
	var st msgStatsOutput
	runMsgJSON(t, &st, "stats", "--json")
	if !st.Ledger || st.Records != 3 || st.Targets.TextPct.Value == nil || *st.Targets.TextPct.Value != 100 {
		t.Fatalf("stats: %+v", st)
	}
}

func TestMsgSelfReadSpoolsACallAndNoLedgerIsExplicit(t *testing.T) {
	l := msgTestLedger(t)
	t.Setenv("AGENTDECK_INSTANCE_ID", "sess-self")
	t.Cleanup(session.SetCommsLedgerForTest(true))
	if _, _, err := l.Commit(comms.Record{Kind: comms.KindTurn, From: "kid", To: []string{"sess-self"}, Tier: comms.TierUrgent, Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	var read msgReadOutput
	runMsgJSON(t, &read, "read", "--json")
	if read.Consumer != "sess-self" || len(read.Records) != 1 {
		t.Fatalf("self read: %+v", read)
	}
	entries, err := session.ReadCommsSpool("sess-self")
	if err != nil || len(entries) != 1 || entries[0].Edge != session.CommsEdgeCall || entries[0].Event != comms.CallMsgRead {
		t.Fatalf("a read from inside a session is measured: %+v %v", entries, err)
	}

	// Another HOME: no ledger. export and stats say so in JSON; read fails.
	t.Setenv("HOME", t.TempDir())
	var exp msgExportOutput
	runMsgJSON(t, &exp, "export", "--json")
	if exp.Ledger || exp.Records == nil || len(exp.Records) != 0 {
		t.Fatalf("export without a ledger: %+v", exp)
	}
	var st msgStatsOutput
	runMsgJSON(t, &st, "stats", "--json")
	if st.Ledger || st.Records != 0 || st.Targets.WakesPerParentHour.Met != nil {
		t.Fatalf("stats without a ledger: %+v", st)
	}
	var buf bytes.Buffer
	if err := runMsg(&buf, []string{"read", "--json"}, ""); err == nil {
		t.Fatal("read without a ledger must be an error, not an empty queue")
	}
}

// Verifier P3 round 1 (B): a wake line the daemon types is a wake record,
// never a send record.
func TestMachineWakeLinesAreNotSends(t *testing.T) {
	t.Setenv(session.MachineSendEnv, "1")
	if ledgerSendAllowed() {
		t.Fatal("a machine wake line must not be recorded as a send")
	}
	t.Setenv(session.MachineSendEnv, "")
	if !ledgerSendAllowed() {
		t.Fatal("a person's or a session's send is recorded")
	}
}
