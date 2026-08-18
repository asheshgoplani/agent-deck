package ui

import "testing"

// Pressing q twice (or any path that queues two quitMsg) used to run
// performFinalShutdown twice, double-closing shutdown channels and panicking
// the program. The second quitMsg must be a no-op.
func TestSecondQuitMsgDoesNotRunShutdownAgain(t *testing.T) {
	h := &Home{}

	_, cmd := h.updateInner(quitMsg(false))
	if cmd == nil {
		t.Fatal("first quitMsg should schedule shutdown")
	}
	if !h.shutdownStarted {
		t.Fatal("first quitMsg should mark shutdown as started")
	}

	_, cmd2 := h.updateInner(quitMsg(false))
	if cmd2 != nil {
		t.Fatal("second quitMsg should not schedule shutdown again")
	}
}

func TestTryQuitIsNoOpWhileQuitting(t *testing.T) {
	h := &Home{isQuitting: true}
	if _, cmd := h.tryQuit(); cmd != nil {
		t.Fatal("tryQuit should not queue a second quit while already quitting")
	}
}
