package tmux

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestPipeManager_BurstClose_DoesNotCrashServer is a regression check for the
// 2026-05-07 #4980 trigger pattern: many ControlPipe.Close() calls dispatched
// within a few milliseconds of each other against a tmux server with active
// pane state. The race is server-side in `control_notify_client_detached`,
// which iterates remaining control clients on each detach to send
// %client-detached notifications — back-to-back detaches let the iteration
// hit a freed-but-still-listed client.
//
// Method:
//  1. Spawn N tmux sessions, each running a small notification-load script
//     (windowing operations + steady output) so the server's notify-walk
//     list is non-trivial.
//  2. Open a ControlPipe per session via PipeManager.
//  3. Release all goroutines from a barrier so every Disconnect() (which
//     calls cp.Close()) starts within microseconds of the others —
//     compressing the close cascade more tightly than the 16-ms real-world
//     reload_storage_changed pattern.
//  4. Assert the tmux server is still alive via `tmux list-sessions`.
//  5. Repeat for many trials and report the crash rate.
//
// This test takes seconds and does not run on -short. It also requires a
// short TMPDIR (e.g. TMPDIR=/tmp/) on macOS — the default
// /private/var/folders/... path overflows tmux's 104-char socket limit.
//
// Tunable via env (helpful when measuring baseline crash rate or tuning the
// future closeGate fix):
//
//	BURST_CLOSE_TRIALS  - number of trials (default 100)
//	BURST_CLOSE_N       - sessions per trial (default 10)
//
// On any crash, the test logs the trial number and continues so the rate is
// observable in a single run; t.Errorf at the end fails the overall test if
// crashes > 0.
func TestPipeManager_BurstClose_DoesNotCrashServer(t *testing.T) {
	skipIfNoTmuxBinary(t)
	if testing.Short() {
		t.Skip("integration: real tmux required, takes seconds")
	}
	// Gated: at the un-gated production default (closeGateStagger=0)
	// this harness produces 0 crashes — a fresh isolated server can't
	// reach the freed-but-still-listed timing window without server-
	// aging state. The harness reproduces reliably (~14 % at
	// stagger=15ms) when AGENT_DECK_CLOSE_STAGGER_MS is set into the
	// danger band, which is why it's kept in tree as a regression-
	// against-mistakes canary, not run on the default test path.
	if os.Getenv("AGENT_DECK_BURST_TEST") == "" {
		t.Skip("integration: set AGENT_DECK_BURST_TEST=1 to run")
	}

	trials := envInt("BURST_CLOSE_TRIALS", 100)
	n := envInt("BURST_CLOSE_N", 10)
	socket := isolatedTmuxSocket(t)

	crashes := 0
	for trial := 0; trial < trials; trial++ {
		if !runBurstCloseTrial(t, socket, trial, n) {
			crashes++
			// Give the OS a beat to settle before the next trial recreates
			// a fresh server.
			time.Sleep(50 * time.Millisecond)
		}
	}

	t.Logf("burst-close: %d/%d trials crashed (N=%d sessions per trial)", crashes, trials, n)
	if crashes > 0 {
		t.Errorf("tmux server crashed under burst-close pattern: %d/%d trials (#4980)", crashes, trials)
	}
}

// runBurstCloseTrial returns true if the tmux server survived the trial.
func runBurstCloseTrial(t *testing.T, socket string, trial, n int) (alive bool) {
	t.Helper()

	sessions := make([]string, n)
	for i := range sessions {
		sessions[i] = fmt.Sprintf("burst-close-%d-%d", trial, i)
		if !spawnNoisySession(t, socket, sessions[i]) {
			t.Logf("trial %d: spawn %q failed; skipping", trial, sessions[i])
			return true
		}
	}
	defer killSessionsBestEffort(socket, sessions)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pm := NewPipeManager(ctx, func(string) {})
	defer pm.Close()

	for _, name := range sessions {
		if err := pm.Connect(name, socket); err != nil {
			t.Logf("trial %d: Connect(%s) failed: %v; skipping trial", trial, name, err)
			return true
		}
	}

	// Age the connections — the real-world reproduction had established,
	// long-lived control clients with accumulated state (scrollback,
	// per-pane buffers, copy-mode etc.). 50 ms barely lets the handshake
	// complete; let them settle for longer to approach the real-world
	// "freed-but-still-in-list" timing window.
	ageDur := time.Duration(envInt("BURST_CLOSE_AGE_MS", 200)) * time.Millisecond
	time.Sleep(ageDur)

	// Stir the server's notify-walk list by triggering a few new-window /
	// kill-window events across random sessions. This mimics the real
	// workload where the server is mid-bookkeeping when the close cascade
	// arrives. (Skips silently if a stir op fails — best-effort load.)
	if envInt("BURST_CLOSE_STIR", 1) > 0 {
		for _, name := range sessions[:n/2] {
			_ = tmuxExec(socket, "new-window", "-t", name).Run()
			_ = tmuxExec(socket, "kill-window", "-t", name+":^").Run()
		}
	}

	// Two-wave close pattern. The bug requires that one client's notify-walk
	// iterate over another client whose free has begun but whose unlink
	// from the global control-clients list hasn't yet completed. With ALL
	// pipes closing in one wave, the server may have no remaining clients
	// to iterate over by the time the race-prone state appears. Splitting
	// into two waves separated by a small gap gives the first wave's
	// detaches a population of freshly-freed-but-still-listed siblings
	// during the second wave's notify-walks — mirroring the real-world
	// reload_storage_changed cascade more faithfully than a single-wave
	// barrier.
	//
	// BURST_CLOSE_RECONNECT=0 disables the reconnect arm.
	// BURST_CLOSE_GAP_MS controls the inter-wave gap (default 5 ms).
	reconnectArm := envInt("BURST_CLOSE_RECONNECT", 1) > 0
	gap := time.Duration(envInt("BURST_CLOSE_GAP_MS", 5)) * time.Millisecond

	half := n / 2
	if half == 0 {
		half = 1
	}
	wave1 := sessions[:half]
	wave2 := sessions[half:]

	dispatchWave := func(names []string, barrier <-chan struct{}, wg *sync.WaitGroup) {
		for _, name := range names {
			name := name
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-barrier
				pm.Disconnect(name)
				if reconnectArm {
					_ = pm.Connect(name, socket)
				}
			}()
		}
	}

	var wg sync.WaitGroup
	b1 := make(chan struct{})
	b2 := make(chan struct{})
	dispatchWave(wave1, b1, &wg)
	dispatchWave(wave2, b2, &wg)
	close(b1)
	time.Sleep(gap)
	close(b2)
	wg.Wait()

	// Liveness check — the bug crashes the entire tmux server, not just
	// one session. If `list-sessions` fails or reports "no server running"
	// we know the server died.
	out, err := tmuxExec(socket, "list-sessions").CombinedOutput()
	if err != nil || strings.Contains(string(out), "no server running") {
		t.Logf("trial %d: server died after burst-close (err=%v out=%q)",
			trial, err, strings.TrimSpace(string(out)))
		return false
	}
	return true
}

// spawnNoisySession creates a detached tmux session with extra panes and
// some steady output, to give the server's control-client notify-walk a
// non-trivial bookkeeping load. Returns false on creation failure (caller
// should skip the trial rather than fail).
func spawnNoisySession(t *testing.T, socket, name string) bool {
	t.Helper()
	// Body process: print a short line every ~5ms forever. Enough output
	// to keep the server's per-pane buffers churning without blowing up
	// log/disk.
	body := "while :; do printf 'x\\n'; sleep 0.005; done"
	if err := tmuxExec(socket, "new-session", "-d", "-s", name, "sh", "-c", body).Run(); err != nil {
		return false
	}
	// Add 2 extra windows, each with a split pane, so each session has
	// 3 windows × 1-2 panes. Mirrors a realistic editor+repl layout.
	for i := 0; i < 2; i++ {
		if err := tmuxExec(socket, "new-window", "-t", name, "sh", "-c", body).Run(); err != nil {
			return false
		}
		if err := tmuxExec(socket, "split-window", "-t", name, "sh", "-c", body).Run(); err != nil {
			return false
		}
	}
	return true
}

func killSessionsBestEffort(socket string, sessions []string) {
	for _, name := range sessions {
		_ = tmuxExec(socket, "kill-session", "-t", name).Run()
	}
}

// isolatedTmuxSocket returns a unique short -L socket name for a fresh tmux
// server scoped to this test. Uses tmux -L (default tmpdir, short name) rather
// than -S (full path) so we don't run afoul of the 104-char socket-path limit
// that bites macOS /var/folders/... defaults. Registers a t.Cleanup that
// kills the test server so we don't leak background tmux processes between
// test runs. CRITICAL: every test in this package that targets tmux MUST go
// through an isolated socket — this package's whole reason for existing is
// to crash the targeted server, and the user's real agent-deck-managed
// sessions live on the user's default socket.
func isolatedTmuxSocket(t *testing.T) string {
	t.Helper()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand.Read for socket suffix: %v", err)
	}
	socket := "ad-burst-test-" + hex.EncodeToString(b[:])
	t.Cleanup(func() {
		// kill-server returns "no server running" on success-path teardown
		// (server already gone) — we don't care, just want to ensure no
		// background tmux processes outlive the test.
		_ = tmuxExec(socket, "kill-server").Run()
	})
	return socket
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	var out int
	if _, err := fmt.Sscanf(v, "%d", &out); err != nil || out <= 0 {
		return def
	}
	return out
}
