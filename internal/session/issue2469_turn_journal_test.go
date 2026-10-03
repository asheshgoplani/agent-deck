package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Issue #2469: a child running background agents woke its parent on every
// background completion with a text-less record (161 wakeups in 160 min, 0
// carried text). These tests drive the daemon's emission paths against a
// real transcript file and assert the new contract: one record per turn,
// text inside, info never wakes, urgent wakes once, a sentinel turn is one
// finished record.

type turnTestFixture struct {
	d          *TransitionDaemon
	parent     *Instance
	child      *Instance
	transcript string
	lines      []string
	sends      *int
	byID       map[string]*Instance
}

func newTurnTestFixture(t *testing.T) *turnTestFixture {
	t.Helper()
	storage := reviewTestHome(t, "default")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	inboxConfigOverride = nil
	t.Cleanup(func() { inboxConfigOverride = nil })
	turnFacts = &turnFactsCache{entries: map[string]turnFactsCacheEntry{}}

	project := t.TempDir()
	parent := NewInstanceWithTool("conductor-test", project, "claude")
	parent.ID = "parent-2469"
	parent.Status = StatusWaiting
	child := NewInstanceWithTool("board-zero", project, "claude")
	child.ID = "child-2469"
	child.ParentSessionID = parent.ID
	child.ClaudeSessionID = "11111111-2222-3333-4444-555555555555"
	child.Status = StatusWaiting
	if err := storage.SaveWithGroups([]*Instance{parent, child}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	resolved := project
	if r, err := filepath.EvalSymlinks(project); err == nil {
		resolved = r
	}
	dir := filepath.Join(GetClaudeConfigDir(), "projects", ConvertToClaudeDirName(resolved))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir transcript dir: %v", err)
	}
	transcript := filepath.Join(dir, child.ClaudeSessionID+".jsonl")

	sends := 0
	n := NewTransitionNotifier()
	n.wake = &wakeNudgeWiring{
		nudger: NewWakeNudger(0),
		now:    time.Now,
		isIdle: func(*Instance, string) bool { return true },
		send:   func(*Instance, string, string) error { sends++; return nil },
	}
	d := &TransitionDaemon{
		notifier:      n,
		lastStatus:    map[string]map[string]string{},
		initialized:   map[string]bool{},
		lastDone:      map[string]map[string]DoneSignal{},
		lastTurn:      map[string]map[string]string{},
		lastDoneScan:  map[string]map[string]time.Time{},
		turnLiveCheck: func(*Instance) bool { return true },
	}
	return &turnTestFixture{d: d, parent: parent, child: child, transcript: transcript, sends: &sends,
		byID: map[string]*Instance{parent.ID: parent, child.ID: child}}
}

// appendTurn appends transcript lines and bumps the mtime so the facts cache
// sees a changed file even within the same second.
func (f *turnTestFixture) appendTurn(t *testing.T, lines ...string) {
	t.Helper()
	f.lines = append(f.lines, lines...)
	if err := os.WriteFile(f.transcript, []byte(strings.Join(f.lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	future := time.Now().Add(time.Duration(len(f.lines)) * time.Second)
	_ = os.Chtimes(f.transcript, future, future)
}

func (f *turnTestFixture) inboxRecords(t *testing.T) []TransitionNotificationEvent {
	t.Helper()
	events, err := ReadInboxEvents(f.parent.ID)
	if err != nil {
		t.Fatalf("ReadInboxEvents: %v", err)
	}
	return events
}

func TestIssue2469_BackgroundTurnsAreOneInfoRecordAndNeverWake(t *testing.T) {
	f := newTurnTestFixture(t)
	f.appendTurn(t, fxHuman("u0", "run the board"), fxAssistantText("a0", "Starting 13 lanes."))
	statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}

	// The human-triggered first turn is a plain reply: one info record, no wake.
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	if got := f.inboxRecords(t); len(got) != 1 || got[0].Tier != TurnTierInfo || got[0].Text != "Starting 13 lanes." {
		t.Fatalf("first turn: %+v", got)
	}
	if *f.sends != 0 {
		t.Fatalf("a reply without a question must not wake, sends=%d", *f.sends)
	}

	// A background agent finishes: task-notification turn with new progress text.
	f.appendTurn(t, fxTaskNotification("u1"), fxAssistantToolUse("a1"), fxToolResult("u2"), fxAssistantText("a2", "Lane C merged; verifier running."))
	// Every observation path fires, repeatedly, as the real daemon does for
	// the 45 s hook freshness window: recorded turns, hook candidates, and a
	// snapshot edge, plus a waiting->idle flip.
	for i := 0; i < 15; i++ {
		f.d.recordTerminalTurns("default", f.byID, statuses, nil)
		f.d.emitHookTransitionCandidates("default", f.byID, nil, statuses, map[string]hookTransitionCandidate{
			f.child.ID: {ToStatus: "waiting", Timestamp: time.Now()},
		})
	}
	f.d.emitTurn("default", f.child, f.byID, "running", "idle", time.Now(), false)

	got := f.inboxRecords(t)
	if len(got) != 2 {
		t.Fatalf("want exactly one new record for the background turn, inbox has %d: %+v", len(got), got)
	}
	rec := got[1]
	if rec.Tier != TurnTierInfo || rec.Trigger != TurnTriggerTask {
		t.Fatalf("background turn must be info/task: %+v", rec)
	}
	if rec.Text != "Lane C merged; verifier running." || rec.TextHash == "" || rec.TurnUUID != "a2" {
		t.Fatalf("record must carry the child's text: %+v", rec)
	}
	if *f.sends != 0 {
		t.Fatalf("info must never wake the parent, sends=%d", *f.sends)
	}

	st, _ := ReadInboxStats(f.parent.ID)
	if st.RecordsInfo != 2 || st.RecordsUrgent != 0 || st.NoiseSuppressed+st.DedupSuppressed < 15 || st.WakeupsSuppressed != 2 || st.WakeupsUrgent != 0 {
		t.Fatalf("stats: %+v", st)
	}

	journal, err := ReadTurnJournal(f.child.ID, 0)
	if err != nil || len(journal) != 2 {
		t.Fatalf("journal: %v %+v", err, journal)
	}
	if journal[1].Seq != 2 || journal[1].Tier != TurnTierInfo || rec.Seq != 2 {
		t.Fatalf("journal seq/tier: %+v record seq %d", journal[1], rec.Seq)
	}
}

func TestIssue2469_SentinelTurnIsOneFinishedRecord(t *testing.T) {
	f := newTurnTestFixture(t)
	f.appendTurn(t, fxTaskNotification("u1"), fxAssistantText("a1", "Board is at zero.\n===AGENTDECK_DONE=== status=ok summary=12 merged, 1 blocked"))
	statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}
	hs := &HookStatus{Status: "waiting", Event: "Stop", UpdatedAt: time.Now(), DoneStatus: "ok", DoneSummary: "12 merged, 1 blocked"}

	for i := 0; i < 3; i++ {
		f.d.recordTerminalTurns("default", f.byID, statuses, map[string]*HookStatus{f.child.ID: hs})
		f.d.emitDoneSignals("default", f.byID, map[string]*HookStatus{f.child.ID: hs})
	}
	got := f.inboxRecords(t)
	if len(got) != 1 {
		t.Fatalf("a sentinel turn is ONE record (finished), got %d: %+v", len(got), got)
	}
	if got[0].Kind != transitionKindFinished || got[0].DoneStatus != "ok" || got[0].DoneSummary != "12 merged, 1 blocked" || got[0].Tier != TurnTierUrgent {
		t.Fatalf("finished record: %+v", got[0])
	}
	if *f.sends != 1 {
		t.Fatalf("sentinel wakes once, sends=%d", *f.sends)
	}
	if entry, ok := ReadLedgerEntry(f.child.ID); !ok || entry.Status != "ok" {
		t.Fatalf("completion ledger must be written: %v %+v", ok, entry)
	}
}

func TestIssue2469_PendingTurnRetriesInsteadOfRecordingTwice(t *testing.T) {
	f := newTurnTestFixture(t)
	// Stop fired, assistant record not flushed yet.
	f.appendTurn(t, fxAssistantText("a0", "old"), fxTaskNotification("u1"))
	statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	if got := f.inboxRecords(t); len(got) != 0 {
		t.Fatalf("pending turn must not be recorded: %+v", got)
	}
	// The record lands on the next poll.
	f.appendTurn(t, fxAssistantText("a1", "Lane D verified."))
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	got := f.inboxRecords(t)
	if len(got) != 1 || got[0].Text != "Lane D verified." {
		t.Fatalf("want one record once flushed: %+v", got)
	}
}

func TestIssue2469_WakeOnInfoRestoresPerTurnWake(t *testing.T) {
	f := newTurnTestFixture(t)
	inboxConfigOverride = &InboxConfig{WakeOn: []string{"urgent", "info"}}
	f.appendTurn(t, fxTaskNotification("u1"), fxAssistantText("a1", "progress"))
	statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	if *f.sends != 1 {
		t.Fatalf("wake_on=[urgent,info] must wake for info, sends=%d", *f.sends)
	}
}

func TestIssue2469_NoTranscriptKeepsLegacyUrgentPath(t *testing.T) {
	f := newTurnTestFixture(t)
	f.child.ClaudeSessionID = "" // no readable transcript
	statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	got := f.inboxRecords(t)
	if len(got) != 1 || got[0].Tier != "" || !got[0].IsUrgent() {
		t.Fatalf("legacy record expected: %+v", got)
	}
	if *f.sends != 1 {
		t.Fatalf("legacy records keep waking, sends=%d", *f.sends)
	}
}

func TestIssue2469_StopBlockCarriesText(t *testing.T) {
	events := []TransitionNotificationEvent{
		{ChildTitle: "board", ChildSessionID: "c1", ToStatus: "waiting", Tier: TurnTierInfo, Text: "Lane C merged.\nTwo verifiers running."},
		{ChildTitle: "lead", ChildSessionID: "c2", ToStatus: "waiting"},
	}
	out := FormatCompletionsForInjection(events)
	if !strings.Contains(out, "- [info] board (c1): waiting\n    Lane C merged.\n    Two verifiers running.\n") {
		t.Fatalf("text not carried:\n%s", out)
	}
	if !strings.Contains(out, "- lead (c2): waiting\n") {
		t.Fatalf("legacy line changed:\n%s", out)
	}
}

// Review round 1 (reviews/pr-comms-verify1.md) F1: a NEW turn a human started
// whose reply repeats the previous reply word for word is news, not noise.
func TestIssue2469_IdenticalReplyToNewHumanTurnIsDelivered(t *testing.T) {
	f := newTurnTestFixture(t)
	statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}
	f.appendTurn(t, fxHuman("u0", "run tests"), fxAssistantText("a0", "All 42 tests pass."))
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	f.appendTurn(t, fxHuman("u1", "run them again"), fxAssistantText("a1", "All 42 tests pass."))
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	got := f.inboxRecords(t)
	if len(got) != 2 || got[1].TurnUUID != "a1" || got[1].Tier != TurnTierInfo {
		t.Fatalf("identical reply to a new human turn must be a new (info) record: %+v", got)
	}
	if *f.sends != 0 {
		t.Fatalf("plain replies never wake, sends=%d", *f.sends)
	}
	// The same words in a new BACKGROUND turn are still deduped as noise.
	f.appendTurn(t, fxTaskNotification("u2"), fxAssistantText("a2", "All 42 tests pass."))
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	if got := f.inboxRecords(t); len(got) != 2 {
		t.Fatalf("repeated background text is noise: %+v", got)
	}
}

// F2: a later sentinel turn that repeats an earlier completion's status and
// summary is a distinct turn and must survive the consumed-turn ledger.
func TestIssue2469_RepeatedDoneSummaryOnNewTurnIsDelivered(t *testing.T) {
	f := newTurnTestFixture(t)
	statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}
	f.appendTurn(t, fxHuman("u0", "ship"), fxAssistantText("a0", "Shipped.\n===AGENTDECK_DONE=== status=ok summary=shipped"))
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	if drained, err := DrainInboxForParent(f.parent.ID); err != nil || len(drained) != 1 {
		t.Fatalf("first drain: %d %v", len(drained), err)
	}
	f.appendTurn(t, fxHuman("u1", "ship again"), fxAssistantText("a1", "Shipped.\n===AGENTDECK_DONE=== status=ok summary=shipped"))
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	drained, err := DrainInboxForParent(f.parent.ID)
	if err != nil || len(drained) != 1 || drained[0].TurnUUID != "a1" {
		t.Fatalf("second completion with the same summary must be delivered: %d %v %+v", len(drained), err, drained)
	}
}

// F3: a commit that fails transiently (here the per-child pending cap) is
// retried on the next poll instead of being journaled as already seen.
func TestIssue2469_TransientCommitFailureIsRetried(t *testing.T) {
	f := newTurnTestFixture(t)
	statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}
	for i := 0; i < maxPendingTurnsPerChild; i++ {
		commitTestRecord(t, f.parent.ID, TransitionNotificationEvent{ChildSessionID: f.child.ID, ChildTitle: "board", Tier: TurnTierInfo, Text: "p", LastOutputHash: fmt.Sprintf("turn:fill-%d", i)})
	}
	f.appendTurn(t, fxHuman("u0", "status?"), fxAssistantText("a0", "Blocked on CI."))
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	if LastTurnJournalEntry(f.child.ID) != nil {
		t.Fatal("a failed commit must not be journaled")
	}
	if _, err := DrainInboxForParent(f.parent.ID); err != nil {
		t.Fatalf("drain: %v", err)
	}
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	got := f.inboxRecords(t)
	if len(got) != 1 || got[0].TurnUUID != "a0" {
		t.Fatalf("the turn must be delivered once the inbox has room: %+v", got)
	}
}

// A plain reply to the parent's own tagged send is info: one record, no wake,
// delivered by the prompt-time drain on the parent's next turn.
func TestIssue2469_PlainReplyToParentSendIsInfoAndInjectedNextPrompt(t *testing.T) {
	f := newTurnTestFixture(t)
	statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}
	f.appendTurn(t, fxHuman("u0", "[agent-deck from:parent-2469] status?"), fxAssistantText("a0", "Round 2 report saved, waiting for Docker."))
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	got := f.inboxRecords(t)
	if len(got) != 1 || got[0].Tier != TurnTierInfo || got[0].Trigger != TurnTriggerSend {
		t.Fatalf("plain reply must be one info record: %+v", got)
	}
	if *f.sends != 0 {
		t.Fatalf("a plain reply must not wake, sends=%d", *f.sends)
	}
	text, events, err := DrainForPrompt(f.parent.ID)
	if err != nil || len(events) != 1 || !strings.Contains(text, "Round 2 report saved, waiting for Docker.") {
		t.Fatalf("the next prompt must carry the reply: err=%v events=%d text=%q", err, len(events), text)
	}
}

// An urgent turn still wakes: the conductor ruling narrows urgent, it does
// not remove it.
func TestIssue2469_QuestionToParentStillWakes(t *testing.T) {
	f := newTurnTestFixture(t)
	statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}
	f.appendTurn(t, fxHuman("u0", "[agent-deck from:parent-2469] proceed"), fxAssistantText("a0", "Two options remain.\nNEED: merge or hold?"))
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
	got := f.inboxRecords(t)
	if len(got) != 1 || got[0].Tier != TurnTierUrgent || !got[0].Question {
		t.Fatalf("question must be an urgent record: %+v", got)
	}
	if *f.sends != 1 {
		t.Fatalf("question must wake once, sends=%d", *f.sends)
	}
}
