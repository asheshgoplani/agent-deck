package session

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Bug 1 / Layer 1 — fingerprint dedup.
//
// Issue #824 reproduced: the same logical event was persisted via
// WriteInboxEvent and logMissed repeatedly, producing 13 duplicate inbox JSONL
// lines and 7 duplicate notifier-missed entries within a few seconds. The fix
// fingerprints by sha256(child_id|from|to|timestamp.UnixNano()) and skips the
// write when the same fingerprint has already been persisted.

// TestDedup_InboxSameFingerprintOnce calls WriteInboxEvent twice with the
// same event (identical child, from, to, timestamp). The inbox must contain
// exactly one JSONL line, not two.
func TestDedup_InboxSameFingerprintOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	t.Cleanup(func() {
		ClearUserConfigCache()
		ResetInboxFingerprintCacheForTest()
	})
	ResetInboxFingerprintCacheForTest()

	parent := "parent-dedup"
	ts := time.Unix(1700000000, 0).UTC()
	ev := TransitionNotificationEvent{
		ChildSessionID:  "child-dup",
		ChildTitle:      "worker",
		Profile:         "_test",
		FromStatus:      "running",
		ToStatus:        "waiting",
		Timestamp:       ts,
		TargetSessionID: parent,
		TargetKind:      "parent",
	}

	for i := 0; i < 5; i++ {
		if err := WriteInboxEvent(parent, ev); err != nil {
			t.Fatalf("WriteInboxEvent #%d: %v", i, err)
		}
	}

	got, err := ReadAndTruncateInbox(parent)
	if err != nil {
		t.Fatalf("ReadAndTruncateInbox: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("inbox dedup: expected 1 event after 5 writes of same fingerprint, got %d", len(got))
	}

	// A different timestamp must NOT dedup — that's a different logical event.
	ev2 := ev
	ev2.Timestamp = ts.Add(1 * time.Second)
	if err := WriteInboxEvent(parent, ev); err != nil {
		t.Fatalf("re-write same fp: %v", err)
	}
	if err := WriteInboxEvent(parent, ev2); err != nil {
		t.Fatalf("write distinct fp: %v", err)
	}
	got, err = ReadAndTruncateInbox(parent)
	if err != nil {
		t.Fatalf("ReadAndTruncateInbox 2: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 1 (post-truncate ev) + 1 (ev2 distinct) = 2, got %d", len(got))
	}
}

// TestDedup_InboxFingerprintSurvivesProcessRestart guards against the case
// where the process restarts between writes. The in-memory dedup cache is
// gone, but the on-disk JSONL still carries the fingerprint, so a second
// write of the same event must still be a no-op via file-scan recovery.
func TestDedup_InboxFingerprintSurvivesProcessRestart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	t.Cleanup(func() {
		ClearUserConfigCache()
		ResetInboxFingerprintCacheForTest()
	})
	ResetInboxFingerprintCacheForTest()

	parent := "parent-dedup-restart"
	ts := time.Unix(1700000100, 0).UTC()
	ev := TransitionNotificationEvent{
		ChildSessionID:  "child-restart",
		ChildTitle:      "worker",
		Profile:         "_test",
		FromStatus:      "running",
		ToStatus:        "waiting",
		Timestamp:       ts,
		TargetSessionID: parent,
		TargetKind:      "parent",
	}

	if err := WriteInboxEvent(parent, ev); err != nil {
		t.Fatalf("first write: %v", err)
	}

	// Simulate process restart: drop in-memory cache; the on-disk file still
	// holds the fingerprint and must be the source of truth for dedup.
	ResetInboxFingerprintCacheForTest()

	if err := WriteInboxEvent(parent, ev); err != nil {
		t.Fatalf("second write: %v", err)
	}

	got, err := ReadAndTruncateInbox(parent)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("file-scan dedup: expected 1 line across simulated restart, got %d", len(got))
	}
}

// TestDedup_MissedLogSameFingerprintOnce: the same logical event logged to
// notifier-missed.log repeatedly must produce exactly one line per
// (fingerprint, reason) pair, not seven.
func TestDedup_MissedLogSameFingerprintOnce(t *testing.T) {
	dir := t.TempDir()
	n := &TransitionNotifier{
		statePath:  filepath.Join(dir, "state.json"),
		logPath:    filepath.Join(dir, "transition-notifier.log"),
		missedPath: filepath.Join(dir, "notifier-missed.log"),
		orphanPath: filepath.Join(dir, "notifier-orphans.log"),
		state: transitionNotifyState{
			Records: map[string]transitionNotifyRecord{},
		},
	}

	ts := time.Unix(1700000200, 0).UTC()
	ev := TransitionNotificationEvent{
		ChildSessionID:  "child-missed",
		ChildTitle:      "worker",
		Profile:         "_test",
		FromStatus:      "running",
		ToStatus:        "waiting",
		Timestamp:       ts,
		TargetSessionID: "parent-missed",
		TargetKind:      "parent",
	}

	for i := 0; i < 7; i++ {
		n.logMissed(ev, "exhausted_busy_retries")
	}

	data, err := os.ReadFile(n.missedPath)
	if err != nil {
		t.Fatalf("read missed log: %v", err)
	}
	lines := countNonBlankLines(string(data))
	if lines != 1 {
		t.Fatalf("missed log dedup: expected 1 line for repeated (fp, reason), got %d (data=%q)", lines, data)
	}

	// A different reason for the same fingerprint must still record once,
	// because the operator-actionable signal is (event, reason), not event
	// alone.
	n.logMissed(ev, "expired")
	n.logMissed(ev, "expired")
	data, _ = os.ReadFile(n.missedPath)
	lines = countNonBlankLines(string(data))
	if lines != 2 {
		t.Fatalf("missed log: expected 2 lines (one per distinct reason), got %d", lines)
	}
}

func countNonBlankLines(s string) int {
	n := 0
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) != "" {
			n++
		}
	}
	return n
}

// Bug 2 / Layer 2 — top-level conductor self-suppress.
//
// PR #807's parent-resolution check only catches the case where the loaded
// child's parent_session_id equals its own id. The real top-level
// case in production is `parent_session_id = ""` AND the loaded Instance's
// title starts with `conductor-`. That child must self-suppress without an
// orphan WARN, since it isn't an orphan — it's the root.

// TestSelfSuppress_TopLevelConductorWithEmptyParent: a real top-level
// conductor (empty parent, conductor- prefix on the loaded instance title)
// must drop without writing to notifier-orphans.log.
func TestSelfSuppress_TopLevelConductorWithEmptyParent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })
	if err := os.MkdirAll(home+"/.agent-deck", 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	profile := "_test-self-suppress-empty"
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	defer storage.Close()

	now := time.Now()
	conductor := &Instance{
		ID:              "conductor-empty-parent",
		Title:           "conductor-agent-deck",
		ProjectPath:     "/tmp/conductor",
		GroupPath:       DefaultGroupPath,
		ParentSessionID: "", // top-level — empty, NOT self-pointing
		Tool:            "claude",
		Status:          StatusWaiting,
		CreatedAt:       now,
	}
	if err := storage.SaveWithGroups([]*Instance{conductor}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	n := NewTransitionNotifier()

	// Critically: ChildTitle on the event is intentionally EMPTY here, so the
	// outer line-211 title-prefix check is bypassed. The fix must still
	// recognize the loaded Instance as a top-level conductor.
	ev := TransitionNotificationEvent{
		ChildSessionID: conductor.ID,
		ChildTitle:     "", // bypasses outer isConductorSessionTitle check
		Profile:        profile,
		FromStatus:     "running",
		ToStatus:       "waiting",
		Timestamp:      now,
	}
	result := n.NotifyTransition(ev)
	n.Flush()

	if result.DeliveryResult != transitionDeliveryDropped {
		t.Fatalf("top-level conductor (empty parent) must self-suppress with dropped, got %q", result.DeliveryResult)
	}

	// The crucial regression: orphan log must NOT be written. A top-level
	// conductor is not an orphan; logging it as one floods notifier-orphans
	// with non-actionable noise (and, in production, made the operator chase
	// a non-existent linkage problem).
	orphanData, err := os.ReadFile(transitionNotifierOrphanLogPath())
	if err == nil && strings.Contains(string(orphanData), conductor.ID) {
		t.Fatalf("top-level conductor must NOT be logged as orphan, got: %s", orphanData)
	}
}

// TestSelfSuppress_TopLevelConductorWithParentMatchingSelf preserves the
// existing behavior from #807 for the case where parent_session_id literally
// points at the child's own id (which historically is how some conductors
// self-link to fake a parent edge).
func TestSelfSuppress_TopLevelConductorWithParentMatchingSelf(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })
	if err := os.MkdirAll(home+"/.agent-deck", 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	profile := "_test-self-suppress-self"
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	defer storage.Close()

	now := time.Now()
	conductor := &Instance{
		ID:              "conductor-self-pointer",
		Title:           "conductor-agent-deck",
		ProjectPath:     "/tmp/conductor",
		GroupPath:       DefaultGroupPath,
		ParentSessionID: "conductor-self-pointer", // points at itself
		Tool:            "claude",
		Status:          StatusWaiting,
		CreatedAt:       now,
	}
	if err := storage.SaveWithGroups([]*Instance{conductor}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	n := NewTransitionNotifier()

	ev := TransitionNotificationEvent{
		ChildSessionID: conductor.ID,
		ChildTitle:     "", // bypasses outer line-211 check
		Profile:        profile,
		FromStatus:     "running",
		ToStatus:       "waiting",
		Timestamp:      now,
	}
	result := n.NotifyTransition(ev)
	n.Flush()

	if result.DeliveryResult != transitionDeliveryDropped {
		t.Fatalf("self-pointing conductor must drop, got %q", result.DeliveryResult)
	}
	orphanData, err := os.ReadFile(transitionNotifierOrphanLogPath())
	if err == nil && strings.Contains(string(orphanData), conductor.ID) {
		t.Fatalf("self-pointing conductor must NOT be logged as orphan, got: %s", orphanData)
	}
}
