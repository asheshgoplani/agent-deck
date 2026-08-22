package session

// Issue #1948 — the remote-side READ of the cross-machine pull.
//
// A conductor on machine A drains machine B by running B's `inbox export` over
// ssh. These tests pin the two properties that make that safe:
//
//   - the export CONSUMES NOTHING (byte-identical inbox afterwards, ledger
//     intact, repeated exports return the same set), so two conductors draining
//     the same host both get the records;
//   - every exported record is fingerprint-stable under the EXISTING
//     EventFingerprint rule, so the receiving inbox's own dedup collapses a
//     re-drain without a second dedup rule.

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func exportTestHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ResetInboxFingerprintCacheForTest()
}

// A completion that could never resolve a local parent (its conductor lives on
// another machine) still reaches the export via the completion ledger — that is
// the whole cross-machine case.
func TestIssue1948_Export_SurfacesLedgerCompletionWithNoLocalParent(t *testing.T) {
	exportTestHome(t)

	finished := time.Now().Add(-2 * time.Minute).Truncate(time.Millisecond)
	if err := WriteLedgerEntry(CompletionLedgerEntry{
		ChildID:    "worker-remote-1",
		Profile:    "default",
		Title:      "build-index",
		Status:     "ok",
		Summary:    "index rebuilt",
		FinishedAt: finished,
	}); err != nil {
		t.Fatalf("write ledger: %v", err)
	}

	records, err := ExportPendingRecords()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("want 1 exported record, got %d: %+v", len(records), records)
	}
	got := records[0]
	if got.ChildSessionID != "worker-remote-1" || got.Kind != transitionKindFinished {
		t.Fatalf("wrong record shape: %+v", got)
	}
	if got.DoneStatus != "ok" || got.DoneSummary != "index rebuilt" {
		t.Fatalf("completion outcome lost in export: %+v", got)
	}
	if !got.Timestamp.Equal(finished) {
		t.Fatalf("timestamp must come from the ledger (fingerprint stability): got %v want %v",
			got.Timestamp, finished)
	}
}

// Pending transition records (the quota-stalled / running→waiting half of the
// report) come out too, alongside completions.
func TestIssue1948_Export_IncludesPendingInboxTransitions(t *testing.T) {
	exportTestHome(t)

	if err := WriteInboxEvent("parent-on-this-host", TransitionNotificationEvent{
		ChildSessionID: "worker-remote-2",
		ChildTitle:     "migrate",
		Profile:        "default",
		FromStatus:     "running",
		ToStatus:       "waiting",
		Timestamp:      time.Now(),
	}); err != nil {
		t.Fatalf("seed inbox: %v", err)
	}

	records, err := ExportPendingRecords()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(records) != 1 || records[0].ChildSessionID != "worker-remote-2" {
		t.Fatalf("pending transition missing from export: %+v", records)
	}
	if records[0].ToStatus != "waiting" {
		t.Fatalf("transition detail lost: %+v", records[0])
	}
}

// NON-DESTRUCTIVE: the export must not consume, truncate or mark anything. Two
// conductors draining this host both have to get the records, and this host's
// own conductor must still find its inbox exactly as it left it.
func TestIssue1948_Export_IsNonDestructive(t *testing.T) {
	exportTestHome(t)

	if err := WriteInboxEvent("parent-on-this-host", TransitionNotificationEvent{
		ChildSessionID: "worker-remote-3",
		Profile:        "default",
		FromStatus:     "running",
		ToStatus:       "waiting",
		Timestamp:      time.Now(),
	}); err != nil {
		t.Fatalf("seed inbox: %v", err)
	}
	if err := WriteLedgerEntry(CompletionLedgerEntry{
		ChildID: "worker-remote-4", Profile: "default", Status: "ok",
		Summary: "done", FinishedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed ledger: %v", err)
	}

	inboxPath := InboxPathFor("parent-on-this-host")
	before, err := os.ReadFile(inboxPath)
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	ledgerDir, err := CompletionLedgerDir()
	if err != nil {
		t.Fatalf("ledger dir: %v", err)
	}
	ledgerBefore := dirFingerprint(t, ledgerDir)

	first, err := ExportPendingRecords()
	if err != nil {
		t.Fatalf("first export: %v", err)
	}
	second, err := ExportPendingRecords()
	if err != nil {
		t.Fatalf("second export: %v", err)
	}

	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("a second conductor must see the same records: first=%d second=%d", len(first), len(second))
	}
	after, err := os.ReadFile(inboxPath)
	if err != nil {
		t.Fatalf("inbox must survive the export: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("export mutated the inbox file:\nbefore=%q\nafter =%q", before, after)
	}
	if got := dirFingerprint(t, ledgerDir); !reflect.DeepEqual(got, ledgerBefore) {
		t.Fatalf("export mutated the completion ledger:\nbefore=%v\nafter =%v", ledgerBefore, got)
	}

	// The host's own conductor still drains its inbox normally afterwards.
	drained, err := DrainInboxForParent("parent-on-this-host")
	if err != nil {
		t.Fatalf("local drain after export: %v", err)
	}
	if len(drained) != 1 || drained[0].ChildSessionID != "worker-remote-3" {
		t.Fatalf("export disturbed the host's own delivery path: %+v", drained)
	}
}

// The same logical completion held in BOTH the ledger and a pending inbox
// record crosses the wire once — collapsed by the existing EventFingerprint
// rule, not by a second one.
//
// The production case where the two copies carry DIFFERENT timestamps is
// TestIssue1948P2b_LedgerAndInboxCopiesDedupeAcrossDifferentTimestamps; this
// one keeps the simplest shape.
func TestIssue1948_Export_DedupsLedgerAndInboxCopiesOfOneCompletion(t *testing.T) {
	exportTestHome(t)

	finished := time.Now().Truncate(time.Millisecond)
	ev := TransitionNotificationEvent{
		ChildSessionID: "worker-remote-5",
		ChildTitle:     "verify",
		Profile:        "default",
		Kind:           transitionKindFinished,
		DoneStatus:     "ok",
		DoneSummary:    "all green",
		Timestamp:      finished,
	}
	if err := CommitToInbox("parent-on-this-host", ev); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := WriteLedgerEntry(CompletionLedgerEntry{
		ChildID: "worker-remote-5", Profile: "default", Title: "verify",
		Status: "ok", Summary: "all green", FinishedAt: finished,
	}); err != nil {
		t.Fatalf("write ledger: %v", err)
	}

	records, err := ExportPendingRecords()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("one completion must export once, got %d: %+v", len(records), records)
	}
}

// The dead-letter store is deliberately out of scope: it holds records that
// were suppressed ON PURPOSE (no_notify, self_conductor), and re-delivering
// those to a remote conductor would undo the suppression. Its completions are
// already covered by the ledger.
func TestIssue1948_Export_SkipsDeadLetterStore(t *testing.T) {
	exportTestHome(t)

	if _, err := writeDeadLetter(TransitionNotificationEvent{
		ChildSessionID:   "worker-suppressed",
		Profile:          "default",
		Kind:             transitionKindFinished,
		DoneStatus:       "ok",
		Timestamp:        time.Now(),
		DeadLetterReason: deadLetterReasonNoNotify,
	}); err != nil {
		t.Fatalf("seed dead letter: %v", err)
	}

	records, err := ExportPendingRecords()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	for _, r := range records {
		if r.ChildSessionID == "worker-suppressed" {
			t.Fatalf("dead-lettered suppression must not be exported: %+v", r)
		}
	}
}

// A pulled record must be re-writable into the DRAINING machine's inbox
// idempotently, using the inbox's own dedup — this is the local half of the
// property `remote drain` depends on.
func TestIssue1948_ExportedRecordIsIdempotentOnTheReceivingInbox(t *testing.T) {
	exportTestHome(t)

	if err := WriteLedgerEntry(CompletionLedgerEntry{
		ChildID: "worker-remote-6", Profile: "default", Title: "ship",
		Status: "ok", Summary: "shipped", FinishedAt: time.Now(),
	}); err != nil {
		t.Fatalf("write ledger: %v", err)
	}
	records, err := ExportPendingRecords()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("want 1 record, got %d", len(records))
	}

	written, err := WriteInboxEventIfNew("conductor-elsewhere", records[0])
	if err != nil || !written {
		t.Fatalf("first ingest must write: written=%v err=%v", written, err)
	}
	// Second drain of an unchanged remote: the export returns the identical
	// record and the inbox refuses the duplicate.
	again, err := ExportPendingRecords()
	if err != nil {
		t.Fatalf("second export: %v", err)
	}
	written, err = WriteInboxEventIfNew("conductor-elsewhere", again[0])
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if written {
		t.Fatalf("second drain double-wrote the same record")
	}

	pending, err := ReadInboxEvents("conductor-elsewhere")
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("inbox must hold exactly one copy, holds %d: %+v", len(pending), pending)
	}
}

// dirFingerprint snapshots a directory's file names and contents so a test can
// prove nothing in it changed.
func dirFingerprint(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		out[e.Name()] = string(data)
	}
	return out
}
