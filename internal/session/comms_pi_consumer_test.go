package session

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/comms"
	"github.com/asheshgoplani/agent-deck/internal/events"
)

// PR-B commit 3a: a listed pi parent is a ledger consumer like Claude (R1),
// its prompt hook is before_agent_start, and an attempt is bound to its wake by
// a nonce (design C). Tests that need a nonce or a new field build it through
// JSON, so they compile against today's types and fail on behaviour.

// newPiParentFixture is newDeliverFixture with a pi parent.
func newPiParentFixture(t *testing.T) *deliverFixture { return newPiParentFixtureIn(t, "") }

// newPiParentFixtureIn runs the pass in its own profile when one is named. A
// test that publishes to the events bus needs one: the process-wide bus caches
// a profile's directory at its first publish, and a later test in a different
// HOME would write into a directory the first test already removed.
func newPiParentFixtureIn(t *testing.T, profile string) *deliverFixture {
	t.Helper()
	f := &deliverFixture{commsFixture: newCommsFixture(t), clock: time.Now(), profile: profile}
	// A listed pi parent whose launch exported the socket: only such a pi enrols.
	f.parent.Tool, f.parent.PiSocketExported = "pi", true
	if profile != "" {
		f.parent.ID, f.parent.Title = "pi-"+profile, "conductor-"+profile
		f.child.ID, f.child.ParentSessionID = "kid-"+profile, f.parent.ID
		f.byID = map[string]*Instance{f.parent.ID: f.parent, f.child.ID: f.child}
		storage, err := NewStorageWithProfile(profile)
		if err != nil {
			t.Fatal(err)
		}
		if err := storage.Save([]*Instance{f.parent, f.child}); err != nil {
			t.Fatal(err)
		}
		_ = storage.Close()
	}
	t.Cleanup(SetCommsConsumersForTest([]string{f.parent.Title}))
	prevNow := commsNow
	commsNow = func() time.Time { return f.clock }
	t.Cleanup(func() { commsNow = prevNow })
	f.d.notifier.wake.send = func(_ *Instance, _ string, msg string) error {
		f.sent = append(f.sent, msg)
		*f.sends++
		return nil
	}
	f.l = f.d.commsLedgerFor(f.profileName())
	if f.l == nil {
		t.Fatal("ledger did not open")
	}
	f.deliver(t, "waiting")
	return f
}

// carryAttempt puts one wake attempt, bound to nonce, in flight for the record
// (attempted, as after Do #1 and before any socket reply). This is the single
// place a test calls Carry: when Carry takes the nonce, only this changes.
func (f *deliverFixture) carryAttempt(t *testing.T, nonce string, preview bool, id string) {
	t.Helper()
	_, err := f.l.Reader().Do(f.parent.ID, func(p comms.Pass) ([]events.Cursor, error) {
		var take []comms.Exported
		for _, e := range p.Pending {
			if e.Record.ID == id {
				take = append(take, e)
			}
		}
		if len(take) != 1 {
			t.Fatalf("record %s is not pending", id)
		}
		p.File.Carry(take, comms.ViaWake, f.clock.UnixMilli())
		if preview {
			p.File.MarkPreview(exportedCursors(take))
		}
		// The nonce through JSON: the field is the coder's to add.
		raw, _ := json.Marshal(p.File.Inflight)
		var m []map[string]any
		_ = json.Unmarshal(raw, &m)
		for i := range m {
			if c, _ := m[i]["cursor"].(float64); events.Cursor(c) == take[0].Cursor {
				m[i]["nonce"] = nonce
			}
		}
		raw, _ = json.Marshal(m)
		p.File.Inflight = nil
		_ = json.Unmarshal(raw, &p.File.Inflight)
		p.File.LastWake = f.clock.UnixMilli()
		p.File.AutoWakes++
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *deliverFixture) setAutoWakes(t *testing.T, n int) {
	t.Helper()
	if _, err := f.l.Reader().Do(f.parent.ID, func(p comms.Pass) ([]events.Cursor, error) {
		p.File.AutoWakes = n
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
}

// inflight returns the consumer's attempts as generic maps, so the nonce is
// readable before the field exists.
func (f *deliverFixture) inflight(t *testing.T) []map[string]any {
	t.Helper()
	raw, err := json.Marshal(f.state(t).Inflight)
	if err != nil {
		t.Fatal(err)
	}
	var m []map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

func inflightFor(m []map[string]any, id string) map[string]any {
	for _, in := range m {
		if in["id"] == id {
			return in
		}
	}
	return nil
}

// promptEdge spools the prompt_start edge pi's before_agent_start produces and
// ingests it: the line the run started with, and the nonce when it was a doorbell.
func (f *deliverFixture) promptEdge(t *testing.T, prompt, nonce string) {
	t.Helper()
	body := map[string]any{"harness": "pi", "event": "before_agent_start", "edge": CommsEdgePromptStart,
		"instance": f.parent.ID, "prompt": prompt, "t_signal": f.clock.UnixMilli()}
	if nonce != "" {
		body["wake_nonce"] = nonce
	}
	raw, _ := json.Marshal(body)
	var e CommsSpoolEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	spoolTurn(t, e)
	f.d.ingestCommsSpool("default", f.byID)
}

func TestCommsConsumes_ListedPiParentEnrols(t *testing.T) {
	pi := NewInstanceWithTool("pi-parent", t.TempDir(), "pi")
	pi.PiSocketExported = true
	for _, list := range [][]string{{"*"}, {pi.ID}, {"pi-parent"}} {
		if !commsConsumes(pi, list, map[string]int{"pi-parent": 1}) {
			t.Errorf("a socket-exported pi session listed by %v is not a ledger consumer", list)
		}
	}
	pi.PiSocketExported = false
	for _, list := range [][]string{{"*"}, {pi.ID}, {"pi-parent"}} {
		if commsConsumes(pi, list, map[string]int{"pi-parent": 1}) {
			t.Errorf("a pi session listed by %v whose launch exported no socket is a ledger consumer", list)
		}
	}
	f := newPiParentFixture(t)
	en, ok := LoadCommsEnrollment(f.parent.ID)
	if !ok || en.Tool != "pi" || en.Draining {
		t.Fatalf("the listed pi parent's marker: %+v (found %v), want an enrolled Tool pi", en, ok)
	}
}

// R1: no enrolment for an unlisted pi parent, no injection, and the typed line
// stays as PR-A left it.
func TestCommsConsumes_UnlistedPiParentDoesNot(t *testing.T) {
	f := &deliverFixture{commsFixture: newCommsFixture(t), clock: time.Now()}
	f.parent.Tool = "pi"
	t.Cleanup(SetCommsConsumersForTest([]string{"somebody-else"}))
	f.l = f.d.commsLedgerFor("default")
	f.d.deliverCommsLedger("default", f.byID, map[string]string{f.parent.ID: "waiting", f.child.ID: "waiting"})
	if _, ok := LoadCommsEnrollment(f.parent.ID); ok {
		t.Fatal("an unlisted pi parent was enrolled")
	}
	if dl, ok := LedgerPromptContext(f.parent.ID, "hello", ""); ok {
		dl.Done(false)
		t.Fatal("an unlisted pi parent got ledger context: it must keep the inbox path")
	}
	if commsConsumes(f.parent, []string{"somebody-else"}, map[string]int{}) {
		t.Fatal("commsConsumes admitted an unlisted pi session")
	}
}

// Forge 3 / Max 2: an attempt is acked by the prompt edge that carries its own
// nonce, whatever text the edge shows; another attempt, and an unknown nonce,
// are left alone; and a prompt edge that carries a nonce never resets the
// automatic wake count, while the parent's own prompt does.
func TestAckShownInPrompt_NonceAcksOnlyItsAttemptAndNeverResetsCaps(t *testing.T) {
	f := newPiParentFixture(t)
	r1 := f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: "first result"})
	r2 := f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: "second result"})
	f.carryAttempt(t, "NONCEONE", false, r1.ID)
	f.carryAttempt(t, "NONCETWO", false, r2.ID)
	f.setAutoWakes(t, 3)

	// A wake edge naming a nonce nobody carried acks nothing.
	f.promptEdge(t, comms.WakePrefix+" 1 pending", "NONCEUNKNOWN")
	if got := f.inflight(t); len(got) != 2 {
		t.Fatalf("an unknown nonce acked something: %v", got)
	}

	// The edge for the first wake: no ids on the line, only the nonce.
	f.promptEdge(t, comms.WakePrefix+" 1 pending", "NONCEONE")
	st := f.state(t)
	if inflightFor(f.inflight(t), r1.ID) != nil {
		t.Errorf("the attempt whose nonce the edge carried is still in flight: %v", f.inflight(t))
	}
	if in := inflightFor(f.inflight(t), r2.ID); in == nil || in["state"] != comms.ReceiptAttempted {
		t.Errorf("the other attempt must stay in flight and attempted: %v", f.inflight(t))
	}
	if st.Pending != 1 {
		t.Errorf("pending = %d, want 1 (only the second record is still owed)", st.Pending)
	}
	if st.AutoWakes != 3 {
		t.Errorf("a doorbell's prompt edge reset the wake count to %d", st.AutoWakes)
	}

	// The same edge again changes nothing.
	f.promptEdge(t, comms.WakePrefix+" 1 pending", "NONCEONE")
	if st2 := f.state(t); st2.Pending != 1 || st2.AutoWakes != 3 {
		t.Errorf("a replayed edge changed the state: %+v", st2)
	}

	// The parent's own prompt: no nonce, not a wake line.
	f.promptEdge(t, "what is the status?", "")
	if st3 := f.state(t); st3.AutoWakes != 0 {
		t.Errorf("the parent's own prompt left the wake count at %d, want 0", st3.AutoWakes)
	}
	if inflightFor(f.inflight(t), r2.ID) == nil {
		t.Error("the parent's own prompt acked an attempt it did not show")
	}
}

// Forge 6: a pi parent is injected at before_agent_start, so a preview in a
// wake line shows it only the start of a record: not an ack.
func TestAckShownInPrompt_PiPreviewIsNotAnAck(t *testing.T) {
	f := newPiParentFixture(t)
	long := f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: "start " + strings.Repeat("x", 2000) + " TAILMARKER"})
	f.carryAttempt(t, "NONCEPREVIEW", true, long.ID)
	f.promptEdge(t, comms.WakePrefix+" 1 pending", "NONCEPREVIEW")
	if in := inflightFor(f.inflight(t), long.ID); in == nil {
		t.Fatalf("a preview was acked as delivered: %v", f.inflight(t))
	}
	if f.state(t).Pending != 1 {
		t.Fatal("the previewed record is no longer owed")
	}
	// The run's own context carries it whole.
	dl, ok := LedgerPromptContext(f.parent.ID, comms.WakePrefix+" 1 pending", "")
	if !ok || !strings.Contains(dl.Text, "TAILMARKER") {
		t.Fatalf("the previewed record must arrive whole in the injection (ok %v)", ok)
	}
	dl.Done(true)
}

// Max 2 (hook side): LedgerPromptContext(id, prompt, nonce) acks the attempt
// its nonce names and injects what else is owed, without touching the wake count.
func TestLedgerPromptContext_NonceAcksItsAttemptAndInjectsTheRest(t *testing.T) {
	f := newPiParentFixture(t)
	r1 := f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: "first result"})
	r2 := f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: "second result"})
	f.carryAttempt(t, "NONCEONE", false, r1.ID)
	f.carryAttempt(t, "NONCETWO", false, r2.ID)
	f.setAutoWakes(t, 3)

	dl, ok := LedgerPromptContext(f.parent.ID, comms.WakePrefix+" 1 pending", "NONCEONE")
	if !ok {
		t.Fatal("enrolled pi parent fell back to the inbox")
	}
	if strings.Contains(dl.Text, "first result") {
		t.Errorf("the record the nonce names was injected again: %q", dl.Text)
	}
	if !strings.Contains(dl.Text, "second result") {
		t.Errorf("the other record owed was not injected: %q", dl.Text)
	}
	dl.Done(true)
	if st := f.state(t); st.AutoWakes != 3 {
		t.Errorf("a doorbell's hook reset the wake count to %d", st.AutoWakes)
	}
}

// The injection is plain context in one budget with the inbox.
func TestLedgerPromptContext_PiInjectsPendingAndInboxWithinBudget(t *testing.T) {
	f := newPiParentFixture(t)
	for i := 0; i < 8; i++ {
		f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: fmt.Sprintf("ledger %d %s", i, strings.Repeat("l", 580))})
		if err := CommitToInbox(f.parent.ID, TransitionNotificationEvent{ChildSessionID: fmt.Sprintf("r1:kid%d", i), ChildTitle: "kid", ToStatus: "waiting", Tier: TurnTierInfo,
			Text: fmt.Sprintf("inbox %d %s", i, strings.Repeat("i", 580)), TargetKind: "parent", Profile: "default", SourceRemote: "r1"}); err != nil {
			t.Fatal(err)
		}
	}
	dl, ok := LedgerPromptContext(f.parent.ID, "hi", "")
	if !ok {
		t.Fatal("enrolled pi parent fell back to the inbox")
	}
	if len(dl.Text) > 10000 || !strings.Contains(dl.Text, "ledger 0") || !strings.Contains(dl.Text, "inbox 0") {
		t.Fatalf("one budget for both stores: %d bytes\n%s", len(dl.Text), dl.Text)
	}
	if strings.HasPrefix(strings.TrimSpace(dl.Text), "{") {
		t.Errorf("a pi injection is plain text, not Claude's JSON envelope: %.80q", dl.Text)
	}
	if !InboxHasPending(f.parent.ID) {
		t.Fatal("what did not fit stays queued for the next prompt")
	}
	dl.Done(true)
}

// A wake record the send leg spooled keeps the outcome it carries; one without
// a state is the typed record it always was.
func TestCommsIngest_WakeSpoolStateIsKept(t *testing.T) {
	f := newPiParentFixture(t)
	spool := func(state, via, ref string) {
		body := map[string]any{"harness": "pi", "event": "inbox", "edge": CommsEdgeWake, "instance": f.parent.ID,
			"via": via, "ref": ref, "text": "[agent-deck msg] 1 pending", "t_signal": f.clock.UnixMilli()}
		if state != "" {
			body["state"] = state
		}
		raw, _ := json.Marshal(body)
		var e CommsSpoolEntry
		if err := json.Unmarshal(raw, &e); err != nil {
			t.Fatal(err)
		}
		spoolTurn(t, e)
	}
	for _, state := range []string{"landed", "queued", "pushed"} {
		spool(state, "socket", "NONCE-"+state)
	}
	spool("", "tmux", "NONCE-typed")
	f.d.ingestCommsSpool("default", f.byID)
	got := map[string]comms.Record{}
	for _, r := range f.wakeRecords(t) {
		got[r.Ref] = r
	}
	for _, state := range []string{"landed", "queued", "pushed"} {
		r, ok := got["NONCE-"+state]
		if !ok || r.State != state || r.Via != "socket" {
			t.Errorf("the %s wake record is %+v (found %v), want State %q Via socket", state, r, ok, state)
		}
	}
	if r := got["NONCE-typed"]; r.State != comms.StateTyped || r.Via != "tmux" {
		t.Errorf("a wake spooled without a state must stay typed via tmux: %+v", r)
	}
}

// Forge 12: pi's new prompt edge arrives at before_agent_start, before the run's
// turn_end. A turn classified from an early edge must get the tier and trigger
// it got when the prompt travelled with the turn (PR-A).
func TestCommsIngest_PiEarlyPromptEdgeKeepsPRATiers(t *testing.T) {
	for name, prompt := range map[string]string{
		"a background notification": "<task-notification>background task complete</task-notification>",
		"a send from a parent":      "[agent-deck from:conductor-1] build it",
		"a human prompt":            "please look at the failing test",
	} {
		t.Run(name, func(t *testing.T) {
			f, pi := newPiCommsFixture(t)
			late := piTurn(pi, "entry-late", "Built and tested.\n===AGENTDECK_DONE=== status=ok summary=done", prompt)
			spoolTurn(t, late)
			f.d.ingestCommsSpool("default", f.byID)

			other := NewInstanceWithTool("pi-worker-2", t.TempDir(), "pi")
			other.ID = "pi-2471"
			other.ParentSessionID = f.parent.ID
			other.Status = StatusWaiting
			f.byID[other.ID] = other
			saveCommsFixtureInstances(t, other)
			spoolTurn(t, CommsSpoolEntry{Harness: "pi", Event: "before_agent_start", Edge: CommsEdgePromptStart, Instance: other.ID, Prompt: prompt})
			f.d.ingestCommsSpool("default", f.byID)
			early := piTurn(other, "entry-early", "Built and tested.\n===AGENTDECK_DONE=== status=ok summary=done", "")
			early.SessionID = "pi-sess-1"
			spoolTurn(t, early)
			f.d.ingestCommsSpool("default", f.byID)

			var a, b *comms.Record
			for _, r := range f.ledgerRecords(t) {
				r := r
				if r.Kind != comms.KindTurn {
					continue
				}
				switch r.From {
				case pi.ID:
					a = &r
				case other.ID:
					b = &r
				}
			}
			if a == nil || b == nil {
				t.Fatalf("turns: late %v early %v", a, b)
			}
			if a.Tier != b.Tier || a.Trigger != b.Trigger || a.ReplyTo != b.ReplyTo {
				t.Errorf("an early prompt edge changed the classification: with the turn (tier %q trigger %q reply %q), early (tier %q trigger %q reply %q)",
					a.Tier, a.Trigger, a.ReplyTo, b.Tier, b.Trigger, b.ReplyTo)
			}
		})
	}
}
