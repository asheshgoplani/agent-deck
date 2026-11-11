package session

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/comms"
)

// PR-B commit 3, review round 4 (Forge R3). Tests first.
//
//  1. Inbox records a pi injection carries are LEASED under its delivery token:
//     the receipt finalizes them, expiry or replacement requeues them.
//  2. A pi launched with a socket path in AGENTDECK_PI_SOCKET is never typed into; any
//     connect failure leaves the records pending and the settle retries. The
//     typed doorbell is for a pi launched without the socket.
//  3. A pane tree read that fails is pushed, not a permanent refusal.
//  4. A backward clock step never holds records.
//
// Assumed of the implementation, where a test had to pick: lease and attempt
// times come from commsNow (the fixture's clock); a launch that exports the
// socket is recorded on the persisted instance or its launch record, so a
// daemon that reloads the session still knows it; a pi with no such record is
// treated as the pre-PR runtime probe treated it (an absent socket is typed).

// ---- 1. inbox records: peeked, consumed on the receipt -----------------------
// (Round 5 replaced the round 4 lease with peek-then-consume-on-receipt: the
// hook reads the inbox records it injects without draining them, records their
// ids under the delivery token, and only the receipt consumes them, by id.)

// inboxOnly commits a record to the parent's inbox with no ledger twin: the
// shape of a child's status change or a reply.
func (f *deliverFixture) inboxOnly(t *testing.T, text string) {
	t.Helper()
	ev := TransitionNotificationEvent{ChildSessionID: f.child.ID, ChildTitle: "board-zero", Profile: "default", ToStatus: "waiting",
		TargetSessionID: f.parent.ID, TargetKind: "parent", Tier: TurnTierUrgent, Text: text, Timestamp: f.clock}
	if err := CommitToInbox(f.parent.ID, ev); err != nil {
		t.Fatal(err)
	}
}

// A pi injection that carries an inbox-only record has a delivery token, and
// without a receipt nothing is consumed: the record is still in the inbox, every
// drain sees it (the CLI drain included), and it rides the next run start.
func TestPiInboxPeek_NoReceiptLeavesTheRecordInTheInbox(t *testing.T) {
	f := newPiParentFixture(t)
	f.inboxOnly(t, "inbox-only status text")
	dl, ok := LedgerPromptContext(f.parent.ID, "carry on", "", f.parent.Tool == "pi")
	if !ok || strings.Count(dl.Text, "inbox-only status text") != 1 {
		t.Fatalf("first injection (ok %v): %v", ok, dl)
	}
	if token := tokenOf(t, dl); !tokenRE.MatchString(token) {
		t.Fatalf("an injection that carries only inbox records has token %q: the extension has nothing to echo, so the receipt can never consume them", token)
	}
	dl.Done(true) // printed; the extension dropped it: no receipt
	if !InboxHasPending(f.parent.ID) {
		t.Fatal("the inbox record was consumed by an injection nobody received")
	}
	// The next run start shows it again (at-least-once), with no clock to wait for.
	next, ok := LedgerPromptContext(f.parent.ID, "carry on again", "", f.parent.Tool == "pi")
	if !ok || next == nil || strings.Count(next.Text, "inbox-only status text") != 1 {
		t.Fatalf("an unreceipted inbox record did not ride the next run start (ok %v, %v)", ok, next)
	}
	next.Done(true)
	// The CLI drain (`inbox drain self`) sees it too.
	drained, err := DrainInboxForParent(f.parent.ID)
	if err != nil || len(drained) != 1 || !strings.Contains(drained[0].Text, "inbox-only status text") {
		t.Fatalf("the CLI drain did not see the peeked record: %v %v", drained, err)
	}
}

// With the receipt the record is consumed, once: it is gone, a repeated receipt
// changes nothing, and nothing is shown again.
func TestPiInboxPeek_ReceiptConsumesOnce(t *testing.T) {
	f := newPiParentFixture(t)
	f.inboxOnly(t, "inbox-only status text")
	dl, ok := LedgerPromptContext(f.parent.ID, "carry on", "", f.parent.Tool == "pi")
	if !ok || !strings.Contains(dl.Text, "inbox-only status text") {
		t.Fatalf("no injection (ok %v)", ok)
	}
	token := tokenOf(t, dl)
	if !tokenRE.MatchString(token) {
		t.Fatalf("token %q is not a ULID", token)
	}
	dl.Done(true)
	f.injectedEdge(t, token)
	f.injectedEdge(t, token) // a duplicate receipt must not double count or resurrect it
	if InboxHasPending(f.parent.ID) {
		t.Error("the inbox record of a received injection is still pending")
	}
	if next, _ := LedgerPromptContext(f.parent.ID, "carry on again", "", f.parent.Tool == "pi"); next != nil && strings.Contains(next.Text, "inbox-only status text") {
		t.Errorf("a received record was injected again: %q", next.Text)
	}
	if st, err := ReadInboxStats(f.parent.ID); err != nil || st.RecordsDelivered > 1 {
		t.Errorf("records delivered = %d (err %v), want it consumed once", st.RecordsDelivered, err)
	}
}

// Claude is unchanged: its prompt hook printing the records is the delivery, so
// the drain consumes them at once.
func TestPiInboxPeek_ClaudeRecordsAreConsumedAtOnce(t *testing.T) {
	f := newDeliverFixture(t) // a Claude parent
	ev := TransitionNotificationEvent{ChildSessionID: f.child.ID, ChildTitle: "board-zero", Profile: "default", ToStatus: "waiting",
		TargetSessionID: f.parent.ID, TargetKind: "parent", Tier: TurnTierUrgent, Text: "claude inbox text", Timestamp: f.clock}
	if err := CommitToInbox(f.parent.ID, ev); err != nil {
		t.Fatal(err)
	}
	text, evs, err := DrainForPrompt(f.parent.ID)
	if err != nil || len(evs) != 1 || !strings.Contains(text, "claude inbox text") {
		t.Fatalf("drain: %q %v %v", text, evs, err)
	}
	if InboxHasPending(f.parent.ID) {
		t.Error("a Claude parent's records must be consumed when its hook prints them")
	}
}

// ---- 2. the launch decides typing -------------------------------------------

// launchPi runs the real launch path for the fixture's pi parent with a fake pi
// of the given version on PATH, then persists the instance and reloads it the
// way the daemon sees it. The launch exports the socket from pi 1.2.0.
func (f *deliverFixture) launchPi(t *testing.T, version string, exports bool) {
	t.Helper()
	installPiExtension(t)
	bin := t.TempDir()
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	writeFakePi(t, bin, "echo "+version)
	f.parent.IdentityInjectionDisabled = true
	cmd := f.parent.buildPiCommand("pi")
	if has := exportsPiSocket(cmd); has != exports {
		t.Fatalf("test setup: pi %s launch exports the socket = %v, want %v (%q)", version, has, exports, cmd)
	}
	storage, err := NewStorageWithProfile(f.profileName())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = storage.Close() }()
	var all []*Instance
	for _, in := range f.byID {
		all = append(all, in)
	}
	if err := storage.Save(all); err != nil {
		t.Fatal(err)
	}
	// The launch's own targeted write (persistLaunchDecision): a save carries
	// an extras key it omits forward, so only this clears an earlier true.
	if err := storage.GetDB().WriteLaunchDecision(f.parent.ID, f.parent.tmuxSession.TmuxName(), f.parent.Tool, f.parent.PiSocketExported); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range loaded {
		if in.ID == f.parent.ID {
			f.parent, f.byID[in.ID] = in, in
			return
		}
	}
	t.Fatal("the launched parent was not persisted")
}

// staleSocket is a socket file nobody listens on: ECONNREFUSED.
func staleSocket(t *testing.T, dir string) string {
	t.Helper()
	stale := filepath.Join(dir, "stale.sock")
	ln, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	return stale
}

// A pi launched with the socket exported has a v5 extension: no connect failure
// (a missing file, a refused connection: a rebind or reload window) may type
// into it. The records stay pending and the settle rings again once the socket
// is back.
func TestPiLaunchedWithSocket_ConnectFailureNeverTypesAndTheSettleRetries(t *testing.T) {
	for name, path := range map[string]func(w *piWakeFx) string{
		"ENOENT: no socket file":       func(w *piWakeFx) string { return filepath.Join(filepath.Dir(w.ext.path), "none.sock") },
		"ECONNREFUSED: a stale socket": func(w *piWakeFx) string { return staleSocket(t, filepath.Dir(w.ext.path)) },
	} {
		t.Run(name, func(t *testing.T) {
			piSocketShortData(t)
			w := newPiWakeFixture(t, replyWith("submitted"))
			w.launchPi(t, "1.3.0", true)
			down := path(w)
			piWakeSocketFor = func(string, string) string { return down }

			// The inbox doorbell path.
			out, err := wakeSend(w.parent, "default", comms.WakePrefix+" 1 pending", "01J0ABCDEF0123456789ABCDEF", []string{"r1"})
			if err != nil {
				t.Fatalf("wakeSend: %v", err)
			}
			if out.Accepted || out.Via == "tmux" {
				t.Errorf("wakeSend treated a pi with a v5 extension as typeable: %+v", out)
			}
			// The ledger pass.
			w.urgent(t, "shipped the build")
			w.deliver(t, "waiting")
			time.Sleep(300 * time.Millisecond) // a typed doorbell is dispatched in the background
			if typed := w.typed.all(); len(typed) != 0 {
				t.Fatalf("typed %q into a pi launched with the socket", typed)
			}
			if st := w.state(t); len(st.Inflight) != 0 || st.Pending != 1 {
				t.Errorf("the records must stay pending after a connect failure: %+v", st)
			}

			// The extension is back: the next pass rings it, and still types nothing.
			piWakeSocketFor = func(string, string) string { return w.ext.path }
			w.clock = w.clock.Add(10 * time.Minute)
			w.deliver(t, "waiting")
			w.ext.waitFrame(t)
			if typed := w.typed.all(); len(typed) != 0 {
				t.Errorf("the retry typed %q", typed)
			}
		})
	}
}

// A pi launched WITHOUT the socket export (too old, no extension, path too
// long, socket off) is woken by the typed doorbell, as it always was.
func TestPiLaunchedWithoutSocket_TypesTheDoorbell(t *testing.T) {
	piSocketShortData(t)
	w := newPiWakeFixture(t, replyWith("submitted"))
	w.launchPi(t, "1.1.0", false)
	piWakeSocketFor = func(string, string) string { return "" }
	if _, err := wakeSend(w.parent, "default", comms.WakePrefix+" 1 pending", "01J0ABCDEF0123456789ABCDEF", []string{"r1"}); err != nil {
		t.Fatalf("wakeSend: %v", err)
	}
	if typed := w.typed.waitTyped(t, 1); len(typed) != 1 {
		t.Fatalf("typed %q, want the doorbell once", typed)
	}
	if w.ext.count() != 0 {
		t.Errorf("a frame reached the extension (%d)", w.ext.count())
	}
}

// Review item 4: enrolment follows the launch. A listed pi whose launch
// exported no socket (here pi 1.1.0, below the floor) is not enrolled after a
// daemon pass and keeps the inbox path (no marker, no ledger context), as
// before this change; once a launch exports the socket (pi 1.3.0), the next
// pass enrols it.
func TestCommsConsumes_AListedPiEnrolsOnlyOnceALaunchExportedTheSocket(t *testing.T) {
	piSocketShortData(t)
	// Not newDeliverFixture: it enrols its parent while it is still a Claude.
	f := &deliverFixture{commsFixture: newCommsFixture(t), clock: time.Now()}
	f.parent.Tool = "pi"
	t.Cleanup(SetCommsConsumersForTest([]string{f.parent.Title}))
	f.l = f.d.commsLedgerFor(f.profileName())
	if f.l == nil {
		t.Fatal("ledger did not open")
	}

	f.launchPi(t, "1.1.0", false)
	f.deliver(t, "waiting")
	if _, ok := LoadCommsEnrollment(f.parent.ID); ok {
		t.Fatal("a listed pi launched without the socket was enrolled")
	}
	if dl, ok := LedgerPromptContext(f.parent.ID, "hello", "", f.parent.Tool == "pi"); ok {
		dl.Done(false)
		t.Fatal("a listed pi launched without the socket got ledger context; it must keep the inbox path")
	}

	f.launchPi(t, "1.3.0", true)
	f.deliver(t, "waiting")
	if en, ok := LoadCommsEnrollment(f.parent.ID); !ok || en.Tool != "pi" || en.Draining {
		t.Fatalf("after a socket-exported launch the listed pi is not enrolled: %+v (found %v)", en, ok)
	}
}

// Forge round 2 #1: the marker of a pi parent says its launch exported the
// wake socket, so its hooks take the ledger's edges only from the pi that
// serves it (PiLedgerServedOnly). A relaunch without the socket drains the
// parent and the marker stops asking for a serving pi.
func TestCommsEnrollment_APiParentAsksForTheServingPiWhileItsLaunchExportsTheSocket(t *testing.T) {
	piSocketShortData(t)
	f := &deliverFixture{commsFixture: newCommsFixture(t), clock: time.Now()}
	f.parent.Tool = "pi"
	t.Cleanup(SetCommsConsumersForTest([]string{f.parent.Title}))
	f.l = f.d.commsLedgerFor(f.profileName())
	if f.l == nil {
		t.Fatal("ledger did not open")
	}

	f.launchPi(t, "1.3.0", true)
	f.deliver(t, "waiting")
	if en, ok := LoadCommsEnrollment(f.parent.ID); !ok || !en.PiSocket || en.Draining {
		t.Fatalf("a socket-exported pi's marker = %+v (found %v), want enrolled with pi_socket", en, ok)
	}
	if !PiLedgerServedOnly(f.parent.ID) {
		t.Fatal("a socket-exported pi parent takes ledger edges from a pi that does not serve its socket")
	}

	f.launchPi(t, "1.1.0", false)
	f.deliver(t, "waiting")
	if en, ok := LoadCommsEnrollment(f.parent.ID); !ok || en.PiSocket || !en.Draining {
		t.Fatalf("after a relaunch without the socket the marker = %+v (found %v), want draining without pi_socket", en, ok)
	}
	if PiLedgerServedOnly(f.parent.ID) {
		t.Error("a pi relaunched without the socket still waits for a pi that serves one")
	}

	// A Claude parent never asks for one.
	f.parent.Tool, f.parent.PiSocketExported = "claude", false
	f.deliver(t, "waiting")
	if en, ok := LoadCommsEnrollment(f.parent.ID); ok && en.PiSocket {
		t.Errorf("a Claude parent's marker asks for a serving pi: %+v", en)
	}
}

// ---- 3. a partial pane tree --------------------------------------------------

// setPanePIDs points the pane-tree seam at (set, err). The seam answers the set
// alone today and is meant to carry the read error too: this fits either shape.
func setPanePIDs(t *testing.T, set map[int]bool, err error) {
	t.Helper()
	orig := piWakePanePIDs
	t.Cleanup(func() { piWakePanePIDs = orig })
	ft := reflect.TypeOf(piWakePanePIDs)
	fn := reflect.MakeFunc(ft, func([]reflect.Value) []reflect.Value {
		out := []reflect.Value{reflect.ValueOf(set)}
		if ft.NumOut() == 2 {
			out = append(out, reflect.ValueOf(&err).Elem())
		}
		return out
	})
	reflect.ValueOf(&piWakePanePIDs).Elem().Set(fn)
}

// A tree read that failed partway yields a non-empty set that may miss the live
// peer. That is pushed, like an empty tree: nothing typed, no permanent refusal,
// the settle retries. Only a complete, successfully read tree with a foreign
// peer is permanent.
func TestCommsDeliver_PiPartialPaneTreeIsPushedNotPermanent(t *testing.T) {
	w := newPiWakeFixture(t, replyWith("submitted"))
	setPanePIDs(t, map[int]bool{os.Getpid() + 1: true}, errors.New("read /proc: partial tree"))
	w.urgent(t, "shipped the build")
	w.deliver(t, "waiting")
	time.Sleep(300 * time.Millisecond)
	if typed := w.typed.all(); len(typed) != 0 {
		t.Fatalf("typed %q after a partial tree read", typed)
	}
	if w.ext.count() != 0 {
		t.Errorf("a frame reached a peer the partial tree could not vet (%d)", w.ext.count())
	}
	for _, r := range w.wakeRecs(t) {
		if r.State == comms.StateFailed {
			t.Errorf("a partial tree read is a permanent refusal: %+v", r)
		}
	}
	if stopped := PiSocketWakesStopped(); len(stopped) != 0 {
		t.Errorf("socket wakes stopped for a transient tree read error: %+v", stopped)
	}
	st := w.state(t)
	if len(st.Inflight) != 1 || st.Inflight[0].State != comms.ReceiptAttempted {
		t.Fatalf("a partial tree must leave the wake attempted: %+v", st)
	}
	first, _ := jsonHas(t, st.Inflight[0], "nonce")
	// The settle retries once, under a new nonce, never typing and never giving up.
	w.clock = w.clock.Add(95 * time.Second)
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
}

// ---- 4. a backward clock step -------------------------------------------------

// A wall clock stepped back leaves attempts with a negative age. That counts as
// outside the receipt window: an injection nobody received is injected again.
func TestLedgerPromptContext_BackwardClockStepDoesNotHoldAnInjection(t *testing.T) {
	f := newPiParentFixture(t)
	f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: "child result"})
	dl, ok := LedgerPromptContext(f.parent.ID, "carry on", "", f.parent.Tool == "pi")
	if !ok || !strings.Contains(dl.Text, "child result") {
		t.Fatalf("no injection (ok %v)", ok)
	}
	dl.Done(true) // no receipt
	f.clock = f.clock.Add(-10 * time.Minute)
	again, ok := LedgerPromptContext(f.parent.ID, "carry on again", "", f.parent.Tool == "pi")
	if !ok || again == nil || !strings.Contains(again.Text, "child result") {
		t.Fatalf("a backward clock step held the record: an attempt with a negative age is outside the receipt window (ok %v, %v)", ok, again)
	}
	again.Done(true)
}

// The same step must not stall a wake attempt: its negative age is stale, so the
// pass settles it instead of waiting for the clock to catch up.
func TestCommsDeliver_BackwardClockStepSettlesAWakeAttempt(t *testing.T) {
	w := newPiWakeFixture(t, replyWith("submitted"))
	setPanePIDs(t, map[int]bool{}, nil) // an unvetted wake stays attempted
	w.urgent(t, "shipped the build")
	w.deliver(t, "waiting")
	st := w.state(t)
	if len(st.Inflight) != 1 {
		t.Fatalf("test setup: want one attempted wake, got %+v", st)
	}
	first, _ := jsonHas(t, st.Inflight[0], "nonce")
	w.clock = w.clock.Add(-10 * time.Minute)
	w.deliver(t, "waiting")
	st = w.state(t)
	for _, in := range st.Inflight {
		if n, _ := jsonHas(t, in, "nonce"); n == first {
			t.Fatalf("a wake attempt from the future was never settled: %+v", st)
		}
	}
	dl, ok := LedgerPromptContext(w.parent.ID, "carry on", "", w.parent.Tool == "pi")
	if !ok || !strings.Contains(dl.Text, "shipped the build") {
		t.Errorf("the record was held by the backward step (ok %v)", ok)
	}
	dl.Done(false)
}
