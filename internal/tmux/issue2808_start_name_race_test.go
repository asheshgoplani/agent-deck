package tmux

import (
	"sync"
	"testing"
	"time"
)

// TestStartRenameDoesNotRaceStatusReads_Issue2808 runs Session.Start on a
// session whose tmux name is already taken (so Start rewrites Name with a fresh
// suffix) while another goroutine keeps reading the session's status, the way
// the TUI's status poll does (GetStatus, the cached liveness check, the
// batched pane-capture prime and the window-cache refresh). Before the fix
// Start wrote Name without any lock and those readers read it without one, so
// `go test -race` reported a DATA RACE here. Run it with -race; without -race it only checks the rename happened.
func TestStartRenameDoesNotRaceStatusReads_Issue2808(t *testing.T) {
	skipIfNoTmuxBinary(t)

	occupant := NewSession("race2808", t.TempDir())
	if err := occupant.Start(""); err != nil {
		t.Fatalf("start occupant: %v", err)
	}
	t.Cleanup(func() { _ = occupant.Kill() })

	// Same tmux name as the live occupant: Start must rename it.
	sess := NewSession("race2808", t.TempDir())
	sess.Name = occupant.Name
	takenName := occupant.Name

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// After Start returns, read a fixed number of extra rounds so the
		// reader also observes the post-Start state (no sleep needed).
		postStart := -1
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) && postStart != 0 {
			_, _ = sess.GetStatus()
			_ = sess.ExistsCached()
			_ = PrimePaneCaptures([]*Session{sess})
			_ = RefreshCachedWindows(sess)
			if postStart > 0 {
				postStart--
				continue
			}
			select {
			case <-stop:
				postStart = 3
			default:
			}
		}
	}()

	err := sess.Start("")
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = sess.Kill() })

	if got := sess.Name; got == takenName {
		t.Fatalf("Start kept the taken name %q; want a fresh suffix", got)
	}
}

// TestStartRenameDoesNotRaceDetectTool_Issue2808 is the tight-loop variant:
// the reader calls only methods that take no lock Start also takes around the
// rename (DetectTool with its tool cache disabled, windowTarget, and
// GetEnvironment), so nothing orders its reads after Start's writes by
// coincidence. DetectTool runs on every status pass for shell sessions and
// reads both Name and Command, which Start rewrites. Run it with -race.
func TestStartRenameDoesNotRaceDetectTool_Issue2808(t *testing.T) {
	skipIfNoTmuxBinary(t)

	occupant := NewSession("race2808b", t.TempDir())
	if err := occupant.Start(""); err != nil {
		t.Fatalf("start occupant: %v", err)
	}
	t.Cleanup(func() { _ = occupant.Kill() })

	sess := NewSession("race2808b", t.TempDir())
	sess.Name = occupant.Name
	takenName := occupant.Name
	// Expire the detected tool immediately so every DetectTool call goes past
	// the cache to the Command and Name reads, as a fresh status pass does.
	sess.toolDetectExpiry = 0

	stop := make(chan struct{})
	started := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		close(started)
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			_ = sess.DetectTool()
			_ = sess.windowTarget(0)
			_, _ = sess.GetEnvironment("AGENTDECK_RACE_2808")
			select {
			case <-stop:
				return
			default:
			}
		}
	}()
	<-started

	err := sess.Start("")
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = sess.Kill() })

	if got := sess.Name; got == takenName {
		t.Fatalf("Start kept the taken name %q; want a fresh suffix", got)
	}
}
