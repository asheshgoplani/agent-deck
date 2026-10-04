package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/comms"
)

// P3: a remote's ledger records ride the talkback round trip, are imported
// with the remote as origin, once, and carry a cross-host latency only when
// the clock offset measured on that round trip makes it honest.

func TestCrossHostLatencyIsHonestAboutClocks(t *testing.T) {
	// Local round trip 1000..1100 (midpoint 1050, uncertainty 51 ms).
	cases := []struct {
		name                   string
		signal, imp, remoteNow int64
		want                   int64
		ok                     bool
	}{
		// Remote clock 10 s ahead: signal at remote 9000+... corrected.
		{"remote ahead", 10_000 + 1050 - 3000, 1100, 10_000 + 1050, 3050, true},
		{"remote behind", -5_000 + 1050 - 3000 + 1_000_000, 1100, -5_000 + 1050 + 1_000_000, 3050, true},
		// Estimate within the uncertainty: unknown, never a tiny or negative number.
		{"within uncertainty", 1050 + 30, 1100, 1050, 0, false},
		{"apparently negative", 1050 + 5000, 1100, 1050, 0, false},
		{"no signal", 0, 1100, 1050, 0, false},
	}
	for _, c := range cases {
		got, unc, ok := crossHostLatency(c.signal, c.imp, c.remoteNow, 1000, 1100)
		if ok != c.ok || (ok && (got != c.want || unc != 51)) {
			t.Errorf("%s: latency %d ±%d ok=%v, want %d ok=%v", c.name, got, unc, ok, c.want, c.ok)
		}
	}
}

func TestRemoteLedgerRecordsRideTheTalkbackRoundTripOnce(t *testing.T) {
	f := newCommsFixture(t)
	// The "remote": another profile's ledger on the same disk, exported the
	// way `inbox export --after -` does on the remote host.
	remote, err := comms.Open("remotehost")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = remote.Close() })
	signal := time.Now().Add(-3 * time.Second).UnixMilli()
	turn, _, err := remote.Commit(comms.Record{Kind: comms.KindTurn, From: "rchild", To: []string{f.parent.ID}, Tier: comms.TierUrgent,
		Text: "remote done", Done: "ok", Summary: "remote done", Key: "turn:rchild:1", TSignal: signal})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []comms.Record{
		{Kind: comms.KindWake, From: "agent-deck", To: []string{f.parent.ID}, Text: "measurement row stays home"},
		{Kind: comms.KindTurn, From: "rchild", To: []string{f.parent.ID}, Tier: comms.TierNoise, Text: "noise stays home", Key: "turn:rchild:2"},
	} {
		if _, _, err := remote.Commit(r); err != nil {
			t.Fatal(err)
		}
	}
	fetches := 0
	deps := RemoteTalkbackDeps{
		FetchAfter: func(_ context.Context, cursor RemoteCursor) (RemoteExport, error) {
			fetches++
			t.Setenv("AGENTDECK_PROFILE", "remotehost")
			defer t.Setenv("AGENTDECK_PROFILE", "default")
			// Over the wire: the cursor as JSON, the export as JSON.
			wire, _ := cursor.MarshalJSON()
			parsed, err := ParseRemoteCursor(string(wire))
			if err != nil {
				return RemoteExport{}, err
			}
			if parsed.Comms == nil {
				t.Fatal("a puller with the ledger on must send a _comms position")
			}
			return ExportRecordsAfter(parsed)
		},
		WriterProbe: func(context.Context) (WriterStatus, error) { return WriterStatus{Running: true}, nil },
		Parent:      func() (*Instance, string) { return f.parent, "default" },
		Wake:        func(*Instance, string, TransitionNotificationEvent) {},
	}
	first, err := RunRemoteTalkback(context.Background(), "r1", f.parent.ID, deps)
	if err != nil {
		t.Fatal(err)
	}
	saved := loadRemoteCommsCursor("r1", "default")
	if first.CursorAfter == nil || first.CursorAfter.Comms == nil || *first.CursorAfter.Comms != saved {
		t.Fatalf("reported cursor must include the accepted profile position: %+v, saved %+v", first.CursorAfter, saved)
	}
	if first.CursorBefore.Comms.After != 0 {
		t.Fatalf("cursor_before was mutated: %+v", first.CursorBefore.Comms)
	}
	parentCursor, _, err := LoadRemoteCursor("r1", f.parent.ID)
	if err != nil || parentCursor.Comms != nil {
		t.Fatalf("profile position must not persist per parent: %+v, %v", parentCursor, err)
	}
	f.d.ingestCommsSpool("default", f.byID)
	imported := func() []comms.Record {
		var out []comms.Record
		for _, r := range f.ledgerRecords(t) {
			if r.Origin != "" {
				out = append(out, r)
			}
		}
		return out
	}
	got := imported()
	if len(got) != 1 {
		t.Fatalf("want exactly the remote turn imported (no wake, no noise), got %+v", got)
	}
	r := got[0]
	if r.Origin != "r1" || r.ID != turn.ID || r.Key != turn.Key || r.Store != remote.Store().ID || r.SrcCursor == 0 || r.TImport == 0 {
		t.Fatalf("imported identity %+v", r)
	}
	if r.XLatencyMS < 2500 || r.XErrMS <= 0 || r.XLatencyMS <= r.XErrMS {
		t.Fatalf("cross-host latency %d ±%d, want about 3 s", r.XLatencyMS, r.XErrMS)
	}
	if comms.Deliverable(r, f.parent.ID) {
		t.Fatal("a pulled record is delivered by the talkback inbox path, not the ledger, in this phase")
	}
	// A second round trip adds nothing.
	second, err := RunRemoteTalkback(context.Background(), "r1", f.parent.ID, deps)
	if err != nil {
		t.Fatal(err)
	}
	if second.CursorBefore.Comms == nil || *second.CursorBefore.Comms != *first.CursorAfter.Comms {
		t.Fatalf("cursor discontinuity: first %+v, second %+v", first.CursorAfter, second.CursorBefore)
	}
	f.d.ingestCommsSpool("default", f.byID)
	if n := len(imported()); n != 1 || fetches != 2 {
		t.Fatalf("second pull: %d imported records after %d fetches", n, fetches)
	}
	// A repeated batch (a crash after the spool write, before the position
	// moved) imports nothing twice.
	if err := acceptRemoteComms("r1", "default", &RemoteCommsExport{Ledger: true, Store: remote.Store().ID, Epoch: 1, NowMS: time.Now().UnixMilli(),
		Through: 1, Records: []comms.Exported{{Cursor: 1, Record: turn}}}, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	f.d.ingestCommsSpool("default", f.byID)
	if n := len(imported()); n != 1 {
		t.Fatalf("a replayed batch duplicated: %d", n)
	}
}

func TestAnOlderRemoteWithoutTheCommsAnswerChangesNothing(t *testing.T) {
	f := newCommsFixture(t)
	deps := RemoteTalkbackDeps{
		FetchAfter: func(_ context.Context, cursor RemoteCursor) (RemoteExport, error) {
			return RemoteExport{CursorNext: RemoteCursor{Seqs: map[string]int64{}}}, nil // no "comms" key
		},
		WriterProbe: func(context.Context) (WriterStatus, error) { return WriterStatus{Running: true}, nil },
		Parent:      func() (*Instance, string) { return f.parent, "default" },
	}
	if _, err := RunRemoteTalkback(context.Background(), "old", f.parent.ID, deps); err != nil {
		t.Fatal(err)
	}
	if hasCommsImports("default") {
		t.Fatal("an older remote must not produce an import")
	}
	if c := loadRemoteCommsCursor("old", "default"); c.Store != "" || c.After != 0 {
		t.Fatalf("no position from an older remote: %+v", c)
	}
	// And the inbox cursor never carries the ledger position.
	if saved, _, _ := LoadRemoteCursor("old", f.parent.ID); saved.Comms != nil {
		t.Fatalf("the per-parent talkback cursor must not hold _comms: %+v", saved.Comms)
	}
}

// Verifier P3 round 1 (C): a pulled record whose key the origin reused for
// different content is skipped and logged; it never wedges the import or
// the local spool behind it.
func TestAConflictingPulledRecordNeverWedgesIngest(t *testing.T) {
	f := newCommsFixture(t)
	a := comms.Record{V: 1, ID: comms.NewID(time.Now()), Kind: comms.KindTurn, From: "rchild", To: []string{f.parent.ID}, Tier: comms.TierInfo,
		Text: "first", Key: "turn:rchild:same", Store: "REMOTESTORE", Epoch: 1}
	b := a
	b.ID, b.Text = comms.NewID(time.Now()), "different content, same key"
	c := a
	c.ID, c.Key, c.Text = comms.NewID(time.Now()), "turn:rchild:other", "after the conflict"
	if err := acceptRemoteComms("r1", "default", &RemoteCommsExport{Ledger: true, Store: "REMOTESTORE", Epoch: 1, NowMS: time.Now().UnixMilli(),
		Through: 3, Records: []comms.Exported{{Cursor: 1, Record: a}, {Cursor: 2, Record: b}, {Cursor: 3, Record: c}}}, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "agent-turn-complete", Instance: f.codex.ID, SessionID: "th", TurnID: "t1", Text: "local work"})
	f.d.ingestCommsSpool("default", f.byID)
	var texts []string
	for _, r := range f.ledgerRecords(t) {
		texts = append(texts, r.Text)
	}
	joined := strings.Join(texts, "|")
	if !strings.Contains(joined, "first") || !strings.Contains(joined, "after the conflict") || strings.Contains(joined, "different content") || !strings.Contains(joined, "local work") {
		t.Fatalf("records after a conflict must still land: %q", joined)
	}
	if hasCommsImports("default") {
		t.Fatal("the batch must be consumed")
	}
}

// Verifier P3 round 1 (D, F, G): an export that is not sure answers "no
// ledger" with the position unchanged: the switch off on the remote, or an
// unreadable registry.
func TestAnUncertainExportNeverMovesThePosition(t *testing.T) {
	_ = newCommsFixture(t)
	t.Setenv("AGENTDECK_PROFILE", "remotehost2")
	remote, err := comms.Open("remotehost2")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = remote.Close() })
	if _, _, err := remote.Commit(comms.Record{Kind: comms.KindTurn, From: "x", To: []string{"elsewhere"}, Tier: comms.TierInfo, Text: "t", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(SetCommsLedgerForTest(false))
	out := exportCommsAfter(RemoteCommsCursor{Store: "old", Epoch: 1, After: 7}, time.Now())
	if out.Ledger || out.Through != 7 || len(out.Records) != 0 {
		t.Fatalf("switch off on the remote: %+v", out)
	}
	SetCommsLedgerForTest(true)
	out = exportCommsAfter(RemoteCommsCursor{}, time.Now())
	if !out.Ledger || len(out.Records) != 1 || out.Through < out.After {
		t.Fatalf("a first pull exports the record: %+v", out)
	}
}

// Verifier P3 round 2 (#4): a batch pulled for one local profile keeps only
// records addressed to that profile's sessions.
func TestAPulledBatchKeepsOnlyThisProfilesRecords(t *testing.T) {
	f := newCommsFixture(t)
	mine := comms.Record{V: 1, ID: comms.NewID(time.Now()), Kind: comms.KindTurn, From: "rchild", To: []string{f.parent.ID}, Tier: comms.TierInfo,
		Text: "for this profile", Key: "turn:rchild:a", Store: "REMOTESTORE", Epoch: 1}
	theirs := mine
	theirs.ID, theirs.Key, theirs.To, theirs.Text = comms.NewID(time.Now()), "turn:rchild:b", []string{"work-profile-parent"}, "for another profile"
	if err := acceptRemoteComms("r1", "default", &RemoteCommsExport{Ledger: true, Store: "REMOTESTORE", Epoch: 1, NowMS: time.Now().UnixMilli(),
		Through: 2, Records: []comms.Exported{{Cursor: 1, Record: mine}, {Cursor: 2, Record: theirs}}}, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	f.d.ingestCommsSpool("default", f.byID)
	var texts []string
	for _, r := range f.ledgerRecords(t) {
		texts = append(texts, r.Text)
	}
	if j := strings.Join(texts, "|"); !strings.Contains(j, "for this profile") || strings.Contains(j, "for another profile") {
		t.Fatalf("imported: %q", j)
	}
}

// Verifier P3 round 2 (#11): a record no ledger could have committed (no
// sender) is skipped; the batch and the local spool go on.
func TestAnInvalidPulledRecordNeverWedgesIngest(t *testing.T) {
	f := newCommsFixture(t)
	bad := comms.Record{V: 1, ID: comms.NewID(time.Now()), Kind: comms.KindTurn, To: []string{f.parent.ID}, Text: "no sender", Key: "k1", Store: "REMOTESTORE"}
	good := comms.Record{V: 1, ID: comms.NewID(time.Now()), Kind: comms.KindTurn, From: "rc", To: []string{f.parent.ID}, Tier: comms.TierInfo, Text: "fine", Key: "k2", Store: "REMOTESTORE", Epoch: 1}
	if err := acceptRemoteComms("r1", "default", &RemoteCommsExport{Ledger: true, Store: "REMOTESTORE", Epoch: 1, NowMS: time.Now().UnixMilli(),
		Through: 2, Records: []comms.Exported{{Cursor: 1, Record: bad}, {Cursor: 2, Record: good}}}, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	spoolTurn(t, CommsSpoolEntry{Harness: "codex", Event: "agent-turn-complete", Instance: f.codex.ID, SessionID: "th", TurnID: "t1", Text: "local work"})
	f.d.ingestCommsSpool("default", f.byID)
	var texts []string
	for _, r := range f.ledgerRecords(t) {
		texts = append(texts, r.Text)
	}
	if j := strings.Join(texts, "|"); !strings.Contains(j, "fine") || !strings.Contains(j, "local work") || strings.Contains(j, "no sender") {
		t.Fatalf("imported: %q", j)
	}
}

// Verifier P3 round 3 (#1): a pulled batch the ledger cannot write is kept
// (its remote position already moved past it) and lands after the reopen.
func TestAPulledBatchSurvivesALedgerWriteFailure(t *testing.T) {
	f := newCommsFixture(t)
	rec := comms.Record{V: 1, ID: comms.NewID(time.Now()), Kind: comms.KindTurn, From: "rc", To: []string{f.parent.ID}, Tier: comms.TierInfo,
		Text: "keep me", Key: "k-keep", Store: "REMOTESTORE", Epoch: 1}
	if err := acceptRemoteComms("r1", "default", &RemoteCommsExport{Ledger: true, Store: "REMOTESTORE", Epoch: 1, NowMS: time.Now().UnixMilli(),
		Through: 1, Records: []comms.Exported{{Cursor: 1, Record: rec}}}, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	l := f.d.commsLedgerFor("default")
	_ = l.Close() // the bus refuses every write from here on
	for i := 0; i < 5; i++ {
		if f.d.importCommsSpool(l, "default", f.byID) {
			t.Fatal("a failed write must not count as imported")
		}
	}
	if !hasCommsImports("default") {
		t.Fatal("the batch must be kept, never set aside, after a write failure")
	}
	f.d.dropCommsLedger("default")
	f.d.ledgerOpenFailed = nil
	f.d.ingestCommsSpool("default", f.byID)
	found := false
	for _, r := range f.ledgerRecords(t) {
		found = found || r.Text == "keep me"
	}
	if !found || hasCommsImports("default") {
		t.Fatal("after the reopen the batch lands")
	}
}

// Verifier P3 round 3 (#2): every profile's import directory is pruned.
func TestImportPruneRunsForEveryProfile(t *testing.T) {
	f := newCommsFixture(t)
	old := time.Now().Add(-8 * 24 * time.Hour)
	for _, profile := range []string{"default", "work"} {
		dir := commsImportDir(profile)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "x.json.rejected")
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(p, old, old)
	}
	f.d.ingestCommsSpool("default", f.byID)
	f.d.ingestCommsSpool("work", map[string]*Instance{})
	for _, profile := range []string{"default", "work"} {
		if _, err := os.Stat(filepath.Join(commsImportDir(profile), "x.json.rejected")); err == nil {
			t.Fatalf("profile %s was not pruned", profile)
		}
	}
}
