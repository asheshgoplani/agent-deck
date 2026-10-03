package session

import (
	"strings"
	"testing"
	"time"
)

// Comms redesign PR5: a `session send` from inside a session carries a
// "[agent-deck from:<id>]" envelope; the receiver's reply turn is committed
// to its parent as before AND, when the sender is someone else (a sibling),
// to the sender's inbox as an urgent "reply" record that wakes it.

type pr5Fixture struct {
	*turnTestFixture
	woken map[string]int // target id -> wake sends
	kinds map[string]string
}

func newPR5Fixture(t *testing.T, extra ...*Instance) *pr5Fixture {
	t.Helper()
	f := &pr5Fixture{turnTestFixture: newTurnTestFixture(t), woken: map[string]int{}, kinds: map[string]string{}}
	f.saveRegistry(t, extra...)
	// Production gate for the target kind, status forced idle: proves the
	// gate itself (not a test stub) lets a non-conductor reply target wake.
	withNoopStatusProbe(t)
	f.d.notifier.wake = &wakeNudgeWiring{
		nudger: NewWakeNudger(0),
		now:    time.Now,
		isIdle: func(p *Instance, kind string) bool {
			f.kinds[p.ID] = kind
			return parentIsNudgeableIdle(p, kind)
		},
		send: func(p *Instance, _, _ string) error { f.woken[p.ID]++; return nil },
	}
	return f
}

// saveRegistry rewrites the registry as parent + child + extra.
func (f *pr5Fixture) saveRegistry(t *testing.T, extra ...*Instance) {
	t.Helper()
	storage, err := NewStorageWithProfile("default")
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer storage.Close()
	all := append([]*Instance{f.parent, f.child}, extra...)
	if err := storage.SaveWithGroups(all, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}
	f.byID = map[string]*Instance{}
	for _, inst := range all {
		f.byID[inst.ID] = inst
	}
}

func (f *pr5Fixture) removeFromRegistry(t *testing.T, id string) {
	t.Helper()
	storage, err := NewStorageWithProfile("default")
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer storage.Close()
	if err := storage.DeleteInstance(id); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	delete(f.byID, id)
}

func (f *pr5Fixture) runTaggedTurn(t *testing.T, fromID string) {
	t.Helper()
	f.appendTurn(t, fxHuman("u0", SendEnvelope(fromID)+"\nwhich port does the API use?"), fxAssistantText("a0", "The API listens on 8443."))
	statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
}

func pr5Sibling(project string) *Instance {
	sib := NewInstanceWithTool("api-worker", project, "claude")
	sib.ID = "sibling-pr5"
	sib.ParentSessionID = "parent-2469"
	sib.Status = StatusIdle
	return sib
}

func TestPR5_SiblingReplyIsCommittedUrgentToSenderAndToParent(t *testing.T) {
	f := newPR5Fixture(t)
	sib := pr5Sibling(f.child.ProjectPath)
	f.saveRegistry(t, sib)

	f.runTaggedTurn(t, sib.ID)

	parentRecs := f.inboxRecords(t)
	if len(parentRecs) != 1 || parentRecs[0].Trigger != TurnTriggerSend || parentRecs[0].FromID != sib.ID ||
		parentRecs[0].TargetKind != "parent" || parentRecs[0].Tier != TurnTierInfo {
		t.Fatalf("parent must keep exactly its own copy (info: a plain reply): %+v", parentRecs)
	}
	replies, err := ReadInboxEvents(sib.ID)
	if err != nil {
		t.Fatalf("ReadInboxEvents(sender): %v", err)
	}
	if len(replies) != 1 {
		t.Fatalf("sender must get exactly one reply record, got %d: %+v", len(replies), replies)
	}
	r := replies[0]
	if r.TargetKind != "reply" || r.Tier != TurnTierUrgent || r.TargetSessionID != sib.ID ||
		r.ChildSessionID != f.child.ID || r.Text != "The API listens on 8443." {
		t.Fatalf("reply record: %+v", r)
	}
	if f.woken[sib.ID] != 1 || f.kinds[sib.ID] != "reply" {
		t.Fatalf("the non-conductor sender must be woken once as a reply target: woken=%v kinds=%v", f.woken, f.kinds)
	}
	if f.woken[f.parent.ID] != 0 {
		t.Fatalf("the parent is not woken for a plain reply: woken=%v", f.woken)
	}

	// The reply drains into the sender's next turn, labelled as a reply.
	text, _, err := DrainForPrompt(sib.ID)
	if err != nil || !strings.Contains(text, "- [urgent] reply from="+f.child.ID+" board-zero: waiting\n    The API listens on 8443.") {
		t.Fatalf("sender prompt drain: err=%v\n%s", err, text)
	}
}

func TestPR5_UnknownOrRemovedSenderCommitsOnlyToParent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		remove bool
	}{{"unknown", false}, {"removed", true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPR5Fixture(t)
			from := "ghost-session"
			if tc.remove {
				sib := pr5Sibling(f.child.ProjectPath)
				f.saveRegistry(t, sib)
				f.removeFromRegistry(t, sib.ID) // the sender was removed before the reply landed
				from = sib.ID
			}
			f.runTaggedTurn(t, from)

			if got := f.inboxRecords(t); len(got) != 1 || got[0].FromID != from {
				t.Fatalf("parent copy: %+v", got)
			}
			if InboxHasPending(from) {
				t.Fatalf("a sender not in the registry must get nothing")
			}
			if len(f.woken) != 0 {
				t.Fatalf("a plain reply wakes nobody: %v", f.woken)
			}
		})
	}
}

func TestPR5_SenderIsParentCommitsOnce(t *testing.T) {
	f := newPR5Fixture(t)
	f.runTaggedTurn(t, f.parent.ID)

	got := f.inboxRecords(t)
	if len(got) != 1 || got[0].TargetKind != "parent" || got[0].FromID != f.parent.ID {
		t.Fatalf("a send from the parent is ONE record in the parent inbox: %+v", got)
	}
	if len(f.woken) != 0 {
		t.Fatalf("a plain reply to the parent's own send wakes nobody: %v", f.woken)
	}
}

func TestPR5_SelfSendIsNotRoutedBackToTheChild(t *testing.T) {
	f := newPR5Fixture(t)
	f.runTaggedTurn(t, f.child.ID)
	if InboxHasPending(f.child.ID) {
		t.Fatal("a turn started by the child's own tagged send must not land in its own inbox")
	}
	if got := f.inboxRecords(t); len(got) != 1 {
		t.Fatalf("parent copy: %+v", got)
	}
}

func TestPR5_NudgeGateAcceptsNonConductorReplyTarget(t *testing.T) {
	withNoopStatusProbe(t)
	worker := &Instance{ID: "w", Title: "api-worker", Tool: "claude", Status: StatusIdle}
	if !parentIsNudgeableIdle(worker, "reply") {
		t.Fatal("an idle non-conductor reply target must be nudgeable")
	}
	if parentIsNudgeableIdle(worker, "parent") {
		t.Fatal("a non-conductor parent target stays un-nudgeable")
	}
	busy := &Instance{ID: "w", Title: "api-worker", Tool: "claude", Status: StatusRunning}
	if parentIsNudgeableIdle(busy, "reply") {
		t.Fatal("a busy reply target must not be nudged (its Stop/prompt drain delivers)")
	}
}

// Fix round 2: the wake line is typed into the reply target's pane and
// carries the child's text. Only a Claude-compatible pane drains it; a shell
// would execute it and another harness would strand the record.
func TestPR5_NudgeGateRejectsNonClaudeReplyTarget(t *testing.T) {
	withNoopStatusProbe(t)
	for _, tool := range []string{"shell", "codex", "gemini", ""} {
		target := &Instance{ID: "w", Title: "conductor-w", Tool: tool, Status: StatusIdle}
		if parentIsNudgeableIdle(target, "reply") {
			t.Fatalf("a %q reply target must not be woken", tool)
		}
	}
}

// pr5AssertOneReply reads the sender's inbox and asserts exactly one urgent
// reply record for the fixture child's answer.
func pr5AssertOneReply(t *testing.T, f *pr5Fixture, senderID string) {
	t.Helper()
	replies, err := ReadInboxEvents(senderID)
	if err != nil {
		t.Fatalf("ReadInboxEvents(sender): %v", err)
	}
	if len(replies) != 1 {
		t.Fatalf("sender must get exactly one reply record, got %d: %+v", len(replies), replies)
	}
	r := replies[0]
	if r.TargetKind != "reply" || r.Tier != TurnTierUrgent || r.TargetSessionID != senderID ||
		r.ChildSessionID != f.child.ID || r.Text != "The API listens on 8443." || r.DeadLetterReason != "" {
		t.Fatalf("reply record: %+v", r)
	}
	if f.woken[senderID] != 1 || f.kinds[senderID] != "reply" {
		t.Fatalf("the sender must be woken once as a reply target: woken=%v kinds=%v", f.woken, f.kinds)
	}
}

// Fix round 2: a child asks its TOP-LEVEL conductor with a tagged send. The
// conductor has no parent (its own turns are self-suppressed), yet its answer
// must reach the child that asked.
func TestPR5_TopLevelConductorReplyReachesAskingChild(t *testing.T) {
	f := newPR5Fixture(t)
	f.child.Title = "conductor-ops"
	f.child.ParentSessionID = ""
	asker := NewInstanceWithTool("asker", f.child.ProjectPath, "claude")
	asker.ID = "asker-pr5"
	asker.ParentSessionID = f.child.ID
	asker.Status = StatusIdle
	f.saveRegistry(t, asker)

	f.runTaggedTurn(t, asker.ID)

	pr5AssertOneReply(t, f, asker.ID)
	if got := f.inboxRecords(t); len(got) != 0 {
		t.Fatalf("the unrelated session must get nothing: %+v", got)
	}
	if InboxHasPending(f.child.ID) {
		t.Fatal("the conductor's own inbox must not hold its answer")
	}
}

// Fix round 2: a peer (no parent: -no-parent or orphan) answers a tagged
// send; the answer reaches the sender.
func TestPR5_PeerReceiverReplyReachesSender(t *testing.T) {
	f := newPR5Fixture(t)
	f.child.ParentSessionID = ""
	sib := pr5Sibling(f.child.ProjectPath)
	f.saveRegistry(t, sib)

	f.runTaggedTurn(t, sib.ID)

	pr5AssertOneReply(t, f, sib.ID)
	if got := f.inboxRecords(t); len(got) != 0 {
		t.Fatalf("no parent copy for a peer: %+v", got)
	}
}

// Fix round 2: a receiver whose parent was removed lands in the unowned
// ledger; the sender still gets its answer.
func TestPR5_MissingParentReceiverReplyReachesSender(t *testing.T) {
	f := newPR5Fixture(t)
	f.child.ParentSessionID = "removed-parent"
	sib := pr5Sibling(f.child.ProjectPath)
	f.saveRegistry(t, sib)

	f.runTaggedTurn(t, sib.ID)

	pr5AssertOneReply(t, f, sib.ID)
}

// Fix round 2: a reply is routed back only to a Claude-compatible sender;
// any other tool gets no record (it has no prompt-time drain, so the record
// would sit undrained and block later ones) and no wake.
func TestPR5_NonClaudeSenderGetsNoReply(t *testing.T) {
	for _, tool := range []string{"shell", "codex"} {
		t.Run(tool, func(t *testing.T) {
			f := newPR5Fixture(t)
			sib := pr5Sibling(f.child.ProjectPath)
			sib.Tool = tool
			f.saveRegistry(t, sib)

			f.runTaggedTurn(t, sib.ID)

			if InboxHasPending(sib.ID) || f.woken[sib.ID] != 0 {
				t.Fatalf("a %s sender must get no reply record or wake: woken=%v", tool, f.woken)
			}
			if got := f.inboxRecords(t); len(got) != 1 || got[0].FromID != sib.ID {
				t.Fatalf("the parent keeps its copy: %+v", got)
			}
		})
	}
}

func TestPR5_EnvelopeRoundTripsThroughClassifier(t *testing.T) {
	if got := SendEnvelope(" abc-123 "); got != "[agent-deck from:abc-123]" {
		t.Fatalf("envelope: %q", got)
	}
	rec := transcriptTurnRecord{Type: "user"}
	rec.Message.Content = []byte(`"` + SendEnvelope("abc-123") + `\nhello"`)
	trigger, from := classifyTrigger(rec)
	if trigger != TurnTriggerSend || from != "abc-123" {
		t.Fatalf("classifier: %q %q", trigger, from)
	}
	if !HasSendEnvelope("  "+SendEnvelope("x")+"\nhi") || HasSendEnvelope("hi [agent-deck from:x]") {
		t.Fatal("HasSendEnvelope must match a leading envelope only")
	}
}

func TestPR5_ReplyRecordRendering(t *testing.T) {
	ev := TransitionNotificationEvent{ChildSessionID: "c9", ChildTitle: "api", ToStatus: "waiting", Tier: TurnTierUrgent, TargetKind: "reply", Text: "8443"}
	if got := FormatInboxRecords([]TransitionNotificationEvent{ev}, "h"); got != "h\n- [urgent] reply from=c9 api: waiting\n    8443\n" {
		t.Fatalf("FormatInboxRecords: %q", got)
	}
	if got := NudgeHeadline(ev); !strings.HasPrefix(got, "[INBOX] reply · api (c9): waiting — 8443") {
		t.Fatalf("NudgeHeadline: %q", got)
	}
	ev.TargetKind = "parent"
	if got := FormatInboxRecords([]TransitionNotificationEvent{ev}, "h"); strings.Contains(got, "reply") {
		t.Fatalf("parent copy must render as before: %q", got)
	}
}

func TestPR5_IdentityPromptExplainsEnvelope(t *testing.T) {
	inst := &Instance{ID: "id-1", Title: "t", Tool: "claude"}
	if !strings.Contains(inst.BuildIdentityPrompt(), "Messages from other agent-deck sessions start with `[agent-deck from:<id>]`; reply by answering normally, the sender is notified.") {
		t.Fatal("identity prompt must explain the envelope")
	}
	// A session whose turns are never reported cannot promise the sender
	// is notified: it is told to answer with an explicit send.
	quiet := &Instance{ID: "id-2", Title: "t", Tool: "claude", NoTransitionNotify: true}
	got := quiet.BuildIdentityPrompt()
	if strings.Contains(got, "the sender is notified") ||
		!strings.Contains(got, "this session's turns are not reported, so reply with `agent-deck session send <id> \"answer\"`") {
		t.Fatalf("no-notify identity prompt must ask for an explicit reply:\n%s", got)
	}
}

func TestPR5_TagSendsConfigDefaultsOn(t *testing.T) {
	var nilCfg *UserConfig
	off := false
	if !nilCfg.GetTagSends() || !(&UserConfig{}).GetTagSends() {
		t.Fatal("tag_sends defaults to true")
	}
	if (&UserConfig{Send: SendSettings{TagSends: &off}}).GetTagSends() {
		t.Fatal("tag_sends = false must turn tagging off")
	}
}

// pollTurns runs one more daemon pass over the fixture's unchanged statuses
// (a turn whose parent commit failed is retried on every pass).
func (f *pr5Fixture) pollTurns(t *testing.T) {
	t.Helper()
	statuses := map[string]string{f.child.ID: "waiting", f.parent.ID: "waiting"}
	f.d.recordTerminalTurns("default", f.byID, statuses, nil)
}

// newSaturatedParentFixture is a stopped parent whose inbox already holds
// the per-child cap of undrained records from the fixture child, plus an
// idle Claude sibling that asks the child with a tagged send.
func newSaturatedParentFixture(t *testing.T) (*pr5Fixture, *Instance) {
	t.Helper()
	f := newPR5Fixture(t)
	f.parent.Status = StatusStopped
	sib := pr5Sibling(f.child.ProjectPath)
	f.saveRegistry(t, sib)
	fillPendingTurns(t, f.parent.ID, f.child.ID, maxPendingTurnsPerChild)
	return f, sib
}

// Fix round 3 (verify r2 finding 1): the parent's commit fails while its
// inbox is saturated (the turn is retried), but the sibling's answer must
// not wait on the parent's backlog. Retries must neither duplicate the
// reply nor wake the sender again.
func TestPR5_ParentBackpressureDoesNotHoldSiblingReply(t *testing.T) {
	f, sib := newSaturatedParentFixture(t)

	f.runTaggedTurn(t, sib.ID)
	pr5AssertOneReply(t, f, sib.ID)
	if LastTurnJournalEntry(f.child.ID) != nil {
		t.Fatal("the parent commit failed: the turn must stay unjournaled so it is retried")
	}

	for i := 0; i < 3; i++ {
		f.pollTurns(t)
	}
	pr5AssertOneReply(t, f, sib.ID)
	if f.woken[f.parent.ID] != 0 {
		t.Fatalf("a stopped parent is never woken: %v", f.woken)
	}
}

// Fix round 3 (verify r2 finding 1): once the sender consumed the reply,
// later retries of the same turn (parent still saturated, then drained)
// commit the parent's copy only: no second reply record, no second wake.
func TestPR5_ParentBackpressureReplyNotRedeliveredAfterDrains(t *testing.T) {
	f, sib := newSaturatedParentFixture(t)

	f.runTaggedTurn(t, sib.ID)
	text, _, err := DrainForPrompt(sib.ID)
	if err != nil || !strings.Contains(text, "reply from="+f.child.ID) {
		t.Fatalf("the sender must drain its reply while the parent is saturated: err=%v\n%s", err, text)
	}

	f.pollTurns(t)
	if InboxHasPending(sib.ID) {
		t.Fatal("a consumed reply must not be committed again by a retry")
	}

	if _, err := DrainInboxForParent(f.parent.ID); err != nil {
		t.Fatalf("parent drain: %v", err)
	}
	f.pollTurns(t)
	got := f.inboxRecords(t)
	if len(got) != 1 || got[0].FromID != sib.ID || got[0].TargetKind != "parent" {
		t.Fatalf("the parent gets its copy once it has room: %+v", got)
	}
	if InboxHasPending(sib.ID) || f.woken[sib.ID] != 1 {
		t.Fatalf("the sender is answered and woken exactly once: pending=%v woken=%v", InboxHasPending(sib.ID), f.woken)
	}
}

// Fix round 3 (verify r2 finding 2): an idle Claude parent that is NOT a
// conductor asked its own child with a tagged send. The child's answer is a
// reply to it, so it is woken (as a reply target) while the record stays
// one "parent" copy. Other parents keep the conductor-only gate.
func TestPR5_ParentThatAskedIsWokenForTheReply(t *testing.T) {
	for _, tc := range []struct {
		name, title, tool string
		tagged            bool
		wantWoken         bool
		wantKind          string
	}{
		{"non-conductor claude parent asked", "api-lead", "claude", true, true, "reply"},
		{"non-conductor claude parent, human turn", "api-lead", "claude", false, false, "parent"},
		{"non-conductor shell parent asked", "api-lead", "shell", true, false, "parent"},
		{"conductor codex parent asked", "conductor-ops", "codex", true, true, "parent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPR5Fixture(t)
			f.parent.Title = tc.title
			f.parent.Tool = tc.tool
			f.saveRegistry(t)

			// A reply that asks back is urgent; a plain answer would be info
			// and wake nobody (conductor ruling 2026-10-03).
			const reply = "The API listens on 8443.\nNEED: should 8080 stay open too?"
			prompt := "which port does the API use?"
			if tc.tagged {
				prompt = SendEnvelope(f.parent.ID) + "\n" + prompt
			}
			f.appendTurn(t, fxHuman("u0", prompt), fxAssistantText("a0", reply))
			f.pollTurns(t)

			got := f.inboxRecords(t)
			if len(got) != 1 || got[0].TargetKind != "parent" || got[0].Tier != TurnTierUrgent {
				t.Fatalf("the parent holds exactly its own urgent copy: %+v", got)
			}
			if woken := f.woken[f.parent.ID] == 1; woken != tc.wantWoken || f.kinds[f.parent.ID] != tc.wantKind {
				t.Fatalf("woken=%v kinds=%v, want woken=%v kind=%q", f.woken, f.kinds, tc.wantWoken, tc.wantKind)
			}
			if len(f.woken) > 1 {
				t.Fatalf("only the parent may be woken: %v", f.woken)
			}
		})
	}
}
