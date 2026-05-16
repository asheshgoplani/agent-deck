//go:build !windows

package web

import (
	"os"
	"sync"
	"testing"
	"time"
)

// newRacingPipeBridge builds a tmuxPTYBridge whose ptmx is one end of an
// os.Pipe and arranges a drain goroutine on the other end so writes don't
// block. The bridge has no underlying *exec.Cmd, so Close() short-circuits
// the cmd-management path. Used to flush out concurrency bugs on the ptmx
// field without depending on a real tmux server.
func newRacingPipeBridge(t *testing.T) (*tmuxPTYBridge, func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}

	drained := make(chan struct{})
	go func() {
		defer close(drained)
		buf := make([]byte, 1024)
		for {
			if _, err := r.Read(buf); err != nil {
				return
			}
		}
	}()

	b := &tmuxPTYBridge{
		tmuxSession: "race-test",
		sessionID:   "race",
		ptmx:        w,
		done:        make(chan struct{}),
	}

	cleanup := func() {
		_ = r.Close()
		<-drained
	}
	return b, cleanup
}

// TestTmuxPTYBridge_WriteInput_RaceWithClose proves WriteInput can run
// concurrently with Close without a data race on the ptmx field. Before
// the v1.9 fix, WriteInput touched b.ptmx without holding ptmxMu, so this
// test under -race reported "DATA RACE" against Close's b.ptmx = nil
// store. (V1.9 T5, race-review 2.1.)
func TestTmuxPTYBridge_WriteInput_RaceWithClose(t *testing.T) {
	b, cleanup := newRacingPipeBridge(t)
	defer cleanup()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			_ = b.WriteInput("x")
		}
	}()
	go func() {
		defer wg.Done()
		// Sleep just enough so WriteInput has started looping before Close
		// tears the ptmx down.
		time.Sleep(100 * time.Microsecond)
		b.Close()
	}()
	wg.Wait()
}

// TestTmuxPTYBridge_SnapshotPtmx_RaceWithClose proves the helper that
// streamOutput uses to read b.ptmx is safe against a concurrent Close.
// streamOutput itself wraps `b.snapshotPtmx()` + `ptmx.Read(buf)`; this
// test exercises the snapshot loop directly, avoiding the *websocket.Conn
// fixture that streamOutput's error path requires. The race detector
// adjudicates the b.ptmx field accesses across the loop and Close's
// Lock-guarded `b.ptmx = nil` store. (V1.9 T5, race-review 2.1.)
func TestTmuxPTYBridge_SnapshotPtmx_RaceWithClose(t *testing.T) {
	b, cleanup := newRacingPipeBridge(t)
	defer cleanup()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			ptmx := b.snapshotPtmx()
			if ptmx == nil {
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		time.Sleep(100 * time.Microsecond)
		b.Close()
		close(stop)
	}()
	wg.Wait()
}
