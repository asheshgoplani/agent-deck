package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// Regression coverage for the 2026-07-20 "done_status is never populated"
// outage. The completion ledger is written only by the transition daemon; when
// that daemon is down (there, a macOS Background Task Management LWCR mismatch
// after a binary reinstall left it crash-looping on EX_CONFIG for a week) the
// ledger stops being written and every child reads done_status=null forever —
// indistinguishable from "nobody finished". Seven workers printed the sentinel
// as their final line; `session children --json` reported null for all seven,
// so every `until … done_status != null` loop hung until its harness killed it.
//
// `session children` must therefore be able to answer from the child's own
// transcript, where the sentinel has been sitting the whole time.

// seedTranscript writes a Claude transcript for inst at the exact path
// ClaudeTranscriptPathForInstance derives, so the test exercises the real
// resolution rather than a hand-built path.
func seedTranscript(t *testing.T, inst *session.Instance, lines ...string) {
	t.Helper()
	path := session.ClaudeTranscriptPathForInstance(inst)
	if path == "" {
		t.Fatal("no transcript path derived; instance needs a ClaudeSessionID")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir transcript dir: %v", err)
	}
	var body string
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
}

func doneRecord(ts, status, summary string) string {
	return `{"type":"assistant","timestamp":"` + ts +
		`","message":{"content":[{"type":"text","text":"===AGENTDECK_DONE=== status=` +
		status + ` summary=` + summary + `"}]}}`
}

// newLedgerlessChild builds a claude child whose transcript path resolves under
// an isolated HOME, with no ledger entry on disk.
func newLedgerlessChild(t *testing.T, title string) *session.Instance {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	child := session.NewInstanceWithTool(title, t.TempDir(), "claude")
	child.ClaudeSessionID = "11111111-2222-3333-4444-555555555555"
	if _, found := session.ReadLedgerEntry(child.ID); found {
		t.Fatal("precondition: this child must have no ledger entry")
	}
	return child
}

func TestBuildChildRows_ReportsCompletionWhenTheDaemonNeverWroteALedgerEntry(t *testing.T) {
	child := newLedgerlessChild(t, "impl-146-s2-service")
	finishedAt := time.Now().Add(-10 * time.Minute).UTC()
	seedTranscript(t,
		child,
		`{"type":"user","timestamp":"`+finishedAt.Add(-time.Hour).Format(time.RFC3339Nano)+`","message":{"content":"implement S2"}}`,
		doneRecord(finishedAt.Format(time.RFC3339Nano), "ok", "d531d4d route + module wiring"),
	)

	rows := buildChildRows([]*session.Instance{child}, nil)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].DoneStatus != "ok" {
		t.Fatalf("done_status: got %q, want %q — a printed sentinel must be visible without the daemon",
			rows[0].DoneStatus, "ok")
	}
	if rows[0].DoneSummary != "d531d4d route + module wiring" {
		t.Errorf("done_summary: got %q", rows[0].DoneSummary)
	}
	if rows[0].DoneAt == "" {
		t.Error("done_at must carry the finishing turn's clock")
	}
	if !childTerminal(rows[0]) {
		t.Error("a child that asserted done is terminal, so --until-done can return")
	}
}

// The transcript answers "did the CURRENT turn finish". A child handed new work
// must stop reporting the old completion, or a parent reads the previous
// round's report as the answer to the current one — the exact failure the
// ledger's staleness verdict exists to prevent.
func TestBuildChildRows_TranscriptCompletionDoesNotSurviveNewWork(t *testing.T) {
	child := newLedgerlessChild(t, "review-146-s1-r1")
	seedTranscript(t,
		child,
		doneRecord("2026-07-26T10:05:00.000Z", "ok", "round one clean"),
		`{"type":"user","timestamp":"2026-07-26T11:00:00.000Z","message":{"content":"now review S2"}}`,
		`{"type":"assistant","timestamp":"2026-07-26T11:00:30.000Z","message":{"content":[{"type":"text","text":"Reading the diff."}]}}`,
	)

	rows := buildChildRows([]*session.Instance{child}, nil)
	if rows[0].DoneStatus != "" {
		t.Fatalf("a superseded completion must not be re-served, got done_status=%q", rows[0].DoneStatus)
	}
	if childTerminal(rows[0]) {
		t.Error("a child with outstanding work is not terminal")
	}
}

// A transcript-derived completion is aged against last_sent_at exactly like a
// ledger one, so the fallback cannot smuggle a stale report past the guard.
func TestBuildChildRows_TranscriptCompletionIsAgedAgainstTheLastSend(t *testing.T) {
	db := newTempStateDB(t)
	child := newLedgerlessChild(t, "plan-vacancy-autofill")
	if err := db.SaveInstances([]*statedb.InstanceRow{{
		ID: child.ID, Title: child.Title, Tool: "claude",
	}}); err != nil {
		t.Fatalf("seed instance row: %v", err)
	}

	finishedAt := time.Now().Add(-30 * time.Minute).UTC()
	seedTranscript(t, child, doneRecord(finishedAt.Format(time.RFC3339Nano), "ok", "plan written"))

	// Delivered AFTER the completion: the report answers earlier work.
	if err := db.WriteLastSentAt(child.ID, time.Now().Unix()); err != nil {
		t.Fatalf("write last_sent_at: %v", err)
	}

	rows := buildChildRows([]*session.Instance{child}, db)
	if rows[0].DoneStatus != "ok" {
		t.Fatalf("the report itself must stay visible, got %q", rows[0].DoneStatus)
	}
	if !rows[0].DoneStale {
		t.Fatal("a transcript completion older than the newest delivery must be marked stale")
	}
	if childTerminal(rows[0]) {
		t.Error("a stale completion is not terminal")
	}
}

// The ledger stays authoritative when it has an entry: it is durable across the
// child being handed new work, which the transcript deliberately is not.
func TestBuildChildRows_LedgerWinsOverTranscript(t *testing.T) {
	child := newLedgerlessChild(t, "fix-plan-vacancy-autofill-r1")
	seedTranscript(t, child, doneRecord("2026-07-26T10:05:00.000Z", "fail", "from the transcript"))
	if err := session.WriteLedgerEntry(session.CompletionLedgerEntry{
		ChildID:    child.ID,
		Status:     "ok",
		Summary:    "from the ledger",
		FinishedAt: time.Now(),
	}); err != nil {
		t.Fatalf("write ledger entry: %v", err)
	}

	rows := buildChildRows([]*session.Instance{child}, nil)
	if rows[0].DoneStatus != "ok" || rows[0].DoneSummary != "from the ledger" {
		t.Fatalf("the durable ledger entry must win, got %q/%q", rows[0].DoneStatus, rows[0].DoneSummary)
	}
}

// The turn-start fleet snapshot rides on the same rows, so it stops reporting
// "0 done" for a fleet that has finished.
func TestFormatChildrenContext_CountsTranscriptDerivedCompletions(t *testing.T) {
	child := newLedgerlessChild(t, "impl-146-s1-backend")
	seedTranscript(t, child, doneRecord(time.Now().UTC().Format(time.RFC3339Nano), "ok", "S1 landed"))

	got := formatChildrenContext(buildChildRows([]*session.Instance{child}, nil))
	if want := "1 children: 0 running, 0 waiting, 1 done"; !strings.Contains(got, want) {
		t.Fatalf("snapshot must count the completion:\nwant substring %q\ngot:\n%s", want, got)
	}
	if !strings.Contains(got, "completed: impl-146-s1-backend → ok") {
		t.Errorf("snapshot must announce the completion, got:\n%s", got)
	}
}
