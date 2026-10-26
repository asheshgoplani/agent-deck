//go:build !windows
// +build !windows

package tmux

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestPump wires a pump to an os.Pipe standing in for the host tty. The
// pipe is enough because run() only needs a pollable fd — it never puts the
// descriptor into raw mode itself.
func newTestPump(t *testing.T, opts AttachOptions) (*attachStdinPump, *os.File, *bytes.Buffer) {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = r.Close()
		_ = w.Close()
	})

	out := &bytes.Buffer{}
	detach := opts.DetachByte
	if detach == 0 {
		detach = 17 // Ctrl+Q
	}
	return &attachStdinPump{
		in:     r,
		out:    out,
		detach: detach,
		opts:   opts,
		// Backdate so the reply-quarantine filter is disarmed and plain input
		// passes through verbatim; the armed path has its own coverage.
		startTime: time.Now().Add(-time.Hour),
		ioErrors:  make(chan error, 2),
	}, w, out
}

type pumpResult struct {
	outcome     SwitchIntent
	interrupted bool
}

func runPump(ctx context.Context, p *attachStdinPump) <-chan pumpResult {
	done := make(chan pumpResult, 1)
	go func() {
		outcome, interrupted := p.run(ctx)
		done <- pumpResult{outcome: outcome, interrupted: interrupted}
	}()
	return done
}

// TestAttachStdinPump_StopsOnContextCancel is the regression test for the
// stolen-keystroke bug: when a session's pane process exits on its own, nothing
// pressed the detach key, so the stdin reader had no reason to stop. It stayed
// parked in a blocking os.Stdin.Read that cancel() could not interrupt, and
// AttachWithOptions returned anyway. Back on the deck, the stale reader and
// Bubble Tea's reader were both queued on the same tty; the stale one had
// waited longer, so it won the next keystroke, forwarded it to the closed PTY
// and died — the user's first keypress after a session ended did nothing.
func TestAttachStdinPump_StopsOnContextCancel(t *testing.T) {
	pump, w, _ := newTestPump(t, AttachOptions{})

	ctx, cancel := context.WithCancel(context.Background())
	done := runPump(ctx, pump)

	// Let the pump settle into its poll loop with no input available.
	time.Sleep(2 * AttachStdinPollInterval)
	select {
	case res := <-done:
		t.Fatalf("pump exited before cancel: %+v", res)
	default:
	}

	cancelledAt := time.Now()
	cancel()

	// The bound must stay UNDER AttachStdinReaderStopTimeout, not merely under
	// some generous ceiling. cleanupAttach gives up waiting at that backstop and
	// returns anyway, so a pump that exits slower than it is still broken in
	// production - the reader outlives the attach and eats a keypress - while a
	// test that allowed backstop+1s would happily pass. Assert the invariant
	// cleanupAttach actually depends on: the pump exits before the backstop fires.
	select {
	case res := <-done:
		if elapsed := time.Since(cancelledAt); elapsed >= AttachStdinReaderStopTimeout {
			t.Errorf("pump took %v to exit, want < %v (cleanupAttach's backstop); "+
				"at or beyond it the reader outlives the attach",
				elapsed, AttachStdinReaderStopTimeout)
		}
		if res.interrupted {
			t.Errorf("interrupted = true on a context cancel, want false")
		}
		if res.outcome != SwitchNone {
			t.Errorf("outcome = %v, want SwitchNone", res.outcome)
		}
	case <-time.After(AttachStdinReaderStopTimeout):
		t.Fatal("pump did not exit before cleanupAttach's backstop; a surviving " +
			"reader swallows the first keystroke on return to the deck")
	}

	// The pump is gone, so a keystroke arriving now must be left for the next
	// reader rather than consumed and dropped.
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatalf("write after cancel: %v", err)
	}
	if !PollFdReady(int(pump.in.Fd()), time.Second) {
		t.Fatal("keystroke written after the pump exited was consumed by it")
	}
	buf := make([]byte, 1)
	if _, err := pump.in.Read(buf); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if buf[0] != 'x' {
		t.Errorf("read back %q, want %q", buf[0], 'x')
	}
}

func TestAttachStdinPump_ForwardsInputAndInterceptsDetach(t *testing.T) {
	pump, w, out := newTestPump(t, AttachOptions{DetachByte: 17})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runPump(ctx, pump)

	if _, err := w.Write([]byte("hello\x11")); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case res := <-done:
		if !res.interrupted {
			t.Error("interrupted = false on the detach key, want true")
		}
		if res.outcome != SwitchNone {
			t.Errorf("outcome = %v, want SwitchNone for a plain detach", res.outcome)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pump did not exit on the detach key")
	}

	// Bytes ahead of the detach key are forwarded; the detach key itself is not.
	if got := out.String(); got != "hello" {
		t.Errorf("forwarded %q, want %q", got, "hello")
	}
}

// syncBuffer is a pump output the test can read while the pump writes to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitFor blocks until the pump has forwarded exactly want.
func (b *syncBuffer) waitFor(t *testing.T, want string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if b.String() == want {
			return
		}
	}
	t.Fatalf("forwarded %q, want %q", b.String(), want)
}

// runPumpUntilDetach runs pump, writes each input only once the pump has
// forwarded everything expected so far (so each is a read of its own, as
// separate keystrokes are), then Ctrl+Q, and returns what reached tmux.
func runPumpUntilDetach(t *testing.T, pump *attachStdinPump, w *os.File, steps ...[2]string) string {
	t.Helper()
	out := &syncBuffer{}
	pump.out = out

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runPump(ctx, pump)

	for _, step := range steps {
		if _, err := w.Write([]byte(step[0])); err != nil {
			t.Fatalf("write: %v", err)
		}
		out.waitFor(t, step[1])
	}
	if _, err := w.Write([]byte{0x11}); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case res := <-done:
		if !res.interrupted {
			t.Error("interrupted = false on the detach key, want true")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("pump did not exit on the detach key; forwarded %q", out.String())
	}
	return out.String()
}

// TestAttachStdinPump_AltEscapeStringIntroducerDoesNotFreezeInput pins the
// em-dash freeze (#2931): with Option-as-Meta (Terminal.app, iTerm2 "Esc+"),
// Alt+Shift+- sends ESC _, the APC introducer. The reply filter took it for a
// terminal reply and discarded everything after it waiting for a terminator,
// Ctrl+Q included, so the attach could only be escaped by closing the tab.
func TestAttachStdinPump_AltEscapeStringIntroducerDoesNotFreezeInput(t *testing.T) {
	pump, w, _ := newTestPump(t, AttachOptions{DetachByte: 17})

	got := runPumpUntilDetach(t, pump, w,
		[2]string{"\x1b_", "\x1b_"},
		[2]string{"a", "\x1b_a"},
	)
	if want := "\x1b_a"; got != want {
		t.Errorf("forwarded %q, want %q", got, want)
	}
}

// fullReadPrefix fills the pump's 32-byte read up to the last two bytes, so
// ESC plus an introducer written after it ends a full read.
var fullReadPrefix = strings.Repeat("x", 30)

// A full read can end on a reply's introducer while the rest of the reply is
// already waiting: that is still a reply, and its payload must not reach the
// pane as typed text.
func TestAttachStdinPump_DiscardsReplyCutByAFullRead(t *testing.T) {
	pump, w, _ := newTestPump(t, AttachOptions{DetachByte: 17})

	got := runPumpUntilDetach(t, pump, w,
		[2]string{fullReadPrefix + "\x1bP>|iTerm2 3.6.10\x1b\\k", fullReadPrefix + "k"},
	)
	if want := fullReadPrefix + "k"; got != want {
		t.Errorf("forwarded %q, want %q", got, want)
	}
}

// A full read ending on ESC _ with nothing else waiting is still the key.
func TestAttachStdinPump_AltEscapeStringIntroducerEndingAFullRead(t *testing.T) {
	pump, w, _ := newTestPump(t, AttachOptions{DetachByte: 17})

	got := runPumpUntilDetach(t, pump, w,
		[2]string{fullReadPrefix + "\x1b_", fullReadPrefix + "\x1b_"},
		[2]string{"a", fullReadPrefix + "\x1b_a"},
	)
	if want := fullReadPrefix + "\x1b_a"; got != want {
		t.Errorf("forwarded %q, want %q", got, want)
	}
}

// TestAttachStdinPump_SwallowsStaleDAReplyDuringQuarantine reproduces #2356:
// switching sessions (Windows Terminal, v1.16.16) leaked literal DA1/DA2
// device-attributes reply text into the new session's prompt. The reply the
// outer terminal sends for the just-detached attach's query can still be in
// flight over the network/SSH link when the next attach's stdin pump starts.
// Because that stray reply is byte-for-byte the same shape as a fresh reply,
// the previous termreply filter passed every DA/DSR-final CSI through
// unconditionally while armed, so both the new attach's own legitimate reply
// AND the stale one from the torn-down attach were forwarded into the pane —
// tmux only had one outstanding query to consume, so the second, unmatched
// copy fell through as literal keyboard input.
//
// This test wires a fresh pump exactly as a real attach would (startTime =
// now, so the post-attach quarantine window is armed), writes one legitimate
// DA1+DA2 reply pair immediately, then simulates the stale reply from the
// previous, already-torn-down attach arriving ~100ms later — still inside
// the 500ms quarantine window. It asserts that only the first pair reaches
// `out` (the stand-in for the tmux attach PTY / ultimately the pane) and
// that no fragment of the stale second pair leaks through.
func TestAttachStdinPump_SwallowsStaleDAReplyDuringQuarantine(t *testing.T) {
	pump, w, out := newTestPump(t, AttachOptions{DetachByte: 17})
	pump.startTime = time.Now() // arm the quarantine window like a real attach

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runPump(ctx, pump)

	const daReplyPair = "\x1b[?61;4;6;7;14;21;22;23;24;28;32;42;52c\x1b[>0;10;1c"

	// The new attach's own tmux client negotiates immediately.
	if _, err := w.Write([]byte(daReplyPair)); err != nil {
		t.Fatalf("write legitimate reply: %v", err)
	}

	// A late-arriving stray reply addressed to the previous (already
	// detached) attach's query, still in flight when the new attach started.
	time.Sleep(100 * time.Millisecond)
	if _, err := w.Write([]byte(daReplyPair)); err != nil {
		t.Fatalf("write stale reply: %v", err)
	}

	// Give the pump a moment to read and filter both writes, then detach.
	time.Sleep(50 * time.Millisecond)
	if _, err := w.Write([]byte{17}); err != nil {
		t.Fatalf("write detach key: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pump did not exit on the detach key")
	}

	if got := out.String(); got != daReplyPair {
		t.Errorf("pane input = %q, want exactly one reply pair %q (stale reply must not leak into the pane)", got, daReplyPair)
	}
}

const hostColorReplies = "\x1b]10;rgb:0000/0000/0000\x1b\\\x1b]11;rgb:fefe/ffff/ffff\x1b\\"

func TestAttachStdinPump_ForwardsColorReplyDuringQuarantine(t *testing.T) {
	pump, w, out := newTestPump(t, AttachOptions{DetachByte: 17})

	if got := tmuxInputFromArmedPump(t, pump, w, out, hostColorReplies); got != hostColorReplies {
		t.Errorf("pane input = %q, want the color replies %q", got, hostColorReplies)
	}
}

func tmuxInputFromArmedPump(t *testing.T, pump *attachStdinPump, w *os.File, out *bytes.Buffer, input string) string {
	t.Helper()
	pump.startTime = time.Now()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runPump(ctx, pump)

	if _, err := w.Write([]byte(input + "\x11")); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pump did not exit on the detach key")
	}
	return out.String()
}

func TestAttachStdinPump_ReportsSwitchIntent(t *testing.T) {
	pump, w, _ := newTestPump(t, AttachOptions{DetachByte: 17, SwitchKeyByte: 19})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runPump(ctx, pump)

	if _, err := w.Write([]byte{19}); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case res := <-done:
		if !res.interrupted {
			t.Error("interrupted = false on the switch key, want true")
		}
		if res.outcome != SwitchRequested {
			t.Errorf("outcome = %v, want SwitchRequested", res.outcome)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pump did not exit on the switch key")
	}
}

// TestAttachStdinPump_StopsOnEOF covers the closed-stdin path: the pump must
// report a non-interrupt exit rather than spinning on a dead descriptor.
func TestAttachStdinPump_StopsOnEOF(t *testing.T) {
	pump, w, _ := newTestPump(t, AttachOptions{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runPump(ctx, pump)

	if err := w.Close(); err != nil {
		t.Fatalf("close write end: %v", err)
	}

	select {
	case res := <-done:
		if res.interrupted {
			t.Error("interrupted = true on EOF, want false")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pump did not exit on stdin EOF")
	}
}

// --- QuiesceAttachInput: the teardown half of the fix ---
//
// These pin the two invariants cleanupAttach depends on and that the pump tests
// above cannot see: that the stdin reader is joined BEFORE the input queue is
// flushed, and that the flush runs on every exit path rather than only after a
// detach keypress.

func TestQuiesceAttachInput_WaitsForReaderBeforeFlushing(t *testing.T) {
	readerDone := make(chan struct{})

	var mu sync.Mutex
	var flushedWhileReaderAlive bool
	var flushed, quarantined bool
	readerAlive := true

	flush := func() {
		mu.Lock()
		defer mu.Unlock()
		flushed = true
		if readerAlive {
			// Flushing here would discard the capability replies the reader is
			// about to consume anyway, and worse, leave the reader running to
			// eat a real keystroke after the queue was cleared.
			flushedWhileReaderAlive = true
		}
	}
	quarantine := func() {
		mu.Lock()
		defer mu.Unlock()
		quarantined = true
	}

	// Release the reader only after quiesce has had time to block on it.
	go func() {
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		readerAlive = false
		mu.Unlock()
		close(readerDone)
	}()

	exited := QuiesceAttachInput(readerDone, AttachStdinReaderStopTimeout, flush, quarantine)

	if !exited {
		t.Error("exited = false, want true (the reader finished well inside the backstop)")
	}
	mu.Lock()
	defer mu.Unlock()
	if flushedWhileReaderAlive {
		t.Error("flushed the input queue while the stdin reader was still running")
	}
	if !flushed {
		t.Error("flush was never called")
	}
	if !quarantined {
		t.Error("quarantine was never armed")
	}
}

// TestQuiesceAttachInput_FlushesOnEveryExitPath guards the removal of the old
// `if didDetach` gate. The flush used to run only when the user pressed the
// detach key; on the process-exit path the stale reader incidentally swallowed
// the terminal's capability replies instead. With the reader now stopping
// cleanly, skipping the flush here would let those bytes reach the TUI.
func TestQuiesceAttachInput_FlushesOnEveryExitPath(t *testing.T) {
	readerDone := make(chan struct{})
	close(readerDone) // reader already gone, as on a process-exit teardown

	flushed, quarantined := false, false
	exited := QuiesceAttachInput(readerDone, AttachStdinReaderStopTimeout,
		func() { flushed = true },
		func() { quarantined = true },
	)

	if !exited {
		t.Error("exited = false, want true")
	}
	if !flushed || !quarantined {
		t.Errorf("flushed=%v quarantined=%v, want both true on every exit path",
			flushed, quarantined)
	}
}

// TestQuiesceAttachInput_BackstopFires covers the wedged-reader case: quiesce
// must give up rather than hang the TUI forever, and must still flush.
func TestQuiesceAttachInput_BackstopFires(t *testing.T) {
	neverDone := make(chan struct{}) // reader wedged in an uninterruptible read

	flushed := false
	start := time.Now()
	exited := QuiesceAttachInput(neverDone, 100*time.Millisecond,
		func() { flushed = true },
		func() {},
	)
	elapsed := time.Since(start)

	if exited {
		t.Error("exited = true, want false (the reader never finished)")
	}
	if !flushed {
		t.Error("flush must still run after the backstop fires")
	}
	if elapsed < 100*time.Millisecond {
		t.Errorf("returned after %v, want >= the 100ms backstop", elapsed)
	}
	if elapsed > time.Second {
		t.Errorf("returned after %v, far past the backstop", elapsed)
	}
}

// ASK-92: a lone Ctrl+V is reported to OnCtrlV (the TUI's "paste images from
// your own machine" hint over plain ssh) after it is forwarded, so the key
// reaches the program first; Ctrl+V inside other input is not a paste.
func TestAttachStdinPump_LoneCtrlVIsForwardedThenReported(t *testing.T) {
	var mu sync.Mutex
	var forwardedFirst []bool
	reported := make(chan struct{}, 4)
	var out *bytes.Buffer // the pump's output, assigned before the pump runs
	opts := AttachOptions{DetachByte: 17, OnCtrlV: func() {
		mu.Lock()
		forwardedFirst = append(forwardedFirst, bytes.HasSuffix(out.Bytes(), []byte{0x16}))
		mu.Unlock()
		reported <- struct{}{}
	}}
	pump, w, pumpOut := newTestPump(t, opts)
	out = pumpOut

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runPump(ctx, pump)

	if _, err := w.Write([]byte{0x16}); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case <-reported:
	case <-time.After(2 * time.Second):
		t.Fatal("a lone Ctrl+V was not reported to OnCtrlV")
	}
	if _, err := w.Write([]byte("a\x16b")); err != nil {
		t.Fatalf("write: %v", err)
	}
	time.Sleep(4 * AttachStdinPollInterval)
	if _, err := w.Write([]byte{0x11}); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pump did not detach")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(forwardedFirst) != 1 || !forwardedFirst[0] {
		t.Fatalf("one report, after the key was forwarded: %v", forwardedFirst)
	}
	if got := out.String(); got != "\x16a\x16b" {
		t.Fatalf("Ctrl+V reaches the program unchanged: %q", got)
	}
}

// ASK-92 review: with tmux extended-keys on, the terminal can send Ctrl+V as
// modifyOtherKeys or CSI u; those count as a lone Ctrl+V too, and Ctrl+V
// inside other input or Ctrl+Shift+V does not.
func TestIsLoneCtrlV_RawAndExtendedKeyForms(t *testing.T) {
	for in, want := range map[string]bool{
		"\x16":           true,
		"\x1b[27;5;118~": true,
		"\x1b[118;5u":    true,
		"a\x16":          false,
		"\x16\x16":       false,
		"\x1b[118;6u":    false, // Ctrl+Shift+V
		"\x1b[27;6;118~": false,
		"\x1b[118;5uabc": false,
		"":               false,
	} {
		if got := isLoneCtrlV([]byte(in)); got != want {
			t.Errorf("isLoneCtrlV(%q) = %v, want %v", in, got, want)
		}
	}
}
