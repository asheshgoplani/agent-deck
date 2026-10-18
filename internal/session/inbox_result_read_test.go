package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Self-withdrawing inbox notices (prime-agent "notices withdraw
// themselves when the result was already read"). A parent that read a
// child's turn with `session output` is not woken, blocked or shown the
// record for that same turn again; a turn it has not read is delivered
// exactly as before. The match is on the turn's transcript uuid, never on
// time.
//
// The read marker is written straight to its on-disk layout here, so these
// tests compile against releases without the feature and also pin that
// layout (<runtime>/result-reads/<reader>.json, child id -> {"turns":
// [uuid...], "at": unix ms, "read_at": {uuid: unix ms}}).

// writeResultReadForTest records a read made now.
func writeResultReadForTest(t *testing.T, readerID, childID, turnUUID string) {
	t.Helper()
	writeResultReadAtForTest(t, readerID, childID, turnUUID, time.Now())
}

func writeResultReadAtForTest(t *testing.T, readerID, childID, turnUUID string, at time.Time) {
	t.Helper()
	path := filepath.Join(runtimeDirOrTemp("result-reads"), sanitizeInboxName(readerID)+".json")
	type entry struct {
		Turns  []string         `json:"turns"`
		At     int64            `json:"at"`
		ReadAt map[string]int64 `json:"read_at,omitempty"`
	}
	reads := map[string]entry{}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &reads)
	}
	e := reads[childID]
	e.Turns = append(e.Turns, turnUUID)
	e.At = at.UnixMilli()
	if e.ReadAt == nil {
		e.ReadAt = map[string]int64{}
	}
	e.ReadAt[turnUUID] = at.UnixMilli()
	reads[childID] = e
	data, err := json.Marshal(reads)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// resultReadFixture is one idle parent with n children, a notifier with a
// counting wake wiring, and a fixed instant every child finished at.
type resultReadFixture struct {
	n        *TransitionNotifier
	parentID string
	children []*Instance
	finished time.Time
	wakes    int
	wakeFor  []string
}

func newResultReadFixture(t *testing.T, children int) *resultReadFixture {
	t.Helper()
	inboxTestHome(t)
	profile := "_test-result-read"
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	t.Cleanup(func() { storage.Close() })
	now := time.Now()
	f := &resultReadFixture{parentID: "rr-parent", finished: now.Add(-time.Minute)}
	all := []*Instance{{ID: f.parentID, Title: "orchestrator", ProjectPath: "/tmp/p", GroupPath: DefaultGroupPath,
		Tool: "claude", Status: StatusIdle, CreatedAt: now}}
	for i := 1; i <= children; i++ {
		c := &Instance{ID: fmt.Sprintf("rr-child-%d", i), Title: fmt.Sprintf("worker-%d", i), ProjectPath: "/tmp/c",
			GroupPath: DefaultGroupPath, ParentSessionID: f.parentID, Tool: "claude", Status: StatusRunning, CreatedAt: now}
		f.children = append(f.children, c)
		all = append(all, c)
	}
	if err := storage.SaveWithGroups(all, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	f.n = NewTransitionNotifier()
	f.n.wake = &wakeNudgeWiring{
		nudger: NewWakeNudger(0),
		now:    time.Now,
		isIdle: func(*Instance, string) bool { return true },
		send: func(_ *Instance, _ string, line string) error {
			f.wakes++
			f.wakeFor = append(f.wakeFor, line)
			return nil
		},
	}
	return f
}

// turnOf is the transcript uuid of child's final assistant record for the
// fixture's one turn.
func turnOf(child *Instance) string { return "uuid-final-" + child.ID }

// finish commits child's completion as the daemon would: stamped with the
// fixture's instant and carrying the uuid of the turn's final record.
func (f *resultReadFixture) finish(t *testing.T, child *Instance, summary string) {
	t.Helper()
	f.finishAt(t, child, summary, turnOf(child), f.finished)
}

func (f *resultReadFixture) finishAt(t *testing.T, child *Instance, summary, turnUUID string, at time.Time) {
	t.Helper()
	ev := TransitionNotificationEvent{ChildSessionID: child.ID, ChildTitle: child.Title, Profile: "_test-result-read",
		DoneStatus: "ok", DoneSummary: summary, Timestamp: at, TurnUUID: turnUUID, Tier: TurnTierUrgent,
		LastOutputHash: "turn:" + turnUUID}
	if res := f.n.NotifyFinished(ev); res.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("commit for %s = %q", child.ID, res.DeliveryResult)
	}
}

func childIDs(events []TransitionNotificationEvent) []string {
	var out []string
	for _, ev := range events {
		out = append(out, ev.ChildSessionID)
	}
	return out
}

// A notice whose turn the parent already read is neither woken for nor
// delivered; the one for an unread turn still is, exactly once.
func TestResultRead_NoticeForReadResultIsWithdrawn(t *testing.T) {
	f := newResultReadFixture(t, 2)
	read, unread := f.children[0], f.children[1]
	writeResultReadForTest(t, f.parentID, read.ID, turnOf(read))

	f.finish(t, read, "read already")
	f.finish(t, unread, "not read yet")

	if f.wakes != 1 || !strings.Contains(strings.Join(f.wakeFor, "\n"), unread.Title) {
		t.Fatalf("wakes = %d %q, want exactly one, for %s", f.wakes, f.wakeFor, unread.Title)
	}
	delivered, err := DrainInboxForParent(f.parentID)
	if err != nil {
		t.Fatal(err)
	}
	if got := childIDs(delivered); len(got) != 1 || got[0] != unread.ID {
		t.Fatalf("drain delivered %v, want only %s", got, unread.ID)
	}
	if InboxHasPending(f.parentID) {
		t.Fatal("a withdrawn notice must be retired by the drain, not left pending")
	}
	// The withdrawn turn is consumed: a replay of it is not delivered later.
	f.finish(t, read, "read already")
	if again, _ := DrainInboxForParent(f.parentID); len(again) != 0 {
		t.Fatalf("replayed withdrawn turn was delivered: %v", childIDs(again))
	}
}

// Review regression (round 2). A hook-driven record is stamped
// with the hook's whole second, and Stop can fire before the turn's final
// assistant record is written (#1186). A parent that runs `session output`
// inside that second, or right after `session children --until-done`
// returns, is shown mid-turn or previous-turn text. That read is later
// than the record's stamp yet names an older record, so the notice for the
// final turn must still be delivered.
func TestResultRead_ReadInsideHookSecondOfOlderTurnStillDelivers(t *testing.T) {
	f := newResultReadFixture(t, 2)
	lateWrite, sameSecond := f.children[0], f.children[1]
	stop := time.Unix(time.Now().Add(-time.Minute).Unix(), 0) // whole-second hook stamp

	// Final record lands at stop+800ms; the parent read at stop+300ms and
	// was shown the previous turn.
	writeResultReadForTest(t, f.parentID, sameSecond.ID, "uuid-previous-"+sameSecond.ID)
	// Stop fired before the final record was written; the parent read the
	// mid-turn record right after --until-done returned.
	writeResultReadForTest(t, f.parentID, lateWrite.ID, "uuid-midturn-"+lateWrite.ID)

	f.finishAt(t, sameSecond, "final text", turnOf(sameSecond), stop)
	f.finishAt(t, lateWrite, "final text", turnOf(lateWrite), stop)

	if f.wakes != 2 {
		t.Fatalf("wakes = %d, want 2: neither read showed the final turn", f.wakes)
	}
	delivered, err := DrainInboxForParent(f.parentID)
	if err != nil || len(delivered) != 2 {
		t.Fatalf("drain = %v err=%v, want both final turns delivered", childIDs(delivered), err)
	}
}

// A child that answers two turns with the same text ("Done.") has two
// distinct records: reading the first does not withdraw the second.
func TestResultRead_SameTextNewTurnStillDelivers(t *testing.T) {
	f := newResultReadFixture(t, 1)
	child := f.children[0]
	writeResultReadForTest(t, f.parentID, child.ID, "uuid-turn-1")
	f.finishAt(t, child, "Done.", "uuid-turn-2", f.finished)
	delivered, err := DrainInboxForParent(f.parentID)
	if err != nil || len(delivered) != 1 || f.wakes != 1 {
		t.Fatalf("drain = %v wakes=%d err=%v, want the second turn delivered", childIDs(delivered), f.wakes, err)
	}
}

// A notice that was committed (and woken for) before the parent read the
// turn withdraws itself at the next drain: the prompt-time injection
// leaves it out and the Stop hook does not block for it.
func TestResultRead_ReadAfterCommitWithdrawsFromPromptAndStop(t *testing.T) {
	f := newResultReadFixture(t, 2)
	read, unread := f.children[0], f.children[1]
	f.finish(t, read, "alpha result")
	f.finish(t, unread, "beta result")
	writeResultReadForTest(t, f.parentID, read.ID, turnOf(read))

	text, events, err := DrainForPrompt(f.parentID)
	if err != nil {
		t.Fatal(err)
	}
	if got := childIDs(events); len(got) != 1 || got[0] != unread.ID {
		t.Fatalf("prompt drain showed %v, want only %s", got, unread.ID)
	}
	if strings.Contains(text, "alpha result") || !strings.Contains(text, "beta result") {
		t.Fatalf("prompt injection must carry only the unread result:\n%s", text)
	}
	if InboxHasPending(f.parentID) {
		t.Fatal("withdrawn record left pending after the prompt drain")
	}

	// Stop hook: only an already-read urgent record pending -> no block.
	second := TransitionNotificationEvent{ChildSessionID: read.ID, ChildTitle: read.Title, Profile: "_test-result-read",
		FromStatus: "running", ToStatus: "waiting", Timestamp: time.Now().Add(-10 * time.Second), Tier: TurnTierUrgent,
		Text: "alpha second turn", TurnUUID: "uuid-alpha-2", TurnFingerprint: read.ID + "@turn2"}
	if _, _, err := commitToInbox(f.parentID, second); err != nil {
		t.Fatal(err)
	}
	if !InboxHasUrgentPending(f.parentID) {
		t.Fatal("setup: the second turn must be pending and urgent before the read")
	}
	writeResultReadForTest(t, f.parentID, read.ID, "uuid-alpha-2")
	if InboxHasUrgentPending(f.parentID) {
		t.Fatal("an already-read record must not count as urgent pending")
	}
	if _, blocked, err := DrainForStopHook(f.parentID, false); err != nil || blocked {
		t.Fatalf("Stop hook blocked=%v err=%v for a result the parent already read", blocked, err)
	}
}

// A question is never withdrawn: reading the child's output does not answer it.
func TestResultRead_QuestionIsNeverWithdrawn(t *testing.T) {
	f := newResultReadFixture(t, 1)
	child := f.children[0]
	writeResultReadForTest(t, f.parentID, child.ID, "uuid-q1")
	ev := TransitionNotificationEvent{ChildSessionID: child.ID, ChildTitle: child.Title, Profile: "_test-result-read",
		FromStatus: "running", ToStatus: "waiting", Timestamp: f.finished, Tier: TurnTierUrgent, Question: true,
		Text: "should I merge?", TurnUUID: "uuid-q1", TurnFingerprint: child.ID + "@q1"}
	if _, _, err := commitToInbox(f.parentID, ev); err != nil {
		t.Fatal(err)
	}
	delivered, err := DrainInboxForParent(f.parentID)
	if err != nil || len(delivered) != 1 {
		t.Fatalf("question drain = %v err=%v, want delivered", childIDs(delivered), err)
	}
}

// TestResultRead_ScriptedInboxScenarioCounts is the before/after scenario:
// six children finish; the parent had already read the final turn of three
// of them with `session output`, and for a fourth it read inside the hook
// second but was shown the previous turn. It counts the notices that reach
// the parent: wakes typed plus records delivered.
func TestResultRead_ScriptedInboxScenarioCounts(t *testing.T) {
	f := newResultReadFixture(t, 6)
	for _, c := range f.children[:3] {
		writeResultReadForTest(t, f.parentID, c.ID, turnOf(c))
	}
	writeResultReadForTest(t, f.parentID, f.children[3].ID, "uuid-previous-"+f.children[3].ID)
	for _, c := range f.children {
		f.finish(t, c, "result of "+c.Title)
	}
	delivered, err := DrainInboxForParent(f.parentID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("SCENARIO children=6 final_turn_read=3 older_turn_read=1 wakes=%d records_delivered=%d notices_total=%d",
		f.wakes, len(delivered), f.wakes+len(delivered))
	if f.wakes != 3 || len(delivered) != 3 {
		t.Fatalf("wakes=%d delivered=%d, want 3 and 3 (only the turns not read)", f.wakes, len(delivered))
	}
}
