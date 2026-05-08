package tmux

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestKillStaleControlClients_BurstDoesNotCrashServer is a regression check
// for the 2026-05-08 10:32:17 trigger pattern. From the debug log:
//
//	10:32:17.145  killed_stale_control_client align-laurel pid=17364
//	10:32:17.150  killed_stale_control_client investigate-s3 pid=17357
//	10:32:17.150  killed_stale_control_client gleaming-fern pid=17360
//	10:32:17.156  killed_stale_control_client gleaming-fern pid=17363
//	10:32:17.156  killed_stale_control_client investigate-s3 pid=17362
//	             [server dies — 5 SIGTERMs in 11 ms across 3 parallel
//	              Connect() invocations]
//
// 5 SIGTERMs to control-mode tmux clients in 11 ms expose tmux #4980's
// use-after-free in control_notify_client_detached. The EOF fast-path
// fix landed on the agent-deck-owned control pipe (`ControlPipe.Close`),
// but `killStaleControlClients` uses `softKillProcess` (SIGTERM+grace) on
// orphan tmux -C clients we don't own the stdin of — so the EOF fix
// doesn't cover this path.
//
// This test spawns N `tmux -C attach-session` children on the isolated
// socket, lets them complete the control-mode handshake, then barrier-
// releases N concurrent `softKillProcess` calls to compress the cascade
// to microseconds. Liveness check via `tmux list-sessions` confirms
// whether the server survived.
//
// Tunable via env (helpful when measuring baseline rate or tuning the
// future closeGate fix):
//
//	SIGTERM_BURST_TRIALS  - number of trials (default 100)
//	SIGTERM_BURST_N       - clients per trial (default 10)
//	SIGTERM_BURST_AGE_MS  - settle time after attach before SIGTERM
//	                        (default 100 ms — enough for the control
//	                        handshake to complete on macOS)
//
// Crashes are tallied across all trials and the test fails if any occur.
func TestKillStaleControlClients_BurstDoesNotCrashServer(t *testing.T) {
	skipIfNoTmuxBinary(t)
	if testing.Short() {
		t.Skip("integration: real tmux required, takes seconds")
	}
	// Gated: 100 trials × 10 clients × 100 ms settle ≈ 4 min on macOS,
	// and at every closeGate cadence tested so far the harness produces
	// 0 crashes — the orphan-PID SIGTERM-burst path needs server-aging
	// state that a per-trial fresh server can't reach. Kept on-demand
	// rather than on the default test path.
	if os.Getenv("AGENT_DECK_BURST_TEST") == "" {
		t.Skip("integration: set AGENT_DECK_BURST_TEST=1 to run")
	}

	trials := envInt("SIGTERM_BURST_TRIALS", 100)
	n := envInt("SIGTERM_BURST_N", 10)
	socket := isolatedTmuxSocket(t)

	crashes := 0
	for trial := 0; trial < trials; trial++ {
		if !runSigtermBurstTrial(t, socket, trial, n) {
			crashes++
			time.Sleep(50 * time.Millisecond)
		}
	}

	t.Logf("sigterm-burst: %d/%d trials crashed (N=%d clients per trial)", crashes, trials, n)
	if crashes > 0 {
		t.Errorf("tmux server crashed under SIGTERM burst: %d/%d trials (#4980)", crashes, trials)
	}
}

// runSigtermBurstTrial returns true if the tmux server survived the trial.
func runSigtermBurstTrial(t *testing.T, socket string, trial, n int) (alive bool) {
	t.Helper()

	sessions := make([]string, n)
	for i := range sessions {
		sessions[i] = fmt.Sprintf("sigterm-burst-%d-%d", trial, i)
		if !spawnNoisySession(t, socket, sessions[i]) {
			t.Logf("trial %d: spawn %q failed; skipping", trial, sessions[i])
			return true
		}
	}
	defer killSessionsBestEffort(socket, sessions)

	// Spawn a `tmux -C attach-session -t SESSION` child per session. These
	// stand in for the orphan control clients that killStaleControlClients
	// targets in production.
	clients := make([]*exec.Cmd, 0, n)
	stdins := make([]io.Closer, 0, n)
	defer func() {
		// Belt-and-suspenders cleanup. softKillProcess in the barrier-release
		// path should have already reaped most clients; this is for any
		// straggler.
		for i, c := range clients {
			if c.Process != nil {
				_ = c.Process.Kill()
				_ = c.Wait()
			}
			if i < len(stdins) {
				_ = stdins[i].Close()
			}
		}
	}()
	for _, name := range sessions {
		cmd := tmuxExec(socket, "-C", "attach-session", "-t", name)
		// Pipe stdin so the child doesn't see EOF and exit before the
		// SIGTERM cascade. We never close these — the cascade is purely
		// signal-driven (mirrors killStaleControlClients, which also
		// signals orphan PIDs without owning their stdin).
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Logf("trial %d: stdin pipe for %s: %v; skip", trial, name, err)
			return true
		}
		if err := cmd.Start(); err != nil {
			t.Logf("trial %d: start tmux -C for %s: %v; skip", trial, name, err)
			_ = stdin.Close()
			return true
		}
		clients = append(clients, cmd)
		stdins = append(stdins, stdin)
	}

	// Settle: let each client complete the control-mode handshake and
	// register in the server's control-clients list.
	settle := time.Duration(envInt("SIGTERM_BURST_AGE_MS", 100)) * time.Millisecond
	time.Sleep(settle)

	// Sanity check: server should actually report >= N clients attached.
	out, err := tmuxExec(socket, "list-clients").Output()
	if err != nil {
		t.Logf("trial %d: list-clients failed pre-barrier: %v; skip", trial, err)
		return true
	}
	attached := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			attached++
		}
	}
	if attached < n {
		t.Logf("trial %d: only %d/%d clients attached pre-barrier; skip", trial, attached, n)
		return true
	}

	// Barrier-release N concurrent softKillProcess calls. Goroutine wake-up
	// + a single syscall.Kill should compress the cascade tighter than the
	// 11 ms observed in the real-world crash 2.
	var wg sync.WaitGroup
	barrier := make(chan struct{})
	for _, c := range clients {
		c := c
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-barrier
			if c.Process != nil {
				_ = softKillProcess(c.Process.Pid, controlClientKillGrace)
			}
		}()
	}
	close(barrier)
	wg.Wait()

	// Liveness check.
	if err := tmuxExec(socket, "list-sessions").Run(); err != nil {
		t.Logf("trial %d: tmux server died after SIGTERM burst (err=%v)", trial, err)
		return false
	}
	return true
}
