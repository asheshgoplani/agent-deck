package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ASK-92: Claude Code's idle auto-compaction ("Compacted while idle, before
// the prompt cache expired") rewrites the child's context while it sits at
// its prompt. The transcript gains a compact_boundary, an isCompactSummary
// user record, attachments and an informational notice, but no assistant
// reply. The parent got an urgent, text-less waiting record for it.
//
// testdata/claude-idle-compaction.jsonl is a real Claude Code 2.1.302 tail
// (2026-10-18), trimmed: a tagged send, its reply, then the idle compaction.

// idleCompactionFixture returns the fixture split into the turn before the
// compaction and the compaction itself.
func idleCompactionFixture(t *testing.T) (before, compaction []string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "claude-idle-compaction.jsonl"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	for i, line := range lines {
		if strings.Contains(line, `"subtype":"compact_boundary"`) {
			return lines[:i], lines[i:]
		}
	}
	t.Fatal("fixture has no compact_boundary")
	return nil, nil
}

// appendSettled appends lines and dates the file past the flush-race window,
// as the daemon sees it on the polls after the compaction finished.
func (f *turnTestFixture) appendSettled(t *testing.T, lines ...string) {
	t.Helper()
	f.appendTurn(t, lines...)
	at := time.Now().Add(-time.Hour + time.Duration(len(f.lines))*time.Second)
	if err := os.Chtimes(f.transcript, at, at); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

// observeAllPaths drives every daemon observation path once, as a real poll
// after the compaction would: the recorded-turn scan, a hook candidate and
// the snapshot edge (the pane ran while compacting).
func (f *turnTestFixture) observeAllPaths(statuses map[string]string) {
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	f.d.emitHookTransitionCandidates("default", f.byID, nil, statuses, map[string]hookTransitionCandidate{
		f.child.ID: {ToStatus: "waiting", Timestamp: time.Now()},
	})
	f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
}

func TestClassifyTranscriptTail_IdleCompactionIsNotAPendingTurn(t *testing.T) {
	before, compaction := idleCompactionFixture(t)
	facts := classifyTranscriptTail(append(append([]string{}, before...), compaction...))
	if facts.Pending {
		t.Fatalf("an idle compaction is not a prompt awaiting its reply: %+v", facts)
	}
	if facts.UUID != "" || facts.Text != "" || facts.HasDone || facts.Question {
		t.Fatalf("an idle compaction carries no turn: %+v", facts)
	}
}

func TestIdleCompaction_SendsParentNoRecord(t *testing.T) {
	f := newTurnTestFixture(t)
	statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}
	before, compaction := idleCompactionFixture(t)

	f.appendSettled(t, before...)
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	got := f.inboxRecords(t)
	if len(got) != 1 || got[0].Text != "PR #2714 is open and not merged. Head is `acd6cb8f`." {
		t.Fatalf("the reply before the compaction must be one record with its text: %+v", got)
	}

	f.appendSettled(t, compaction...)
	for i := 0; i < 3; i++ {
		f.observeAllPaths(statuses)
	}
	if got := f.inboxRecords(t); len(got) != 1 {
		t.Fatalf("idle compaction must add no parent record, inbox has %d: %+v", len(got), got[1:])
	}
	if *f.sends != 0 {
		t.Fatalf("idle compaction must not wake the parent, sends=%d", *f.sends)
	}
}

func TestIdleCompaction_RealTurnAfterItIsDelivered(t *testing.T) {
	f := newTurnTestFixture(t)
	statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}
	before, compaction := idleCompactionFixture(t)

	f.appendSettled(t, before...)
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	f.appendSettled(t, compaction...)
	f.observeAllPaths(statuses)

	f.appendSettled(t, fxHuman("u9", "is it merged now?"), fxAssistantText("a9", "Not yet: CI is red on lint. Should I fix it?"))
	f.observeAllPaths(statuses)

	var rec *TransitionNotificationEvent
	got := f.inboxRecords(t)
	for i := range got {
		if got[i].TurnUUID == "a9" {
			if rec != nil {
				t.Fatalf("the turn after the compaction was recorded twice: %+v", got)
			}
			rec = &got[i]
		}
	}
	if rec == nil {
		t.Fatalf("the turn after the compaction was not recorded: %+v", got)
	}
	if rec.Text != "Not yet: CI is red on lint. Should I fix it?" ||
		rec.Trigger != TurnTriggerHuman || rec.Tier != TurnTierUrgent || !rec.Question {
		t.Fatalf("the turn after the compaction must be delivered with its text: %+v", rec)
	}
}

// A long turn auto-compacted in the middle, then the child called a tool and
// stopped at a permission prompt: the tail after the summary holds only tool
// records. That child is waiting on someone and the parent must still hear.
func TestIdleCompaction_MidTurnCompactionThenPermissionWaitReachesParent(t *testing.T) {
	f := newTurnTestFixture(t)
	statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}
	before, compaction := idleCompactionFixture(t)

	f.appendSettled(t, before...)
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	n := len(f.inboxRecords(t))

	tail := append([]string{fxHuman("u5", "run the migration")}, compaction[:2]...) // boundary, summary
	f.appendSettled(t, append(tail, fxAssistantToolUse("a5"))...)
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	if got := f.inboxRecords(t); len(got) != n+1 {
		t.Fatalf("the permission wait after a mid-turn compaction must reach the parent once, new records: %+v", got[n:])
	}
}
