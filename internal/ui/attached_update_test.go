package ui

import (
	"context"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Defect 310: a TUI attached to a session never took an update. The loop
// is parked in tea.Exec for the whole attach, so the binary watch and
// auto_restart (both tick driven) stood still and nothing said a newer
// build was waiting; the owner's Mac deck ran a pre-1.16.30 build for
// ~22 h. On detach the restart then waited for the next tick and a probe,
// which a quick re-attach beat every time.

// runAttachWorker feeds the attach worker one tick per time in ticks and
// returns once all of them were handled.
func runAttachWorker(t *testing.T, h *Home, ticks ...time.Time) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan time.Time)
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.runAttachedHeartbeat(ctx, ch)
	}()
	for _, now := range ticks {
		ch <- now
	}
	// An unbuffered send returns once the worker took the tick; one more
	// send waits for it to finish the last one.
	ch <- ticks[len(ticks)-1]
	cancel()
	<-done
}

// TestAttachedUpdate_DetachRunsRestart pins (b): the detach back to the
// home screen runs the existing restart path at once, both for a build
// that was already waiting when the attach began (remote attach returns
// carry no session id) and for one that landed during the attach, which
// only the attach worker saw.
func TestAttachedUpdate_DetachRunsRestart(t *testing.T) {
	t.Run("pending before attach", func(t *testing.T) {
		h := newAutoRestartTestHome(t)
		h.isAttaching.Store(true)
		_ = h.maybeAutoRestart()
		_, cmd := h.Update(statusUpdateMsg{})
		if cmd == nil || !h.restartRequested || !h.isQuitting {
			t.Fatalf("detach: requested=%v quitting=%v, want the restart armed", h.restartRequested, h.isQuitting)
		}
	})

	t.Run("landed while attached", func(t *testing.T) {
		stubUpdateSettings(t, session.UpdateSettings{})
		h := newRestartTestHome(t)
		h.isAttaching.Store(true)
		_ = h.maybeAutoRestart() // nothing newer yet

		stubStatBinary(t, fpAt(2, 2), nil)
		prev := probeBinaryVersion
		probeBinaryVersion = func(string) (string, error) { return "1.16.1", nil }
		t.Cleanup(func() { probeBinaryVersion = prev })
		runAttachWorker(t, h, time.Now())
		if h.restartRequested {
			t.Fatal("the attach worker must never restart")
		}

		_, cmd := h.Update(statusUpdateMsg{attachedSessionID: "s1"})
		if cmd == nil || !h.restartRequested || !h.isQuitting {
			t.Fatalf("detach: requested=%v quitting=%v, want the restart armed", h.restartRequested, h.isQuitting)
		}
		if got := h.installedUpdateVersion(); got != "1.16.1" {
			t.Fatalf("installed = %q, want the worker's probe handed to the watch", got)
		}
	})
}
