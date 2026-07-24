package tmux

import (
	"testing"
	"time"
)

// A process that did not itself spawn the pane gets a zeroed startup window:
// ReconnectSession/ReconnectSessionLazy set startupAt to the zero time, and the
// process that ran `session restart` has already exited. GetStatus then reads a
// booting agent — no spinner, no prompt yet — as "waiting", i.e. ready to
// receive keystrokes it will silently discard. MarkStartupAt lets the CLI
// restore that window from the durable spawn stamp.
func TestMarkStartupAt_SeedsStartupWindowForAnotherProcess(t *testing.T) {
	sess := ReconnectSessionLazy("agentdeck_seed_test", "seed-test", t.TempDir(), "", "waiting")

	sess.mu.Lock()
	seeded := sess.inStartupWindowLocked()
	sess.mu.Unlock()
	if seeded {
		t.Fatal("a reconnected session must start with no startup window (guards the premise of this fix)")
	}

	sess.MarkStartupAt(time.Now())

	sess.mu.Lock()
	seeded = sess.inStartupWindowLocked()
	sess.mu.Unlock()
	if !seeded {
		t.Fatal("MarkStartupAt(now) must put the session inside its startup window")
	}
}

// A spawn older than the startup window must not re-open it: a long-running
// session is not "starting" just because it was once restarted.
func TestMarkStartupAt_OldSpawnDoesNotOpenWindow(t *testing.T) {
	sess := ReconnectSessionLazy("agentdeck_seed_old", "seed-old", t.TempDir(), "", "waiting")
	sess.MarkStartupAt(time.Now().Add(-2 * startupStateWindow))

	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.inStartupWindowLocked() {
		t.Fatal("a spawn older than startupStateWindow must leave the window closed")
	}
}

// Zero clears the window, matching the internal resets.
func TestMarkStartupAt_ZeroClearsWindow(t *testing.T) {
	sess := ReconnectSessionLazy("agentdeck_seed_zero", "seed-zero", t.TempDir(), "", "waiting")
	sess.MarkStartupAt(time.Now())
	sess.MarkStartupAt(time.Time{})

	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.inStartupWindowLocked() {
		t.Fatal("MarkStartupAt(zero) must clear the startup window")
	}
}
