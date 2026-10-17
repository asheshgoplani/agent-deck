package session

// Incremental remote talkback, review round 2: positions follow write order,
// not timestamps; an unclassified transition of a journaled child still
// crosses; a legacy remote stays enrolled; a partial batch still wakes; and
// concurrent cursor saves do not clobber each other.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func exportHasChild(exp RemoteExport, child string) bool {
	for _, ev := range exp.Records {
		if ev.ChildSessionID == child {
			return true
		}
	}
	return false
}

// A completion written AFTER another child's newer-stamped one must still
// cross: producers stamp completions with the hook's time, a worker writes
// from another process, clocks step backwards.
func TestIssue2469PR3_LateStampedLedgerCompletionStillCrosses(t *testing.T) {
	cursorTestHome(t)
	base := time.Now().Add(-10 * time.Minute)
	if err := WriteLedgerEntry(CompletionLedgerEntry{ChildID: "child-a", Profile: "default", Status: "ok",
		Summary: "a done", FinishedAt: base.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	first, err := ExportRecordsAfter(DefaultProfile, RemoteCursor{})
	if err != nil || !exportHasChild(first, "child-a") {
		t.Fatalf("drain 1: %+v %v", first.Records, err)
	}

	if err := WriteLedgerEntry(CompletionLedgerEntry{ChildID: "child-b", Profile: "default", Status: "fail",
		Summary: "b failed", FinishedAt: base}); err != nil {
		t.Fatal(err)
	}
	second, err := ExportRecordsAfter(DefaultProfile, first.CursorNext)
	if err != nil {
		t.Fatal(err)
	}
	if !exportHasChild(second, "child-b") || exportHasChild(second, "child-a") {
		t.Fatalf("want only the late-stamped child-b completion, got %+v", second.Records)
	}

	// The same child's next completion stamped before its previous one (a
	// clock step) is still a new entry.
	if err := WriteLedgerEntry(CompletionLedgerEntry{ChildID: "child-a", Profile: "default", Status: "ok",
		Summary: "a again", FinishedAt: base.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	third, err := ExportRecordsAfter(DefaultProfile, second.CursorNext)
	if err != nil || len(third.Records) != 1 || third.Records[0].DoneSummary != "a again" {
		t.Fatalf("clock-stepped completion lost: %+v %v", third.Records, err)
	}
	fourth, err := ExportRecordsAfter(DefaultProfile, third.CursorNext)
	if err != nil || len(fourth.Records) != 0 {
		t.Fatalf("an up-to-date cursor must fetch nothing: %+v %v", fourth.Records, err)
	}
}

// _unowned is read by position: a record appended later with an older stamp
// crosses, and a rewritten file (operator purge) restarts from the top.
func TestIssue2469PR3_UnownedByPositionNotStamp(t *testing.T) {
	cursorTestHome(t)
	base := time.Now().Add(-10 * time.Minute)
	unowned := func(child string, at time.Time) {
		t.Helper()
		if _, err := recordUnownedTransition(TransitionNotificationEvent{ChildSessionID: child, Profile: "default",
			FromStatus: "running", ToStatus: "error", Timestamp: at}); err != nil {
			t.Fatal(err)
		}
	}
	unowned("u-new", base.Add(time.Minute))
	first, err := ExportRecordsAfter(DefaultProfile, RemoteCursor{})
	if err != nil || len(first.Records) != 1 || first.CursorNext.Unowned.N != 1 {
		t.Fatalf("drain 1: %+v next=%+v %v", first.Records, first.CursorNext, err)
	}
	unowned("u-old", base)
	second, err := ExportRecordsAfter(DefaultProfile, first.CursorNext)
	if err != nil || len(second.Records) != 1 || second.Records[0].ChildSessionID != "u-old" {
		t.Fatalf("an older-stamped record appended later was lost: %+v %v", second.Records, err)
	}

	// A purge rewrites the file: the mark no longer matches, so the export
	// starts over and dedup on the receiver absorbs the repeats.
	if err := writeFileDurable(InboxPathFor(UnownedInboxID), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	unowned("u-after-purge", base.Add(-time.Minute))
	third, err := ExportRecordsAfter(DefaultProfile, second.CursorNext)
	if err != nil || len(third.Records) != 1 || third.Records[0].ChildSessionID != "u-after-purge" {
		t.Fatalf("record after a purge lost: %+v %v", third.Records, err)
	}
}

// A journaled child's transition the producer could not classify (no
// transcript) lands in _unowned with Seq 0 and no journal line; it must cross.
// Its classified turns (Seq > 0) ride the journal and do not cross twice.
func TestIssue2469PR3_UnjournaledUnownedRecordOfJournaledChildCrosses(t *testing.T) {
	cursorTestHome(t)
	at := time.Now().Add(-5 * time.Minute)
	line := journalTurn(t, "wj", TurnTierUrgent, "classified", at)
	if _, err := recordUnownedTransition(TransitionNotificationEvent{ChildSessionID: "wj", Profile: "default",
		FromStatus: "running", ToStatus: "waiting", Timestamp: at, Seq: line.Seq, Tier: TurnTierUrgent,
		TurnUUID: line.UUID, TextHash: line.TextHash, Text: "classified"}); err != nil {
		t.Fatal(err)
	}
	first, err := ExportRecordsAfter(DefaultProfile, RemoteCursor{})
	if err != nil || len(first.Records) != 1 || first.Records[0].Seq != 1 {
		t.Fatalf("want the journal line only: %+v %v", first.Records, err)
	}

	if _, err := recordUnownedTransition(TransitionNotificationEvent{ChildSessionID: "wj", Profile: "default",
		FromStatus: "running", ToStatus: "error", Timestamp: at.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	second, err := ExportRecordsAfter(DefaultProfile, first.CursorNext)
	if err != nil || len(second.Records) != 1 || second.Records[0].ToStatus != "error" || second.Records[0].Seq != 0 {
		t.Fatalf("an unclassified flip of a journaled child was lost: %+v %v", second.Records, err)
	}
}

// After a drain of an old remote the conductor consumes the ingested records;
// it must stay enrolled for scheduled talkback.
func TestIssue2469PR3_LegacyRemoteStaysEnrolledAfterConsume(t *testing.T) {
	cursorTestHome(t)
	deps := RemoteTalkbackDeps{
		RemoteProfile: DefaultProfile,
		FetchAfter: func(context.Context, RemoteCursor) (RemoteExport, error) {
			return RemoteExport{}, ErrRemoteCursorUnsupported
		},
		FetchAll: func(context.Context) ([]TransitionNotificationEvent, error) {
			return []TransitionNotificationEvent{{ChildSessionID: "w8", Profile: "default", Kind: transitionKindFinished, DoneStatus: "ok",
				DoneSummary: "built", Timestamp: time.Now().Add(-time.Minute)}}, nil
		},
		WriterProbe: func(context.Context) (WriterStatus, error) { return WriterStatus{Running: true}, nil },
	}
	res, err := RunRemoteTalkback(context.Background(), "boxold", "conductor-leg", deps)
	if err != nil || !res.Legacy || res.Written != 1 || res.CursorAfter != nil {
		t.Fatalf("legacy drain: %+v %v", res, err)
	}
	if _, err := ReadAndTruncateInbox("conductor-leg"); err != nil {
		t.Fatal(err)
	}
	if !talkbackEnrolled("boxold", "conductor-leg") {
		t.Fatal("a legacy remote lost scheduler enrollment once the conductor consumed its records")
	}
	// The marker carries no position: a later upgraded remote starts fresh.
	var sent []RemoteCursor
	deps.FetchAfter = func(_ context.Context, c RemoteCursor) (RemoteExport, error) {
		sent = append(sent, c)
		return RemoteExport{Writer: &WriterStatus{Running: true}}, nil
	}
	if _, err := RunRemoteTalkback(context.Background(), "boxold", "conductor-leg", deps); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || len(sent[0].Seqs) != 0 || len(sent[0].Ledger) != 0 || sent[0].Unowned.N != 0 {
		t.Fatalf("legacy marker must not act as a position: %+v", sent)
	}
	if c, _, _ := LoadRemoteCursor("boxold", "conductor-leg"); c.Legacy {
		t.Fatal("an incremental drain must replace the legacy marker")
	}
}

// An urgent record inserted before a later write of the batch failed is fresh
// only now (the retry sees it AlreadyPresent), so it must wake now.
func TestIssue2469PR3_IngestErrorStillWakesForInsertedUrgent(t *testing.T) {
	cursorTestHome(t)
	at := time.Now().Add(-time.Minute)
	journalTurn(t, "wu", TurnTierUrgent, "need-input", at)
	journalTurn(t, "wu", TurnTierInfo, "progress", at)
	parent := NewInstance("conductor-err", t.TempDir())
	parent.ID = "conductor-err"
	var fetches []RemoteCursor
	deps := localTalkbackDeps(&fetches)
	deps.Parent = func() (*Instance, string) { return parent, "default" }
	wakes := 0
	deps.Wake = func(*Instance, string, TransitionNotificationEvent) { wakes++ }

	calls := 0
	remoteIngestWrite = func(p string, ev TransitionNotificationEvent) (InboxEventPresence, error) {
		calls++
		if calls == 2 {
			return InboxEventPresenceUnknown, errors.New("disk full")
		}
		return WriteInboxEventIfUnseen(p, ev)
	}
	t.Cleanup(func() { remoteIngestWrite = WriteInboxEventIfUnseen })

	res, err := RunRemoteTalkback(context.Background(), "boxb", parent.ID, deps)
	if err == nil || !res.Woke || wakes != 1 {
		t.Fatalf("failed batch: err=%v woke=%v wakes=%d", err, res.Woke, wakes)
	}
	remoteIngestWrite = WriteInboxEventIfUnseen
	res, err = RunRemoteTalkback(context.Background(), "boxb", parent.ID, deps)
	if err != nil || res.Written != 1 || res.Duplicates != 1 || res.Woke || wakes != 1 {
		t.Fatalf("retry: res=%+v err=%v wakes=%d", res, err, wakes)
	}
}

// A CLI drain and the daemon drain of the same pair save concurrently.
func TestIssue2469PR3_ConcurrentCursorSavesDoNotClobber(t *testing.T) {
	cursorTestHome(t)
	var wg sync.WaitGroup
	errs := make(chan error, 400)
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				c := RemoteCursor{Seqs: map[string]int64{"w": int64(g*1000 + i)}, Ledger: map[string]string{
					strings.Repeat("c", 1+g*40): time.Unix(int64(i), 0).UTC().Format(time.RFC3339)}}
				if err := SaveRemoteCursor("boxb", "conductor-race", c); err != nil {
					errs <- err
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent save failed: %v", err)
	}
	c, found, err := LoadRemoteCursor("boxb", "conductor-race")
	if err != nil || !found || len(c.Ledger) != 1 {
		t.Fatalf("cursor corrupt after concurrent saves: %+v found=%v err=%v", c, found, err)
	}
}
