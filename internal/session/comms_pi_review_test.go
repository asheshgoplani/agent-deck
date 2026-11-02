package session

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/comms"
	"github.com/asheshgoplani/agent-deck/internal/costs"
)

// PR-B commit 3, review round. R-A: a printed context is not proof pi took it.
// R-B: nothing is typed into a live pi. Plus the races and gates the reviews
// found undefended.

// quietBusTeardown keeps the test's HOME until the events bus writer has
// settled: the bus flushes every 50 ms, and a HOME removed under it makes the
// process-wide bus warn "disabled after write failure" in an otherwise green run.
// Registered after the HOME exists, so it runs before the HOME goes.
func quietBusTeardown(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { time.Sleep(300 * time.Millisecond) })
}

var tokenRE = regexp.MustCompile(`^[0-9A-Z]{26}$`)

// tokenOf reads LedgerDelivery.Token by reflection so a missing field fails the
// test, not the build.
func tokenOf(t *testing.T, dl *LedgerDelivery) string {
	t.Helper()
	f := reflect.ValueOf(dl).Elem().FieldByName("Token")
	if !f.IsValid() || f.Kind() != reflect.String {
		t.Fatal("LedgerDelivery has no string Token field: the hook cannot print the delivery token")
	}
	return f.String()
}

// injectedEdge spools the receipt pi's extension sends when it actually returned
// the context to pi: an "injected" edge naming the delivery token.
func (f *deliverFixture) injectedEdge(t *testing.T, token string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"harness": "pi", "event": "turn_start", "edge": "injected",
		"instance": f.parent.ID, "injected_token": token, "t_signal": f.clock.UnixMilli()})
	var e CommsSpoolEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	spoolTurn(t, e)
	f.d.ingestCommsSpool(f.profileName(), f.byID)
}

// R-A: printing the context does not accept the attempt, and the token that the
// extension must echo is recorded on it.
func TestLedgerPromptContext_PiInjectionIsNotAcceptedByPrinting(t *testing.T) {
	f := newPiParentFixture(t)
	rec := f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: "child result"})
	dl, ok := LedgerPromptContext(f.parent.ID, "carry on", "")
	if !ok || !strings.Contains(dl.Text, "child result") {
		t.Fatalf("no injection (ok %v)", ok)
	}
	token := tokenOf(t, dl)
	if !tokenRE.MatchString(token) {
		t.Errorf("token %q is not a ULID", token)
	}
	dl.Done(true) // hook-handler printed it
	in := inflightFor(f.inflight(t), rec.ID)
	if in == nil || in["state"] != comms.ReceiptAttempted || in["via"] != comms.ViaPrompt {
		t.Fatalf("a printed pi injection must stay attempted: %v", f.inflight(t))
	}
	if in["token"] != token {
		t.Errorf("the attempt records token %v, want the printed %q", in["token"], token)
	}
}

// R-A: the extension discards stdout on a timeout or a non-zero exit, and then
// echoes no token. The records are not accepted, not acked, and ride the next run.
func TestLedgerPromptContext_PiNoReceiptMeansTheRecordsRideTheNextRunStart(t *testing.T) {
	f := newPiParentFixture(t)
	f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: "child result"})
	dl, _ := LedgerPromptContext(f.parent.ID, "carry on", "")
	dl.Done(true)                          // printed, then the extension timed out: no receipt follows
	f.clock = f.clock.Add(2 * time.Minute) // past the receipt window
	dl2, ok := LedgerPromptContext(f.parent.ID, "carry on again", "")
	if !ok || !strings.Contains(dl2.Text, "child result") {
		t.Fatalf("without a receipt the record must be injected again (ok %v, text %q)", ok, dl2.Text)
	}
	if st := f.state(t); st.Pending != 1 {
		t.Errorf("the record was acked without a receipt: %+v", st)
	}
	// A new injection, a new token: the first one no longer names the attempt.
	if tokenOf(t, dl2) == tokenOf(t, dl) {
		t.Error("two injections reused one token")
	}
	dl2.Done(true)
}

// R-A: the token echoed back accepts exactly that attempt; the next prompt's
// settle then acks it. A stale token (an earlier injection of the same record)
// and a foreign one change nothing.
func TestCommsIngest_PiInjectedTokenAcceptsExactlyThatAttempt(t *testing.T) {
	f := newPiParentFixture(t)
	rec := f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: "child result"})
	first, _ := LedgerPromptContext(f.parent.ID, "run one", "")
	stale := tokenOf(t, first)
	first.Done(true)
	f.clock = f.clock.Add(2 * time.Minute) // past the receipt window: the record rides again
	second, _ := LedgerPromptContext(f.parent.ID, "run two", "")
	current := tokenOf(t, second)
	second.Done(true)

	f.injectedEdge(t, stale)
	f.injectedEdge(t, "01J0FOREIGNTOKEN00000000AB")
	if in := inflightFor(f.inflight(t), rec.ID); in == nil || in["state"] != comms.ReceiptAttempted {
		t.Fatalf("a stale or foreign token moved the attempt: %v", f.inflight(t))
	}
	f.injectedEdge(t, current)
	if in := inflightFor(f.inflight(t), rec.ID); in == nil || in["state"] != comms.ReceiptTransportAccepted {
		t.Fatalf("the current token did not accept its attempt: %v", f.inflight(t))
	}
	// Accepted is not acked: the next prompt settles it, and injects nothing more.
	next, ok := LedgerPromptContext(f.parent.ID, "run three", "")
	if !ok || strings.Contains(next.Text, "child result") {
		t.Errorf("an accepted record was injected again (ok %v): %q", ok, next.Text)
	}
	next.Done(true)
	if st := f.state(t); st.Pending != 0 || len(st.Inflight) != 0 {
		t.Errorf("after the settle: %+v", st)
	}
}

// Max 2 (iii): a doorbell's edge carries its nonce, and the real inbox line is
// "[INBOX] ...", not a ledger wake line. It must still leave the wake count alone.
func TestAckShownInPrompt_AnInboxDoorbellLineWithANonceNeverResetsTheCap(t *testing.T) {
	f := newPiParentFixture(t)
	r1 := f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: "first result"})
	f.carryAttempt(t, "NONCEINBOXLINE", false, r1.ID)
	f.setAutoWakes(t, 3)
	f.promptEdge(t, "[INBOX] urgent · kid: waiting — details are in this turn's context", "NONCEINBOXLINE")
	if st := f.state(t); st.AutoWakes != 3 {
		t.Errorf("an [INBOX] doorbell run reset the wake count to %d", st.AutoWakes)
	}
}

// Forge M1: the inbox doorbell is reserved synchronously, before its goroutine
// runs, so a ledger pass of the same poll cannot ring a second one.
func TestInboxNudge_PiDoorbellIsReservedBeforeTheGoroutineRuns(t *testing.T) {
	w := newPiWakeFixture(t, replyWith("submitted"))
	gate := make(chan struct{})
	orig := wakeNudgeGoroutineGate
	t.Cleanup(func() { wakeNudgeGoroutineGate = orig })
	wakeNudgeGoroutineGate = func() { <-gate }
	var released bool
	release := func() {
		if !released {
			released = true
			close(gate)
		}
	}
	defer release()
	w.d.notifier.wake.send = sendWakeNudge
	w.urgent(t, "ledger-only urgent record")
	ev := TransitionNotificationEvent{ChildSessionID: w.child.ID, ChildTitle: "kid", Profile: "default", ToStatus: "waiting",
		TargetSessionID: w.parent.ID, TargetKind: "parent", Tier: TurnTierUrgent, Text: "inbox result text", Timestamp: time.Now()}
	if err := CommitToInbox(w.parent.ID, ev); err != nil {
		t.Fatal(err)
	}
	w.d.notifier.fireWakeNudge(w.parent, ev) // dispatched; its goroutine is held at the gate
	w.deliver(t, "waiting")                  // the ledger pass of the same poll
	time.Sleep(300 * time.Millisecond)
	if n := w.ext.count(); n != 0 {
		t.Fatalf("the ledger pass rang a doorbell (%d frames) while the inbox doorbell was dispatched but not yet rung", n)
	}
	release()
	w.ext.waitFrame(t)
	time.Sleep(300 * time.Millisecond)
	if n := w.ext.count(); n != 1 {
		t.Errorf("frames = %d, want exactly the one doorbell", n)
	}
}

// Forge M2: the budget is evaluated outside the consumer lock; a budget load
// that blocks must not keep a hook from running.
func TestCommsDeliver_PiBudgetCheckRunsOutsideTheConsumerLock(t *testing.T) {
	w := newPiWakeFixture(t, replyWith("submitted"))
	block, entered := make(chan struct{}), make(chan struct{}, 1)
	orig := wakeBudgetCheck
	t.Cleanup(func() { wakeBudgetCheck = orig })
	wakeBudgetCheck = func(string, *Instance) costs.BudgetResult {
		entered <- struct{}{}
		<-block
		return costs.BudgetResult{Action: costs.BudgetActionNone}
	}
	var unblocked bool
	unblock := func() {
		if !unblocked {
			unblocked = true
			close(block)
		}
	}
	defer unblock()
	w.urgent(t, "shipped the build")
	done := make(chan struct{})
	go func() { w.deliver(t, "waiting"); close(done) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the pass never reached the budget check")
	}
	start := time.Now()
	dl, ok := LedgerPromptContext(w.parent.ID, "my own prompt", "")
	took := time.Since(start)
	unblock()
	<-done
	if ok {
		dl.Done(false)
	}
	if took > 750*time.Millisecond {
		t.Errorf("the hook waited %v while the budget check was blocked: the consumer lock was held across it", took)
	}
}

// groupOverByTargetAlone is a pi parent in group "team" whose own spend is over
// the group's daily limit, so a group check run on the target alone would stop
// the wake; corrupt then damages the registry the check would sum from.
func groupOverByTargetAlone(t *testing.T, label string, corrupt func(w *piWakeFx, db *sql.DB)) *piWakeFx {
	t.Helper()
	w := newPiWakeFixtureIn(t, uniqueProfile(label), nil)
	quietBusTeardown(t)
	cfgPath, err := GetUserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("[costs.budgets.groups.team]\ndaily_limit = 1.0\n[costs.budgets]\nenforce = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	w.parent.GroupPath = "team"
	seed, err := NewStorageWithProfile(w.profileName())
	if err != nil {
		t.Fatal(err)
	}
	instances, _, err := seed.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, inst := range instances {
		if inst.ID == w.parent.ID {
			inst.GroupPath = "team"
		}
	}
	if err := seed.Save(instances); err != nil {
		t.Fatal(err)
	}
	if err := costs.NewStore(seed.GetDB().DB()).WriteCostEvent(costs.CostEvent{ID: "ev-alone-" + label, SessionID: w.parent.ID,
		Timestamp: time.Now(), Model: "claude-test", InputTokens: 1000, CostMicrodollars: 1_200_000}); err != nil {
		t.Fatal(err)
	}
	corrupt(w, seed.GetDB().DB())
	_ = seed.Close()
	w.ext = newFakeExt(t, replyWith("submitted"))
	piWakeSocketFor = func(string, string) string { return w.ext.path }
	return w
}

// Forge M3: when the registry cannot be loaded the group limit cannot be
// summed, so the group check is skipped (a warned fail-open), not run on the
// target alone. The load error is a real one: a row whose integer column holds
// text fails the scan, as a damaged state.db would.
func TestCommsDeliver_PiGroupBudgetFailsOpenWhenTheRegistryCannotLoad(t *testing.T) {
	w := groupOverByTargetAlone(t, "openfail", func(w *piWakeFx, db *sql.DB) {
		if _, err := db.Exec(`INSERT INTO instances (id, title, project_path, created_at, sort_order) VALUES ('poison', 'poison', '/tmp', 1, 'not-an-int')`); err != nil {
			t.Fatal(err)
		}
		check, err := NewStorageWithProfile(w.profileName())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = check.Close() }()
		if _, _, err := check.LoadWithGroups(); err == nil {
			t.Fatal("test setup: LoadWithGroups still succeeds, so this is not a load error")
		}
	})
	w.urgent(t, "shipped the build")
	w.deliver(t, "waiting")
	if w.ext.count() != 1 {
		t.Errorf("frames = %d: with the registry unreadable the group check is skipped (warned), not run on the target alone", w.ext.count())
	}
}

// A registry that loads but does not hold the target cannot say which group the
// target is in: membership is unknown, so the group check is skipped (warned) as
// for a load error.
func TestCommsDeliver_PiGroupBudgetSkipsWhenTheRegistryLacksTheTarget(t *testing.T) {
	w := groupOverByTargetAlone(t, "lacks", func(w *piWakeFx, db *sql.DB) {
		if _, err := db.Exec(`DELETE FROM instances WHERE id = ?`, w.parent.ID); err != nil {
			t.Fatal(err)
		}
	})
	w.urgent(t, "shipped the build")
	w.deliver(t, "waiting")
	if w.ext.count() != 1 {
		t.Errorf("frames = %d: a registry without the target gives unknown membership, so the group check is skipped", w.ext.count())
	}
}

// Forge M4 + Max 4: Flush waits for the doorbells of its own notifier, not for a
// package-global count, and not past them.
func TestTransitionNotifier_FlushWaitsForItsOwnPiDoorbellsOnly(t *testing.T) {
	w := newPiWakeFixture(t, func(_ int, frame map[string]any, conn net.Conn) {
		time.Sleep(500 * time.Millisecond)
		answer(conn, frame, "submitted")
	})
	wiring := func() *wakeNudgeWiring {
		x := defaultWakeNudgeWiring()
		x.isIdle = func(*Instance, string) bool { return true }
		x.nudger = NewWakeNudger(0)
		return x
	}
	ev := TransitionNotificationEvent{ChildSessionID: w.child.ID, ChildTitle: "kid", Profile: "default", ToStatus: "waiting",
		TargetSessionID: w.parent.ID, TargetKind: "parent", Tier: TurnTierUrgent, Text: "inbox result", Timestamp: time.Now()}
	a, b := &TransitionNotifier{wake: wiring()}, &TransitionNotifier{wake: wiring()}
	a.fireWakeNudge(w.parent, ev) // a's doorbell is now in flight for 500 ms

	start := time.Now()
	b.Flush()
	if took := time.Since(start); took > 200*time.Millisecond {
		t.Errorf("another notifier's Flush waited %v for a doorbell it never dispatched", took)
	}
	a.Flush()
	if w.ext.count() != 1 {
		t.Errorf("Flush returned before the doorbell reached the extension: %d frames", w.ext.count())
	}
	entries, _ := ReadCommsSpool(w.parent.ID)
	spooled := false
	for _, e := range entries {
		raw, _ := json.Marshal(e)
		if strings.Contains(string(raw), `"edge":"wake"`) && strings.Contains(string(raw), `"state":"landed"`) {
			spooled = true
		}
	}
	if !spooled {
		t.Errorf("Flush returned before the leg spooled its outcome: %v", entries)
	}
}

// Forge low: the route (socket or typed) is decided once, at dispatch, and the
// goroutine, the tracking and the wake record all follow that one decision.
func TestInboxNudge_PiRouteIsResolvedOnceAtDispatch(t *testing.T) {
	w := newPiWakeFixture(t, replyWith("submitted"))
	gate := make(chan struct{})
	orig := wakeNudgeGoroutineGate
	t.Cleanup(func() { wakeNudgeGoroutineGate = orig })
	wakeNudgeGoroutineGate = func() { <-gate }
	var released bool
	release := func() {
		if !released {
			released = true
			close(gate)
		}
	}
	defer release()
	// Not enrolled at dispatch: the doorbell is a typed line.
	if err := os.Remove(commsEnrollmentPath(w.parent.ID)); err != nil {
		t.Fatal(err)
	}
	w.d.notifier.wake.send = sendWakeNudge
	ev := TransitionNotificationEvent{ChildSessionID: w.child.ID, ChildTitle: "kid", Profile: "default", ToStatus: "waiting",
		TargetSessionID: w.parent.ID, TargetKind: "parent", Tier: TurnTierUrgent, Text: "inbox result", Timestamp: time.Now()}
	w.d.notifier.fireWakeNudge(w.parent, ev)
	// The enrollment appears while the goroutine is held.
	w.deliver(t, "waiting")
	if _, ok := LoadCommsEnrollment(w.parent.ID); !ok {
		t.Fatal("test setup: the parent was not re-enrolled")
	}
	release()
	w.d.notifier.Flush()
	if typed := w.typed.waitTyped(t, 1); len(typed) != 1 || w.ext.count() != 0 {
		t.Errorf("typed %q frames %d: a doorbell dispatched as typed must stay typed", typed, w.ext.count())
	}
}

// Max 2 (i): a spent budget is not re-read and re-published on every poll.
func TestCommsDeliver_PiSpentBudgetPublishesOnceAcrossPolls(t *testing.T) {
	w := newPiWakeFixtureIn(t, uniqueProfile("hold"), replyWith("submitted"))
	quietBusTeardown(t)
	t.Setenv("AGENTDECK_EVENTS_BUS", "1")
	spendParent(t, w)
	w.urgent(t, "shipped the build")
	w.deliver(t, "waiting")
	w.clock = w.clock.Add(5 * time.Second)
	w.deliver(t, "waiting")
	waitBudgetVia(t, os.Getenv("HOME"), w.parent.ID)
	time.Sleep(500 * time.Millisecond)
	if via := waitBudgetVia(t, os.Getenv("HOME"), w.parent.ID); len(via) != 1 {
		t.Errorf("session.budget_exceeded published %d times over two polls 5 s apart, want once", len(via))
	}
}

// Max 2 (ii): the inbox doorbell is gated by the budget too.
func TestInboxNudge_PiInboxDoorbellHonoursTheSendBudget(t *testing.T) {
	w := newPiWakeFixtureIn(t, uniqueProfile("inboxbudget"), replyWith("submitted"))
	quietBusTeardown(t)
	t.Setenv("AGENTDECK_EVENTS_BUS", "1")
	spendParent(t, w)
	w.d.notifier.wake.send = sendWakeNudge
	ev := TransitionNotificationEvent{ChildSessionID: w.child.ID, ChildTitle: "kid", Profile: w.profileName(), ToStatus: "waiting",
		TargetSessionID: w.parent.ID, TargetKind: "parent", Tier: TurnTierUrgent, Text: "inbox result", Timestamp: time.Now()}
	w.d.notifier.fireWakeNudge(w.parent, ev)
	w.d.notifier.Flush()
	if w.ext.count() != 0 || len(w.typed.all()) != 0 {
		t.Errorf("a spent budget still rang the inbox doorbell: frames %d typed %q", w.ext.count(), w.typed.all())
	}
	if via := waitBudgetVia(t, os.Getenv("HOME"), w.parent.ID); len(via) != 1 || via[0] != "socket" {
		t.Errorf("session.budget_exceeded via %v, want one with via socket", via)
	}
}

// Max 2 (iv): a pi parent's digest nudge rides the socket and leaves no typed record.
func TestInboxNudge_PiDigestNudgeRidesTheSocket(t *testing.T) {
	w := newPiWakeFixture(t, replyWith("submitted"))
	wiring := defaultWakeNudgeWiring()
	wiring.isIdle = func(*Instance, string) bool { return true }
	wiring.nudger = NewWakeNudger(0)
	n := &TransitionNotifier{wake: wiring}
	if !n.fireDigestNudge(w.parent, "default", "[INBOX] 2 info records are waiting") {
		t.Fatal("the digest nudge was not sent")
	}
	n.Flush()
	if w.ext.count() != 1 || len(w.typed.all()) != 0 {
		t.Errorf("frames %d typed %q, want one frame and nothing typed", w.ext.count(), w.typed.all())
	}
	for _, r := range w.wakeRecs(t) {
		if r.Via == "tmux" {
			t.Errorf("a tmux wake record for a socket digest: %+v", r)
		}
	}
}

// spendParent puts 5.0 against the profile's 1.0 daily limit.
func spendParent(t *testing.T, w *piWakeFx) {
	t.Helper()
	cfgPath, err := GetUserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(enforcedDaily1), 0o600); err != nil {
		t.Fatal(err)
	}
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	seed, err := NewStorageWithProfile(w.profileName())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = seed.Close() }()
	if err := costs.NewStore(seed.GetDB().DB()).WriteCostEvent(costs.CostEvent{ID: "ev-spent-" + w.parent.ID, SessionID: w.parent.ID,
		Timestamp: time.Now(), Model: "claude-test", InputTokens: 1000, CostMicrodollars: 5_000_000}); err != nil {
		t.Fatal(err)
	}
}

// piTurnWithInboxTwin commits one turn to the ledger under its real key and the
// inbox's twin of the same turn, the way a live pi child's turn lands in both.
func (f *deliverFixture) piTurnWithInboxTwin(t *testing.T, uuid, text string) {
	t.Helper()
	f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: text, Key: comms.Key(comms.KindTurn, f.child.ID, uuid)})
	twin := TransitionNotificationEvent{ChildSessionID: f.child.ID, ChildTitle: "board-zero", ToStatus: "waiting", Tier: TurnTierInfo,
		Text: text, TurnUUID: uuid, LastOutputHash: (TurnFacts{UUID: uuid}).Signal(), Profile: "default"}
	if committed, _, _ := f.d.notifier.commitEventToInbox(twin); !committed {
		t.Fatal("commit of the inbox twin")
	}
}

// Max 1 (critical): printing is not a receipt, so the shown keys of a pi
// injection wait for it. A dropped injection must not be acked through
// WasShown, and the inbox copy of the turn must not be retired.
func TestLedgerPromptContext_PiShownKeysWaitForTheReceipt(t *testing.T) {
	f := newPiParentFixture(t)
	f.piTurnWithInboxTwin(t, "aaaaaaaa-0000-4000-8000-000000000e01", "which way?")
	dl, ok := LedgerPromptContext(f.parent.ID, "carry on", "")
	if !ok || strings.Count(dl.Text, "which way?") != 1 {
		t.Fatalf("first injection (ok %v): %q", ok, dl.Text)
	}
	dl.Done(true)                          // printed, then the extension dropped it: no receipt
	f.clock = f.clock.Add(2 * time.Minute) // past the receipt window
	if !InboxHasPending(f.parent.ID) {
		t.Fatal("the inbox copy was retired by an injection nobody received")
	}
	dl2, ok := LedgerPromptContext(f.parent.ID, "carry on again", "")
	if !ok || strings.Count(dl2.Text, "which way?") != 1 {
		t.Fatalf("a dropped injection must be injected again, once (ok %v): %q", ok, dl2.Text)
	}
	dl2.Done(true)
	if !InboxHasPending(f.parent.ID) {
		t.Error("the inbox copy was retired by the second unreceipted injection")
	}
}

// With the receipt, the keys move to Shown: the turn is not injected again, and
// the inbox copy is retired.
func TestLedgerPromptContext_PiReceiptMovesTheShownKeys(t *testing.T) {
	f := newPiParentFixture(t)
	f.piTurnWithInboxTwin(t, "aaaaaaaa-0000-4000-8000-000000000e02", "which way?")
	dl, ok := LedgerPromptContext(f.parent.ID, "carry on", "")
	if !ok {
		t.Fatal("no injection")
	}
	token := tokenOf(t, dl)
	dl.Done(true)
	f.injectedEdge(t, token)
	next, _ := LedgerPromptContext(f.parent.ID, "carry on again", "")
	if next != nil && strings.Contains(next.Text, "which way?") {
		t.Errorf("a received turn was injected again: %q", next.Text)
	}
	if next != nil {
		next.Done(true)
	}
	if InboxHasPending(f.parent.ID) {
		t.Error("the inbox copy of a received turn is still pending")
	}
}

// Max 3 (R-B hole): the pane tree is what proves the socket's peer is pi. When
// it cannot be read, or reads empty, nothing is known about the peer, and a
// live pi may be listening: that is pushed, never the typed fallback. The
// records stay attempted and only the 90 s settle retries, under a new nonce.
func TestCommsDeliver_PiPaneTreeHiccupNeverTypes(t *testing.T) {
	for name, tree := range map[string]map[int]bool{
		"a nil tree (the read failed)": nil,
		"an empty tree":                {},
	} {
		t.Run(name, func(t *testing.T) {
			w := newPiWakeFixture(t, replyWith("submitted"))
			piWakePanePIDs = func(*Instance) (map[int]bool, error) { return tree, nil }
			w.urgent(t, "shipped the build")
			w.deliver(t, "waiting")
			time.Sleep(300 * time.Millisecond) // a typed doorbell is dispatched in the background
			if typed := w.typed.all(); len(typed) != 0 {
				t.Fatalf("typed %q into a pane whose tree could not be read: a live pi may be there", typed)
			}
			st := w.state(t)
			if len(st.Inflight) != 1 || st.Inflight[0].State != comms.ReceiptAttempted || st.LastWake == 0 || st.AutoWakes != 1 {
				t.Fatalf("an unvetted wake must stay attempted and consumed: %+v", st)
			}
			first, _ := jsonHas(t, st.Inflight[0], "nonce")
			// The debounce holds until the settle.
			w.clock = w.clock.Add(30 * time.Second)
			w.deliver(t, "waiting")
			if now, _ := jsonHas(t, w.state(t).Inflight[0], "nonce"); now != first {
				t.Errorf("re-woken before the settle: nonce %v -> %v", first, now)
			}
			// The settle retries once, under a new nonce, and still never types.
			w.clock = w.clock.Add(61 * time.Second)
			w.deliver(t, "waiting")
			time.Sleep(300 * time.Millisecond)
			st = w.state(t)
			if len(st.Inflight) != 1 {
				t.Fatalf("after the settle: %+v", st)
			}
			if now, _ := jsonHas(t, st.Inflight[0], "nonce"); now == first {
				t.Errorf("the settle did not retry the wake (nonce %v kept)", now)
			}
			if typed := w.typed.all(); len(typed) != 0 {
				t.Errorf("the retry typed %q", typed)
			}
			// And the record is still owed at the next run start.
			dl, ok := LedgerPromptContext(w.parent.ID, "carry on", "")
			if !ok || !strings.Contains(dl.Text, "shipped the build") {
				t.Errorf("the record was lost (ok %v)", ok)
			}
			dl.Done(false)
		})
	}
}

// Max 3: a COMPLETE tree that does not hold the peer means something other
// than this pane's pi owns the socket. That is a permanent refusal: no typing,
// a failed wake record naming the reason, no more socket wakes for the set.
func TestCommsDeliver_PiForeignPeerInACompleteTreeIsAPermanentRefusal(t *testing.T) {
	w := newPiWakeFixture(t, replyWith("submitted"))
	piWakePanePIDs = func(*Instance) (map[int]bool, error) {
		return map[int]bool{os.Getpid() + 1: true, os.Getpid() + 2: true}, nil
	}
	w.urgent(t, "shipped the build")
	w.deliver(t, "waiting")
	time.Sleep(300 * time.Millisecond)
	if typed := w.typed.all(); len(typed) != 0 {
		t.Fatalf("typed %q although a foreign process owns the socket", typed)
	}
	if w.ext.count() != 0 {
		t.Errorf("a frame reached a socket whose peer is not pi (%d)", w.ext.count())
	}
	recs := w.wakeRecs(t)
	if len(recs) != 1 || recs[0].State != comms.StateFailed || recs[0].Via == "tmux" {
		t.Fatalf("want one failed, non-tmux wake record, got %+v", recs)
	}
	if e, _ := jsonHas(t, recs[0], "err"); e != "peer_not_in_pane_tree" {
		t.Errorf("the record names %v, want peer_not_in_pane_tree", e)
	}
	for _, step := range []time.Duration{61 * time.Second, 5 * time.Minute} {
		w.clock = w.clock.Add(step)
		w.deliver(t, "waiting")
	}
	time.Sleep(300 * time.Millisecond)
	if len(w.typed.all()) != 0 || w.ext.count() != 0 {
		t.Errorf("a permanent refusal repeated: frames %d typed %q", w.ext.count(), w.typed.all())
	}
	dl, ok := LedgerPromptContext(w.parent.ID, "carry on", "")
	if !ok || !strings.Contains(dl.Text, "shipped the build") {
		t.Errorf("the record did not stay deliverable (ok %v)", ok)
	}
	dl.Done(false)
}

// Forge R2 7: type ONLY when pi is provably absent. A socket the daemon will not
// use (open to the group), or one whose peer cannot be vetted, may belong to a
// live pi: nothing is typed, the records stay deliverable at the next run start.
func TestCommsDeliver_PiUnavailableButNotAbsentNeverTypes(t *testing.T) {
	for name, setup := range map[string]func(w *piWakeFx){
		"a socket open to the group (socket_perms)": func(w *piWakeFx) { _ = os.Chmod(w.ext.path, 0o660) },
	} {
		t.Run(name, func(t *testing.T) {
			w := newPiWakeFixture(t, replyWith("submitted"))
			setup(w)
			w.urgent(t, "shipped the build")
			w.deliver(t, "waiting")
			time.Sleep(300 * time.Millisecond) // a typed doorbell is dispatched in the background
			if typed := w.typed.all(); len(typed) != 0 {
				t.Fatalf("typed %q although pi is not provably absent", typed)
			}
			if w.ext.count() != 0 {
				t.Errorf("a frame reached an unvetted socket (%d)", w.ext.count())
			}
			dl, ok := LedgerPromptContext(w.parent.ID, "carry on", "")
			if !ok || !strings.Contains(dl.Text, "shipped the build") {
				t.Errorf("the record did not stay deliverable (ok %v)", ok)
			}
			dl.Done(false)
			// The inbox path (wakeSend) obeys the same rule.
			line := comms.WakePrefix + " 1 pending (1 urgent; data): [urgent] kid (pi) #ABC123: «done»"
			if _, err := wakeSend(w.parent, "default", line, "01J0ABCDEF0123456789ABCDEF", []string{"r1"}); err != nil {
				t.Fatalf("wakeSend: %v", err)
			}
			time.Sleep(300 * time.Millisecond)
			if typed := w.typed.all(); len(typed) != 0 {
				t.Errorf("wakeSend typed %q although pi is not provably absent", typed)
			}
		})
	}
}

// Forge R2 8: a token-bound injection is not returned to pending while its
// receipt may still be on its way. Within the receipt window the next run start
// neither re-injects the record nor drops the attempt; a receipt that arrives
// late then still accepts it, and nothing was shown twice. Past the window it
// rides again.
func TestLedgerPromptContext_PiAttemptWithinTheReceiptWindowIsNotInjectedAgain(t *testing.T) {
	f := newPiParentFixture(t)
	rec := f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: "child result"})
	dl, _ := LedgerPromptContext(f.parent.ID, "run one", "")
	token := tokenOf(t, dl)
	dl.Done(true)
	f.clock = f.clock.Add(5 * time.Second)
	next, _ := LedgerPromptContext(f.parent.ID, "run two", "")
	if next != nil && strings.Contains(next.Text, "child result") {
		t.Fatalf("injected again 5 s after the first, before its receipt could arrive: %q", next.Text)
	}
	if next != nil {
		next.Done(true)
	}
	if in := inflightFor(f.inflight(t), rec.ID); in == nil || in["token"] != token {
		t.Fatalf("the token-bound attempt was dropped inside the receipt window: %v", f.inflight(t))
	}
	// The receipt is spooled but the daemon has not ingested it yet; the next
	// run start still does not duplicate.
	raw, _ := json.Marshal(map[string]any{"harness": "pi", "event": "turn_start", "edge": "injected",
		"instance": f.parent.ID, "injected_token": token, "t_signal": f.clock.UnixMilli()})
	var e CommsSpoolEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	spoolTurn(t, e)
	f.clock = f.clock.Add(5 * time.Second)
	again, _ := LedgerPromptContext(f.parent.ID, "run three", "")
	if again != nil && strings.Contains(again.Text, "child result") {
		t.Errorf("a spooled-but-not-ingested receipt did not stop the duplicate: %q", again.Text)
	}
	if again != nil {
		again.Done(true)
	}
}

// Forge R2 10: the route is resolved once, at dispatch. A parent that stops being
// a ledger consumer between dispatch and the goroutine's run is still rung over
// the socket it was reserved for, never typed.
func TestInboxNudge_PiSocketRouteSurvivesEnrolmentEndingAfterDispatch(t *testing.T) {
	w := newPiWakeFixture(t, replyWith("submitted"))
	gate := make(chan struct{})
	orig := wakeNudgeGoroutineGate
	t.Cleanup(func() { wakeNudgeGoroutineGate = orig })
	wakeNudgeGoroutineGate = func() { <-gate }
	var released bool
	release := func() {
		if !released {
			released = true
			close(gate)
		}
	}
	defer release()
	w.d.notifier.wake.send = sendWakeNudge
	ev := TransitionNotificationEvent{ChildSessionID: w.child.ID, ChildTitle: "kid", Profile: "default", ToStatus: "waiting",
		TargetSessionID: w.parent.ID, TargetKind: "parent", Tier: TurnTierUrgent, Text: "inbox result text", Timestamp: time.Now()}
	if err := CommitToInbox(w.parent.ID, ev); err != nil {
		t.Fatal(err)
	}
	w.d.notifier.fireWakeNudge(w.parent, ev)                            // dispatched as a socket doorbell; held at the gate
	if err := os.Remove(commsEnrollmentPath(w.parent.ID)); err != nil { // the enrolment ends under the held goroutine
		t.Fatal(err)
	}
	if ridesPiSocket(w.parent) {
		t.Fatal("test setup: the parent is still a consumer")
	}
	release()
	w.d.notifier.Flush()
	time.Sleep(200 * time.Millisecond)
	if typed := w.typed.all(); len(typed) != 0 {
		t.Errorf("typed %q: the route changed under a dispatched doorbell", typed)
	}
	if w.ext.count() != 1 {
		t.Errorf("frames = %d, want the doorbell the dispatch reserved", w.ext.count())
	}
}

// Forge R2 9: Flush waits for a slow typed leg, and the typed leg's wake record is
// spooled before Flush returns.
func TestTransitionNotifier_FlushWaitsForASlowTypedLeg(t *testing.T) {
	w := newPiWakeFixture(t, replyWith("submitted"))
	// Relaunched without the socket and still enrolled (its marker drains at the
	// next pass): the one state in which a listed pi's doorbell is typed.
	w.parent.PiSocketExported = false
	piWakeSocketFor = func(string, string) string { return filepath.Join(filepath.Dir(w.ext.path), "none.sock") }
	w.d.notifier.wake.send = sendWakeNudge
	release := w.typed.holdSends() // releases itself after 1.5 s
	defer release()
	ev := TransitionNotificationEvent{ChildSessionID: w.child.ID, ChildTitle: "kid", Profile: "default", ToStatus: "waiting",
		TargetSessionID: w.parent.ID, TargetKind: "parent", Tier: TurnTierUrgent, Text: "inbox result text", Timestamp: time.Now()}
	if err := CommitToInbox(w.parent.ID, ev); err != nil {
		t.Fatal(err)
	}
	w.d.notifier.fireWakeNudge(w.parent, ev)
	done := make(chan struct{})
	start := time.Now()
	go func() { w.d.notifier.Flush(); close(done) }()
	select {
	case <-done:
		t.Fatalf("Flush returned after %v while the typed leg was still held", time.Since(start))
	case <-time.After(600 * time.Millisecond):
	}
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("Flush never returned")
	}
	if typed := w.typed.all(); len(typed) != 1 {
		t.Errorf("typed %q by the time Flush returned, want the doorbell once", typed)
	}
	recs := w.wakeRecs(t)
	if len(recs) != 1 || recs[0].Via != "tmux" {
		t.Errorf("wake records after Flush: %+v, want the typed leg's record", recs)
	}
}

// Forge R2 11: reservations are deleted once their debounce has passed, so the
// map does not grow with every parent that was ever rung.
func TestWakeReservations_ExpiredEntriesAreDeleted(t *testing.T) {
	f := newPiParentFixture(t)
	stale := f.clock.Add(-10 * time.Minute)
	var keys []string
	for i := 0; i < 20; i++ {
		k := wakeReservationKey("dir", fmt.Sprintf("old-parent-%d", i))
		keys = append(keys, k)
		wakeReservations.Store(k, stale)
	}
	t.Cleanup(func() {
		for _, k := range keys {
			wakeReservations.Delete(k)
		}
	})
	reserveWake(f.parent)
	for i := 0; i < 20; i++ {
		_ = wakeReserved("dir", fmt.Sprintf("old-parent-%d", i), f.clock)
	}
	left := 0
	for _, k := range keys {
		if _, ok := wakeReservations.Load(k); ok {
			left++
		}
	}
	if left != 0 {
		t.Errorf("%d expired reservations are still held", left)
	}
}
