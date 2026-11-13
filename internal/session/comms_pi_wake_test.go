package session

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/comms"
	"github.com/asheshgoplani/agent-deck/internal/costs"
)

// PR-B commit 3b: a pi parent's doorbells ride the extension's socket through
// one send leg (design A4, B, F, J). A fake extension listens on a short /tmp
// socket and answers as the real one does; the typed fallback is observed at
// wakeNudgeExec, the one place a line is typed into a pane.

// fakeExt is the extension's end of the socket.
type fakeExt struct {
	path   string
	mu     sync.Mutex
	frames []map[string]any
	seen   chan map[string]any
	script func(n int, frame map[string]any, conn net.Conn)
}

func newFakeExt(t *testing.T, script func(n int, frame map[string]any, conn net.Conn)) *fakeExt {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	dir := filepath.Join(root, "pi")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	e := &fakeExt{path: filepath.Join(dir, "w.sock"), seen: make(chan map[string]any, 64), script: script}
	ln, err := net.Listen("unix", e.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(e.path, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				line, err := bufio.NewReader(conn).ReadBytes('\n')
				if err != nil {
					return
				}
				var frame map[string]any
				if json.Unmarshal(line, &frame) != nil {
					return
				}
				e.mu.Lock()
				e.frames = append(e.frames, frame)
				n := len(e.frames)
				e.mu.Unlock()
				e.seen <- frame
				if e.script != nil {
					e.script(n, frame, conn)
				}
				time.Sleep(50 * time.Millisecond)
			}()
		}
	}()
	return e
}

func (e *fakeExt) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.frames)
}

func (e *fakeExt) frame(t *testing.T, i int) map[string]any {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if i >= len(e.frames) {
		t.Fatalf("the extension saw %d frames, want at least %d", len(e.frames), i+1)
	}
	return e.frames[i]
}

func (e *fakeExt) waitFrame(t *testing.T) map[string]any {
	t.Helper()
	select {
	case f := <-e.seen:
		return f
	case <-time.After(10 * time.Second):
		t.Fatal("no frame reached the extension")
		return nil
	}
}

func answer(conn net.Conn, frame map[string]any, state string) {
	body := map[string]any{"id": frame["id"], "state": state}
	raw, _ := json.Marshal(body)
	_, _ = conn.Write(append(raw, '\n'))
}

func refuse(conn net.Conn, frame map[string]any, state string) {
	raw, _ := json.Marshal(map[string]any{"id": frame["id"], "ok": false, "state": state, "notSent": true})
	_, _ = conn.Write(append(raw, '\n'))
}

func replyWith(state string) func(int, map[string]any, net.Conn) {
	return func(_ int, f map[string]any, c net.Conn) { answer(c, f, state) }
}

func refuseWith(state string) func(int, map[string]any, net.Conn) {
	return func(_ int, f map[string]any, c net.Conn) { refuse(c, f, state) }
}

// typedSpy records every line the daemon types into a pane (the argv of the
// `session send` it runs), and answers success. While block is set the
// "subprocess" does not return, as a send waiting on a target's lock does not.
type typedSpy struct {
	mu    sync.Mutex
	lines []string
	block chan struct{}
}

func newTypedSpy(t *testing.T) *typedSpy {
	t.Helper()
	s := &typedSpy{}
	orig := wakeNudgeExec
	t.Cleanup(func() { wakeNudgeExec = orig })
	wakeNudgeExec = func(_ context.Context, _ string, args ...string) error {
		for i, a := range args {
			if a == "send" && i+2 < len(args) {
				s.mu.Lock()
				s.lines = append(s.lines, args[i+2])
				block := s.block
				s.mu.Unlock()
				if block != nil {
					<-block
				}
			}
		}
		return nil
	}
	return s
}

func (s *typedSpy) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lines...)
}

// holdSends makes every send block until the returned func is called, or for
// 1.5 s at most: an implementation that types in the foreground then fails on
// timing instead of hanging the package.
func (s *typedSpy) holdSends() (release func()) {
	ch := make(chan struct{})
	s.mu.Lock()
	s.block = ch
	s.mu.Unlock()
	var once sync.Once
	release = func() { once.Do(func() { close(ch) }) }
	time.AfterFunc(1500*time.Millisecond, release)
	return release
}

// waitTyped polls for n typed lines: the typed doorbell is dispatched in the
// background, as Claude's is.
func (s *typedSpy) waitTyped(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(s.all()) < n && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	return s.all()
}

type piWakeFx struct {
	*deliverFixture
	ext   *fakeExt
	typed *typedSpy
}

// newPiWakeFixture is a pi parent enrolled in the ledger, a fake extension at
// its socket, and a typed-line spy.
func newPiWakeFixture(t *testing.T, script func(int, map[string]any, net.Conn)) *piWakeFx {
	t.Helper()
	return newPiWakeFixtureIn(t, "", script)
}

// uniqueProfile is a profile name no other test (or run of this one) used, for
// tests that publish to the events bus.
func uniqueProfile(prefix string) string {
	return fmt.Sprintf("%s%d", prefix, time.Now().UnixNano())
}

func newPiWakeFixtureIn(t *testing.T, profile string, script func(int, map[string]any, net.Conn)) *piWakeFx {
	t.Helper()
	f := newPiParentFixtureIn(t, profile)
	w := &piWakeFx{deliverFixture: f, ext: newFakeExt(t, script), typed: newTypedSpy(t)}
	origPath, origPIDs := piWakeSocketFor, piWakePanePIDs
	t.Cleanup(func() { piWakeSocketFor, piWakePanePIDs = origPath, origPIDs })
	piWakeSocketFor = func(_, id string) string {
		if id == f.parent.ID {
			return w.ext.path
		}
		return ""
	}
	piWakePanePIDs = func(*Instance) (map[int]bool, error) { return map[int]bool{os.Getpid(): true}, nil }
	// Registered last, so it runs first: no leg of this fixture's notifier may
	// still be reading the globals the cleanups above restore (the race detector
	// treats that as a data race).
	t.Cleanup(func() { f.d.notifier.Flush() })
	return w
}

// settle waits for every doorbell leg dispatched so far: a typed doorbell is
// sent in the background, counted in the notifier's legs or in strayLegs, so a
// check that nothing was typed after it does not race the leg.
func (w *piWakeFx) settle() { w.d.notifier.Flush() }

// urgent commits a ledger-only urgent record, the kind that wakes a parent.
func (w *piWakeFx) urgent(t *testing.T, text string) comms.Record {
	t.Helper()
	return w.commit(t, comms.Record{Kind: comms.KindDelivery, State: comms.StateFailed, To: []string{w.parent.ID},
		Tier: comms.TierUrgent, Text: text, Done: "ok", Summary: text})
}

// ingest commits what the leg spooled and returns the wake records.
func (w *piWakeFx) wakeRecs(t *testing.T) []comms.Record {
	t.Helper()
	w.d.ingestCommsSpool("default", w.byID)
	return w.wakeRecords(t)
}

func jsonHas(t *testing.T, v any, key string) (any, bool) {
	t.Helper()
	raw, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	got, ok := m[key]
	return got, ok
}

var ulidRE = regexp.MustCompile(`^[0-9A-Z]{26}$`)

// Forge 1 + Max 2: the attempt is carried, with the doorbell's nonce, before
// any I/O, so a failure cannot be re-woken; the nonce is a ULID; a submitted
// reply is transport_accepted and never an ack.
func TestCommsDeliver_PiCarriesBeforeIOAndSubmittedIsTransportAcceptedNotAcked(t *testing.T) {
	var atIO map[string]any
	var atIOState comms.ConsumerFile
	var w *piWakeFx
	w = newPiWakeFixture(t, func(_ int, frame map[string]any, conn net.Conn) {
		st, _, _ := comms.ReadConsumer(w.l.Reader().Dir, w.parent.ID)
		atIOState = st
		atIO = frame
		answer(conn, frame, "submitted")
	})
	rec := w.urgent(t, "shipped the build")
	w.deliver(t, "waiting")

	if w.ext.count() != 1 {
		t.Fatalf("frames = %d, want one doorbell", w.ext.count())
	}
	fr := w.ext.frame(t, 0)
	nonce, _ := fr["id"].(string)
	if !ulidRE.MatchString(nonce) {
		t.Errorf("the frame id %q is not a ULID (26 uppercase alphanumerics)", nonce)
	}
	if ids, _ := fr["ids"].([]any); len(ids) != 1 || ids[0] != rec.ID {
		t.Errorf("frame ids = %v, want [%s]", fr["ids"], rec.ID)
	}
	if line, _ := fr["line"].(string); !strings.HasPrefix(line, comms.WakePrefix) || !strings.Contains(line, "#"+comms.ShortID(rec.ID)) {
		t.Errorf("frame line = %q", fr["line"])
	}
	// At the moment of I/O the attempt was already durable, bound to the nonce.
	if atIO == nil || len(atIOState.Inflight) != 1 {
		t.Fatalf("at I/O time the consumer state was %+v: Do #1 must carry before any I/O", atIOState)
	}
	in := atIOState.Inflight[0]
	if n, _ := jsonHas(t, in, "nonce"); n != nonce || in.Via != comms.ViaWake || in.State != comms.ReceiptAttempted {
		t.Errorf("attempt at I/O time: %+v (nonce %v), want a wake attempt bound to %s", in, n, nonce)
	}
	if atIOState.LastWake == 0 || atIOState.AutoWakes != 1 {
		t.Errorf("at I/O time LastWake %d AutoWakes %d, want set and 1", atIOState.LastWake, atIOState.AutoWakes)
	}
	// After the reply: transport_accepted, still owed (the hook acks), never typed.
	st := w.state(t)
	if len(st.Inflight) != 1 || st.Inflight[0].State != comms.ReceiptTransportAccepted {
		t.Errorf("after a submitted reply: %+v", st.Inflight)
	}
	if st.Pending != 1 || st.Watermark != 0 {
		t.Errorf("the socket reply moved the watermark: %+v", st)
	}
	if len(w.typed.all()) != 0 {
		t.Errorf("a socket wake typed %q", w.typed.all())
	}
	// One wake record, from the leg, with its outcome.
	recs := w.wakeRecs(t)
	if len(recs) != 1 || recs[0].Via != "socket" || recs[0].State != "landed" || recs[0].Ref != nonce {
		t.Errorf("wake records: %+v", recs)
	}
	// The record names the records the doorbell carried.
	if len(recs) == 1 && (len(recs[0].Refs) != 1 || recs[0].Refs[0] != rec.ID) {
		t.Errorf("wake record Refs = %v, want [%s]", recs[0].Refs, rec.ID)
	}
}

func TestCommsDeliver_PiQueuedStaysAttemptedThenHookAcks(t *testing.T) {
	w := newPiWakeFixture(t, replyWith("queued"))
	w.urgent(t, "shipped the build")
	w.deliver(t, "waiting")
	fr := w.ext.frame(t, 0)
	nonce, _ := fr["id"].(string)
	st := w.state(t)
	if len(st.Inflight) != 1 || st.Inflight[0].State != comms.ReceiptAttempted {
		t.Fatalf("a queued reply must leave the attempt attempted: %+v", st.Inflight)
	}
	if recs := w.wakeRecs(t); len(recs) != 1 || recs[0].State != "queued" || recs[0].Via != "socket" {
		t.Errorf("wake record for queued: %+v", recs)
	}
	// The run does start later; its hook names the doorbell, and acks it.
	dl, ok := LedgerPromptContext(w.parent.ID, fr["line"].(string), nonce, w.parent.Tool == "pi")
	if !ok {
		t.Fatal("enrolled pi parent fell back to the inbox")
	}
	if dl.Text != "" {
		t.Errorf("the line showed the record whole; nothing more to inject: %q", dl.Text)
	}
	dl.Done(true)
	if st := w.state(t); st.Pending != 0 || len(st.Inflight) != 0 {
		t.Errorf("after the hook: %+v", st)
	}
}

// R3 + Forge 1: whatever went wrong after the frame was written, nothing is
// typed, no second frame goes out in the pass, the records stay attempted with
// LastWake set, so the next poll does not re-wake; only the 90 s settle retries,
// once, under a new nonce.
func TestCommsDeliver_PiCommittedOrPushedIsNeverTyped(t *testing.T) {
	cases := map[string]func(int, map[string]any, net.Conn){
		"a connection reset after the frame": func(_ int, _ map[string]any, c net.Conn) {
			if uc, ok := c.(*net.UnixConn); ok {
				_ = uc.Close()
			}
		},
		"silence past the reply window": func(int, map[string]any, net.Conn) {},
		"a reply that is not JSON": func(_ int, _ map[string]any, c net.Conn) {
			_, _ = c.Write([]byte("whatever\n"))
		},
	}
	for name, script := range cases {
		t.Run(name, func(t *testing.T) {
			w := newPiWakeFixture(t, script)
			w.urgent(t, "shipped the build")
			w.deliver(t, "waiting")
			if w.ext.count() != 1 {
				t.Fatalf("frames = %d, want 1", w.ext.count())
			}
			if len(w.typed.all()) != 0 {
				t.Fatalf("typed %q after bytes crossed the socket", w.typed.all())
			}
			st := w.state(t)
			if len(st.Inflight) != 1 || st.Inflight[0].State != comms.ReceiptAttempted || st.LastWake == 0 || st.AutoWakes != 1 {
				t.Errorf("state after an unconfirmed wake: %+v", st)
			}
			recs := w.wakeRecs(t)
			for _, r := range recs {
				if r.Via == "tmux" {
					t.Errorf("a typed wake record for a socket wake: %+v", r)
				}
			}
			// The leg's default arm: the write may have landed, so the record says
			// pushed (never landed, never typed), with the ids it carried.
			if len(recs) != 1 || recs[0].Via != "socket" || recs[0].State != "pushed" || len(recs[0].Refs) != 1 {
				t.Errorf("wake records after an unconfirmed wake: %+v, want one via socket, state pushed, carrying its record id", recs)
			}
			// The next polls: the records are in flight and the debounce holds.
			w.clock = w.clock.Add(2 * time.Second)
			w.deliver(t, "waiting")
			w.clock = w.clock.Add(30 * time.Second)
			w.deliver(t, "waiting")
			if w.ext.count() != 1 || len(w.typed.all()) != 0 {
				t.Fatalf("a failed wake was re-rung before the settle: frames %d typed %q", w.ext.count(), w.typed.all())
			}
			// The settle (90 s): one more doorbell, under a new nonce, never typed.
			w.clock = w.clock.Add(61 * time.Second)
			w.deliver(t, "waiting")
			if w.ext.count() != 2 {
				t.Fatalf("after the settle frames = %d, want exactly one retry", w.ext.count())
			}
			if a, b := w.ext.frame(t, 0)["id"], w.ext.frame(t, 1)["id"]; a == b {
				t.Errorf("the retry reused nonce %v", a)
			}
			w.clock = w.clock.Add(2 * time.Minute)
			w.deliver(t, "waiting")
			if w.ext.count() != 2 || len(w.typed.all()) != 0 {
				t.Errorf("a second retry or a typed line: frames %d typed %q", w.ext.count(), w.typed.all())
			}
		})
	}
}

// Forge 2: a transient refusal returns the records to pending, types nothing,
// and keeps LastWake so the debounce still paces the next doorbell.
func TestCommsDeliver_PiRefusedWakeReturnsToPendingWithoutTyping(t *testing.T) {
	w := newPiWakeFixture(t, refuseWith("busy"))
	w.urgent(t, "shipped the build")
	w.deliver(t, "waiting")
	st := w.state(t)
	if len(st.Inflight) != 0 || st.Pending != 1 || st.AutoWakes != 0 || st.LastWake == 0 {
		t.Errorf("after a busy refusal: %+v", st)
	}
	if len(w.typed.all()) != 0 {
		t.Errorf("a refusal typed %q", w.typed.all())
	}
	if recs := w.wakeRecs(t); len(recs) != 0 {
		t.Errorf("a refusal left wake records: %+v", recs)
	}
	w.clock = w.clock.Add(5 * time.Second)
	w.deliver(t, "waiting")
	if w.ext.count() != 1 {
		t.Errorf("the debounce did not hold: %d frames in 5 s", w.ext.count())
	}
	w.clock = w.clock.Add(61 * time.Second)
	w.deliver(t, "waiting")
	if w.ext.count() != 2 {
		t.Errorf("the next doorbell after the debounce: %d frames, want 2", w.ext.count())
	}
}

// A transient refusal of the wake that took the last automatic slot gives the
// slot back, and the retry comes when the debounce ends (60 s), not at the 90 s
// settle the carry noted for that wake.
func TestCommsDeliver_PiRefusedLastSlotWakeRetriesAtTheDebounce(t *testing.T) {
	w := newPiWakeFixture(t, refuseWith("busy"))
	w.setAutoWakes(t, comms.MaxAutoWakes-1)
	w.urgent(t, "shipped the build")
	w.deliver(t, "waiting")
	if st := w.state(t); w.ext.count() != 1 || len(st.Inflight) != 0 || st.AutoWakes != comms.MaxAutoWakes-1 {
		t.Fatalf("after the refused last-slot wake: frames %d, %+v", w.ext.count(), st)
	}
	w.clock = w.clock.Add(61 * time.Second)
	w.deliver(t, "waiting")
	if w.ext.count() != 2 {
		t.Errorf("the retry after the debounce: %d frames, want 2 (the refused wake's settle deadline held it)", w.ext.count())
	}
}

// Max 5: a refusal never consumes the automatic wake cap, while the run of
// refusals is inside its bound (piRefusedWakeBound); every pass rings.
func TestCommsDeliver_PiRefusedWakeDoesNotConsumeTheAutoWakeCap(t *testing.T) {
	w := newPiWakeFixture(t, refuseWith("busy"))
	w.urgent(t, "shipped the build")
	passes := 0
	for start := w.clock; w.clock.Sub(start) < piRefusedWakeBound; w.clock = w.clock.Add(61 * time.Second) {
		w.deliver(t, "waiting")
		passes++
	}
	st := w.state(t)
	if st.AutoWakes != 0 || st.CapNoted {
		t.Errorf("after %d refusals: AutoWakes %d CapNoted %v", passes, st.AutoWakes, st.CapNoted)
	}
	if w.ext.count() != passes {
		t.Errorf("frames = %d, want one per pass (%d)", w.ext.count(), passes)
	}
	for _, r := range w.ledgerRecords(t) {
		if r.Kind == comms.KindError {
			t.Errorf("the cap note was written: %+v", r)
		}
	}
	if recs := w.wakeRecs(t); len(recs) != 0 {
		t.Errorf("a refusal inside the bound left wake records %+v", recs)
	}
}

// Review (#3024, should fix): a pi socket that stays down, or keeps answering
// busy, is not dialled every minute forever in silence. Five minutes after the
// first refusal of the run the wake is settled as a permanent refusal: its
// records stop being rung (they stay pending and ride the next run start), one
// failed wake record names the reason, and `pi-hooks status` lists the parent
// under "Socket wakes stopped". Nothing is typed.
func TestCommsDeliver_PiRefusedWakesAreBoundedThenListedAsStopped(t *testing.T) {
	for name, tc := range map[string]struct {
		reply  string
		down   bool
		reason string
	}{
		"busy":        {reply: "busy", reason: "busy_for_5m"},
		"duplicate":   {reply: "duplicate", reason: "duplicate_for_5m"},
		"socket down": {reply: "busy", down: true, reason: "socket_down_for_5m"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newPiWakeFixture(t, refuseWith(tc.reply))
			// Every ring resolves the socket path once, whether or not a frame
			// reaches the extension, so a ring is counted on every path.
			var dials atomic.Int32
			resolve := piWakeSocketFor
			piWakeSocketFor = func(profile, id string) string {
				dials.Add(1)
				if tc.down {
					return filepath.Join(filepath.Dir(w.ext.path), "none.sock")
				}
				return resolve(profile, id)
			}
			var logBuf syncBuffer
			prevLog := commsLog
			commsLog = slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))
			t.Cleanup(func() { w.settle(); commsLog = prevLog })
			w.urgent(t, "shipped the build")
			start := w.clock
			rings := func() int { return int(dials.Load()) + len(w.typed.all()) }
			var atBound int
			for i := 0; i < 12; i++ {
				w.deliver(t, "waiting")
				if w.clock.Sub(start) >= piRefusedWakeBound && atBound == 0 {
					atBound = i + 1
				}
				w.clock = w.clock.Add(61 * time.Second)
			}
			if atBound == 0 {
				t.Fatal("test setup: the passes never reached the bound")
			}
			if len(w.typed.all()) != 0 {
				t.Fatalf("typed %q: pi has no draft guard", w.typed.all())
			}
			// The pass that crossed the bound is the last to ring.
			if got := rings(); got != atBound {
				t.Errorf("rings = %d, want %d: a ring per pass until the bound, none after", got, atBound)
			}
			if !tc.down && w.ext.count() != atBound {
				t.Errorf("frames = %d, want %d", w.ext.count(), atBound)
			}
			before := rings()
			for _, step := range []time.Duration{5 * time.Minute, time.Hour} {
				w.clock = w.clock.Add(step)
				w.deliver(t, "waiting")
			}
			if rings() != before {
				t.Errorf("the wake was rung again after the bound (%d rings, was %d)", rings(), before)
			}
			st := w.state(t)
			if st.Pending != 1 || len(st.Inflight) != 0 {
				t.Errorf("the record is not back to pending after the bound: %+v", st)
			}
			var failed []comms.Record
			for _, r := range w.wakeRecs(t) {
				if r.Via == "socket" && r.State == comms.StateFailed {
					failed = append(failed, r)
				}
			}
			if len(failed) != 1 || failed[0].Err != tc.reason {
				t.Fatalf("want one failed socket wake record with err %q, got %+v", tc.reason, failed)
			}
			stopped := PiSocketWakesStopped()
			if len(stopped) != 1 || stopped[0].Parent != w.parent.ID || stopped[0].Reason != tc.reason {
				t.Errorf("pi-hooks status lists %+v, want the parent with %q", stopped, tc.reason)
			}
			w.settle()
			if logged := logBuf.take(); strings.Count(logged, `"pi_wake_refused_too_long"`) != 1 {
				t.Errorf("want pi_wake_refused_too_long logged once: %s", logged)
			}
		})
	}
}

// An answer that takes ends the run of refusals: a pi that was busy for four
// minutes, then took a wake, starts a fresh five minutes the next time it is
// refused.
func TestCommsDeliver_PiRefusedWakeBoundRestartsAfterAnAnswer(t *testing.T) {
	var mu sync.Mutex
	queued := false
	w := newPiWakeFixture(t, func(_ int, f map[string]any, c net.Conn) {
		mu.Lock()
		q := queued
		mu.Unlock()
		if q {
			answer(c, f, "queued")
			return
		}
		refuse(c, f, "busy")
	})
	w.urgent(t, "shipped the build")
	for i := 0; i < 4; i++ { // 0..3 min: refused
		w.deliver(t, "waiting")
		w.clock = w.clock.Add(61 * time.Second)
	}
	mu.Lock()
	queued = true
	mu.Unlock()
	w.deliver(t, "waiting") // answered: the run ends
	mu.Lock()
	queued = false
	mu.Unlock()
	if st := w.state(t); st.RefusedSince != 0 {
		t.Fatalf("an answer left RefusedSince %d", st.RefusedSince)
	}
	w.urgent(t, "and the docs")
	for i := 0; i < 4; i++ { // a fresh run: four more minutes refused, inside its own bound
		w.clock = w.clock.Add(61 * time.Second)
		w.deliver(t, "waiting")
	}
	for _, r := range w.wakeRecs(t) {
		if r.Via == "socket" && r.State == comms.StateFailed {
			t.Errorf("a refusal inside a fresh run was settled as failed: %+v", r)
		}
	}
}

// Maintainer (#3024, wake_send.go:278): the run of refusals measures one
// episode. Once the bound settles it and a run start delivers the records, a new
// urgent record hours later is not settled by its first refusal: it is rung
// again on the next pass, with no second failed record.
func TestCommsDeliver_PiRefusedWakeRunEndsWithItsEpisode(t *testing.T) {
	w := newPiWakeFixture(t, refuseWith("busy"))
	w.urgent(t, "shipped the build")
	start := w.clock
	for w.clock.Sub(start) <= piRefusedWakeBound+time.Minute {
		w.deliver(t, "waiting")
		w.clock = w.clock.Add(61 * time.Second)
	}
	failed := func() int {
		n := 0
		for _, r := range w.wakeRecs(t) {
			if r.Via == "socket" && r.State == comms.StateFailed {
				n++
			}
		}
		return n
	}
	if failed() != 1 {
		t.Fatalf("test setup: want the first episode settled with one failed record, got %d", failed())
	}
	// The operator prompts pi: the records ride the run start and are acked.
	dl, ok := LedgerPromptContext(w.parent.ID, "carry on", "", w.parent.Tool == "pi")
	if !ok || !strings.Contains(dl.Text, "shipped the build") {
		t.Fatalf("the run start did not carry the settled record (ok %v)", ok)
	}
	dl.Done(true)
	// The extension returned the context to pi and echoed the receipt; the next
	// run start settles the injection.
	w.injectedEdge(t, tokenOf(t, dl))
	dl2, ok := LedgerPromptContext(w.parent.ID, "and again", "", w.parent.Tool == "pi")
	if !ok {
		t.Fatal("enrolled parent fell back to the inbox path")
	}
	dl2.Done(true)
	w.deliver(t, "waiting")
	if st := w.state(t); st.Pending != 0 {
		t.Fatalf("test setup: the run start did not deliver the record: %+v", st)
	}

	// Hours later a new urgent record's first ring meets one transient refusal.
	w.clock = w.clock.Add(3 * time.Hour)
	w.urgent(t, "and the docs")
	frames := w.ext.count()
	w.deliver(t, "waiting")
	if w.ext.count() != frames+1 {
		t.Fatalf("the new record was not rung (frames %d, was %d)", w.ext.count(), frames)
	}
	if n := failed(); n != 1 {
		t.Errorf("one refusal of a new episode was settled at once: %d failed socket wake records, want 1", n)
	}
	w.clock = w.clock.Add(61 * time.Second)
	w.deliver(t, "waiting")
	if w.ext.count() != frames+2 {
		t.Errorf("the new record was not rung again on the next pass (frames %d, want %d)", w.ext.count(), frames+2)
	}
}

// Forge (p5): the inbox doorbell rides the same socket, so it keeps the run of
// refusals true too. A refused inbox doorbell starts the run, silence (pushed)
// leaves it, and an answered one ends it: the ledger's refusals after the answer
// get a fresh five minutes.
func TestCommsDeliver_PiRefusedWakeRunFollowsTheInboxDoorbell(t *testing.T) {
	var mu sync.Mutex
	mode := "busy"
	setMode := func(m string) {
		mu.Lock()
		mode = m
		mu.Unlock()
	}
	w := newPiWakeFixture(t, func(_ int, f map[string]any, c net.Conn) {
		mu.Lock()
		m := mode
		mu.Unlock()
		switch m {
		case "queued":
			answer(c, f, "queued")
		case "busy":
			refuse(c, f, "busy")
		} // "silent": no reply, the frame reads as pushed
	})
	line := comms.WakePrefix + " 1 pending (1 urgent; data): [urgent] kid (pi) #ABC123: «done»"
	if _, err := wakeSend(w.parent, "default", line, "01J0ABCDEF0123456789ABCDE1", []string{"r1"}); err != nil {
		t.Fatalf("wakeSend: %v", err)
	}
	started := w.clock.UnixMilli()
	if st := w.state(t); st.RefusedSince != started {
		t.Errorf("a refused inbox doorbell did not start the run: RefusedSince %d, want %d", st.RefusedSince, started)
	}
	setMode("silent")
	w.clock = w.clock.Add(time.Second)
	if _, err := wakeSend(w.parent, "default", line, "01J0ABCDEF0123456789ABCDE3", []string{"r1"}); err != nil {
		t.Fatalf("wakeSend: %v", err)
	}
	if st := w.state(t); st.RefusedSince != started {
		t.Errorf("a silent inbox doorbell (no reply) changed the run: RefusedSince %d, want %d", st.RefusedSince, started)
	}
	setMode("busy")
	w.urgent(t, "shipped the build")
	for i := 0; i < 4; i++ { // the ledger is refused for four minutes
		w.clock = w.clock.Add(61 * time.Second)
		w.deliver(t, "waiting")
	}
	setMode("queued")
	if _, err := wakeSend(w.parent, "default", line, "01J0ABCDEF0123456789ABCDE2", []string{"r1"}); err != nil {
		t.Fatalf("wakeSend: %v", err)
	}
	setMode("busy")
	if st := w.state(t); st.RefusedSince != 0 {
		t.Fatalf("an answered inbox doorbell left RefusedSince %d", st.RefusedSince)
	}
	for i := 0; i < 4; i++ { // four more minutes refused: inside the fresh run
		w.clock = w.clock.Add(61 * time.Second)
		w.deliver(t, "waiting")
	}
	for _, r := range w.wakeRecs(t) {
		if r.Via == "socket" && r.State == comms.StateFailed {
			t.Errorf("the ledger's refusals after an answered inbox doorbell were settled as failed: %+v", r)
		}
	}
}

// R-B (revising the earlier permanent-refusal ruling with new evidence): pi has
// no composer-draft guard, so a typed doorbell can merge into and submit an
// operator's draft. Every refusal, post-write or not, therefore types nothing:
// transient ones (busy, duplicate) return the records to pending and give the
// wake back; permanent ones (bad_request, an unknown word) leave the records
// pending, keep the wake consumed, leave a wake record with the reason, and stop
// socket wakes for that record set. The records ride the next run start.
func TestCommsDeliver_PiPostWriteRefusalNeverTypes(t *testing.T) {
	for _, state := range []string{"busy", "duplicate"} {
		t.Run("transient "+state, func(t *testing.T) {
			w := newPiWakeFixture(t, refuseWith(state))
			w.urgent(t, "shipped the build")
			w.deliver(t, "waiting")
			if len(w.typed.all()) != 0 || w.state(t).Pending != 1 || len(w.state(t).Inflight) != 0 || w.state(t).AutoWakes != 0 {
				t.Errorf("%s: typed %q state %+v", state, w.typed.all(), w.state(t))
			}
			if recs := w.wakeRecs(t); len(recs) != 0 {
				t.Errorf("%s left wake records %+v", state, recs)
			}
		})
	}
	for _, state := range []string{"bad_request", "something_new"} {
		t.Run("permanent "+state, func(t *testing.T) {
			w := newPiWakeFixture(t, refuseWith(state))
			rec := w.urgent(t, "shipped the build")
			w.deliver(t, "waiting")
			if w.ext.count() != 1 {
				t.Fatalf("frames = %d, want 1", w.ext.count())
			}
			w.settle() // every doorbell leg dispatched so far has finished
			if typed := w.typed.all(); len(typed) != 0 {
				t.Fatalf("typed %q after a refusal: pi has no draft guard", typed)
			}
			var withReason []comms.Record
			for _, r := range w.wakeRecs(t) {
				if e, _ := jsonHas(t, r, "err"); e == state {
					withReason = append(withReason, r)
				}
				if r.Via == "tmux" {
					t.Errorf("a typed wake record for a refusal: %+v", r)
				}
			}
			if len(withReason) != 1 {
				t.Errorf("want one wake record carrying the reason %q, got %+v", state, w.wakeRecs(t))
			}
			if st := w.state(t); st.AutoWakes != 1 {
				t.Errorf("a permanent refusal keeps the wake consumed: AutoWakes %d, want 1", st.AutoWakes)
			}
			// No more socket wakes for this record set, however long we wait.
			for _, step := range []time.Duration{61 * time.Second, 5 * time.Minute, 5 * time.Minute} {
				w.clock = w.clock.Add(step)
				w.deliver(t, "waiting")
			}
			if w.ext.count() != 1 || len(w.typed.all()) != 0 {
				t.Errorf("a permanent refusal repeated: frames %d typed %q", w.ext.count(), w.typed.all())
			}
			// The records are still owed and arrive with the next run start.
			dl, ok := LedgerPromptContext(w.parent.ID, "carry on", "", w.parent.Tool == "pi")
			if !ok || !strings.Contains(dl.Text, "shipped the build") {
				t.Errorf("the refused records did not stay deliverable (ok %v, record %s)", ok, rec.ID)
			}
			dl.Done(false)
		})
	}
}

// A client-local refusal (an empty nonce or id) is a refusal like any other:
// nothing is typed, and a wake record names the reason. An over-cap line is not
// refused: the leg fits it to the frame first (TestPiWakeLeg_FitsTheFrameTheExtensionTakes).
func TestCommsDeliver_PiClientLocalRefusalTypesNothing(t *testing.T) {
	line := comms.WakePrefix + " 1 pending (1 urgent; data): [urgent] kid (pi) #ABC123: «done»"
	nonce := "01J0ABCDEF0123456789ABCDEF"
	for name, call := range map[string]func(w *piWakeFx) error{
		"an empty nonce": func(w *piWakeFx) error { _, e := wakeSend(w.parent, "default", line, "", []string{"r1"}); return e },
		"an empty id":    func(w *piWakeFx) error { _, e := wakeSend(w.parent, "default", line, nonce, []string{""}); return e },
	} {
		t.Run(name, func(t *testing.T) {
			w := newPiWakeFixture(t, replyWith("submitted"))
			_ = call(w)
			w.settle()
			if typed := w.typed.all(); len(typed) != 0 {
				t.Fatalf("typed %q for a refused call", typed)
			}
			if w.ext.count() != 0 {
				t.Errorf("a frame reached the extension (%d) although the call is refused before the write", w.ext.count())
			}
			recs := w.wakeRecs(t)
			if len(recs) != 1 {
				t.Fatalf("want one wake record with the reason, got %+v", recs)
			}
			if e, _ := jsonHas(t, recs[0], "err"); e == nil || e == "" {
				t.Errorf("the wake record carries no reason: %+v", recs[0])
			}
		})
	}
}

// Only a pi that is provably ABSENT (no socket configured, no socket file, or a
// stale file nobody listens on) gets the typed doorbell, as pi has always been
// woken, and it is dispatched in the background like Claude's: the call returns
// before the typing subprocess does. Every other unavailable (below) types nothing.
func TestCommsDeliver_PiUnavailableTypesOnceInTheBackground(t *testing.T) {
	line := comms.WakePrefix + " 1 pending (1 urgent; data): [urgent] kid (pi) #ABC123: «done»"
	nonce := "01J0ABCDEF0123456789ABCDEF"
	for name, setup := range map[string]func(w *piWakeFx){
		"no socket file": func(w *piWakeFx) {
			piWakeSocketFor = func(string, string) string { return filepath.Join(filepath.Dir(w.ext.path), "none.sock") }
		},
		"no socket path (socket off or no env)": func(w *piWakeFx) {
			piWakeSocketFor = func(string, string) string { return "" }
		},
		"a stale socket file with no listener": func(w *piWakeFx) {
			stale := filepath.Join(filepath.Dir(w.ext.path), "stale.sock")
			ln, err := net.Listen("unix", stale)
			if err != nil {
				t.Fatal(err)
			}
			ln.(*net.UnixListener).SetUnlinkOnClose(false)
			_ = ln.Close() // the file stays, nobody listens: ECONNREFUSED
			piWakeSocketFor = func(string, string) string { return stale }
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := newPiWakeFixture(t, replyWith("submitted"))
			// Relaunched without the socket: still enrolled (its marker drains
			// at the next pass), so its doorbells reach the leg, typed.
			w.parent.PiSocketExported = false
			setup(w)
			release := w.typed.holdSends()
			defer release()
			start := time.Now()
			if _, err := wakeSend(w.parent, "default", line, nonce, []string{"r1"}); err != nil {
				t.Fatalf("wakeSend: %v", err)
			}
			if took := time.Since(start); took > 400*time.Millisecond {
				t.Errorf("wakeSend waited %v for the typing subprocess, want it dispatched in the background", took)
			}
			if typed := w.typed.waitTyped(t, 1); len(typed) != 1 {
				t.Fatalf("typed %q, want the doorbell once", typed)
			}
			release()
			if w.ext.count() != 0 {
				t.Errorf("a frame reached the extension (%d)", w.ext.count())
			}
		})
	}
}

// Max 1 asked that a typed fallback never hold the ledger pass. Since a pi
// enrols only while its launch exports the socket (review item 4), the pass
// never types into a pi at all: a listed pi relaunched without the socket is
// drained by the next pass, which rings nothing and types nothing. Its doorbells
// still reach the leg while the marker drains, and that typed send is dispatched
// in the background (TestCommsDeliver_PiUnavailableTypesOnceInTheBackground).
func TestCommsDeliver_PiRelaunchedWithoutTheSocketIsDrainedAndTheLedgerPassTypesNothing(t *testing.T) {
	w := newPiWakeFixture(t, replyWith("submitted"))
	w.parent.PiSocketExported = false
	piWakeSocketFor = func(string, string) string { return filepath.Join(filepath.Dir(w.ext.path), "none.sock") }
	w.urgent(t, "shipped the build")
	start := time.Now()
	w.deliver(t, "waiting")
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Errorf("the ledger pass took %v", took)
	}
	if en, ok := LoadCommsEnrollment(w.parent.ID); !ok || !en.Draining {
		t.Fatalf("a listed pi relaunched without the socket is not draining: %+v (found %v)", en, ok)
	}
	w.settle()
	if typed := w.typed.all(); len(typed) != 0 || w.ext.count() != 0 {
		t.Errorf("the ledger pass rang a pi launched without the socket: typed %q, frames %d", typed, w.ext.count())
	}
}

// R2: a busy pi is refused, its records stay pending, and the next run start
// delivers them exactly once.
func TestCommsDeliver_PiBusyPendingLandsAtNextRunStartExactlyOnce(t *testing.T) {
	w := newPiWakeFixture(t, refuseWith("busy"))
	w.urgent(t, "shipped the build")
	w.deliver(t, "waiting")
	if st := w.state(t); st.Pending != 1 || st.Watermark != 0 {
		t.Fatalf("a refusal moved the watermark or lost the record: %+v", st)
	}
	// The next run start (a human prompt) carries the record once.
	dl, ok := LedgerPromptContext(w.parent.ID, "carry on", "", w.parent.Tool == "pi")
	if !ok || !strings.Contains(dl.Text, "shipped the build") {
		t.Fatalf("the next run start did not carry the pending record (ok %v)", ok)
	}
	dl.Done(true)
	// The extension returned the context to pi and echoed the receipt.
	w.injectedEdge(t, tokenOf(t, dl))
	// A second run start injects nothing, and the first injection is acked.
	dl2, ok := LedgerPromptContext(w.parent.ID, "and again", "", w.parent.Tool == "pi")
	if !ok || strings.Contains(dl2.Text, "shipped the build") {
		t.Fatalf("the record was injected twice: %q", dl2.Text)
	}
	dl2.Done(true)
	if st := w.state(t); st.Pending != 0 || len(st.Inflight) != 0 {
		t.Errorf("after two run starts: %+v", st)
	}
}

// The sibling of the above (R-A): printing is not a receipt. Without the token
// echo the first injection is not known to have reached pi, so the records are
// injected again at the next run start rather than acked unseen.
func TestCommsDeliver_PiBusyPendingWithoutAReceiptIsInjectedAgain(t *testing.T) {
	w := newPiWakeFixture(t, refuseWith("busy"))
	w.urgent(t, "shipped the build")
	w.deliver(t, "waiting")
	dl, ok := LedgerPromptContext(w.parent.ID, "carry on", "", w.parent.Tool == "pi")
	if !ok || !strings.Contains(dl.Text, "shipped the build") {
		t.Fatalf("the next run start did not carry the pending record (ok %v)", ok)
	}
	dl.Done(true)                          // printed; the extension timed out or the hook failed: no receipt
	w.clock = w.clock.Add(2 * time.Minute) // past the receipt window
	dl2, ok := LedgerPromptContext(w.parent.ID, "and again", "", w.parent.Tool == "pi")
	if !ok || !strings.Contains(dl2.Text, "shipped the build") {
		t.Fatalf("without a receipt the record must be injected again (ok %v): %q", ok, dl2.Text)
	}
	dl2.Done(true)
	if st := w.state(t); st.Pending != 1 {
		t.Errorf("the record was acked without a receipt: %+v", st)
	}
}

// R2: the inbox doorbell and the ledger wake are one doorbell. The inbox path
// rings first and its Do #1 sets LastWake, so the ledger's debounce makes the
// second a no-op; both records ride the one run start, each acked once.
func TestInboxNudge_PiInboxAndLedgerDoorbellsInOnePassRingOnce(t *testing.T) {
	w := newPiWakeFixture(t, replyWith("submitted"))
	w.d.notifier.wake.send = sendWakeNudge // the production sender: a goroutine into the leg
	rec := w.urgent(t, "ledger-only urgent record")
	ev := TransitionNotificationEvent{ChildSessionID: w.child.ID, ChildTitle: "kid", Profile: "default", ToStatus: "waiting",
		TargetSessionID: w.parent.ID, TargetKind: "parent", Tier: TurnTierUrgent, Text: "inbox result text", Timestamp: time.Now()}
	if err := CommitToInbox(w.parent.ID, ev); err != nil {
		t.Fatal(err)
	}
	w.d.notifier.fireWakeNudge(w.parent, ev) // the inbox doorbell, at commit
	first := w.ext.waitFrame(t)
	if ids, _ := first["ids"].([]any); len(ids) == 0 || ids[0] == "" {
		t.Errorf("the inbox doorbell carries the inbox record's id: %v", first["ids"])
	}
	w.deliver(t, "waiting") // the ledger pass of the same poll
	w.settle()
	if w.ext.count() != 1 {
		t.Fatalf("frames = %d, want one doorbell for both", w.ext.count())
	}
	if st := w.state(t); st.LastWake == 0 {
		t.Error("the inbox doorbell did not set LastWake, so the ledger's debounce cannot suppress the second")
	}
	if len(w.typed.all()) != 0 {
		t.Errorf("typed %q", w.typed.all())
	}
	dl, ok := LedgerPromptContext(w.parent.ID, "[agent-deck msg] 1 pending", first["id"].(string), w.parent.Tool == "pi")
	if !ok {
		t.Fatal("fell back to the inbox")
	}
	if !strings.Contains(dl.Text, "ledger-only urgent record") && !strings.Contains(dl.Text, comms.ShortID(rec.ID)) {
		t.Errorf("the ledger record did not ride the run start: %q", dl.Text)
	}
	if !strings.Contains(dl.Text, "inbox result text") {
		t.Errorf("the inbox record did not ride the run start: %q", dl.Text)
	}
	dl.Done(true)
}

// Forge 7: the socket I/O (up to ~3 s) happens outside the consumer lock.
func TestCommsDeliver_PiIOHappensOutsideTheConsumerLock(t *testing.T) {
	release := make(chan struct{})
	w := newPiWakeFixture(t, func(_ int, frame map[string]any, conn net.Conn) {
		<-release
		answer(conn, frame, "submitted")
	})
	w.urgent(t, "shipped the build")
	done := make(chan struct{})
	go func() { w.deliver(t, "waiting"); close(done) }()
	w.ext.waitFrame(t)
	start := time.Now()
	dl, ok := LedgerPromptContext(w.parent.ID, "my own prompt", "", w.parent.Tool == "pi")
	took := time.Since(start)
	close(release)
	<-done
	if !ok {
		t.Fatal("fell back to the inbox")
	}
	dl.Done(false)
	if took > 750*time.Millisecond {
		t.Errorf("the hook waited %v for the wake's socket I/O: the consumer lock was held across it", took)
	}
}

// Max 2: Do #2 is compare-and-set. It touches only the attempt it carried (via
// wake, its nonce); an attempt the hook replaced or acked is left to the hook.
func TestCommsDeliver_PiSecondDoIgnoresAttemptsTheHookReplaced(t *testing.T) {
	t.Run("the hook first: it replaced the attempt, the reply must not promote it", func(t *testing.T) {
		release := make(chan struct{})
		w := newPiWakeFixture(t, func(_ int, frame map[string]any, conn net.Conn) {
			<-release
			answer(conn, frame, "submitted")
		})
		// A long record: the wake line only previews it, so the hook injects it whole.
		w.commit(t, comms.Record{Kind: comms.KindDelivery, State: comms.StateFailed, To: []string{w.parent.ID}, Tier: comms.TierUrgent,
			Text: "start " + strings.Repeat("x", 2000) + " TAILMARKER", Done: "ok", Summary: "start " + strings.Repeat("x", 2000) + " TAILMARKER"})
		done := make(chan struct{})
		go func() { w.deliver(t, "waiting"); close(done) }()
		fr := w.ext.waitFrame(t)
		dl, ok := LedgerPromptContext(w.parent.ID, fr["line"].(string), fr["id"].(string), w.parent.Tool == "pi")
		if !ok || !strings.Contains(dl.Text, "TAILMARKER") {
			t.Fatalf("the hook must inject the previewed record whole (ok %v)", ok)
		}
		close(release) // now the reply: submitted
		<-done
		st := w.state(t)
		if len(st.Inflight) != 1 || st.Inflight[0].Via != comms.ViaPrompt || st.Inflight[0].State != comms.ReceiptAttempted {
			t.Errorf("the socket reply touched the attempt the hook carried: %+v", st.Inflight)
		}
		dl.Done(true)
		// Printing is not a receipt: the attempt stays attempted until pi echoes the token.
		if st := w.state(t); len(st.Inflight) != 1 || st.Inflight[0].State != comms.ReceiptAttempted {
			t.Errorf("after the hook printed: %+v", st.Inflight)
		}
		w.injectedEdge(t, tokenOf(t, dl))
		if st := w.state(t); len(st.Inflight) != 1 || st.Inflight[0].State != comms.ReceiptTransportAccepted {
			t.Errorf("after the receipt: %+v", st.Inflight)
		}
	})

	t.Run("the reply first: the attempt is accepted by nonce, then the hook acks it", func(t *testing.T) {
		w := newPiWakeFixture(t, replyWith("submitted"))
		w.urgent(t, "shipped the build")
		w.deliver(t, "waiting")
		fr := w.ext.frame(t, 0)
		if st := w.state(t); len(st.Inflight) != 1 || st.Inflight[0].State != comms.ReceiptTransportAccepted {
			t.Fatalf("after the reply: %+v", st.Inflight)
		}
		dl, ok := LedgerPromptContext(w.parent.ID, fr["line"].(string), fr["id"].(string), w.parent.Tool == "pi")
		if !ok {
			t.Fatal("fell back to the inbox")
		}
		dl.Done(true)
		if st := w.state(t); st.Pending != 0 || len(st.Inflight) != 0 {
			t.Errorf("after the hook: %+v", st)
		}
	})
}

// waitBudgetVia polls for the session.budget_exceeded frames of one session and
// returns their via, giving the asynchronous publish up to 3 s.
func waitBudgetVia(t *testing.T, home, session string) []any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var via []any
		for _, f := range budgetFramesUnder(t, home) {
			if f["session_id"] == session || f["id"] == session {
				data, _ := f["data"].(map[string]any)
				via = append(via, data["via"])
			}
		}
		if len(via) > 0 || time.Now().After(deadline) {
			return via
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// R5, the daemon half: a spent budget stops the wake before anything is carried.
func TestCommsDeliver_PiWakeHonoursTheSendBudgetBeforeCarry(t *testing.T) {
	w := newPiWakeFixtureIn(t, uniqueProfile("budget"), replyWith("submitted"))
	quietBusTeardown(t)
	t.Setenv("AGENTDECK_EVENTS_BUS", "1") // TestMain turns the bus off for the package
	cfgPath, err := GetUserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		ClearUserConfigCache()
	}
	t.Cleanup(ClearUserConfigCache)
	write(enforcedDaily1)
	seed, err := NewStorageWithProfile(w.profileName())
	if err != nil {
		t.Fatal(err)
	}
	if err := costs.NewStore(seed.GetDB().DB()).WriteCostEvent(costs.CostEvent{ID: "ev-pi-parent", SessionID: w.parent.ID,
		Timestamp: time.Now(), Model: "claude-test", InputTokens: 1000, CostMicrodollars: 5_000_000}); err != nil {
		t.Fatal(err)
	}
	_ = seed.Close()

	w.urgent(t, "shipped the build")
	w.deliver(t, "waiting")
	st := w.state(t)
	if w.ext.count() != 0 || len(w.typed.all()) != 0 {
		t.Errorf("a spent budget still woke the parent: frames %d typed %q", w.ext.count(), w.typed.all())
	}
	if len(st.Inflight) != 0 || st.LastWake != 0 || st.AutoWakes != 0 || st.Pending != 1 {
		t.Errorf("a spent budget carried or debounced: %+v", st)
	}
	// The bus publish is asynchronous by design: poll for the frame.
	if via := waitBudgetVia(t, os.Getenv("HOME"), w.parent.ID); len(via) != 1 || via[0] != "socket" {
		t.Errorf("session.budget_exceeded frames for the parent: via %v, want one with via socket", via)
	}

	// Not spent: the same record wakes.
	write("[costs.budgets]\nenforce = true\ndaily_limit = 100.0\n")
	w.clock = w.clock.Add(61 * time.Second)
	w.deliver(t, "waiting")
	if w.ext.count() != 1 {
		t.Errorf("under budget the wake must go out: %d frames", w.ext.count())
	}
}

// Forge 5 + Max 3: every doorbell path rides the leg for a pi parent, and a
// Claude parent still types on all three.
func TestInboxNudge_PiParentRidesTheSocketOnAllThreeDoorbellPaths(t *testing.T) {
	ev := func(f *deliverFixture) TransitionNotificationEvent {
		return TransitionNotificationEvent{ChildSessionID: f.child.ID, ChildTitle: "kid", Profile: "default", ToStatus: "waiting",
			TargetSessionID: f.parent.ID, TargetKind: "parent", Tier: TurnTierUrgent, Text: "inbox result", Timestamp: time.Now()}
	}
	paths := map[string]func(t *testing.T, f *deliverFixture, e TransitionNotificationEvent) *TransitionNotifier{
		"the daemon's default wiring": func(_ *testing.T, f *deliverFixture, e TransitionNotificationEvent) *TransitionNotifier {
			w := defaultWakeNudgeWiring()
			w.isIdle = func(*Instance, string) bool { return true }
			w.nudger = NewWakeNudger(0)
			n := &TransitionNotifier{wake: w}
			n.fireWakeNudge(f.parent, e)
			return n
		},
		"the remote talkback scheduler": func(_ *testing.T, f *deliverFixture, e TransitionNotificationEvent) *TransitionNotifier {
			w := defaultWakeNudgeWiring()
			w.isIdle = func(*Instance, string) bool { return true }
			w.nudger = NewWakeNudger(0)
			f.d.notifier = &TransitionNotifier{wake: w}
			f.d.newRemoteTalkbackScheduler().alert("r1", remoteTalkbackTarget{Parent: f.parent, Profile: "default"}, "remote r1 talkback failing", time.Now())
			return f.d.notifier
		},
		"the CLI drain's remoteWakeWiring": func(t *testing.T, f *deliverFixture, e TransitionNotificationEvent) *TransitionNotifier {
			orig := remoteWakeWiring
			t.Cleanup(func() { remoteWakeWiring = orig })
			remoteWakeWiring = func() *wakeNudgeWiring {
				w := orig()
				w.isIdle = func(*Instance, string) bool { return true }
				w.nudger = NewWakeNudger(0)
				return w
			}
			WakeParentForRecord(f.parent, "default", e) // its send is synchronous: no goroutine to wait for
			return nil
		},
	}
	for name, ring := range paths {
		t.Run("pi via "+name, func(t *testing.T) {
			w := newPiWakeFixture(t, replyWith("submitted"))
			n := ring(t, w.deliverFixture, ev(w.deliverFixture))
			if n != nil {
				defer n.Flush()
			}
			w.ext.waitFrame(t)
			if n != nil {
				n.Flush()
			}
			w.settle()
			if w.ext.count() != 1 || len(w.typed.all()) != 0 {
				t.Errorf("frames %d typed %q, want one frame and nothing typed", w.ext.count(), w.typed.all())
			}
		})
		t.Run("claude via "+name, func(t *testing.T) {
			f := newDeliverFixture(t) // a Claude parent
			spy := newTypedSpy(t)
			ext := newFakeExt(t, replyWith("submitted"))
			origPath := piWakeSocketFor
			t.Cleanup(func() { piWakeSocketFor = origPath })
			piWakeSocketFor = func(string, string) string { return ext.path }
			n := ring(t, f, ev(f))
			if n != nil {
				defer n.Flush()
			}
			deadline := time.Now().Add(5 * time.Second)
			for len(spy.all()) == 0 && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
			if len(spy.all()) != 1 || ext.count() != 0 {
				t.Errorf("claude: typed %q frames %d, want one typed line and no frame", spy.all(), ext.count())
			}
		})
	}
}

// Max 4: the wake record comes from the send leg with its outcome; fireWakeNudge
// adds no second one. A refusal leaves none. Claude's synchronous tmux record is
// unchanged.
func TestInboxNudge_PiWakeRecordComesFromTheSendLegWithItsOutcome(t *testing.T) {
	fire := func(f *deliverFixture) *TransitionNotifier {
		w := defaultWakeNudgeWiring()
		w.isIdle = func(*Instance, string) bool { return true }
		w.nudger = NewWakeNudger(0)
		n := &TransitionNotifier{wake: w}
		n.fireWakeNudge(f.parent, TransitionNotificationEvent{ChildSessionID: f.child.ID, ChildTitle: "kid",
			Profile: "default", ToStatus: "waiting", TargetSessionID: f.parent.ID, TargetKind: "parent", Tier: TurnTierUrgent, Text: "r", Timestamp: time.Now()})
		return n
	}
	t.Run("a socket wake: one via socket record with the leg's state, none via tmux", func(t *testing.T) {
		w := newPiWakeFixture(t, replyWith("submitted"))
		n := fire(w.deliverFixture)
		fr := w.ext.waitFrame(t)
		n.Flush() // the leg spools its record before it finishes
		recs := w.wakeRecs(t)
		if len(recs) != 1 || recs[0].Via != "socket" || recs[0].State != "landed" || recs[0].Ref != fr["id"] {
			t.Errorf("wake records: %+v", recs)
		}
	})
	t.Run("a refused wake leaves no record", func(t *testing.T) {
		w := newPiWakeFixture(t, refuseWith("busy"))
		n := fire(w.deliverFixture)
		w.ext.waitFrame(t)
		n.Flush()
		if recs := w.wakeRecs(t); len(recs) != 0 {
			t.Errorf("wake records for a refusal: %+v", recs)
		}
	})
	t.Run("claude: the synchronous tmux record as today", func(t *testing.T) {
		f := newDeliverFixture(t)
		w := defaultWakeNudgeWiring()
		w.isIdle = func(*Instance, string) bool { return true }
		w.nudger = NewWakeNudger(0)
		spy := newTypedSpy(t)
		(&TransitionNotifier{wake: w}).fireWakeNudge(f.parent, TransitionNotificationEvent{ChildSessionID: f.child.ID, ChildTitle: "kid",
			Profile: "default", ToStatus: "waiting", TargetSessionID: f.parent.ID, TargetKind: "parent", Tier: TurnTierUrgent, Text: "r", Timestamp: time.Now()})
		// The typed send runs in a goroutine that reads wakeNudgeExec: wait for it
		// to have run before the cleanup restores the variable.
		spy.waitTyped(t, 1)
		f.d.ingestCommsSpool("default", f.byID)
		recs := f.wakeRecords(t)
		if len(recs) != 1 || recs[0].Via != "tmux" || recs[0].State != comms.StateTyped {
			t.Errorf("claude wake records: %+v", recs)
		}
	})
}

// A group daily limit sums the sessions of the target's group, as the CLI send
// does: neither session is over alone, together they are, and the socket wake is
// stopped before anything is carried.
func TestCommsDeliver_PiWakeAppliesAGroupDailyLimitAcrossSiblings(t *testing.T) {
	w := newPiWakeFixtureIn(t, uniqueProfile("group"), replyWith("submitted"))
	quietBusTeardown(t)
	t.Setenv("AGENTDECK_EVENTS_BUS", "1")
	cfgPath, err := GetUserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		ClearUserConfigCache()
	}
	t.Cleanup(ClearUserConfigCache)
	write("[costs.budgets.groups.team]\ndaily_limit = 1.0\n[costs.budgets]\nenforce = true\n")

	// The parent and a sibling share a group; each has spent 0.6 of the 1.0.
	w.parent.GroupPath = "team"
	sibling := NewInstanceWithTool("group-sibling", t.TempDir(), "claude")
	sibling.ID = "sibling-group-3b"
	sibling.GroupPath = "team"
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
	if err := seed.Save(append(instances, sibling)); err != nil {
		t.Fatal(err)
	}
	store := costs.NewStore(seed.GetDB().DB())
	for _, id := range []string{w.parent.ID, sibling.ID} {
		if err := store.WriteCostEvent(costs.CostEvent{ID: "ev-" + id, SessionID: id, Timestamp: time.Now(),
			Model: "claude-test", InputTokens: 1000, CostMicrodollars: 600_000}); err != nil {
			t.Fatal(err)
		}
	}
	_ = seed.Close()

	w.urgent(t, "shipped the build")
	w.deliver(t, "waiting")
	st := w.state(t)
	if w.ext.count() != 0 || len(w.typed.all()) != 0 || len(st.Inflight) != 0 || st.LastWake != 0 {
		t.Errorf("the group limit did not stop the socket wake: frames %d typed %q state %+v", w.ext.count(), w.typed.all(), st)
	}
	if via := waitBudgetVia(t, os.Getenv("HOME"), w.parent.ID); len(via) != 1 || via[0] != "socket" {
		t.Errorf("session.budget_exceeded via %v, want one with via socket", via)
	}

	// Control: the group is under its limit once the limit is raised.
	write("[costs.budgets.groups.team]\ndaily_limit = 100.0\n[costs.budgets]\nenforce = true\n")
	w.clock = w.clock.Add(61 * time.Second)
	w.deliver(t, "waiting")
	if w.ext.count() != 1 {
		t.Errorf("under the group limit the wake must go out: %d frames", w.ext.count())
	}
}

// Buddy L1: an inbox doorbell names the newest records, not the oldest: a long
// backlog is bounded at 8 ids and the 8 are the latest.
func TestInboxNudge_PiDoorbellIDsAreTheNewestEightFingerprints(t *testing.T) {
	w := newPiWakeFixture(t, replyWith("submitted"))
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 10; i++ {
		ev := TransitionNotificationEvent{ChildSessionID: fmt.Sprintf("kid-%02d", i), ChildTitle: "kid", Profile: "default", ToStatus: "waiting",
			TargetSessionID: w.parent.ID, TargetKind: "parent", Tier: TurnTierInfo, Text: fmt.Sprintf("result %d", i),
			Timestamp: base.Add(time.Duration(i) * time.Minute), LastOutputHash: fmt.Sprintf("turn:%02d", i)}
		if err := CommitToInbox(w.parent.ID, ev); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := ReadInboxEvents(w.parent.ID)
	if err != nil || len(pending) != 10 {
		t.Fatalf("test setup: %d pending (%v)", len(pending), err)
	}
	var want []string
	for _, ev := range pending[2:] { // oldest first: the newest eight
		fp := ev.TurnFingerprint
		if fp == "" {
			fp = TurnFingerprint(ev)
		}
		want = append(want, fp)
	}
	nonce := "01J0ABCDEF0123456789ABCDEF"
	if _, err := wakeSend(w.parent, "default", "[INBOX] 10 records are waiting", nonce, inboxDoorbellIDs(w.parent.ID, "[INBOX] 10 records are waiting")); err != nil {
		t.Fatal(err)
	}
	fr := w.ext.waitFrame(t)
	got := map[string]bool{}
	ids, _ := fr["ids"].([]any)
	for _, id := range ids {
		got[fmt.Sprint(id)] = true
	}
	if len(ids) != 8 {
		t.Fatalf("the doorbell carries %d ids, want 8: %v", len(ids), ids)
	}
	for _, fp := range want {
		if !got[fp] {
			t.Errorf("the newest eight lack %s (frame ids %v)", fp, ids)
		}
	}
}

// Buddy L1: with nothing to name in the inbox, the ids are a hash of the line,
// so a repeated nudge of the same line is recognised as a duplicate and another
// line is not.
func TestInboxNudge_PiDoorbellIDsFallBackToALineHash(t *testing.T) {
	w := newPiWakeFixture(t, replyWith("submitted"))
	a := inboxDoorbellIDs(w.parent.ID, "[INBOX] urgent · kid: done")
	again := inboxDoorbellIDs(w.parent.ID, "[INBOX] urgent · kid: done")
	other := inboxDoorbellIDs(w.parent.ID, "[INBOX] urgent · kid: failed")
	if len(a) != 1 || !strings.HasPrefix(a[0], "line-") || len(a[0]) != len("line-")+12 {
		t.Fatalf("ids with an empty inbox = %v, want one line-<12 hex>", a)
	}
	if again[0] != a[0] || other[0] == a[0] {
		t.Errorf("the line hash is not stable per line: %v %v %v", a, again, other)
	}
}

// Forge round 3 #2: a finished turn whose Done summary differs from its body.
// The wake line shows only the summary, so the doorbell's run start must not
// count the turn as carried whole: its body still reaches the parent, whether
// the turn's inbox twin carries the text (a Claude child) or there is none
// (a pi child's twin is textless).
func TestCommsDeliver_PiADoneTurnsBodyReachesTheParentAfterItsDoorbellRun(t *testing.T) {
	for _, twin := range []bool{true, false} {
		name := "ledger only"
		if twin {
			name = "with an inbox twin"
		}
		t.Run(name, func(t *testing.T) {
			w := newPiWakeFixture(t, replyWith("submitted"))
			uuid := fmt.Sprint("done-turn-", twin)
			key := comms.Key(comms.KindTurn, w.child.ID, uuid)
			w.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierUrgent, Done: "ok", Summary: "shipped",
				Text: "XYZBODY details", Key: key})
			if twin {
				ev := TransitionNotificationEvent{ChildSessionID: w.child.ID, ToStatus: "waiting", Tier: TurnTierUrgent,
					Kind: transitionKindFinished, DoneStatus: "ok", DoneSummary: "shipped", Text: "XYZBODY details",
					TurnUUID: uuid, LastOutputHash: (TurnFacts{UUID: uuid}).Signal()}
				if err := WriteInboxEvent(w.parent.ID, ev); err != nil {
					t.Fatal(err)
				}
			}
			w.deliver(t, "waiting")
			fr := w.ext.frame(t, 0)
			nonce, _ := fr["id"].(string)
			line, _ := fr["line"].(string)
			if strings.Contains(line, "XYZBODY") {
				t.Fatalf("test setup: the wake line carried the body: %q", line)
			}
			// The doorbell's run start, then the parent's next one.
			seen := ""
			for _, start := range []struct{ prompt, nonce string }{{line, nonce}, {"next", ""}} {
				dl, ok := LedgerPromptContext(w.parent.ID, start.prompt, start.nonce, w.parent.Tool == "pi")
				if !ok {
					t.Fatal("enrolled parent fell back to the inbox path")
				}
				seen += dl.Text
				if dl.Token != "" {
					dl.Done(true)
					w.injectedEdge(t, dl.Token)
				} else {
					dl.Done(true)
				}
			}
			if !strings.Contains(seen, "XYZBODY details") {
				t.Errorf("the turn's body never reached the parent (wake line %q, run starts %q)", line, seen)
			}
		})
	}
}
