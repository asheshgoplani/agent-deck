package session

import (
	"os"
	"testing"
	"time"
)

// Issue #2473 with comms redesign PR5: a tagged `session send` whose turn
// hands off to background work has its Stop held, so the held send turn (the
// only one carrying trigger send and the sender id) is never recorded. The
// task turn that settles the work must still answer the sender.

// heldSendLaunch drives the child through a tagged send from fromID that
// launches a workflow, with the Stop held for several polls.
func heldSendLaunch(t *testing.T, f *pr5Fixture, fromID string, statuses map[string]string) {
	t.Helper()
	f.appendTurn(t, fxHuman("u0", SendEnvelope(fromID)+"\nrun the follow-on workflow and tell me the result"))
	f.appendTurn(t, fxWorkflowLaunch("wqphbmkuj", "comms-followon-round3")...)
	f.appendTurn(t, fxAssistantText("a0", "Launched comms-followon-round3 in the background."), fxTurnDuration(1))
	stop := map[string]hookTransitionCandidate{f.child.ID: {ToStatus: "waiting", Timestamp: time.Now(), Event: "Stop"}}
	for i := 0; i < 5; i++ {
		f.d.recordTerminalTurns("default", f.byID, statuses, nil)
		f.d.emitHookTransitionCandidates("default", f.byID, statuses, statuses, stop)
	}
}

// heldSendSettle appends the workflow's notification turn and drives every
// observation path as the session settles.
func heldSendSettle(t *testing.T, f *pr5Fixture, running, waiting map[string]string, text string) {
	t.Helper()
	f.appendTurn(t, fxWorkflowNotification("u1", "wqphbmkuj"), fxAssistantText("a1", text), fxTurnDuration(0))
	stop := map[string]hookTransitionCandidate{f.child.ID: {ToStatus: "waiting", Timestamp: time.Now(), Event: "Stop"}}
	f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
	for i := 0; i < 5; i++ {
		f.d.recordTerminalTurns("default", f.byID, waiting, nil)
		f.d.emitHookTransitionCandidates("default", f.byID, running, waiting, stop)
	}
}

func readSenderRecords(t *testing.T, id string) []TransitionNotificationEvent {
	t.Helper()
	recs, err := ReadInboxEvents(id)
	if err != nil {
		t.Fatalf("ReadInboxEvents(%s): %v", id, err)
	}
	return recs
}

// childRecords is the parent's inbox narrowed to the child under test (the
// idle sibling, also the parent's child, records its own idle turn there).
func childRecords(t *testing.T, f *pr5Fixture) []TransitionNotificationEvent {
	t.Helper()
	var out []TransitionNotificationEvent
	for _, r := range f.inboxRecords(t) {
		if r.ChildSessionID == f.child.ID {
			out = append(out, r)
		}
	}
	return out
}

func assertHeldSendAnswered(t *testing.T, f *pr5Fixture, senderID, text string) {
	t.Helper()
	replies := readSenderRecords(t, senderID)
	if len(replies) != 1 {
		t.Fatalf("sender must get exactly one reply when the work ends, got %d: %+v", len(replies), replies)
	}
	r := replies[0]
	if r.TargetKind != InboxTargetKindReply || r.Tier != TurnTierUrgent || r.FromID != senderID || r.Text != text {
		t.Fatalf("sender reply = kind %q tier %q from %q text %q, want reply/urgent/%s/%q", r.TargetKind, r.Tier, r.FromID, r.Text, senderID, text)
	}
	if f.woken[senderID] != 1 {
		t.Fatalf("sender must be woken exactly once, got %d", f.woken[senderID])
	}
	parentRecs := childRecords(t, f)
	if len(parentRecs) != 1 || parentRecs[0].Trigger != TurnTriggerTask || parentRecs[0].Tier != TurnTierInfo ||
		parentRecs[0].TargetKind != "parent" {
		t.Fatalf("parent must keep exactly its single task/info record: %+v", parentRecs)
	}
	if _, err := os.Stat(heldSendPath(f.child.ID)); !os.IsNotExist(err) {
		t.Fatalf("held send record must be cleared once answered (stat err %v)", err)
	}
}

func TestBackgroundWork2473_HeldSendRepliesToSenderWhenWorkEnds(t *testing.T) {
	f := newPR5Fixture(t)
	sib := pr5Sibling(f.child.ProjectPath)
	f.saveRegistry(t, sib)
	running := map[string]string{f.child.ID: "running", f.parent.ID: "waiting", sib.ID: "idle"}
	waiting := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting", sib.ID: "idle"}

	heldSendLaunch(t, f, sib.ID, running)
	if got := readSenderRecords(t, sib.ID); len(got) != 0 {
		t.Fatalf("no reply while the work runs: %+v", got)
	}
	if got := childRecords(t, f); len(got) != 0 {
		t.Fatalf("no parent record while the work runs: %+v", got)
	}

	const result = "comms-followon-round3 finished: 5/5 lanes merged."
	heldSendSettle(t, f, running, waiting, result)
	assertHeldSendAnswered(t, f, sib.ID, result)
}

// The sender survives a notify-daemon restart in the middle of the work.
func TestBackgroundWork2473_HeldSendSurvivesDaemonRestart(t *testing.T) {
	f := newPR5Fixture(t)
	sib := pr5Sibling(f.child.ProjectPath)
	f.saveRegistry(t, sib)
	running := map[string]string{f.child.ID: "running", f.parent.ID: "waiting", sib.ID: "idle"}
	waiting := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting", sib.ID: "idle"}

	heldSendLaunch(t, f, sib.ID, running)
	f.d = &TransitionDaemon{
		notifier:      f.d.notifier,
		lastStatus:    map[string]map[string]string{},
		initialized:   map[string]bool{},
		lastDone:      map[string]map[string]DoneSignal{},
		lastTurn:      map[string]map[string]string{},
		lastDoneScan:  map[string]map[string]time.Time{},
		turnLiveCheck: func(*Instance) bool { return true },
	}

	const result = "comms-followon-round3 finished: 5/5 lanes merged."
	heldSendSettle(t, f, running, waiting, result)
	assertHeldSendAnswered(t, f, sib.ID, result)
}

// A held send whose hold lapses records itself (and replies). The task turn
// that later ends the work must not reply to the same sender a second time.
func TestBackgroundWork2473_HeldSendAnsweredOnceWhenHoldLapses(t *testing.T) {
	f := newPR5Fixture(t)
	sib := pr5Sibling(f.child.ProjectPath)
	f.saveRegistry(t, sib)
	running := map[string]string{f.child.ID: "running", f.parent.ID: "waiting", sib.ID: "idle"}
	waiting := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting", sib.ID: "idle"}

	heldSendLaunch(t, f, sib.ID, running)
	// The hold lapses: the session settles with the send turn still last.
	f.d.recordTerminalTurns("default", f.byID, waiting, nil)
	if got := readSenderRecords(t, sib.ID); len(got) != 1 || got[0].Trigger != TurnTriggerSend {
		t.Fatalf("the lapsed send turn must reply once itself: %+v", got)
	}

	heldSendSettle(t, f, running, waiting, "comms-followon-round3 finished: 5/5 lanes merged.")
	if got := readSenderRecords(t, sib.ID); len(got) != 1 {
		t.Fatalf("the sender was already answered; the task turn must not reply again: %+v", got)
	}
	if got := childRecords(t, f); len(got) != 2 {
		t.Fatalf("parent gets the send turn and the task turn: %+v", got)
	}
}

// A plain background turn with no held send carries no sender: nothing is
// stamped on it and nobody but the parent hears about it.
func TestBackgroundWork2473_TaskTurnWithoutHeldSendHasNoSender(t *testing.T) {
	f := newPR5Fixture(t)
	sib := pr5Sibling(f.child.ProjectPath)
	f.saveRegistry(t, sib)
	running := map[string]string{f.child.ID: "running", f.parent.ID: "waiting", sib.ID: "idle"}
	waiting := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting", sib.ID: "idle"}

	f.appendTurn(t, fxHuman("u0", "run the follow-on workflow"))
	f.appendTurn(t, fxWorkflowLaunch("wqphbmkuj", "comms-followon-round3")...)
	f.appendTurn(t, fxAssistantText("a0", "Launched comms-followon-round3 in the background."), fxTurnDuration(1))
	stop := map[string]hookTransitionCandidate{f.child.ID: {ToStatus: "waiting", Timestamp: time.Now(), Event: "Stop"}}
	f.d.emitHookTransitionCandidates("default", f.byID, running, running, stop)

	heldSendSettle(t, f, running, waiting, "comms-followon-round3 finished: 5/5 lanes merged.")
	if got := readSenderRecords(t, sib.ID); len(got) != 0 {
		t.Fatalf("no sender, no reply: %+v", got)
	}
	parentRecs := childRecords(t, f)
	if len(parentRecs) != 1 || parentRecs[0].FromID != "" {
		t.Fatalf("parent record must carry no sender: %+v", parentRecs)
	}
}

// Round 5 F1: a hook-less Claude session ([claude] hooks_enabled = false), or
// a notify daemon that was down while the launch turn's Stop hook was fresh,
// never yields a hook candidate. The status stays running for the whole
// workflow, so the send turn is never recorded; the poll must still remember
// its sender so the task turn that settles the work answers it.
func TestBackgroundWork2473_HeldSendRepliesWithoutHookCandidate(t *testing.T) {
	f := newPR5Fixture(t)
	sib := pr5Sibling(f.child.ProjectPath)
	f.saveRegistry(t, sib)
	running := map[string]string{f.child.ID: "running", f.parent.ID: "waiting", sib.ID: "idle"}
	waiting := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting", sib.ID: "idle"}

	f.appendTurn(t, fxHuman("u0", SendEnvelope(sib.ID)+"\nrun the follow-on workflow and tell me the result"))
	f.appendTurn(t, fxWorkflowLaunch("wqphbmkuj", "comms-followon-round3")...)
	f.appendTurn(t, fxAssistantText("a0", "Launched comms-followon-round3 in the background."), fxTurnDuration(1))
	for i := 0; i < 5; i++ {
		f.d.recordTerminalTurns("default", f.byID, running, nil)
		f.d.emitHookTransitionCandidates("default", f.byID, running, running, nil)
	}
	if got := readSenderRecords(t, sib.ID); len(got) != 0 {
		t.Fatalf("no reply while the work runs: %+v", got)
	}

	const result = "comms-followon-round3 finished: 5/5 lanes merged."
	f.appendTurn(t, fxWorkflowNotification("u1", "wqphbmkuj"), fxAssistantText("a1", result), fxTurnDuration(0))
	for i := 0; i < 3; i++ {
		f.d.recordTerminalTurns("default", f.byID, waiting, nil)
	}
	f.d.emitTurn("default", f.child, f.byID, "running", "waiting", time.Now(), true)
	assertHeldSendAnswered(t, f, sib.ID, result)
}

// A poll can remember the send while its turn is still writing, under the
// uuid of an earlier assistant record of that turn. If the turn then settles
// between two polls, it records itself with its final uuid and replies; the
// remembered sender must be cleared so the task turn does not reply twice.
func TestBackgroundWork2473_HeldSendRememberedMidTurnAnsweredOnce(t *testing.T) {
	f := newPR5Fixture(t)
	sib := pr5Sibling(f.child.ProjectPath)
	f.saveRegistry(t, sib)
	running := map[string]string{f.child.ID: "running", f.parent.ID: "waiting", sib.ID: "idle"}
	waiting := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting", sib.ID: "idle"}

	f.appendTurn(t, fxHuman("u0", SendEnvelope(sib.ID)+"\nrun the follow-on workflow and tell me the result"))
	f.appendTurn(t, fxAssistantText("a00", "Launching the workflow now."))
	f.appendTurn(t, fxWorkflowLaunch("wqphbmkuj", "comms-followon-round3")...)
	f.d.recordTerminalTurns("default", f.byID, running, nil)
	held := loadHeldSend(f.child.ID)
	if held == nil || held.FromID != sib.ID || held.UUID != "a00" {
		t.Fatalf("the poll must remember the sender mid-turn, got %+v", held)
	}

	// The turn finishes and the hold lapses before the next poll.
	f.appendTurn(t, fxAssistantText("a0", "Launched comms-followon-round3 in the background."), fxTurnDuration(1))
	f.d.recordTerminalTurns("default", f.byID, waiting, nil)
	if got := readSenderRecords(t, sib.ID); len(got) != 1 || got[0].Trigger != TurnTriggerSend {
		t.Fatalf("the settled send turn must reply once itself: %+v", got)
	}

	heldSendSettle(t, f, running, waiting, "comms-followon-round3 finished: 5/5 lanes merged.")
	if got := readSenderRecords(t, sib.ID); len(got) != 1 {
		t.Fatalf("the sender was already answered; the task turn must not reply again: %+v", got)
	}
}

// Round 6 F1: a workflow agent asks for permission, so the child reads
// waiting while the work runs. The send turn is recorded and replies once,
// and its held record is cleared. When the menu is answered the session is
// running on background work again with the send turn still last; the poll
// must not remember that sender again, or the task turn would reply twice.
func TestBackgroundWork2473_HeldSendMenuInterludeAnsweredOnce(t *testing.T) {
	f := newPR5Fixture(t)
	sib := pr5Sibling(f.child.ProjectPath)
	f.saveRegistry(t, sib)
	running := map[string]string{f.child.ID: "running", f.parent.ID: "waiting", sib.ID: "idle"}
	waiting := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting", sib.ID: "idle"}

	heldSendLaunch(t, f, sib.ID, running)
	f.d.recordTerminalTurns("default", f.byID, waiting, nil)
	if got := readSenderRecords(t, sib.ID); len(got) != 1 {
		t.Fatalf("menu interlude: the send turn replies once: %+v", got)
	}
	for i := 0; i < 3; i++ {
		f.d.recordTerminalTurns("default", f.byID, running, nil)
	}
	if held := loadHeldSend(f.child.ID); held != nil {
		t.Fatalf("an answered send turn must not be remembered again, got %+v", held)
	}
	heldSendSettle(t, f, running, waiting, "comms-followon-round3 finished: 5/5 lanes merged.")
	if got := readSenderRecords(t, sib.ID); len(got) != 1 {
		t.Fatalf("sender answered %d times, want 1: %+v", len(got), got)
	}
}

// Round 6 F1, hook-less: no Stop candidate at all. The poll remembers the
// sender, the menu interlude records and answers the send turn, and the
// resumed work must not remember it again.
func TestBackgroundWork2473_HeldSendHooklessMenuInterludeAnsweredOnce(t *testing.T) {
	f := newPR5Fixture(t)
	sib := pr5Sibling(f.child.ProjectPath)
	f.saveRegistry(t, sib)
	running := map[string]string{f.child.ID: "running", f.parent.ID: "waiting", sib.ID: "idle"}
	waiting := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting", sib.ID: "idle"}

	f.appendTurn(t, fxHuman("u0", SendEnvelope(sib.ID)+"\nrun the follow-on workflow and tell me the result"))
	f.appendTurn(t, fxWorkflowLaunch("wqphbmkuj", "comms-followon-round3")...)
	f.appendTurn(t, fxAssistantText("a0", "Launched comms-followon-round3 in the background."), fxTurnDuration(1))
	for i := 0; i < 3; i++ {
		f.d.recordTerminalTurns("default", f.byID, running, nil)
	}
	f.d.recordTerminalTurns("default", f.byID, waiting, nil)
	for i := 0; i < 3; i++ {
		f.d.recordTerminalTurns("default", f.byID, running, nil)
	}
	heldSendSettle(t, f, running, waiting, "comms-followon-round3 finished: 5/5 lanes merged.")
	if got := readSenderRecords(t, sib.ID); len(got) != 1 {
		t.Fatalf("sender answered %d times, want 1: %+v", len(got), got)
	}
}
