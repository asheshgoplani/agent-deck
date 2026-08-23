package session

// Issue #1948, review round 1. One test per finding:
//
//	P1  transitions from a parentless session are dropped as orphans, so a drain
//	    can never return the quota-stall the feature exists to surface.
//	P2a a completion the user suppressed with NoTransitionNotify is exported
//	    anyway, overriding that opt-out over ssh.
//	P2b the ledger and inbox copies of ONE completion never dedupe, because the
//	    fingerprint keyed on the emit instant and the two paths stamp different
//	    ones.
//	P2c an unreadable record file is skipped silently, so a host whose records
//	    cannot be read drains as EMPTY.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// reviewTestHome isolates HOME and returns a storage handle for a dedicated
// profile, mirroring the existing dead-letter tests.
func reviewTestHome(t *testing.T, profile string) *Storage {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	ResetInboxFingerprintCacheForTest()
	t.Cleanup(func() {
		ClearUserConfigCache()
		ResetInboxFingerprintCacheForTest()
	})
	if err := os.MkdirAll(home+"/.agent-deck", 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	t.Cleanup(func() { storage.Close() })
	return storage
}

// parentlessWorker models a worker created on THIS host by a conductor on
// another one: the parent identity is retained, but cannot be resolved in this
// host's registry. A truly parentless child remains an issue #805 orphan and is
// intentionally excluded from the discovery ledger by #2039.
func parentlessWorker(id, title string) *Instance {
	return &Instance{
		ID: id, Title: title, ProjectPath: "/tmp/" + id,
		GroupPath: DefaultGroupPath, Tool: "claude",
		Status: StatusWaiting, CreatedAt: time.Now(),
		ParentSessionID: "conductor-on-another-host",
	}
}

// --- P1 -------------------------------------------------------------------

// The headline: a quota-stalled worker whose conductor is on another machine
// must leave a DRAINABLE record, not an orphan-log line.
func TestIssue1948P1_ParentlessStallIsDrainable(t *testing.T) {
	profile := "_test-1948-p1"
	storage := reviewTestHome(t, profile)

	child := parentlessWorker("worker-b-stalled", "remote-migrate")
	if err := storage.SaveWithGroups([]*Instance{child}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	n := NewTransitionNotifier()
	defer n.Close()
	res := n.NotifyTransition(TransitionNotificationEvent{
		ChildSessionID: child.ID,
		ChildTitle:     child.Title,
		Profile:        profile,
		FromStatus:     "running",
		ToStatus:       "waiting",
		Substate:       "usage-limit",
		LastOutputHash: "pane-hash-quota",
		Timestamp:      time.Now(),
	})
	// The durable _unowned copy is a successful delivery under #2039; its stored
	// record, rather than the success result, retains the resolution reason.
	if res.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("expected the remote-parent record to commit, got %+v", res)
	}

	records, err := ExportPendingRecords()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	var got *TransitionNotificationEvent
	for i := range records {
		if records[i].ChildSessionID == child.ID {
			got = &records[i]
		}
	}
	if got == nil {
		t.Fatalf("a parentless session's stall must be drainable; export returned %+v", records)
	}
	if got.DeadLetterReason != deadLetterReasonParentMissing {
		t.Fatalf("drainable record lost its remote-parent reason: %+v", *got)
	}
	if got.FromStatus != "running" || got.ToStatus != "waiting" {
		t.Fatalf("the flip itself must survive: %+v", *got)
	}
	if got.Substate != "usage-limit" {
		t.Fatalf("the quota-stall substate must survive the round trip: %+v", *got)
	}
}

// A parent recorded but absent from THIS registry is the same steady state.
func TestIssue1948P1_ParentOnAnotherMachineIsDrainable(t *testing.T) {
	profile := "_test-1948-p1-remoteparent"
	storage := reviewTestHome(t, profile)

	child := parentlessWorker("worker-b-remoteparent", "remote-migrate")
	child.ParentSessionID = "conductor-living-on-machine-a"
	if err := storage.SaveWithGroups([]*Instance{child}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	n := NewTransitionNotifier()
	defer n.Close()
	res := n.NotifyTransition(TransitionNotificationEvent{
		ChildSessionID: child.ID, ChildTitle: child.Title, Profile: profile,
		FromStatus: "running", ToStatus: "error", Timestamp: time.Now(),
	})
	if res.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("expected the remote-parent record to commit, got %+v", res)
	}

	pending, err := ReadInboxEvents(UnownedInboxID)
	if err != nil {
		t.Fatalf("read unowned ledger: %v", err)
	}
	if len(pending) != 1 || pending[0].ChildSessionID != child.ID {
		t.Fatalf("a cross-machine parent link must leave a drainable record, got %+v", pending)
	}
	if pending[0].DeadLetterReason != deadLetterReasonParentMissing {
		t.Fatalf("expected parent_removed on stored record, got %+v", pending[0])
	}
}

// The unowned ledger is append-with-dedup like every other inbox file, so one
// stall re-emitted by a later daemon process is kept once.
func TestIssue1948P1_UnownedLedgerDedupsRepeatedIdenticalStall(t *testing.T) {
	profile := "_test-1948-p1-dedup"
	storage := reviewTestHome(t, profile)
	child := parentlessWorker("worker-b-repeat", "remote-migrate")
	if err := storage.SaveWithGroups([]*Instance{child}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	n := NewTransitionNotifier()
	defer n.Close()
	ev := TransitionNotificationEvent{
		ChildSessionID: child.ID, ChildTitle: child.Title, Profile: profile,
		FromStatus: "running", ToStatus: "waiting", LastOutputHash: "same-pane",
		Timestamp: time.Now(),
	}
	// Commit directly: this exercises the ledger's own dedup rather than the
	// notifier's separate short-window duplicate suppression.
	n.commitEventToInbox(ev)
	ResetInboxFingerprintCacheForTest() // a later daemon process, cold cache
	ev.Timestamp = time.Now().Add(time.Minute)
	n.commitEventToInbox(ev)

	pending, err := ReadInboxEvents(UnownedInboxID)
	if err != nil {
		t.Fatalf("read unowned ledger: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("the same stall must be kept once, got %d copies: %+v", len(pending), pending)
	}
}

// A different turn for the same child is a different event and must survive.
func TestIssue1948P1_UnownedLedgerKeepsDistinctStalls(t *testing.T) {
	profile := "_test-1948-p1-distinct"
	storage := reviewTestHome(t, profile)
	child := parentlessWorker("worker-b-two", "remote-migrate")
	if err := storage.SaveWithGroups([]*Instance{child}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	n := NewTransitionNotifier()
	defer n.Close()
	base := TransitionNotificationEvent{
		ChildSessionID: child.ID, ChildTitle: child.Title, Profile: profile,
		FromStatus: "running", ToStatus: "waiting", Timestamp: time.Now(),
	}
	first := base
	first.LastOutputHash = "pane-1"
	second := base
	second.LastOutputHash = "pane-2"
	second.Timestamp = time.Now().Add(time.Minute)

	n.commitEventToInbox(first)
	n.commitEventToInbox(second)

	pending, err := ReadInboxEvents(UnownedInboxID)
	if err != nil {
		t.Fatalf("read unowned ledger: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("two distinct stalls must both be drainable, got %d: %+v", len(pending), pending)
	}
}

// Deliberate suppressions must NOT be diverted into the unowned ledger: an
// opt-out is a choice, not a lost record.
func TestIssue1948P1_UnownedLedgerIgnoresDeliberateSuppression(t *testing.T) {
	profile := "_test-1948-p1-suppressed"
	storage := reviewTestHome(t, profile)

	silent := parentlessWorker("worker-b-silent", "quiet-worker")
	silent.NoTransitionNotify = true
	if err := storage.SaveWithGroups([]*Instance{silent}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	n := NewTransitionNotifier()
	defer n.Close()
	n.commitEventToInbox(TransitionNotificationEvent{
		ChildSessionID: silent.ID, ChildTitle: silent.Title, Profile: profile,
		FromStatus: "running", ToStatus: "waiting", Timestamp: time.Now(),
	})

	pending, err := ReadInboxEvents(UnownedInboxID)
	if err != nil {
		t.Fatalf("read unowned ledger: %v", err)
	}
	for _, p := range pending {
		if p.ChildSessionID == silent.ID {
			t.Fatalf("a suppressed session must not be diverted into the unowned ledger: %+v", p)
		}
	}
}

// --- P2a ------------------------------------------------------------------

// RunTaskWorker writes the ledger entry BEFORE delivery is refused with
// no_notify, so a suppressed session leaves one behind. Exporting it would
// override the user's opt-out over ssh.
func TestIssue1948P2a_SuppressedCompletionIsNotExported(t *testing.T) {
	profile := "_test-1948-p2a"
	storage := reviewTestHome(t, profile)

	silent := parentlessWorker("worker-silent", "quiet-worker")
	silent.NoTransitionNotify = true
	loud := parentlessWorker("worker-loud", "ordinary-worker")
	if err := storage.SaveWithGroups([]*Instance{silent, loud}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	for _, id := range []string{silent.ID, loud.ID} {
		if err := WriteLedgerEntry(CompletionLedgerEntry{
			ChildID: id, Profile: profile, Status: "ok",
			Summary: "finished", FinishedAt: time.Now(),
		}); err != nil {
			t.Fatalf("ledger: %v", err)
		}
	}

	records, err := ExportPendingRecords()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	sawLoud := false
	for _, r := range records {
		if r.ChildSessionID == silent.ID {
			t.Fatalf("exporting a NoTransitionNotify completion overrides the opt-out: %+v", r)
		}
		if r.ChildSessionID == loud.ID {
			sawLoud = true
		}
	}
	if !sawLoud {
		t.Fatalf("the opt-out filter must not swallow ordinary completions: %+v", records)
	}
}

// --- P2b ------------------------------------------------------------------

// The real production shape: the ledger copy carries CompletionRecord.FinishedAt
// while DeliverCompletion stamps the inbox copy with a LATER time.Now(). One
// completion must still cross the wire once.
func TestIssue1948P2b_LedgerAndInboxCopiesDedupeAcrossDifferentTimestamps(t *testing.T) {
	profile := "_test-1948-p2b"
	reviewTestHome(t, profile)

	finished := time.Now().Add(-90 * time.Second)
	if err := WriteLedgerEntry(CompletionLedgerEntry{
		ChildID: "worker-dup", Profile: profile, Title: "ship",
		Status: "ok", Summary: "shipped", FinishedAt: finished,
	}); err != nil {
		t.Fatalf("ledger: %v", err)
	}
	// The same completion, committed later with its own clock reading.
	if err := CommitToInbox("some-local-parent", TransitionNotificationEvent{
		ChildSessionID: "worker-dup", ChildTitle: "ship", Profile: profile,
		Kind: transitionKindFinished, DoneStatus: "ok", DoneSummary: "shipped",
		Timestamp: time.Now(),
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	records, err := ExportPendingRecords()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	count := 0
	for _, r := range records {
		if r.ChildSessionID == "worker-dup" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("one completion exported %d times — the two production copies are not deduping: %+v", count, records)
	}
}

// The fingerprint itself: two stampings of one event are equal; genuinely
// different events are not.
func TestIssue1948P2b_FingerprintIgnoresEmitInstant(t *testing.T) {
	base := TransitionNotificationEvent{
		ChildSessionID: "c1", FromStatus: "running", ToStatus: "waiting",
		LastOutputHash: "h1", Timestamp: time.Now(),
	}
	later := base
	later.Timestamp = base.Timestamp.Add(2 * time.Hour)
	if EventFingerprint(base) != EventFingerprint(later) {
		t.Fatalf("the same event stamped twice must share a fingerprint")
	}

	differentTurn := base
	differentTurn.LastOutputHash = "h2"
	if EventFingerprint(base) == EventFingerprint(differentTurn) {
		t.Fatalf("two different turns must not collapse")
	}

	doneOK := TransitionNotificationEvent{
		ChildSessionID: "c1", Kind: transitionKindFinished, DoneStatus: "ok", Timestamp: time.Now(),
	}
	doneFail := doneOK
	doneFail.DoneStatus = "fail"
	doneFail.Timestamp = doneOK.Timestamp.Add(time.Second)
	if EventFingerprint(doneOK) == EventFingerprint(doneFail) {
		t.Fatalf("distinct completion outcomes must not collapse")
	}

	otherChild := base
	otherChild.ChildSessionID = "c2"
	if EventFingerprint(base) == EventFingerprint(otherChild) {
		t.Fatalf("two children must not share a fingerprint")
	}
}

// --- P2c ------------------------------------------------------------------

// A corrupt ledger file must FAIL the export, not shrink it — otherwise the
// caller reports a reachable remote as holding nothing.
func TestIssue1948P2c_CorruptLedgerFileFailsTheExport(t *testing.T) {
	profile := "_test-1948-p2c"
	reviewTestHome(t, profile)

	if err := WriteLedgerEntry(CompletionLedgerEntry{
		ChildID: "worker-ok", Profile: profile, Status: "ok", FinishedAt: time.Now(),
	}); err != nil {
		t.Fatalf("ledger: %v", err)
	}
	dir, err := CompletionLedgerDir()
	if err != nil {
		t.Fatalf("ledger dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "worker-corrupt.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}

	records, err := ExportPendingRecords()
	if err == nil {
		t.Fatalf("an unreadable record file must fail the export, got %d records", len(records))
	}
	if !strings.Contains(err.Error(), "worker-corrupt.json") {
		t.Fatalf("the error must name the unreadable file, got: %v", err)
	}
}

// An empty (truncated) ledger file is unreadable too — it must not pass as a
// record-free host.
func TestIssue1948P2c_TruncatedLedgerFileFailsTheExport(t *testing.T) {
	profile := "_test-1948-p2c-empty"
	reviewTestHome(t, profile)

	dir, err := CompletionLedgerDir()
	if err != nil {
		t.Fatalf("ledger dir: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "worker-truncated.json"), nil, 0o644); err != nil {
		t.Fatalf("write empty: %v", err)
	}

	if _, err := ExportPendingRecords(); err == nil {
		t.Fatalf("a truncated ledger file must fail the export rather than drain as empty")
	}
}

// The same rule for an inbox file that cannot be read at all.
func TestIssue1948P2c_UnreadableInboxFailsTheExport(t *testing.T) {
	profile := "_test-1948-p2c-inbox"
	reviewTestHome(t, profile)

	if err := WriteInboxEvent("parent-x", TransitionNotificationEvent{
		ChildSessionID: "worker-x", Profile: profile,
		FromStatus: "running", ToStatus: "waiting", Timestamp: time.Now(),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	path := InboxPathFor("parent-x")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Skipf("cannot make a file unreadable here: %v", err)
	}
	defer func() { _ = os.Chmod(path, 0o644) }()
	if _, err := os.ReadFile(path); err == nil {
		t.Skip("this user ignores file permissions (root); the unreadable-file path is not exercisable")
	}

	if _, err := ExportPendingRecords(); err == nil {
		t.Fatalf("an unreadable inbox must fail the export rather than drain as empty")
	}
}
