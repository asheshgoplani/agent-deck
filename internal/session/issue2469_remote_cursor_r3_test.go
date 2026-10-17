package session

// Incremental remote talkback, review round 3: a stale-signal turn (#2184)
// does not collapse into the previous turn, a turn the journal trimmed or
// never got still crosses from _unowned, the cursor travels on stdin, and the
// cursor stays bounded (idle children past the horizon and removed children
// drop out of it), and a same-stamp ledger rewrite with a new outcome crosses.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// unownedTurn journals one turn of child and records its _unowned copy the
// way the producer does (same seq, same stamp).
func unownedTurn(t *testing.T, child, uuid, text string, at time.Time, stale bool, keep int) TurnJournalEntry {
	t.Helper()
	line, err := AppendTurnJournal(TurnJournalEntry{TS: at, Child: child, Profile: "default", Status: "waiting",
		Tier: TurnTierUrgent, UUID: uuid, TextHash: "h-" + text, Text: text}, keep)
	if err != nil {
		t.Fatal(err)
	}
	recordUnownedCopy(t, line, stale)
	return line
}

func recordUnownedCopy(t *testing.T, line TurnJournalEntry, stale bool) {
	t.Helper()
	if _, err := recordUnownedTransition(TransitionNotificationEvent{ChildSessionID: line.Child, Profile: "default",
		FromStatus: "running", ToStatus: "waiting", Timestamp: line.TS, Seq: line.Seq, Tier: line.Tier,
		TurnUUID: line.UUID, TextHash: line.TextHash, Text: line.Text, LastOutputHash: "turn:" + line.UUID,
		OutputHashStale: stale}); err != nil {
		t.Fatal(err)
	}
}

func exportSeqs(exp RemoteExport) []int64 {
	var seqs []int64
	for _, ev := range exp.Records {
		seqs = append(seqs, ev.Seq)
	}
	return seqs
}

// R2-1: an observed flip with an unchanged transcript uuid (issue #2184) is a
// real, urgent turn. The producer keys it on its emit instant; the journal
// rendering must too, or the receiver reads it as the previous turn.
func TestIssue2469PR3R3_StaleSignalTurnCrossesCursorDrain(t *testing.T) {
	cursorTestHome(t)
	unownedTurn(t, "ws", "U1", "same", time.Now().Add(-3*time.Hour), false, 0)
	var fetches []RemoteCursor
	deps := localTalkbackDeps(&fetches)
	if res, err := RunRemoteTalkback(context.Background(), "boxs", "conductor-s", deps); err != nil || res.Written != 1 {
		t.Fatalf("drain 1: %+v %v", res, err)
	}
	if _, err := DrainInboxForParent("conductor-s"); err != nil {
		t.Fatal(err)
	}
	unownedTurn(t, "ws", "U1", "same", time.Now().Add(-time.Minute), true, 0)
	res, err := RunRemoteTalkback(context.Background(), "boxs", "conductor-s", deps)
	if err != nil {
		t.Fatal(err)
	}
	if res.Written != 1 || len(res.Stored) != 1 || !res.Stored[0].OutputHashStale {
		t.Fatalf("stale-signal urgent turn lost in cursor mode: written=%d dup=%d stored=%+v", res.Written, res.Duplicates, res.Stored)
	}
}

func TestIssue2469PR3R3_StaleSignalTurnSameBatch(t *testing.T) {
	cursorTestHome(t)
	unownedTurn(t, "ws", "U1", "same", time.Now().Add(-3*time.Hour), false, 0)
	unownedTurn(t, "ws", "U1", "same", time.Now().Add(-time.Minute), true, 0)
	full, err := ExportPendingRecords(DefaultProfile)
	if err != nil {
		t.Fatal(err)
	}
	exp, err := ExportRecordsAfter(DefaultProfile, RemoteCursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != 2 || len(exp.Records) != 2 {
		t.Fatalf("same-batch stale-signal turn collapsed: full=%d cursor=%d seqs=%v", len(full), len(exp.Records), exportSeqs(exp))
	}
	// Each turn crosses once: the _unowned copies match the journal lines.
	if exp.Records[0].Seq != 1 || exp.Records[1].Seq != 2 || !exp.Records[1].OutputHashStale {
		t.Fatalf("want journal seqs 1 and 2 (2 stale), got %+v", exp.Records)
	}
}

// R2-2: turns the journal trimmed before the conductor drained them still
// cross, from the _unowned copies the producer kept.
func TestIssue2469PR3R3_JournalTrimGapShipsFromUnowned(t *testing.T) {
	cursorTestHome(t)
	base := time.Now().Add(-time.Hour)
	add := func(i int) {
		unownedTurn(t, "wg", fmt.Sprintf("u%d", i), fmt.Sprintf("t%d", i), base.Add(time.Duration(i)*time.Second), false, 4)
	}
	add(1)
	first, err := ExportRecordsAfter(DefaultProfile, RemoteCursor{})
	if err != nil || len(first.Records) != 1 {
		t.Fatalf("drain 1: %+v %v", first.Records, err)
	}
	for i := 2; i <= 10; i++ {
		add(i)
	}
	second, err := ExportRecordsAfter(DefaultProfile, first.CursorNext)
	if err != nil {
		t.Fatal(err)
	}
	got := exportSeqs(second)
	if len(got) != 9 {
		t.Fatalf("want seqs 2..10 exactly once (2..6 from _unowned, 7..10 from the journal), got %v", got)
	}
	for i, seq := range got {
		if seq != int64(i+2) {
			t.Fatalf("want seqs 2..10 in order, got %v", got)
		}
	}
	third, err := ExportRecordsAfter(DefaultProfile, second.CursorNext)
	if err != nil || len(third.Records) != 0 {
		t.Fatalf("the gap must cross once, not on every drain: %v %v", exportSeqs(third), err)
	}
}

// A failed journal append leaves an _unowned record whose seq the next turn's
// line reuses. Both turns must cross.
func TestIssue2469PR3R3_FailedJournalAppendTurnCrosses(t *testing.T) {
	cursorTestHome(t)
	at := time.Now().Add(-10 * time.Minute)
	unownedTurn(t, "wf", "u1", "one", at, false, 0)
	first, err := ExportRecordsAfter(DefaultProfile, RemoteCursor{})
	if err != nil || len(first.Records) != 1 {
		t.Fatalf("drain 1: %+v %v", first.Records, err)
	}
	// Turn two was committed with seq 2, but its journal append failed.
	recordUnownedCopy(t, TurnJournalEntry{TS: at.Add(time.Minute), Child: "wf", Seq: 2, Tier: TurnTierUrgent,
		UUID: "u2", TextHash: "h-two", Text: "two: lost line"}, false)
	// Turn three journals as seq 2.
	unownedTurn(t, "wf", "u3", "three", at.Add(2*time.Minute), false, 0)

	second, err := ExportRecordsAfter(DefaultProfile, first.CursorNext)
	if err != nil {
		t.Fatal(err)
	}
	texts := map[string]bool{}
	for _, ev := range second.Records {
		texts[ev.Text] = true
	}
	if len(second.Records) != 2 || !texts["two: lost line"] || !texts["three"] {
		t.Fatalf("the turn without a journal line was dropped: %+v", second.Records)
	}
}

// R2-3: the cursor names one entry per active remote child, which outgrows a
// single argv string (128 KiB on Linux). It must travel on stdin.
func TestIssue2469PR3R3_CursorTravelsOnStdin(t *testing.T) {
	cursorTestHome(t)
	cursor := RemoteCursor{Seqs: map[string]int64{}}
	for i := 0; i < 6000; i++ {
		cursor.Seqs[fmt.Sprintf("%08x-%d", i, 1759500000+i)] = int64(i + 1)
	}
	arg, _ := json.Marshal(cursor)
	if len(arg) < 128*1024 {
		t.Fatalf("test cursor too small to prove anything: %d bytes", len(arg))
	}

	// The fake remote binary answers with the cursor it read from stdin.
	bin := filepath.Join(t.TempDir(), "agent-deck")
	script := `#!/bin/sh
[ "$4" = "--profile" ] && [ "$5" = "default" ] && [ "$6" = "--after" ] && [ "$7" = "-" ] || { echo "unexpected args: $*" >&2; exit 2; }
printf '{"records":[],"writer":{"running":true},"cursor_next":'
cat
printf '}\n'
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var remoteCmds []string
	r := &SSHRunner{Host: "worker@box-b", AgentDeckPath: bin}
	r.remoteExecFn = func(ctx context.Context, remoteCmd string, stdin []byte) ([]byte, error) {
		remoteCmds = append(remoteCmds, remoteCmd)
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", remoteCmd)
		cmd.Stdin = strings.NewReader(string(stdin))
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("remote command failed: %w", err)
		}
		return out, nil
	}
	exp, err := r.FetchRecordsAfter(context.Background(), cursor)
	if err != nil {
		t.Fatalf("incremental fetch with a %d-byte cursor failed: %v", len(arg), err)
	}
	if len(remoteCmds) != 1 || len(remoteCmds[0]) > 4096 {
		t.Fatalf("the cursor must not ride the remote command line: %d commands, first %d bytes", len(remoteCmds), len(remoteCmds[0]))
	}
	if len(exp.CursorNext.Seqs) != len(cursor.Seqs) || exp.Writer == nil || !exp.Writer.Running {
		t.Fatalf("stdin cursor did not round-trip: %d seqs, writer %+v", len(exp.CursorNext.Seqs), exp.Writer)
	}
}

// R2-3: a child idle past the horizon, and a ledger entry older than it, drop
// out of the export and out of the next cursor.
func TestIssue2469PR3R3_IdleChildrenLeaveTheCursor(t *testing.T) {
	cursorTestHome(t)
	old := time.Now().Add(-remoteTalkbackHorizon - 24*time.Hour)
	journalTurn(t, "idle", TurnTierUrgent, "long ago", old)
	if err := os.Chtimes(TurnJournalPath("idle"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := WriteLedgerEntry(CompletionLedgerEntry{ChildID: "idle-done", Profile: "default", Status: "ok",
		Summary: "old", FinishedAt: old}); err != nil {
		t.Fatal(err)
	}
	ledgerDir, _ := CompletionLedgerDir()
	entries, _ := os.ReadDir(ledgerDir)
	for _, e := range entries {
		_ = os.Chtimes(filepath.Join(ledgerDir, e.Name()), old, old)
	}
	journalTurn(t, "live", TurnTierUrgent, "now", time.Now().Add(-time.Minute))

	held := RemoteCursor{Seqs: map[string]int64{"idle": 1}, Ledger: map[string]string{"idle-done": "x"}}
	exp, err := ExportRecordsAfter(DefaultProfile, held)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := exp.CursorNext.Seqs["idle"]; ok {
		t.Fatalf("a child idle past the horizon stayed in the cursor: %+v", exp.CursorNext)
	}
	if _, ok := exp.CursorNext.Ledger["idle-done"]; ok {
		t.Fatalf("a ledger entry older than the horizon stayed in the cursor: %+v", exp.CursorNext)
	}
	if exp.CursorNext.Seqs["live"] != 1 || len(exp.Records) != 1 || exp.Records[0].ChildSessionID != "live" {
		t.Fatalf("the live child must still ship and stay tracked: %+v %+v", exp.CursorNext, exp.Records)
	}
	// Forgotten is not re-shipped: the next drain ships nothing.
	again, err := ExportRecordsAfter(DefaultProfile, exp.CursorNext)
	if err != nil || len(again.Records) != 0 {
		t.Fatalf("a pruned child came back: %+v %v", again.Records, err)
	}
}

// R2-3: removing a session on the remote (the rm sweep removes its turn
// journal) drops it from the next cursor, and nothing of it is re-shipped.
func TestIssue2469PR3R3_RemovedChildLeavesTheCursor(t *testing.T) {
	cursorTestHome(t)
	at := time.Now().Add(-5 * time.Minute)
	journalTurn(t, "here", TurnTierUrgent, "still here", at)
	journalTurn(t, "gone", TurnTierUrgent, "last words", at.Add(time.Second))

	first, err := ExportRecordsAfter(DefaultProfile, RemoteCursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Records) != 2 || first.CursorNext.Seqs["gone"] != 1 {
		t.Fatalf("drain 1: %+v %+v", first.Records, first.CursorNext)
	}
	if _, err := SweepInboxesForChildSession("gone"); err != nil {
		t.Fatal(err)
	}
	second, err := ExportRecordsAfter(DefaultProfile, first.CursorNext)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := second.CursorNext.Seqs["gone"]; ok || second.CursorNext.Seqs["here"] != 1 || len(second.Records) != 0 {
		t.Fatalf("a removed child must leave the cursor: %+v records=%+v", second.CursorNext, second.Records)
	}
}

// Nit: a ledger rewrite that keeps the stamp but changes the outcome (a #1186
// rescan finding a later sentinel) is a new completion.
func TestIssue2469PR3R3_LedgerRewriteWithSameStampCrosses(t *testing.T) {
	cursorTestHome(t)
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := WriteLedgerEntry(CompletionLedgerEntry{ChildID: "wl", Profile: "default", Status: "fail", Summary: "first", FinishedAt: at}); err != nil {
		t.Fatal(err)
	}
	first, err := ExportRecordsAfter(DefaultProfile, RemoteCursor{})
	if err != nil || len(first.Records) != 1 {
		t.Fatalf("drain 1: %+v %v", first.Records, err)
	}
	if err := WriteLedgerEntry(CompletionLedgerEntry{ChildID: "wl", Profile: "default", Status: "ok", Summary: "later sentinel", FinishedAt: at}); err != nil {
		t.Fatal(err)
	}
	second, err := ExportRecordsAfter(DefaultProfile, first.CursorNext)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Records) != 1 || second.Records[0].DoneSummary != "later sentinel" {
		t.Fatalf("a same-stamp ledger rewrite was held as already crossed: %+v", second.Records)
	}
}
