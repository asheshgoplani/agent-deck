package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIssue2481R2_DigestEscalationKeepsCount(t *testing.T) {
	inboxTestHome(t)
	const parent, child = "digest-parent", "digest-child"
	fillPendingTurns(t, parent, child, maxPendingTurnsPerChild)
	var digest TransitionNotificationEvent
	var newest TransitionNotificationEvent
	for i := 0; i < 3; i++ {
		newest = TransitionNotificationEvent{ChildSessionID: child, TurnUUID: fmt.Sprintf("turn-%d", i), Tier: TurnTierInfo, Text: fmt.Sprintf("info %d", i), TurnFingerprint: fmt.Sprintf("fp-%d", i)}
		var err error
		digest, _, err = commitToInbox(parent, newest)
		if err != nil {
			t.Fatal(err)
		}
	}
	originalFP := digest.TurnFingerprint
	newest.Tier, newest.Text, newest.TurnFingerprint = TurnTierUrgent, "failed", "escalated-fp"
	for i := 0; i < 2; i++ {
		stored, outcome, err := commitToInbox(parent, newest)
		if err != nil {
			t.Fatal(err)
		}
		wantOutcome := inboxCommitDigested
		if i == 1 {
			wantOutcome = inboxCommitUnchanged
		}
		if stored.OverflowTurns != 3 || stored.TurnFingerprint != originalFP || stored.Tier != TurnTierUrgent || stored.Text != "failed" || outcome != wantOutcome {
			t.Fatalf("escalation/retry lost digest: stored=%+v outcome=%v", stored, outcome)
		}
		if !digestHoldsTurn(stored, "fp-1") || !digestHoldsTurn(stored, newest.TurnFingerprint) {
			t.Fatalf("lost dedup history: %+v", stored)
		}
	}
	if got := len(rawInboxRecords(t, parent)); got != 65 {
		t.Fatalf("pending=%d want 65", got)
	}
}

func TestIssue2481R2_DigestWakeGating(t *testing.T) {
	for _, tier := range []string{TurnTierInfo, TurnTierUrgent} {
		t.Run(tier, func(t *testing.T) {
			n, parent, event := newWakeNudgeFixture(t)
			sent := 0
			n.wake = &wakeNudgeWiring{nudger: NewWakeNudger(0), now: func() time.Time { return time.Unix(1000, 0) }, isIdle: func(*Instance, string) bool { return true }, send: func(*Instance, string, string) error { sent++; return nil }}
			for i := 0; i < 65; i++ {
				ev := event
				ev.Tier, ev.TurnUUID, ev.TurnFingerprint = TurnTierInfo, fmt.Sprintf("turn-%d", i), fmt.Sprintf("fp-%d", i)
				if i == 64 {
					ev.Tier = tier
				}
				committed, transient, reason := n.commitEventToInbox(ev)
				if !committed || transient {
					t.Fatalf("commit %d: %v %v %s", i, committed, transient, reason)
				}
			}
			want := 0
			if tier == TurnTierUrgent {
				want = 1
			}
			t.Logf("64 pending info + %s: wakes=%d", tier, sent)
			if sent != want {
				t.Fatalf("wakes=%d want %d", sent, want)
			}
			st, err := ReadInboxStats(parent)
			if err != nil {
				t.Fatal(err)
			}
			if st.WakeupsUrgent != int64(want) || st.WakeupsSuppressed != int64(65-want) {
				t.Fatalf("wake stats: %+v", st)
			}
		})
	}
}

func TestIssue2481R2_FoldedReplyWakesSender(t *testing.T) {
	n, parent, ev := newWakeNudgeFixture(t)
	fillPendingTurns(t, parent, ev.ChildSessionID, maxPendingTurnsPerChild)
	sender := &Instance{ID: parent, Title: "asker", Tool: "claude", Status: StatusIdle}
	sent := 0
	n.wake = &wakeNudgeWiring{nudger: NewWakeNudger(time.Minute), now: func() time.Time { return time.Unix(1000, 0) }, isIdle: func(*Instance, string) bool { return true }, send: func(*Instance, string, string) error { sent++; return nil }}
	ev.TurnUUID, ev.TurnFingerprint = "reply-turn", "reply-fp"
	n.commitReplyToSender(sender, ev)
	n.commitReplyToSender(sender, ev)
	if sent != 1 {
		t.Fatalf("folded reply + retry wakes=%d want 1", sent)
	}
	if got := len(readTransitionLogTitles(t, n.logPath)); got != 1 {
		t.Fatalf("reply log lines=%d want 1", got)
	}
}

func TestIssue2481R2_RotationFailureKeepsRecords(t *testing.T) {
	for _, failure := range []string{"rename", "lock"} {
		t.Run(failure, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "events.log")
			blocker := path + ".1/x"
			if failure == "lock" {
				blocker = path + ".lock"
			}
			if err := os.MkdirAll(blocker, 0700); err != nil {
				t.Fatal(err)
			}
			failures := 0
			for i := 0; i < 10; i++ {
				if err := appendRotatingLogLine(path, []byte(fmt.Sprintf(`{"i":%d,"pad":"0123456789"}`, i)), logRotation{MaxBytes: 64, Keep: 1}); err != nil {
					failures++
				}
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Count(string(data), "\n")
			t.Logf("rotation %s failure: records=%d/10 append errors=%d", failure, lines, failures)
			if lines != 10 || failures != 0 {
				t.Fatalf("lost records: lines=%d errors=%d", lines, failures)
			}
		})
	}
}

func TestIssue2481R2_ResetStatsTakesFileLock(t *testing.T) {
	inboxTestHome(t)
	if err := BumpInboxStats("parent", func(s *InboxStats) { s.RecordsInfo++ }); err != nil {
		t.Fatal(err)
	}
	before := ConfigLockAcquisitionsForTest()
	if err := ResetInboxStats("parent"); err != nil {
		t.Fatal(err)
	}
	if got := ConfigLockAcquisitionsForTest() - before; got != 1 {
		t.Fatalf("reset file locks=%d want 1", got)
	}
	st, err := ReadInboxStats("parent")
	if err != nil || st.RecordsInfo != 0 {
		t.Fatalf("after reset=%+v err=%v", st, err)
	}
}

func TestIssue2481R2_DigestLogsOriginalFingerprints(t *testing.T) {
	n, parent, ev := newWakeNudgeFixture(t)
	fillPendingTurns(t, parent, ev.ChildSessionID, maxPendingTurnsPerChild)
	for i := 0; i < 3; i++ {
		ev.TurnUUID, ev.TurnFingerprint = fmt.Sprintf("turn-%d", i), fmt.Sprintf("fp-%d", i)
		if committed, _, _ := n.commitEventToInbox(ev); !committed {
			t.Fatal("not committed")
		}
	}
	data, err := os.ReadFile(n.logPath)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		fp := fmt.Sprintf(`"turn_fingerprint":"fp-%d"`, i)
		if got := strings.Count(string(data), fp); got != 1 {
			t.Fatalf("%s logged %d times, want 1", fp, got)
		}
	}
}

// The daemon counts successful commits, including turns stored in the digest;
// re-observing an already journaled turn must not count or wake twice.
func TestIssue2481R2_DigestCommittedRecordStats(t *testing.T) {
	f := newTurnTestFixture(t)
	fillPendingTurns(t, f.parent.ID, f.child.ID, maxPendingTurnsPerChild)
	f.appendTurn(t, fxHuman("u1", "status?"), fxAssistantText("a1", "Blocked on the schema. Should I drop the old column?"))
	for i := 0; i < 2; i++ {
		f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
	}
	st, err := ReadInboxStats(f.parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.RecordsUrgent != 1 || st.RecordsInfo != 0 {
		t.Fatalf("digest committed stats=%+v", st)
	}
	recs := f.inboxRecords(t)
	if len(recs) != 65 || recs[len(recs)-1].OverflowTurns != 1 {
		t.Fatalf("digest missing: %+v", recs)
	}
	if *f.sends != 1 {
		t.Fatalf("digest wake count=%d want 1", *f.sends)
	}
}

func TestIssue2481R2_DiagnosticLogsRotate(t *testing.T) {
	inboxTestHome(t)
	n := NewTransitionNotifier()
	ev := logTestEvent(1)
	for _, tc := range []struct {
		name, path string
		write      func()
	}{
		{"missed", n.missedPath, func() { n.logMissed(ev, "test") }},
		{"dead-letter", n.missedPath, func() { NewDeadLetterSink(n.missedPath).writeMissedOnce(ev) }},
		{"orphan", n.orphanPath, func() { n.logOrphanOnce(ev, ev.ChildSessionID) }},
		{"probe", notifierProbeStallLogPath(), func() { (&TransitionDaemon{}).logProbeStall("test", "child", "timeout") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.MkdirAll(filepath.Dir(tc.path), 0700); err != nil {
				t.Fatal(err)
			}
			f, err := os.Create(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.Truncate(transitionLogRotation.MaxBytes); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			tc.write()
			info, err := os.Stat(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Size() == 0 || info.Size() > transitionLogRotation.MaxBytes {
				t.Fatalf("active size=%d", info.Size())
			}
			if _, err := os.Stat(tc.path + ".1"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIssue2481R2_AutoUpdateLogRotatesAfterRun(t *testing.T) {
	inboxTestHome(t)
	path, err := logDataPath("auto-update.log")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(transitionLogRotation.MaxBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := RotateAutoUpdateLog(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatal(err)
	}
	// A new run opens the original pathname, leaving the archived output intact.
	if err := appendLogLine(path, []byte("next run")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "next run\n" {
		t.Fatalf("next run %q err=%v", data, err)
	}
}

func TestIssue2481R2_DigestEscalationWakesThroughDaemon(t *testing.T) {
	f := newTurnTestFixture(t)
	fillPendingTurns(t, f.parent.ID, f.child.ID, maxPendingTurnsPerChild)
	f.appendTurn(t, fxHuman("u1", "run it"), fxAssistantText("a1", "Running the migration now."))
	f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), false)
	if *f.sends != 0 {
		t.Fatalf("info woke parent %d times", *f.sends)
	}
	f.d.emitTurn("default", f.child, f.byID, "running", "error", time.Now(), false)
	recs := f.inboxRecords(t)
	if len(recs) != 65 || recs[64].OverflowTurns != 1 || recs[64].Tier != TurnTierUrgent {
		t.Fatalf("escalated digest: %+v", recs)
	}
	if *f.sends != 1 {
		t.Fatalf("escalation wakes=%d want 1", *f.sends)
	}
	st, err := ReadInboxStats(f.parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Both the original info and its urgent upgrade durably committed. These
	// counters count commits by tier, not the current number of inbox rows.
	if st.RecordsInfo != 1 || st.RecordsUrgent != 1 {
		t.Fatalf("committed upgrades=%+v", st)
	}
}

func TestIssue2481R2_ConsumedDigestDoesNotWake(t *testing.T) {
	for _, reply := range []bool{false, true} {
		t.Run(fmt.Sprintf("reply=%v", reply), func(t *testing.T) {
			n, parent, ev := newWakeNudgeFixture(t)
			fillPendingTurns(t, parent, ev.ChildSessionID, maxPendingTurnsPerChild)
			ev.TurnFingerprint, ev.Tier = "old-digest", TurnTierInfo
			if _, _, err := commitToInbox(parent, ev); err != nil {
				t.Fatal(err)
			}
			consumedTurnsMu.Lock()
			err := saveConsumedTurnsLocked(parent, map[string]int64{"old-digest": time.Now().Unix()})
			consumedTurnsMu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			sent := 0
			n.wake = &wakeNudgeWiring{nudger: NewWakeNudger(0), now: time.Now, isIdle: func(*Instance, string) bool { return true }, send: func(*Instance, string, string) error { sent++; return nil }}
			ev.TurnFingerprint, ev.Tier = "new-turn", TurnTierUrgent
			if reply {
				n.commitReplyToSender(&Instance{ID: parent, Title: "asker", Tool: "claude"}, ev)
			} else if committed, _, _ := n.commitEventToInbox(ev); !committed {
				t.Fatal("not committed")
			}
			if sent != 0 {
				t.Fatalf("consumed digest woke target %d times", sent)
			}
		})
	}
}
