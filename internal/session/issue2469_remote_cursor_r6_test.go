package session

// Incremental remote talkback, review round 6: a remote child whose parent is
// in the remote's own registry belongs to that parent. The cursor export never
// ships it to the --into conductor (journal, completion ledger or _unowned);
// the legacy full export is unchanged. Children whose parent the remote cannot
// resolve (the cross-host conductor) and orphans still cross. The export opens
// only the profiles of the records it considers.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// crossHostParentID is a conductor on another machine: no registry on the
// remote can resolve it.
const crossHostParentID = "conductor-x"

// parentOnOtherHost points the fixture's child at the cross-host conductor,
// the shape `remote add` launches, so the producer commits to _unowned.
func parentOnOtherHost(t *testing.T, f *turnTestFixture) {
	t.Helper()
	f.child.ParentSessionID = crossHostParentID
	saveFixtureRegistry(t, f)
}

func unownedRecords(t *testing.T) []TransitionNotificationEvent {
	t.Helper()
	events, err := ReadInboxEvents(UnownedInboxID)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

// The fixture's child is parented under a session on the same (remote) host.
// Its turn is that parent's: legacy ships it as before, the cursor export
// does not, and a drain neither writes nor wakes.
func TestIssue2469PR3R6_RemoteLocalParentChildStaysHome(t *testing.T) {
	f := newTurnTestFixture(t)
	journal, legacy, cursor, written, wakes := remoteTurnCounts(t, f)
	if journal != 1 || legacy != 1 || len(f.inboxRecords(t)) != 1 {
		t.Fatalf("setup: want journal=1, legacy export=1 and the local parent's record, got journal=%d legacy=%d", journal, legacy)
	}
	if cursor != 0 || written != 0 || wakes != 0 {
		t.Fatalf("a remote-local parent's child crossed: cursor export=%d written=%d wakes=%d", cursor, written, wakes)
	}
	exp, err := ExportRecordsAfter(DefaultProfile, RemoteCursor{})
	if err != nil || exp.CursorNext.Seqs[f.child.ID] != 1 {
		t.Fatalf("the cursor must still move past its lines: %+v %v", exp.CursorNext, err)
	}
}

// Control: the same child under the cross-host conductor crosses in both modes.
func TestIssue2469PR3R6_UnresolvableParentChildStillCrosses(t *testing.T) {
	f := newTurnTestFixture(t)
	parentOnOtherHost(t, f)
	journal, legacy, cursor, written, wakes := remoteTurnCounts(t, f)
	if journal != 1 || legacy != 1 || len(unownedRecords(t)) != 1 {
		t.Fatalf("setup: want journal=1, legacy export=1 and one _unowned record, got journal=%d legacy=%d", journal, legacy)
	}
	if cursor != 1 || written != 1 || wakes != 0 {
		t.Fatalf("a cross-host conductor's child must cross (info, no wake): cursor export=%d written=%d wakes=%d", cursor, written, wakes)
	}
}

// The completion-ledger mirror (written for every child) of a remote-local
// parent's child stays home too; a cross-host child's completion crosses.
func TestIssue2469PR3R6_LedgerMirrorOfLocalParentChildStaysHome(t *testing.T) {
	f := newTurnTestFixture(t)
	xhost := NewInstanceWithTool("lane-b", t.TempDir(), "claude")
	xhost.ID = "child-xhost"
	xhost.ParentSessionID = crossHostParentID
	st, err := NewStorageWithProfile("default")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveWithGroups([]*Instance{f.parent, f.child, xhost}, nil); err != nil {
		t.Fatal(err)
	}
	st.Close()
	at := time.Now().Add(-time.Minute)
	for _, id := range []string{f.child.ID, xhost.ID} {
		if err := WriteLedgerEntry(CompletionLedgerEntry{ChildID: id, Profile: "default", Status: "ok",
			Summary: id + " done", FinishedAt: at}); err != nil {
			t.Fatal(err)
		}
	}

	full, err := ExportPendingRecords(DefaultProfile)
	if err != nil || len(full) != 2 {
		t.Fatalf("legacy export must keep shipping both completions: %+v %v", full, err)
	}
	exp, err := ExportRecordsAfter(DefaultProfile, RemoteCursor{})
	if err != nil {
		t.Fatal(err)
	}
	if exportHasChild(exp, f.child.ID) || !exportHasChild(exp, xhost.ID) || len(exp.Records) != 1 {
		t.Fatalf("want only the cross-host child's completion, got %+v", exp.Records)
	}
	wakes := 0
	res, err := RunRemoteTalkback(context.Background(), "boxd", "conductor-x", wakeCountingDeps(t, &wakes))
	if err != nil || res.Written != 1 || res.Stored[0].ChildSessionID != "boxd:"+xhost.ID {
		t.Fatalf("drain: want only the cross-host completion written, got %+v %v", res, err)
	}
}

// An orphan whose turns crossed is then parented under a session on its own
// host: its later turns belong to that parent and stay home.
func TestIssue2469PR3R6_OrphanAdoptedLocallyStopsCrossing(t *testing.T) {
	f := newTurnTestFixture(t)
	f.child.ParentSessionID = ""
	saveFixtureRegistry(t, f)
	wakes := 0
	deps := wakeCountingDeps(t, &wakes)
	statuses := map[string]string{f.child.ID: "waiting"}
	f.appendTurn(t, fxHuman("u0", "telegram: status?"), fxAssistantText("a0", "All lanes green."))
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	if res, err := RunRemoteTalkback(context.Background(), "boxd", "conductor-x", deps); err != nil || res.Written != 1 {
		t.Fatalf("drain 1: the orphan's turn must cross: %+v %v", res, err)
	}

	f.child.ParentSessionID = f.parent.ID
	saveFixtureRegistry(t, f)
	f.appendTurn(t, fxHuman("u1", "merge lane B"), fxAssistantText("a1", "Lane B merged."))
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	res, err := RunRemoteTalkback(context.Background(), "boxd", "conductor-x", deps)
	if err != nil {
		t.Fatal(err)
	}
	if res.Written != 0 || res.CursorAfter == nil || res.CursorAfter.Seqs[f.child.ID] != 2 {
		t.Fatalf("after adoption: want nothing written and the cursor at seq 2, got %+v", res)
	}
}

// _unowned records the export will not consider (past the horizon, or before
// the cursor's position) must not make it open their profile's registry: an
// old record of a deleted profile would recreate that profile's store.
func TestIssue2469PR3R6_ExportOpensOnlyConsideredProfiles(t *testing.T) {
	cursorTestHome(t)
	unowned := func(child, profile string, at time.Time) {
		t.Helper()
		if _, err := recordUnownedTransition(TransitionNotificationEvent{ChildSessionID: child, Profile: profile,
			FromStatus: "running", ToStatus: "error", Timestamp: at}); err != nil {
			t.Fatal(err)
		}
	}
	storeOf := func(profile string) string {
		t.Helper()
		dir, err := GetProfileDir(profile)
		if err != nil {
			t.Fatal(err)
		}
		return dir
	}
	exists := func(path string) bool { _, err := os.Stat(path); return err == nil }

	unowned("w-ancient", "gone-old", time.Now().Add(-2*remoteTalkbackHorizon))
	unowned("w-recent", "gone-recent", time.Now().Add(-time.Minute))
	// #2539: an export is scoped to one profile; the ancient record's own
	// profile, asked directly, still has nothing in the horizon to open.
	if old, err := ExportRecordsAfter("gone-old", RemoteCursor{}); err != nil || len(old.Records) != 0 {
		t.Fatalf("drain 0: the ancient record is past the horizon: %+v %v", old.Records, err)
	}
	first, err := ExportRecordsAfter("gone-recent", RemoteCursor{})
	if err != nil || len(first.Records) != 1 || first.Records[0].ChildSessionID != "w-recent" {
		t.Fatalf("drain 1: want only the in-horizon record, got %+v %v", first.Records, err)
	}
	if exists(storeOf("gone-old")) {
		t.Fatal("a record past the horizon opened (and recreated) its deleted profile")
	}

	// The in-horizon profile is then deleted; with the record behind the
	// cursor, the next export must not recreate it.
	if err := os.RemoveAll(storeOf("gone-recent")); err != nil {
		t.Fatal(err)
	}
	second, err := ExportRecordsAfter("gone-recent", first.CursorNext)
	if err != nil || len(second.Records) != 0 {
		t.Fatalf("drain 2: %+v %v", second.Records, err)
	}
	if exists(filepath.Join(storeOf("gone-recent"))) {
		t.Fatal("a record before the cursor's position opened (and recreated) its deleted profile")
	}
}
