package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// Issue #1186 + #1225: the daemon turns a worker-printed completion sentinel
// (persisted into the hook status file by the Stop-hook handler) into a
// distinct "finished" event committed to the parent's durable outbox. These
// tests pin the emit side: the finished event lands in the parent inbox with
// the parsed ok/fail outcome, and per-task idempotency (one record, last-wins).

// seedDoneParentChild creates a live conductor parent and a worker child in a
// fresh profile's storage, returning the profile and ids.
func seedDoneParentChild(t *testing.T, profile string) (parentID, childID string) {
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
	defer storage.Close()

	now := time.Now()
	parent := &Instance{
		ID:          "parent-conductor-1186",
		Title:       "conductor-1186",
		ProjectPath: "/tmp/p1186",
		GroupPath:   DefaultGroupPath,
		Tool:        "claude",
		Status:      StatusIdle,
		CreatedAt:   now,
	}
	child := &Instance{
		ID:              "child-worker-1186",
		Title:           "worker",
		ProjectPath:     "/tmp/c1186",
		GroupPath:       DefaultGroupPath,
		ParentSessionID: parent.ID,
		Tool:            "claude",
		Status:          StatusWaiting,
		CreatedAt:       now,
	}
	if err := storage.SaveWithGroups([]*Instance{parent, child}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	return parent.ID, child.ID
}

// TestNotifyFinished_EmitsDoneMessageToParent: after NotifyFinished, the
// finished event lands in the PARENT's durable inbox (issue #1225 pull model),
// carrying Kind=finished and DoneStatus=ok. No push, no fake sender.
func TestNotifyFinished_EmitsDoneMessageToParent(t *testing.T) {
	profile := "_test-1186-finished-ok"
	parentID, childID := seedDoneParentChild(t, profile)

	n := NewTransitionNotifier()
	t.Cleanup(n.Close)

	n.NotifyFinished(TransitionNotificationEvent{
		ChildSessionID: childID,
		ChildTitle:     "worker",
		Profile:        profile,
		DoneStatus:     "ok",
		DoneSummary:    "feature shipped",
		Timestamp:      time.Now(),
	})
	n.Flush()

	inbox := readInboxLines(t, parentID)
	if len(inbox) != 1 {
		t.Fatalf("expected exactly 1 finished record in parent inbox, got %d: %+v", len(inbox), inbox)
	}
	ev := inbox[0]
	if ev.TargetSessionID != parentID {
		t.Errorf("record targeted %q, want parent %q", ev.TargetSessionID, parentID)
	}
	if ev.ChildSessionID != childID {
		t.Errorf("record child %q, want %q", ev.ChildSessionID, childID)
	}
	if ev.Kind != transitionKindFinished {
		t.Errorf("record kind %q, want finished", ev.Kind)
	}
	if ev.DoneStatus != "ok" {
		t.Errorf("record done_status %q, want ok", ev.DoneStatus)
	}
	if ev.DoneSummary != "feature shipped" {
		t.Errorf("record done_summary %q, want %q", ev.DoneSummary, "feature shipped")
	}
}

// TestNotifyFinished_FailStatus: a finished event with DoneStatus "fail" lands
// in the parent inbox with the fail outcome preserved.
func TestNotifyFinished_FailStatus(t *testing.T) {
	profile := "_test-1186-finished-fail"
	parentID, childID := seedDoneParentChild(t, profile)

	n := NewTransitionNotifier()
	t.Cleanup(n.Close)

	n.NotifyFinished(TransitionNotificationEvent{
		ChildSessionID: childID,
		ChildTitle:     "worker",
		Profile:        profile,
		DoneStatus:     "fail",
		DoneSummary:    "build broke",
		Timestamp:      time.Now(),
	})
	n.Flush()

	inbox := readInboxLines(t, parentID)
	if len(inbox) != 1 {
		t.Fatalf("expected 1 finished record, got %d: %+v", len(inbox), inbox)
	}
	if inbox[0].DoneStatus != "fail" || inbox[0].DoneSummary != "build broke" {
		t.Errorf("fail outcome not reflected: status=%q summary=%q", inbox[0].DoneStatus, inbox[0].DoneSummary)
	}
}

// TestDaemon_EmitDoneSignals_HappyAndIdempotent: the daemon's done-signal emit
// commits to the parent inbox once; re-polling the SAME sentinel is idempotent
// (last-wins / turn_fingerprint keeps exactly one pending record), and a
// genuinely new completion produces a fresh record.
func TestDaemon_EmitDoneSignals_HappyAndIdempotent(t *testing.T) {
	profile := "_test-1186-daemon-idem"
	parentID, childID := seedDoneParentChild(t, profile)

	d := NewTransitionDaemon()
	t.Cleanup(d.notifier.Close)

	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer storage.Close()
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	byID := map[string]*Instance{}
	for _, inst := range instances {
		byID[inst.ID] = inst
	}

	hookStatuses := map[string]*HookStatus{
		childID: {
			Status:      "waiting",
			Event:       "Stop",
			DoneStatus:  "ok",
			DoneSummary: "first done",
			UpdatedAt:   time.Now(),
		},
	}

	// First pass commits the finished event to the parent inbox.
	d.emitDoneSignals(profile, byID, hookStatuses)
	d.notifier.Flush()
	if got := readInboxLines(t, parentID); len(got) != 1 {
		t.Fatalf("first emit: inbox has %d records, want 1", len(got))
	}

	// Second pass with the SAME sentinel must NOT add a duplicate record
	// (per-child last-wins: still exactly one pending record).
	d.emitDoneSignals(profile, byID, hookStatuses)
	d.notifier.Flush()
	if got := readInboxLines(t, parentID); len(got) != 1 {
		t.Fatalf("idempotency: inbox has %d records after re-poll of same sentinel, want 1", len(got))
	}

	// A genuinely new completion (different summary) remains alongside the first.
	hookStatuses[childID] = &HookStatus{
		Status:      "waiting",
		Event:       "Stop",
		DoneStatus:  "ok",
		DoneSummary: "second done",
		UpdatedAt:   time.Now(),
	}
	d.emitDoneSignals(profile, byID, hookStatuses)
	d.notifier.Flush()
	got := readInboxLines(t, parentID)
	if len(got) != 2 {
		t.Fatalf("new completion: inbox has %d records, want 2", len(got))
	}
	if got[1].DoneSummary != "second done" {
		t.Fatalf("new completion: pending record summary=%q, want %q", got[1].DoneSummary, "second done")
	}

	// And draining yields the new turn exactly once.
	drained, err := DrainInboxForParent(parentID)
	if err != nil {
		t.Fatalf("DrainInboxForParent: %v", err)
	}
	if len(drained) != 2 || drained[0].DoneSummary != "first done" || drained[1].DoneSummary != "second done" {
		t.Fatalf("drain yielded %+v, want both distinct completions", drained)
	}
}

func TestDaemon_EmitDoneSignals_NoSentinelNoEmit(t *testing.T) {
	profile := "_test-1186-daemon-nosentinel"
	parentID, childID := seedDoneParentChild(t, profile)

	d := NewTransitionDaemon()
	t.Cleanup(d.notifier.Close)

	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer storage.Close()
	instances, _, _ := storage.LoadWithGroups()
	byID := map[string]*Instance{}
	for _, inst := range instances {
		byID[inst.ID] = inst
	}

	// Ordinary mid-task Stop: hook status present but NO done fields.
	hookStatuses := map[string]*HookStatus{
		childID: {Status: "waiting", Event: "Stop", UpdatedAt: time.Now()},
	}
	d.emitDoneSignals(profile, byID, hookStatuses)
	d.notifier.Flush()
	if got := readInboxLines(t, parentID); len(got) != 0 {
		t.Fatalf("no sentinel must not commit a finished event; inbox has %d records", len(got))
	}
	_ = strings.TrimSpace // keep imports stable across edits
}

// --- Flush-race rescan (issue #1186, Stop hook outrunning the transcript flush) ---
//
// Claude Code can fire the Stop hook before appending the turn's final
// assistant record. The hook is synchronous (#1225) so it must not wait out
// the flush; it persists the transcript path into the hook status file and
// the daemon finishes the scan on its poll loop. These tests pin that retry:
// pending tail -> no emit (and no resolved marker, so the next poll retries);
// flushed sentinel -> exactly-once emit; stale or unsafe paths -> ignored.

// writePendingTranscript writes JSONL lines under the test HOME's ~/.claude
// (so the daemon's containment guard passes) and returns the path.
func writePendingTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("user home: %v", err)
	}
	dir := home + "/.claude/projects/p"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := dir + "/transcript.jsonl"
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

func appendTranscriptLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open transcript for append: %v", err)
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
	_ = f.Close()
}

func loadInstancesByID(t *testing.T, profile string) map[string]*Instance {
	t.Helper()
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer storage.Close()
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	byID := map[string]*Instance{}
	for _, inst := range instances {
		byID[inst.ID] = inst
	}
	return byID
}

// TestDaemon_FlushRaceRescan_EmitsAfterFlush: a pending hook status emits
// nothing while the tail is unflushed (and stays retryable), then emits the
// finished event exactly once after the sentinel turn lands in the file.
func TestDaemon_FlushRaceRescan_EmitsAfterFlush(t *testing.T) {
	profile := "_test-1186-rescan-flush"
	parentID, childID := seedDoneParentChild(t, profile)

	d := NewTransitionDaemon()
	t.Cleanup(d.notifier.Close)
	byID := loadInstancesByID(t, profile)

	path := writePendingTranscript(t,
		scanAssistantLine(t, "previous turn, no sentinel"),
		scanUserLine,
	)
	hs := &HookStatus{
		Status:         "waiting",
		Event:          "Stop",
		TranscriptPath: path,
		UpdatedAt:      time.Now(),
	}
	hookStatuses := map[string]*HookStatus{childID: hs}

	// Unflushed: no emit, and the scan must NOT be marked resolved so the
	// next poll retries.
	d.emitDoneSignals(profile, byID, hookStatuses)
	d.notifier.Flush()
	if got := readInboxLines(t, parentID); len(got) != 0 {
		t.Fatalf("pending tail must not emit; inbox has %d records", len(got))
	}
	if _, resolved := d.lastDoneScan[profile][childID]; resolved {
		t.Fatalf("unresolved scan must not be marked resolved (it would never retry)")
	}

	// The flush lands; the next poll emits exactly once.
	appendTranscriptLine(t, path, scanAssistantLine(t, "wrapped up\n===AGENTDECK_DONE=== status=ok summary=after the flush"))
	d.emitDoneSignals(profile, byID, hookStatuses)
	d.notifier.Flush()
	got := readInboxLines(t, parentID)
	if len(got) != 1 {
		t.Fatalf("expected 1 finished record after flush, got %d", len(got))
	}
	if got[0].Kind != transitionKindFinished || got[0].DoneStatus != "ok" || got[0].DoneSummary != "after the flush" {
		t.Fatalf("wrong record: %+v", got[0])
	}

	// Re-poll: resolved marker + lastDone dedup keep it at one record.
	d.emitDoneSignals(profile, byID, hookStatuses)
	d.notifier.Flush()
	if got := readInboxLines(t, parentID); len(got) != 1 {
		t.Fatalf("re-poll after resolution must not duplicate; inbox has %d records", len(got))
	}
}

// TestDaemon_FlushRaceRescan_NoSentinelResolvesQuiet: an ordinary turn (no
// sentinel) that flushes resolves the pending scan with no emission, and the
// resolved marker stops further transcript reads for that Stop edge.
func TestDaemon_FlushRaceRescan_NoSentinelResolvesQuiet(t *testing.T) {
	profile := "_test-1186-rescan-quiet"
	parentID, childID := seedDoneParentChild(t, profile)

	d := NewTransitionDaemon()
	t.Cleanup(d.notifier.Close)
	byID := loadInstancesByID(t, profile)

	path := writePendingTranscript(t,
		scanAssistantLine(t, "previous turn"),
		scanUserLine,
		scanAssistantLine(t, "just a normal answer, nothing to report"),
	)
	hs := &HookStatus{
		Status:         "waiting",
		Event:          "Stop",
		TranscriptPath: path,
		UpdatedAt:      time.Now(),
	}
	d.emitDoneSignals(profile, byID, map[string]*HookStatus{childID: hs})
	d.notifier.Flush()
	if got := readInboxLines(t, parentID); len(got) != 0 {
		t.Fatalf("sentinel-less turn must not emit; inbox has %d records", len(got))
	}
	resolved, ok := d.lastDoneScan[profile][childID]
	if !ok || !resolved.Equal(hs.UpdatedAt) {
		t.Fatalf("conclusive no-sentinel scan must be marked resolved at the hook timestamp; got %v ok=%v", resolved, ok)
	}
}

// TestDaemon_FlushRaceRescan_StaleHookIgnored: a pending hook status older
// than hookFreshWindow is never rescanned — the freshness window is the
// retry bound, same as the pre-existing done-fields path.
func TestDaemon_FlushRaceRescan_StaleHookIgnored(t *testing.T) {
	profile := "_test-1186-rescan-stale"
	parentID, childID := seedDoneParentChild(t, profile)

	d := NewTransitionDaemon()
	t.Cleanup(d.notifier.Close)
	byID := loadInstancesByID(t, profile)

	path := writePendingTranscript(t,
		scanAssistantLine(t, "late turn\n===AGENTDECK_DONE=== status=ok summary=too late"),
	)
	hs := &HookStatus{
		Status:         "waiting",
		Event:          "Stop",
		TranscriptPath: path,
		UpdatedAt:      time.Now().Add(-2 * hookFreshWindow),
	}
	d.emitDoneSignals(profile, byID, map[string]*HookStatus{childID: hs})
	d.notifier.Flush()
	if got := readInboxLines(t, parentID); len(got) != 0 {
		t.Fatalf("stale pending hook must not emit; inbox has %d records", len(got))
	}
}

// TestDaemon_FlushRaceRescan_PathOutsideClaudeRejected: the daemon re-applies
// the ~/.claude containment guard before reading a path it found in a hook
// status file — a sentinel-bearing file elsewhere on disk must not emit.
func TestDaemon_FlushRaceRescan_PathOutsideClaudeRejected(t *testing.T) {
	profile := "_test-1186-rescan-guard"
	parentID, childID := seedDoneParentChild(t, profile)

	d := NewTransitionDaemon()
	t.Cleanup(d.notifier.Close)
	byID := loadInstancesByID(t, profile)

	outside := t.TempDir() + "/transcript.jsonl"
	if err := os.WriteFile(outside, []byte(scanAssistantLine(t, "===AGENTDECK_DONE=== status=ok summary=spoof")+"\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	hs := &HookStatus{
		Status:         "waiting",
		Event:          "Stop",
		TranscriptPath: outside,
		UpdatedAt:      time.Now(),
	}
	d.emitDoneSignals(profile, byID, map[string]*HookStatus{childID: hs})
	d.notifier.Flush()
	if got := readInboxLines(t, parentID); len(got) != 0 {
		t.Fatalf("out-of-containment path must not emit; inbox has %d records", len(got))
	}
	if _, resolved := d.lastDoneScan[profile][childID]; !resolved {
		t.Fatalf("rejected path should be marked resolved so the daemon does not retry it")
	}
}

// --- Notifications off: the completion is still recorded ---
//
// A child with NoTransitionNotify (or a host with [notifications]
// transition_events = false) must not message its parent, but its sentinel
// is still a completion: it goes to the ledger that `session children` reads,
// and the suppression is logged once instead of vanishing.

func TestDaemon_EmitDoneSignals_NoNotifyChildStillRecorded(t *testing.T) {
	assertDoneRecordedNotDelivered(t, "_test-1186-done-no-notify", "no_notify", func(byID map[string]*Instance, childID string) {
		byID[childID].NoTransitionNotify = true
	})
}

func TestDaemon_EmitDoneSignals_TransitionEventsOffStillRecorded(t *testing.T) {
	assertDoneRecordedNotDelivered(t, "_test-1186-done-events-off", "transition_events_disabled", func(map[string]*Instance, string) {
		path, err := GetUserConfigPath()
		if err != nil {
			t.Fatalf("config path: %v", err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir config dir: %v", err)
		}
		if err := os.WriteFile(path, []byte("[notifications]\ntransition_events = false\n"), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		ClearUserConfigCache()
		if GetNotificationsSettings().GetTransitionEventsEnabled() {
			t.Fatal("transition_events = false was not picked up")
		}
	})
}

func assertDoneRecordedNotDelivered(t *testing.T, profile, wantReason string, mute func(byID map[string]*Instance, childID string)) {
	t.Helper()
	parentID, childID := seedDoneParentChild(t, profile)
	logDir := t.TempDir()
	logging.Shutdown()
	logging.Init(logging.Config{Debug: true, LogDir: logDir, Level: "info"})
	t.Cleanup(logging.Shutdown)

	byID := loadInstancesByID(t, profile)
	mute(byID, childID)
	hs := &HookStatus{Status: "waiting", Event: "Stop", DoneStatus: "ok", DoneSummary: "muted but done", UpdatedAt: time.Now()}
	hookStatuses := map[string]*HookStatus{childID: hs}

	d := NewTransitionDaemon()
	t.Cleanup(d.notifier.Close)
	d.emitDoneSignals(profile, byID, hookStatuses)
	d.emitDoneSignals(profile, byID, hookStatuses) // re-read on the next poll
	d.notifier.Flush()

	// A restarted daemon (empty lastDone) re-reading the still-fresh file
	// recognises the same completion from the ledger.
	d2 := NewTransitionDaemon()
	t.Cleanup(d2.notifier.Close)
	d2.emitDoneSignals(profile, byID, hookStatuses)
	d2.notifier.Flush()

	entry, ok := ReadLedgerEntry(childID)
	if !ok {
		t.Fatal("muted child's completion missing from the ledger")
	}
	if !ledgerSuppressedFlag(t, childID) {
		t.Fatalf("muted ledger entry must be marked suppressed")
	}
	// Never delivered, so never exported, although the stored child (or,
	// for the global switch, the export's registry check) does not opt out.
	assertNotExported(t, profile, childID)
	if entry.Status != "ok" || entry.Summary != "muted but done" || !entry.FinishedAt.Equal(hs.UpdatedAt) || entry.Profile != profile {
		t.Fatalf("ledger entry = %+v, want ok / %q at %v", entry, "muted but done", hs.UpdatedAt)
	}
	if got := readInboxLines(t, parentID); len(got) != 0 {
		t.Fatalf("muted completion reached the parent inbox: %+v", got)
	}
	if dl, err := ListDeadLetters(); err != nil || len(dl) != 0 {
		t.Fatalf("muted completion must not dead-letter: %+v (err %v)", dl, err)
	}

	var lines []map[string]any
	for _, r := range readLogRecords(t, logDir) {
		if r["msg"] == "done_suppressed" {
			lines = append(lines, r)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("want exactly 1 done_suppressed log line across polls and a restart, got %d: %+v", len(lines), lines)
	}
	if lines[0]["child"] != childID || lines[0]["profile"] != profile || lines[0]["reason"] != wantReason || lines[0]["status"] != "ok" {
		t.Fatalf("done_suppressed fields = %+v, want child=%s profile=%s reason=%s status=ok", lines[0], childID, profile, wantReason)
	}

	// The same sentinel on a later Stop is a new completion: a new record
	// with a fresh done_at and a new line.
	second := hs.UpdatedAt.Add(time.Second)
	hookStatuses[childID] = &HookStatus{Status: "waiting", Event: "Stop", DoneStatus: "ok", DoneSummary: "muted but done", UpdatedAt: second}
	d.emitDoneSignals(profile, byID, hookStatuses)
	d.notifier.Flush()
	if entry, _ := ReadLedgerEntry(childID); !entry.FinishedAt.Equal(second) {
		t.Fatalf("identical completion on a new Stop not recorded with a fresh done_at: %+v", entry)
	}
	n := 0
	for _, r := range readLogRecords(t, logDir) {
		if r["msg"] == "done_suppressed" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("want 2 done_suppressed lines after a second completion, got %d", n)
	}
	if got := readInboxLines(t, parentID); len(got) != 0 {
		t.Fatalf("second muted completion reached the parent inbox: %+v", got)
	}
}

// A completion recorded while muted was never delivered, so it must not make
// an identical completion after notifications are turned back on look like a
// repeat. Re-reading the same Stop edge after unmuting is still that recorded
// completion and is not delivered.
func TestDaemon_EmitDoneSignals_UnmutedIdenticalCompletionDelivered(t *testing.T) {
	profile := "_test-1186-done-unmute"
	parentID, childID := seedDoneParentChild(t, profile)
	byID := loadInstancesByID(t, profile)
	d := NewTransitionDaemon()
	t.Cleanup(d.notifier.Close)

	first := time.Now()
	muted := map[string]*HookStatus{childID: {Status: "waiting", Event: "Stop", DoneStatus: "ok", DoneSummary: "same result", UpdatedAt: first}}
	byID[childID].NoTransitionNotify = true
	d.emitDoneSignals(profile, byID, muted)
	d.notifier.Flush()
	if _, ok := ReadLedgerEntry(childID); !ok {
		t.Fatal("muted completion not recorded")
	}
	if !ledgerSuppressedFlag(t, childID) {
		t.Fatal("muted ledger entry must be marked suppressed")
	}

	byID[childID].NoTransitionNotify = false
	assertNotExported(t, profile, childID)
	d.emitDoneSignals(profile, byID, muted)
	d.notifier.Flush()
	if got := readInboxLines(t, parentID); len(got) != 0 {
		t.Fatalf("the muted Stop edge re-read after unmuting must not be delivered: %+v", got)
	}

	again := map[string]*HookStatus{childID: {Status: "waiting", Event: "Stop", DoneStatus: "ok", DoneSummary: "same result", UpdatedAt: first.Add(time.Minute)}}
	d.emitDoneSignals(profile, byID, again)
	d.notifier.Flush()
	got := readInboxLines(t, parentID)
	if len(got) != 1 || got[0].Kind != transitionKindFinished || got[0].DoneSummary != "same result" {
		t.Fatalf("identical completion after unmuting must be delivered once, got %+v", got)
	}
	entry, _ := ReadLedgerEntry(childID)
	if ledgerSuppressedFlag(t, childID) {
		t.Fatal("a delivered completion must clear the suppressed mark")
	}
	if !entry.FinishedAt.Equal(first.Add(time.Minute)) || entry.Repeats != 0 {
		t.Fatalf("delivered completion must overwrite the muted entry without a repeat count: %+v", entry)
	}
}

// ledgerSuppressedFlag reads the child's ledger file as raw JSON and reports
// its "suppressed" field (raw so the test compiles against older code).
func ledgerSuppressedFlag(t *testing.T, childID string) bool {
	t.Helper()
	path, err := completionLedgerPath(childID)
	if err != nil {
		t.Fatalf("ledger path: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse ledger: %v", err)
	}
	return m["suppressed"] == true
}

func writeRawLedger(t *testing.T, childID string, fields map[string]any) {
	t.Helper()
	path, err := completionLedgerPath(childID)
	if err != nil {
		t.Fatalf("ledger path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir ledger: %v", err)
	}
	raw, _ := json.Marshal(fields)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write ledger: %v", err)
	}
}

func assertNotExported(t *testing.T, profile, childID string) {
	t.Helper()
	records, err := ExportPendingRecords(profile)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	for _, r := range records {
		if r.ChildSessionID == childID {
			t.Fatalf("a completion recorded while muted must not be exported: %+v", r)
		}
	}
}

func countDoneSuppressed(t *testing.T, logDir string) int {
	t.Helper()
	n := 0
	for _, r := range readLogRecords(t, logDir) {
		if r["msg"] == "done_suppressed" {
			n++
		}
	}
	return n
}

// A child muted while the hook file of an already delivered completion is
// still fresh: the delivered ledger entry stands (no rewrite as suppressed,
// no done_suppressed line). Covers the hook-path delivery (entry time is the
// Stop edge) and an emitTurn delivery (entry time is later than the edge).
func TestDaemon_EmitDoneSignals_MutedAfterDeliveryLeavesEntry(t *testing.T) {
	for _, tc := range []struct {
		name    string
		deliver func(t *testing.T, d *TransitionDaemon, profile, childID string, byID map[string]*Instance, hs *HookStatus)
	}{
		{"hook path", func(t *testing.T, d *TransitionDaemon, profile, childID string, byID map[string]*Instance, hs *HookStatus) {
			d.emitDoneSignals(profile, byID, map[string]*HookStatus{childID: hs})
			d.notifier.Flush()
		}},
		{"emitTurn", func(t *testing.T, _ *TransitionDaemon, profile, childID string, _ map[string]*Instance, hs *HookStatus) {
			writeRawLedger(t, childID, map[string]any{"child_id": childID, "profile": profile, "status": "ok", "summary": "delivered once",
				"finished_at": hs.UpdatedAt.Add(2 * time.Second), "turn_uuid": "turn-1"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := "_test-1186-done-mute-after"
			_, childID := seedDoneParentChild(t, profile)
			logDir := t.TempDir()
			logging.Shutdown()
			logging.Init(logging.Config{Debug: true, LogDir: logDir, Level: "info"})
			t.Cleanup(logging.Shutdown)
			byID := loadInstancesByID(t, profile)
			d := NewTransitionDaemon()
			t.Cleanup(d.notifier.Close)
			hs := &HookStatus{Status: "waiting", Event: "Stop", DoneStatus: "ok", DoneSummary: "delivered once", UpdatedAt: time.Now()}

			tc.deliver(t, d, profile, childID, byID, hs)
			path, _ := completionLedgerPath(childID)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("delivered entry missing: %v", err)
			}

			byID[childID].NoTransitionNotify = true
			d.emitDoneSignals(profile, byID, map[string]*HookStatus{childID: hs})
			d.notifier.Flush()
			after, _ := os.ReadFile(path)
			if string(after) != string(before) {
				t.Fatalf("muting must not rewrite the delivered entry:\nbefore %s\nafter  %s", before, after)
			}
			if n := countDoneSuppressed(t, logDir); n != 0 {
				t.Fatalf("muting after delivery logged %d done_suppressed lines, want 0", n)
			}
		})
	}
}

// A failed ledger write leaves the muted completion unhandled, so the next
// poll records it and logs it then.
func TestDaemon_EmitDoneSignals_MutedLedgerWriteFailureRetried(t *testing.T) {
	profile := "_test-1186-done-mute-retry"
	_, childID := seedDoneParentChild(t, profile)
	logDir := t.TempDir()
	logging.Shutdown()
	logging.Init(logging.Config{Debug: true, LogDir: logDir, Level: "info"})
	t.Cleanup(logging.Shutdown)
	byID := loadInstancesByID(t, profile)
	byID[childID].NoTransitionNotify = true
	d := NewTransitionDaemon()
	t.Cleanup(d.notifier.Close)
	hookStatuses := map[string]*HookStatus{childID: {Status: "waiting", Event: "Stop", DoneStatus: "ok", DoneSummary: "retry me", UpdatedAt: time.Now()}}

	dir, err := CompletionLedgerDir()
	if err != nil {
		t.Fatalf("ledger dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("block ledger dir: %v", err)
	}
	d.emitDoneSignals(profile, byID, hookStatuses)
	if _, ok := ReadLedgerEntry(childID); ok {
		t.Fatal("ledger write was expected to fail")
	}
	if n := countDoneSuppressed(t, logDir); n != 0 {
		t.Fatalf("a failed write must not log done_suppressed, got %d", n)
	}

	if err := os.Remove(dir); err != nil {
		t.Fatalf("unblock ledger dir: %v", err)
	}
	d.emitDoneSignals(profile, byID, hookStatuses)
	if entry, ok := ReadLedgerEntry(childID); !ok || entry.Summary != "retry me" {
		t.Fatalf("the next poll must record the completion after a failed write: %+v ok=%v", entry, ok)
	}
	if n := countDoneSuppressed(t, logDir); n != 1 {
		t.Fatalf("want 1 done_suppressed line after the retry, got %d", n)
	}
}

// checkDoneRepeat ignores a suppressed entry outright, the same Stop edge
// included: it was never delivered.
func TestCheckDoneRepeat_IgnoresSuppressedEntry(t *testing.T) {
	exportTestHome(t)
	at := time.Now().Truncate(time.Second)
	writeRawLedger(t, "child-sup", map[string]any{"child_id": "child-sup", "profile": "p", "status": "ok", "summary": "s",
		"finished_at": at, "suppressed": true})
	if repeat, counted := checkDoneRepeat("child-sup", "p", DoneSignal{Status: "ok", Summary: "s"}, "", true, true, at); repeat || counted {
		t.Fatalf("suppressed entry treated as delivered: repeat=%v counted=%v", repeat, counted)
	}
}
