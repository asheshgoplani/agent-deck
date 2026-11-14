package tmux

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestPrimePaneCapturesTimeoutDoesNotFanOut is the #3089 regression: on a
// loaded host the batched capture timed out and every pane it had not reached
// was then captured by a tmux client of its own (58 live panes: 5 tmux calls
// became 164 and a 0.45 s listing took 7 s). After a timeout no further batch
// may run on that socket and CapturePane must not start a subprocess: it
// answers with the pane's last frame, or ErrCaptureTimeout when it has none.
func TestPrimePaneCapturesTimeoutDoesNotFanOut(t *testing.T) {
	prevTimeout := tmuxPollTimeout
	tmuxPollTimeout = 50 * time.Millisecond
	t.Cleanup(func() { tmuxPollTimeout = prevTimeout })

	calls := 0
	withCaptureBatchRun(t, func(ctx context.Context, _ string, _ []string) ([]byte, error) {
		calls++
		<-ctx.Done()
		return nil, ctx.Err()
	})
	sessions := batchSessions(captureBatchMax*2+1, "batch-timeout")
	// One pane has a frame from an earlier pass: that is what it reports.
	stale := sessions[len(sessions)-1]
	stale.cacheMu.Lock()
	stale.cacheContent = "earlier frame\n> \n"
	stale.cacheTime = time.Now().Add(-time.Minute)
	stale.cacheMu.Unlock()

	skipsBefore := CaptureBatchSkips()
	if got := PrimePaneCaptures(sessions); got != 0 {
		t.Fatalf("primed %d sessions from a batch that timed out", got)
	}
	if calls != 1 {
		t.Fatalf("ran %d batched tmux clients after the first one timed out, want 1", calls)
	}
	if got := CaptureBatchSkips() - skipsBefore; got != int64(len(sessions)) {
		t.Fatalf("CaptureBatchSkips grew by %d, want %d", got, len(sessions))
	}

	startsBefore := SubprocessStarts()
	for _, s := range sessions {
		got, err := s.CapturePane()
		if s == stale {
			if err != nil || got != "earlier frame\n> \n" {
				t.Fatalf("stale pane: CapturePane = %q, %v; want its last frame", got, err)
			}
			continue
		}
		if !errors.Is(err, ErrCaptureTimeout) {
			t.Fatalf("%s: CapturePane err = %v, want ErrCaptureTimeout", s.Name, err)
		}
	}
	if started := SubprocessStarts() - startsBefore; started != 0 {
		t.Fatalf("CapturePane after a timed-out batch started %d tmux processes, want 0", started)
	}

	// Anything that changes the pane (send-keys) ends the skip.
	sessions[0].invalidateCache()
	sessions[0].cacheMu.RLock()
	_, skipped, _ := sessions[0].captureSkippedLocked(time.Now())
	sessions[0].cacheMu.RUnlock()
	if skipped {
		t.Fatal("invalidateCache must end the capture skip")
	}
}
