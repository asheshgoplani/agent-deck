package session

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/comms"
)

// Comms Ledger delivery canary ([comms] consumers, docs/comms.md
// "Delivery"): the inbox keeps every record, wake, digest and Stop block.
// The ledger adds prompt context, deduplicated by exact transcript turn
// identity, and wakes or Stop-blocks only for ledger-only urgent records.

type deliverFixture struct {
	*commsFixture
	l     *comms.Ledger
	sent  []string
	clock time.Time
}

func newDeliverFixture(t *testing.T) *deliverFixture {
	t.Helper()
	f := &deliverFixture{commsFixture: newCommsFixture(t), clock: time.Now()}
	t.Cleanup(SetCommsConsumersForTest([]string{f.parent.Title}))
	prevNow := commsNow
	commsNow = func() time.Time { return f.clock }
	t.Cleanup(func() { commsNow = prevNow })
	f.d.notifier.wake.send = func(_ *Instance, _ string, msg string) error {
		f.sent = append(f.sent, msg)
		*f.sends++
		return nil
	}
	f.l = f.d.commsLedgerFor("default")
	if f.l == nil {
		t.Fatal("ledger did not open")
	}
	f.deliver(t, "waiting") // enrolls the parent
	if _, ok := LoadCommsEnrollment(f.parent.ID); !ok {
		t.Fatal("listed parent not enrolled")
	}
	return f
}

func (f *deliverFixture) deliver(t *testing.T, parentStatus string) {
	t.Helper()
	f.d.deliverCommsLedger("default", f.byID, map[string]string{f.parent.ID: parentStatus, f.child.ID: "waiting"})
}

func (f *deliverFixture) commit(t *testing.T, r comms.Record) comms.Record {
	t.Helper()
	if r.To == nil {
		r.To = []string{f.parent.ID}
	}
	if r.From == "" {
		r.From = f.child.ID
	}
	if r.TRecord == 0 {
		r.TRecord = f.clock.UnixMilli()
	}
	got, _, err := f.l.Commit(r)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func (f *deliverFixture) state(t *testing.T) comms.ConsumerFile {
	t.Helper()
	st, _, err := comms.ReadConsumer(f.l.Reader().Dir, f.parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func (f *deliverFixture) wakeRecords(t *testing.T) []comms.Record {
	t.Helper()
	var out []comms.Record
	for _, r := range f.ledgerRecords(t) {
		if r.Kind == comms.KindWake {
			out = append(out, r)
		}
	}
	return out
}

func TestLedgerConsumer_UrgentWakesOnceWithTextAndThePromptAcknowledgesIt(t *testing.T) {
	f := newDeliverFixture(t)
	done := f.commit(t, comms.Record{Kind: comms.KindDelivery, State: comms.StateFailed, To: []string{f.parent.ID}, Tier: comms.TierUrgent, Text: "built it\n===AGENTDECK_DONE=== status=ok summary=shipped",
		Done: "ok", Summary: "shipped the build"})
	f.deliver(t, "waiting")
	if len(f.sent) != 1 {
		t.Fatalf("want one wake, got %d: %q", len(f.sent), f.sent)
	}
	line := f.sent[0]
	if !strings.HasPrefix(line, comms.WakePrefix) || !strings.Contains(line, "shipped the build") || !strings.Contains(line, "#"+comms.ShortID(done.ID)) || strings.Contains(line, "\n") {
		t.Fatalf("the wake line must carry the record's text and id on one line: %q", line)
	}
	if w := f.wakeRecords(t); len(w) != 1 || w[0].Trigger != "ledger" || len(w[0].Refs) != 1 || w[0].Refs[0] != done.ID || w[0].To[0] != f.parent.ID {
		t.Fatalf("wake record %+v", w)
	}
	// The same pass again, and a minute later: the wake is in flight, no second one.
	f.deliver(t, "waiting")
	f.clock = f.clock.Add(61 * time.Second)
	f.deliver(t, "running")
	if len(f.sent) != 1 {
		t.Fatalf("an urgent record wakes once: %q", f.sent)
	}
	// The turn the line started: the prompt hook acknowledges what it showed
	// and injects nothing more.
	dl, ok := LedgerPromptContext(f.parent.ID, line)
	if !ok {
		t.Fatal("enrolled parent fell back to the inbox path")
	}
	if dl.Text != "" {
		t.Fatalf("records the wake line showed must not be injected again: %q", dl.Text)
	}
	dl.Done(true)
	st := f.state(t)
	if st.Pending != 0 || len(st.Inflight) != 0 || st.Watermark != f.l.Cursor() {
		// the wake record (the last cursor) is not this consumer's news either
		t.Fatalf("after the prompt: %+v (cursor %d)", st, f.l.Cursor())
	}
	if st.AutoWakes != 1 {
		t.Fatalf("auto wakes %d", st.AutoWakes)
	}
}

func TestLedgerConsumer_InfoRidesOnTheNextOwnPromptAndNeverWakes(t *testing.T) {
	f := newDeliverFixture(t)
	info := f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: "step 2 of 5 done"})
	f.deliver(t, "waiting")
	if len(f.sent) != 0 {
		t.Fatalf("info must not wake: %q", f.sent)
	}
	dl, ok := LedgerPromptContext(f.parent.ID, "what's the status?")
	if !ok || !strings.Contains(dl.Text, "step 2 of 5 done") || !strings.Contains(dl.Text, "#"+comms.ShortID(info.ID)) {
		t.Fatalf("info must ride on the next own prompt: %+v", dl)
	}
	dl.Done(true)
	if st := f.state(t); len(st.Inflight) != 1 || st.Inflight[0].Via != comms.ViaPrompt || st.Inflight[0].State != comms.ReceiptTransportAccepted || st.Pending != 1 {
		t.Fatalf("injected records stay in flight until the turn ends: %+v", st)
	}
	// The turn ends: Stop confirms them; nothing urgent, no block.
	sdl, ok := LedgerStopDecision(f.parent.ID, false)
	if !ok || sdl.Blocked {
		t.Fatalf("stop: %+v", sdl)
	}
	sdl.Done(false)
	if st := f.state(t); st.Pending != 0 || len(st.Inflight) != 0 {
		t.Fatalf("after Stop: %+v", st)
	}
	// Nothing new: the next prompt injects nothing.
	dl, _ = LedgerPromptContext(f.parent.ID, "next")
	if dl.Text != "" {
		t.Fatalf("a delivered record came back: %q", dl.Text)
	}
	dl.Done(false)
}

func TestLedgerConsumer_ANeverPrintedInjectionIsShownAgain(t *testing.T) {
	f := newDeliverFixture(t)
	f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: "lost to a killed hook"})
	dl, _ := LedgerPromptContext(f.parent.ID, "hi")
	dl.Done(false) // the hook could not print
	again, _ := LedgerPromptContext(f.parent.ID, "hi again")
	if !strings.Contains(again.Text, "lost to a killed hook") {
		t.Fatalf("an injection that never reached stdout must be shown again: %q", again.Text)
	}
	again.Done(true)
}

func TestLedgerConsumer_PendingTurnsNeverCauseALedgerWake(t *testing.T) {
	for _, tier := range []string{comms.TierInfo, comms.TierUrgent} {
		t.Run(tier, func(t *testing.T) {
			f := newDeliverFixture(t)
			f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: tier, Text: "pending turn", TRecord: f.clock.Add(-20 * time.Minute).UnixMilli()})
			for i := 0; i < 4; i++ {
				f.deliver(t, "waiting")
				f.clock = f.clock.Add(16 * time.Minute)
			}
			if len(f.sent) != 0 || len(f.wakeRecords(t)) != 0 {
				t.Fatalf("pending turns never cause ledger wakes or digests: %q", f.sent)
			}
			dl, ok := LedgerPromptContext(f.parent.ID, "back")
			if !ok || strings.Count(dl.Text, "pending turn") != 1 {
				t.Fatalf("the pending turn must arrive once at the prompt: %+v", dl)
			}
			dl.Done(true)
		})
	}
}

func TestLedgerConsumer_BusyParentGetsUrgentAtItsStop(t *testing.T) {
	f := newDeliverFixture(t)
	info := f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: "fyi"})
	q := f.commit(t, comms.Record{Kind: comms.KindDelivery, State: comms.StateFailed, To: []string{f.parent.ID}, From: f.codex.ID, Tier: comms.TierUrgent, Text: "which branch should I use?", Q: true})
	f.deliver(t, "running")
	if len(f.sent) != 0 {
		t.Fatalf("a busy parent is never typed into: %q", f.sent)
	}
	dl, ok := LedgerStopDecision(f.parent.ID, false)
	if !ok || !dl.Blocked || dl.Decision.Decision != "block" || !strings.Contains(dl.Decision.Reason, "which branch") ||
		!strings.Contains(dl.Decision.Reason, "#"+comms.ShortID(q.ID)) || !strings.Contains(dl.Decision.Reason, "#"+comms.ShortID(info.ID)) {
		t.Fatalf("urgent at Stop, info riding along: %+v", dl)
	}
	dl.Done(true)
	// The continuation ends: its Stop confirms the records; nothing left.
	next, _ := LedgerStopDecision(f.parent.ID, true)
	if next.Blocked {
		t.Fatalf("delivered records blocked again: %+v", next)
	}
	next.Done(false)
	if st := f.state(t); st.Pending != 0 || len(st.Inflight) != 0 {
		t.Fatalf("after the continuation: %+v", st)
	}
	stops := 0
	for _, r := range f.ledgerRecords(t) {
		if r.Kind == comms.KindWake && r.Via == "stop" {
			stops++
		}
	}
	f.d.ingestCommsSpool("default", f.byID)
	for _, r := range f.ledgerRecords(t) {
		if r.Kind == comms.KindWake && r.Via == "stop" && r.Trigger == "ledger" {
			stops++
		}
	}
	if stops != 1 {
		t.Fatalf("the Stop block is one measured wake, got %d", stops)
	}
}

func TestLedgerConsumer_StopBlocksAreBounded(t *testing.T) {
	f := newDeliverFixture(t)
	for i := 0; i < MaxStopHookBlocks+2; i++ {
		f.commit(t, comms.Record{Kind: comms.KindDelivery, State: comms.StateFailed, To: []string{f.parent.ID}, Tier: comms.TierUrgent, Text: "again " + strings.Repeat("x", i+1), Q: true})
		dl, _ := LedgerStopDecision(f.parent.ID, i > 0)
		if want := i < MaxStopHookBlocks; dl.Blocked != want {
			t.Fatalf("block %d: blocked=%v want %v", i, dl.Blocked, want)
		}
		dl.Done(dl.Blocked)
	}
}

func TestLedgerConsumer_AutoWakesPauseAtTheCapUntilAnOwnTurn(t *testing.T) {
	f := newDeliverFixture(t)
	for i := 0; i < comms.MaxAutoWakes+2; i++ {
		f.commit(t, comms.Record{Kind: comms.KindDelivery, State: comms.StateFailed, To: []string{f.parent.ID}, Tier: comms.TierUrgent, Text: "urgent " + strings.Repeat("y", i+1), Q: true})
		f.clock = f.clock.Add(2 * time.Minute)
		before := len(f.sent)
		f.deliver(t, "waiting")
		if n := len(f.sent); n > before {
			// the woken turn shows the line and ends
			dl, _ := LedgerPromptContext(f.parent.ID, f.sent[n-1])
			dl.Done(true)
			sdl, _ := LedgerStopDecision(f.parent.ID, false)
			sdl.Done(false)
		}
	}
	if len(f.sent) != comms.MaxAutoWakes {
		t.Fatalf("automatic wakes must pause at %d, got %d", comms.MaxAutoWakes, len(f.sent))
	}
	var pause []comms.Record
	for _, r := range f.ledgerRecords(t) {
		if r.Kind == comms.KindError && strings.Contains(r.Err, "paused") {
			pause = append(pause, r)
		}
	}
	if len(pause) != 1 || pause[0].To[0] != f.parent.ID {
		t.Fatalf("the pause is one record for the parent: %+v", pause)
	}
	// The parent's own prompt resets the count and carries what waited.
	dl, _ := LedgerPromptContext(f.parent.ID, "ok, what happened?")
	if !strings.Contains(dl.Text, "paused") || !strings.Contains(dl.Text, "urgent") {
		t.Fatalf("own prompt: %q", dl.Text)
	}
	dl.Done(true)
	if st := f.state(t); st.AutoWakes != 0 || st.CapNoted {
		t.Fatalf("own turn must reset the cap: %+v", st)
	}
}

// Verifier round 1 (#2): what the inbox held before the parent was
// enrolled stays on the inbox path and is delivered.
func TestLedgerConsumer_EnrollmentKeepsWhatTheInboxAlreadyHeld(t *testing.T) {
	f := newCommsFixture(t)
	before := TransitionNotificationEvent{ChildSessionID: f.child.ID, ChildTitle: "board-zero", ToStatus: "waiting", Tier: TurnTierInfo,
		Text: "from before the canary", TargetKind: "parent", Profile: "default"}
	if err := CommitToInbox(f.parent.ID, before); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(SetCommsConsumersForTest([]string{f.parent.Title}))
	f.d.deliverCommsLedger("default", f.byID, map[string]string{f.parent.ID: "waiting"})
	dl, ok := LedgerPromptContext(f.parent.ID, "hi")
	if !ok || !strings.Contains(dl.Text, "from before the canary") {
		t.Fatalf("a record the inbox held at enrollment was lost: %+v", dl)
	}
	dl.Done(true)
}

// Verifier round 1 (#3, #8): a parent leaving the canary (or the ledger
// turned off) still gets every record the ledger owed it, then its marker
// goes; new records take the inbox path.
func TestLedgerConsumer_UnlistingDrainsWhatTheLedgerOwed(t *testing.T) {
	for _, how := range []string{"unlisted", "ledger off"} {
		t.Run(how, func(t *testing.T) {
			f := newDeliverFixture(t)
			for i := 0; i < 6; i++ {
				f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: fmt.Sprintf("owed %d %s", i, strings.Repeat("x", 2500))})
			}
			if how == "unlisted" {
				t.Cleanup(SetCommsConsumersForTest(nil))
				f.deliver(t, "waiting")
				if en, ok := LoadCommsEnrollment(f.parent.ID); !ok || !en.Draining {
					t.Fatalf("unlisting must drain, not drop: %+v ok=%v", en, ok)
				}
			} else {
				t.Cleanup(SetCommsLedgerForTest(false))
			}
			if commsDeliversTo(f.parent.ID) {
				t.Fatal("a draining parent is back on the inbox path")
			}
			f.clock = f.clock.Add(commsDrainGrace + time.Minute)
			seen := map[string]bool{}
			for turn := 0; turn < 6; turn++ {
				dl, ok := LedgerPromptContext(f.parent.ID, "next")
				if !ok {
					break
				}
				for i := 0; i < 6; i++ {
					if strings.Contains(dl.Text, fmt.Sprintf("owed %d ", i)) {
						seen[fmt.Sprintf("%d", i)] = true
					}
				}
				dl.Done(true)
				sdl, _ := LedgerStopDecision(f.parent.ID, false)
				sdl.Done(false)
			}
			if len(seen) != 6 {
				t.Fatalf("records the ledger owed were lost: saw %v", seen)
			}
			if _, ok := LoadCommsEnrollment(f.parent.ID); ok {
				t.Fatal("the marker must go once the ledger owes nothing")
			}
		})
	}
}

// Verifier round 1 (#6): a ledger pass that fails consumes nothing; the
// inbox path runs as before.
func TestLedgerConsumer_AFailedPassFallsBackToTheInbox(t *testing.T) {
	f := newDeliverFixture(t)
	f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierUrgent, Text: "urgent", Q: true})
	if err := os.WriteFile(comms.ConsumerPath(f.l.Reader().Dir, f.parent.ID), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := LedgerPromptContext(f.parent.ID, "hi"); ok {
		t.Fatal("a broken consumer state must fall back to the inbox path")
	}
	if _, ok := LedgerStopDecision(f.parent.ID, false); ok {
		t.Fatal("a broken consumer state must fall back to the inbox Stop drain")
	}
}

// Verifier round 1 (#7): when the ledger has nothing urgent the Stop hook
// does not block on it, so the inbox's Stop drain still blocks for a
// remote urgent record (hook_handler runs DrainForStopHook next).
func TestLedgerConsumer_RemoteUrgentStillBlocksAtStop(t *testing.T) {
	f := newDeliverFixture(t)
	remote := TransitionNotificationEvent{ChildSessionID: "r1:kid", ChildTitle: "kid", ToStatus: "waiting", Tier: TurnTierUrgent,
		Text: "remote question?", Question: true, TargetKind: "parent", Profile: "default", SourceRemote: "r1"}
	if err := CommitToInbox(f.parent.ID, remote); err != nil {
		t.Fatal(err)
	}
	dl, ok := LedgerStopDecision(f.parent.ID, false)
	if !ok || !dl.Blocked || dl.Decision.Decision != "block" || !strings.Contains(dl.Decision.Reason, "remote question?") {
		t.Fatalf("the ledger hook returns the inbox block for the remote record: %+v", dl)
	}
	dl.Done(true)
}

// Verifier round 1 (#5): a record too long for the line is previewed in
// the wake (never an empty wake), wakes once, and a Claude parent gets it
// whole in its context.
func TestLedgerConsumer_ALongRecordIsPreviewedOnceAndDeliveredWhole(t *testing.T) {
	f := newDeliverFixture(t)
	long := f.commit(t, comms.Record{Kind: comms.KindDelivery, State: comms.StateFailed, To: []string{f.parent.ID}, Tier: comms.TierUrgent, Q: true, Text: "decide: " + strings.Repeat("option ", 170) + "END"})
	for i := 0; i < 5; i++ {
		f.deliver(t, "waiting")
		f.clock = f.clock.Add(61 * time.Second)
	}
	// Never an empty wake, never one per minute: the wake carries a preview,
	// and a wake that did not take is retried once, no more.
	if len(f.sent) == 0 || len(f.sent) > commsMaxWakeTries || !strings.Contains(f.sent[0], "#"+comms.ShortID(long.ID)) || !strings.Contains(f.sent[0], "(cut;") {
		t.Fatalf("a preview wake, at most %d times: %d %q", commsMaxWakeTries, len(f.sent), f.sent)
	}
	dl, _ := LedgerPromptContext(f.parent.ID, f.sent[0])
	if !strings.Contains(dl.Text, "END") {
		t.Fatalf("a Claude parent gets the previewed record whole: %q", dl.Text)
	}
	dl.Done(true)
}

// Verifier round 1 (#10): a stopped or busy parent is not touched by the
// daemon's pass (no rewrite, no activity that would hold compaction).
func TestLedgerConsumer_ANonIdleParentIsNotTouched(t *testing.T) {
	f := newDeliverFixture(t)
	f.commit(t, comms.Record{Kind: comms.KindDelivery, State: comms.StateFailed, To: []string{f.parent.ID}, Tier: comms.TierUrgent, Q: true, Text: "q?"})
	path := comms.ConsumerPath(f.l.Reader().Dir, f.parent.ID)
	before, _ := os.ReadFile(path)
	for _, st := range []string{"stopped", "running", "error", ""} {
		f.deliver(t, st)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) || len(f.sent) != 0 {
		t.Fatalf("a non-idle parent's state changed or it was typed into: %d wakes", len(f.sent))
	}
}

// A Codex parent has no prompt-time injection installed: it stays on the
// inbox in this phase (verifier round 2: a wake-only path could strand
// records), while its children's records still reach the ledger.
func TestLedgerConsumer_ACodexParentStaysOnTheInbox(t *testing.T) {
	f := newDeliverFixture(t)
	cx := NewInstanceWithTool("conductor-codex", t.TempDir(), "codex")
	cx.ID, cx.Status = "codex-parent", StatusWaiting
	f.byID[cx.ID] = cx
	t.Cleanup(SetCommsConsumersForTest([]string{"*"}))
	f.d.deliverCommsLedger("default", f.byID, map[string]string{cx.ID: "waiting"})
	if _, ok := LoadCommsEnrollment(cx.ID); ok {
		t.Fatal("a Codex parent must not be enrolled")
	}
	if !commsDeliversTo(f.parent.ID) {
		t.Fatal(`"*" still enrolls the Claude parent`)
	}
}

// Verifier round 2 (#3): an unreadable consumer state is rebuilt at the
// enrollment point by the next daemon pass, so the records the inbox no
// longer holds are delivered (at least once), never stranded.
func TestLedgerConsumer_ABrokenStateIsRebuiltAndItsRecordsDelivered(t *testing.T) {
	f := newDeliverFixture(t)
	f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: "must not be stranded"})
	if err := os.WriteFile(comms.ConsumerPath(f.l.Reader().Dir, f.parent.ID), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := LedgerPromptContext(f.parent.ID, "hi"); ok {
		t.Fatal("a broken state falls back to the inbox path for this prompt")
	}
	f.deliver(t, "running") // any pass, idle or not
	dl, ok := LedgerPromptContext(f.parent.ID, "hi again")
	if !ok || !strings.Contains(dl.Text, "must not be stranded") {
		t.Fatalf("after the rebuild the record is delivered: %+v", dl)
	}
	dl.Done(true)
}

// Verifier round 2 (#10): a parent re-listed while it drains keeps what
// the ledger owed it; what the inbox delivered after it left is not shown
// again.
func TestLedgerConsumer_ReListingWhileDrainingDoesNotRepeatInboxDeliveries(t *testing.T) {
	f := newDeliverFixture(t)
	f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: "owed before leaving"})
	t.Cleanup(SetCommsConsumersForTest(nil))
	f.deliver(t, "running")
	f.clock = f.clock.Add(commsTransitionGrace + time.Minute)
	f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: "delivered by the inbox meanwhile"})
	t.Cleanup(SetCommsConsumersForTest([]string{f.parent.Title}))
	f.deliver(t, "running")
	dl, ok := LedgerPromptContext(f.parent.ID, "back")
	if !ok || !strings.Contains(dl.Text, "owed before leaving") || strings.Contains(dl.Text, "delivered by the inbox meanwhile") {
		t.Fatalf("re-list: %q", dl.Text)
	}
	dl.Done(true)
}

func TestLedgerConsumer_UnlistingHandsTheParentBackToTheInbox(t *testing.T) {
	f := newDeliverFixture(t)
	t.Cleanup(SetCommsConsumersForTest(nil))
	f.deliver(t, "waiting")
	if commsDeliversTo(f.parent.ID) {
		t.Fatal("unlisted parent still on the ledger path")
	}
	f.d.notifier.fireWakeNudge(f.parent, TransitionNotificationEvent{ChildSessionID: f.child.ID, Tier: TurnTierUrgent, TargetKind: "parent", Profile: "default"})
	if len(f.sent) != 1 {
		t.Fatalf("the inbox wakes it again: %q", f.sent)
	}
	// Nothing was owed: the first prompt after the drain grace removes the marker.
	f.clock = f.clock.Add(commsDrainGrace + time.Minute)
	if dl, ok := LedgerPromptContext(f.parent.ID, "hi"); ok {
		dl.Done(false)
	}
	if _, ok := LoadCommsEnrollment(f.parent.ID); ok {
		t.Fatal("a drained marker must go")
	}
	if _, ok := LedgerPromptContext(f.parent.ID, "hi"); ok {
		t.Fatal("no marker: the inbox path alone")
	}
}

func TestLedgerConsumer_InboxOnlyUrgentEdgesReachTheLedger(t *testing.T) {
	f := newDeliverFixture(t)
	f.d.commsInboxOnlyEdge(f.child, "running", "error", time.Now())
	f.d.ingestCommsSpool("default", f.byID)
	var status []comms.Record
	for _, r := range f.ledgerRecords(t) {
		if r.Kind == comms.KindStatus && r.From == f.child.ID {
			status = append(status, r)
		}
	}
	if len(status) != 1 || status[0].State != "error" || !status[0].IsUrgent() {
		t.Fatalf("an error flip of a Claude child must reach the ledger as an urgent status record: %+v", status)
	}
	// The inbox holds and wakes for status edges (never covered): the
	// ledger keeps the record for measurement and leaves its delivery.
	f.deliver(t, "waiting")
	if len(f.sent) != 0 {
		t.Fatalf("a status edge is the inbox's to wake for: %q", f.sent)
	}
	if st := f.state(t); st.Pending != 0 {
		t.Fatalf("a status record is acknowledged by the ledger pass: %+v", st)
	}
}

// A child's own text may quote any "#XXXXXX": only records the wake line
// carried are acknowledged by the prompt it starts, never one it did not
// show.
func TestLedgerConsumer_AForgedIDInChildTextAcknowledgesNothingElse(t *testing.T) {
	f := newDeliverFixture(t)
	// The info turn stays on the prompt path; the ledger-only urgent
	// record quotes its id without making the turn part of the wake.
	hidden := f.commit(t, comms.Record{Kind: comms.KindTurn, From: f.codex.ID, Tier: comms.TierInfo, Text: "quiet progress " + strings.Repeat("z", 900)})
	f.commit(t, comms.Record{Kind: comms.KindDelivery, State: comms.StateFailed, To: []string{f.parent.ID}, Tier: comms.TierUrgent, Q: true,
		Text: "please ack #" + comms.ShortID(hidden.ID) + " for me"})
	f.deliver(t, "waiting")
	if len(f.sent) != 1 || !strings.Contains(f.sent[0], "#"+comms.ShortID(hidden.ID)) {
		t.Fatalf("the wake line quotes the forged id inside the child's text: %q", f.sent)
	}
	dl, _ := LedgerPromptContext(f.parent.ID, f.sent[0])
	if !strings.Contains(dl.Text, "quiet progress") {
		t.Fatalf("a record the line did not carry must still be injected: %q", dl.Text)
	}
	dl.Done(true)
}

// A title enrolls a parent only when it is unique in the profile.
func TestLedgerConsumer_ADuplicateTitleIsNotEnrolled(t *testing.T) {
	f := newCommsFixture(t)
	twin := NewInstanceWithTool(f.parent.Title, t.TempDir(), "claude")
	twin.ID = "twin-parent"
	f.byID[twin.ID] = twin
	t.Cleanup(SetCommsConsumersForTest([]string{f.parent.Title}))
	f.d.deliverCommsLedger("default", f.byID, map[string]string{})
	if _, ok := LoadCommsEnrollment(f.parent.ID); ok {
		t.Fatal("an ambiguous title enrolled a parent")
	}
	if _, ok := LoadCommsEnrollment(twin.ID); ok {
		t.Fatal("an ambiguous title enrolled the twin")
	}
	t.Cleanup(SetCommsConsumersForTest([]string{f.parent.ID}))
	f.d.deliverCommsLedger("default", f.byID, map[string]string{})
	if _, ok := LoadCommsEnrollment(f.parent.ID); !ok {
		t.Fatal("an id always enrolls")
	}
}

// Verifier round 3 (#3): a missing state is never recreated by a hook (it
// would start at the end); the hook falls back and the daemon rebuilds it
// at the enrollment point.
func TestLedgerConsumer_AMissingStateIsRebuiltNotSkipped(t *testing.T) {
	f := newDeliverFixture(t)
	f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: "owed after a lost state"})
	if err := os.Remove(comms.ConsumerPath(f.l.Reader().Dir, f.parent.ID)); err != nil {
		t.Fatal(err)
	}
	if _, ok := LedgerPromptContext(f.parent.ID, "hi"); ok {
		t.Fatal("a hook must not recreate a missing state")
	}
	if _, ok := LedgerStopDecision(f.parent.ID, false); ok {
		t.Fatal("a hook must not recreate a missing state")
	}
	f.deliver(t, "running")
	dl, ok := LedgerPromptContext(f.parent.ID, "again")
	if !ok || !strings.Contains(dl.Text, "owed after a lost state") {
		t.Fatalf("the rebuilt state delivers what was owed: %+v", dl)
	}
	dl.Done(true)
}

// Verifier round 3 (#2): a hook pass that leaves records pending re-arms
// the daemon's pass, so an urgent record the Stop budget could not carry
// still wakes the parent once it is idle.
func TestLedgerConsumer_AHookPassReArmsTheDaemon(t *testing.T) {
	f := newDeliverFixture(t)
	f.commit(t, comms.Record{Kind: comms.KindDelivery, State: comms.StateFailed, To: []string{f.parent.ID}, Tier: comms.TierUrgent, Q: true, Text: "blocked on you"})
	f.deliver(t, "running") // busy: nothing typed
	stopBlockMu.Lock()
	_ = saveStopBlockCountLocked(f.parent.ID, MaxStopHookBlocks)
	stopBlockMu.Unlock()
	dl, _ := LedgerStopDecision(f.parent.ID, true) // budget spent: no block
	if dl.Blocked {
		t.Fatal("budget spent")
	}
	dl.Done(false)
	f.clock = f.clock.Add(2 * time.Minute)
	f.deliver(t, "waiting")
	if len(f.sent) != 1 || !strings.Contains(f.sent[0], "blocked on you") {
		t.Fatalf("the idle parent is woken for the urgent record: %q", f.sent)
	}
}

// Verifier round 3 (#5): a turn signalled before the parent left the canary
// but committed to the ledger later (a lagging spool) is still owed.
func TestLedgerConsumer_ALaggingTurnSignalledBeforeLeavingIsOwed(t *testing.T) {
	f := newDeliverFixture(t)
	t.Cleanup(SetCommsConsumersForTest(nil))
	f.deliver(t, "running")
	en, _ := LoadCommsEnrollment(f.parent.ID)
	f.clock = f.clock.Add(5 * time.Minute)
	f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: "late but owed", TSignal: en.DrainedAt - 1000, TRecord: f.clock.UnixMilli()})
	dl, ok := LedgerPromptContext(f.parent.ID, "hi")
	if !ok || !strings.Contains(dl.Text, "late but owed") {
		t.Fatalf("a turn signalled before leaving is delivered however late it lands: %+v", dl)
	}
	dl.Done(true)
}

// Verifier round 3 (#10): the ledger's records and the inbox's share one
// context budget under Claude's additionalContext limit.
func TestLedgerConsumer_OnePromptContextBudget(t *testing.T) {
	f := newDeliverFixture(t)
	for i := 0; i < 8; i++ {
		f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: fmt.Sprintf("ledger %d %s", i, strings.Repeat("l", 580))})
		if err := CommitToInbox(f.parent.ID, TransitionNotificationEvent{ChildSessionID: fmt.Sprintf("r1:kid%d", i), ChildTitle: "kid", ToStatus: "waiting", Tier: TurnTierInfo,
			Text: fmt.Sprintf("inbox %d %s", i, strings.Repeat("i", 580)), TargetKind: "parent", Profile: "default", SourceRemote: "r1"}); err != nil {
			t.Fatal(err)
		}
	}
	dl, _ := LedgerPromptContext(f.parent.ID, "hi")
	if len(dl.Text) > 10000 || !strings.Contains(dl.Text, "ledger 0") || !strings.Contains(dl.Text, "inbox 0") {
		t.Fatalf("one budget for both stores: %d bytes\n%s", len(dl.Text), dl.Text)
	}
	if !InboxHasPending(f.parent.ID) {
		t.Fatal("what did not fit stays queued for the next prompt")
	}
	dl.Done(true)
}

// Round 4 redesign: the inbox keeps every record and runs unchanged (its
// wakes included); the ledger adds the text at the prompt and the two never
// show the same transcript turn twice, matched by its exact identity.
func TestLedgerConsumer_TheInboxIsUntouchedAndTurnsAreShownOnce(t *testing.T) {
	f := newDeliverFixture(t)
	uuid := "aaaaaaaa-0000-4000-8000-000000000042"
	f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierUrgent, Q: true, Text: "which way?", Key: comms.Key(comms.KindTurn, f.child.ID, uuid)})
	twin := TransitionNotificationEvent{ChildSessionID: f.child.ID, ChildTitle: "board-zero", ToStatus: "waiting", Tier: TurnTierUrgent, Question: true,
		Text: "which way?", TurnUUID: uuid, LastOutputHash: (TurnFacts{UUID: uuid}).Signal(), Profile: "default"}
	completion := TransitionNotificationEvent{ChildSessionID: f.child.ID, ChildTitle: "board-zero", ToStatus: "waiting", Tier: TurnTierUrgent,
		Kind: transitionKindFinished, DoneStatus: "ok", DoneSummary: "run-task finished", Profile: "default"}
	for _, ev := range []TransitionNotificationEvent{twin, completion} {
		if committed, _, _ := f.d.notifier.commitEventToInbox(ev); !committed {
			t.Fatal("commit")
		}
	}
	if len(f.sent) == 0 {
		t.Fatal("the inbox still wakes the parent for urgent records")
	}
	f.deliver(t, "waiting")
	for _, line := range f.sent {
		if strings.HasPrefix(line, comms.WakePrefix) {
			t.Fatalf("the ledger never wakes for a turn the inbox holds: %q", line)
		}
	}
	dl, ok := LedgerPromptContext(f.parent.ID, "hi")
	if !ok || strings.Count(dl.Text, "which way?") != 1 || !strings.Contains(dl.Text, "run-task finished") {
		t.Fatalf("the turn once, the completion from the inbox: %q", dl.Text)
	}
	dl.Done(true)
	if InboxHasPending(f.parent.ID) {
		t.Fatal("the inbox twin is retired once the ledger showed the turn")
	}
	// The other direction: a turn the inbox showed first is not shown again
	// when the ledger commits it later. Supply the production turn signal too:
	// TurnUUID alone does not identify a turn to the inbox consumed ledger.
	uuid2 := "aaaaaaaa-0000-4000-8000-000000000043"
	if committed, _, _ := f.d.notifier.commitEventToInbox(TransitionNotificationEvent{ChildSessionID: f.child.ID, ChildTitle: "board-zero",
		ToStatus: "waiting", Tier: TurnTierInfo, Text: "later turn", TurnUUID: uuid2, LastOutputHash: (TurnFacts{UUID: uuid2}).Signal(), Profile: "default"}); !committed {
		t.Fatal("commit")
	}
	dl, _ = LedgerPromptContext(f.parent.ID, "next")
	if strings.Count(dl.Text, "later turn") != 1 {
		t.Fatalf("the inbox shows the turn the ledger does not hold yet: %q", dl.Text)
	}
	dl.Done(true)
	f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: "later turn", Key: comms.Key(comms.KindTurn, f.child.ID, uuid2)})
	dl, _ = LedgerPromptContext(f.parent.ID, "again")
	if strings.Contains(dl.Text, "later turn") {
		t.Fatalf("a turn the inbox showed must not be shown again by the ledger: %q", dl.Text)
	}
	dl.Done(true)
}

func TestLedgerConsumer_FinalPassFlipsSurviveShownTurn(t *testing.T) {
	for _, path := range []string{"prompt", "stop"} {
		for _, flip := range []string{"error", "stale"} {
			t.Run(path+"/"+flip, func(t *testing.T) {
				f := newDeliverFixture(t)
				uuid := "shown-turn"
				f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: "previous body", Key: comms.Key(comms.KindTurn, f.child.ID, uuid)})
				dl, _ := LedgerPromptContext(f.parent.ID, "first")
				dl.Done(true)
				ev := TransitionNotificationEvent{ChildSessionID: f.child.ID, FromStatus: "running", ToStatus: "waiting", Tier: TurnTierUrgent, Text: "new attention edge", TurnUUID: uuid, LastOutputHash: (TurnFacts{UUID: uuid}).Signal(), Timestamp: f.clock}
				if flip == "error" {
					ev.ToStatus = "error"
				} else {
					ev.OutputHashStale = true
				}
				if err := WriteInboxEvent(f.parent.ID, ev); err != nil {
					t.Fatal(err)
				}
				if path == "prompt" {
					dl, _ = LedgerPromptContext(f.parent.ID, "next")
				} else {
					dl, _ = LedgerStopDecision(f.parent.ID, false)
				}
				if !strings.Contains(dl.Text, ev.Text) || (path == "stop" && !dl.Blocked) {
					t.Fatalf("new edge lost: %+v", dl)
				}
				dl.Done(true)
				stats, err := ReadInboxStats(f.parent.ID)
				if err != nil || stats.ShadowedByLedger != 0 {
					t.Fatalf("edge counted as duplicate: %+v, %v", stats, err)
				}
			})
		}
	}
}

func TestLedgerConsumer_FinalPassDoneBodyAndStopDedup(t *testing.T) {
	for _, path := range []string{"prompt", "inbox-stop", "ledger-stop"} {
		t.Run(path, func(t *testing.T) {
			f := newDeliverFixture(t)
			uuid := "done-turn"
			key := comms.Key(comms.KindTurn, f.child.ID, uuid)
			f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierUrgent, Done: "ok", Summary: "shipped", Text: "XYZBODY details", Key: key})
			ev := TransitionNotificationEvent{ChildSessionID: f.child.ID, ToStatus: "waiting", Tier: TurnTierUrgent, Kind: transitionKindFinished, DoneStatus: "ok", DoneSummary: "shipped", Text: "XYZBODY details", TurnUUID: uuid, LastOutputHash: (TurnFacts{UUID: uuid}).Signal()}
			if err := WriteInboxEvent(f.parent.ID, ev); err != nil {
				t.Fatal(err)
			}
			if path == "ledger-stop" {
				f.commit(t, comms.Record{Kind: comms.KindDelivery, State: comms.StateFailed, Tier: comms.TierUrgent, Text: "send failed"})
			}
			var dl *LedgerDelivery
			if path == "prompt" {
				dl, _ = LedgerPromptContext(f.parent.ID, "first")
			} else {
				dl, _ = LedgerStopDecision(f.parent.ID, false)
			}
			if !strings.Contains(dl.Text, "shipped") || strings.Count(dl.Text, ev.Text) != 1 {
				t.Fatalf("summary and body required once: %q", dl.Text)
			}
			if path != "inbox-stop" && (!strings.Contains(dl.Text, "«") || !strings.Contains(dl.Text, "»")) {
				t.Fatalf("missing quotation: %q", dl.Text)
			}
			dl.Done(true)
			st := f.state(t)
			if !st.WasShown(key) {
				t.Fatal("delivered turn not remembered")
			}
			next, _ := LedgerPromptContext(f.parent.ID, "next")
			if strings.Contains(next.Text, ev.Text) || strings.Contains(next.Text, "shipped") {
				t.Fatalf("turn repeated: %q", next.Text)
			}
			next.Done(true)
			if InboxHasPending(f.parent.ID) {
				t.Fatal("twin still pending")
			}
		})
	}
}

func TestLedgerConsumer_FinalPassStopCountsShownTwin(t *testing.T) {
	f := newDeliverFixture(t)
	uuid := "shown-turn"
	f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierUrgent, Text: "pick one please", Key: comms.Key(comms.KindTurn, f.child.ID, uuid)})
	dl, _ := LedgerPromptContext(f.parent.ID, "first")
	dl.Done(true)
	if err := WriteInboxEvent(f.parent.ID, TransitionNotificationEvent{ChildSessionID: f.child.ID, ToStatus: "waiting", Tier: TurnTierUrgent, Text: "pick one please", TurnUUID: uuid, LastOutputHash: (TurnFacts{UUID: uuid}).Signal()}); err != nil {
		t.Fatal(err)
	}
	dl, _ = LedgerStopDecision(f.parent.ID, false)
	if dl.Blocked || dl.Text != "" {
		t.Fatalf("shown twin blocked Stop: %+v", dl)
	}
	dl.Done(false)
	stats, err := ReadInboxStats(f.parent.ID)
	if err != nil || stats.ShadowedByLedger != 1 {
		t.Fatalf("Stop duplicate count: %+v, %v", stats, err)
	}
}

func TestLedgerConsumer_FinalPassOnlyExactTurnKeys(t *testing.T) {
	base := TransitionNotificationEvent{ChildSessionID: "child", TurnUUID: "uuid", LastOutputHash: (TurnFacts{UUID: "uuid"}).Signal(), ToStatus: "waiting"}
	for _, kind := range []string{"turn", "finished", "no-signal", "other-signal", "stale", "error", "remote", "other-fingerprint"} {
		t.Run(kind, func(t *testing.T) {
			ev := base
			switch kind {
			case "finished":
				ev.Kind = transitionKindFinished
				ev.DoneStatus = "ok"
			case "no-signal":
				ev.LastOutputHash = ""
			case "other-signal":
				ev.LastOutputHash = "pane-hash"
			case "stale":
				ev.OutputHashStale = true
			case "error":
				ev.ToStatus = "error"
			case "remote":
				ev.SourceRemote = "peer"
			case "other-fingerprint":
				ev.TurnFingerprint = "custom"
			}
			want := ""
			if kind == "turn" || kind == "finished" {
				want = comms.Key(comms.KindTurn, "child", "uuid")
			}
			if got := inboxTurnKey(ev); got != want {
				t.Fatalf("key %q, want %q", got, want)
			}
		})
	}
}

func TestLedgerConsumer_QueuedLandedAndTypedNeverWakeSender(t *testing.T) {
	for _, state := range []string{comms.StateLanded, comms.StateTyped} {
		t.Run(state, func(t *testing.T) {
			f := newDeliverFixture(t)
			SpoolCommsSend(f.parent.ID, f.child.ID, "build it", "queue", "settled-send")
			SpoolCommsDelivery(f.parent.ID, f.child.ID, "settled-send", state, "queue", "settled: no transcript confirmation", true)
			f.d.ingestCommsSpool("default", f.byID)
			var found bool
			for _, r := range f.ledgerRecords(t) {
				if r.Kind == comms.KindDelivery {
					found = true
					if r.State != state || r.Tier != comms.TierInfo || len(r.To) != 1 || r.To[0] != f.parent.ID {
						t.Fatalf("sender delivery: %+v", r)
					}
				}
			}
			if !found {
				t.Fatal("missing delivery")
			}
			f.deliver(t, "waiting")
			if len(f.sent) != 0 {
				t.Fatalf("successful settled send woke sender: %q", f.sent)
			}
			dl, _ := LedgerStopDecision(f.parent.ID, false)
			if dl.Blocked {
				t.Fatalf("successful settled send blocked Stop: %+v", dl)
			}
			dl.Done(false)
		})
	}
}

func TestLedgerConsumer_QueuedFailureInboxOwnsNotification(t *testing.T) {
	for _, inboxFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(inboxFirst), func(t *testing.T) {
			f := newDeliverFixture(t)
			const reason = "composer refused the queued message"
			if err := CommitToInbox(f.parent.ID, TransitionNotificationEvent{
				ChildSessionID: f.child.ID, ChildTitle: f.child.Title, Profile: "default",
				ToStatus: "send_failed", LastOutputHash: "send:failed-request", Text: reason, Timestamp: time.Now(),
			}); err != nil {
				t.Fatal(err)
			}
			if inboxFirst {
				dl, _ := LedgerPromptContext(f.parent.ID, "first")
				if strings.Count(dl.Text, reason) != 1 {
					t.Fatalf("inbox notice: %q", dl.Text)
				}
				dl.Done(true)
			}
			SpoolCommsSend(f.parent.ID, f.child.ID, "build it", "queue", "failed-request")
			SpoolCommsInboxFailure(f.parent.ID, f.child.ID, "failed-request", "queue", reason)
			f.d.ingestCommsSpool("default", f.byID)
			var found bool
			for _, r := range f.ledgerRecords(t) {
				if r.Kind == comms.KindDelivery {
					found = true
					if r.Trigger != "inbox" || r.State != comms.StateFailed || r.Tier != comms.TierUrgent || len(r.To) != 1 || r.To[0] != f.parent.ID || r.Ref == "" {
						t.Fatalf("audit receipt must retain failure and routing: %+v", r)
					}
				}
			}
			if !found {
				t.Fatal("missing audit receipt")
			}
			f.deliver(t, "waiting")
			if len(f.sent) != 0 {
				t.Fatalf("ledger duplicated inbox wake: %q", f.sent)
			}
			dl, _ := LedgerPromptContext(f.parent.ID, "next")
			want := 1
			if inboxFirst {
				want = 0
			}
			if strings.Count(dl.Text, reason) != want {
				t.Fatalf("duplicate failure notice: %q", dl.Text)
			}
			dl.Done(true)
		})
	}
}
