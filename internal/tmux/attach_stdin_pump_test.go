//go:build !windows
// +build !windows

package tmux

import (
	"bytes"
	"context"
	"os"
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
