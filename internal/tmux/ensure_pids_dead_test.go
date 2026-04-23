package tmux

import (
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// EnsurePIDsDead must synchronously reap SIGHUP-immune children by the
// time it returns. Previously the (unexported) ensureProcessesDead ran
// in a goroutine. In a short-lived CLI process such as `agent-deck
// session remove`, the CLI exits before the goroutine finishes —
// leaving an orphan claude process behind.
//
// Observed 2026-04-22 on the maintainer's host: PID 321456, 33-hour
// orphan with AGENTDECK_INSTANCE_ID set, no corresponding agent-deck
// session record. Root cause #59.
func TestEnsurePIDsDead_SynchronouslyKillsSigHupImmuneChild(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("posix signal semantics only; GOOS=%s", runtime.GOOS)
	}

	// `trap '' HUP; sleep 30` emulates claude 2.1.27+ which ignores
	// SIGHUP. This is the real-world case that triggered the orphan
	// bug — tmux kill-session sends SIGHUP and the claude child
	// keeps running.
	cmd := exec.Command("sh", "-c", "trap '' HUP; sleep 30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start shell: %v", err)
	}
	pid := cmd.Process.Pid
	// Reap the process as soon as it exits so the kernel zombie entry
	// clears and kill(pid, 0) correctly returns ESRCH. Without this,
	// Go keeps the PID in its wait-for-me set and signal-0 reports
	// "alive" on a defunct-but-unreaped process — the classic zombie
	// pitfall that trips up every "is this pid dead" check.
	reaped := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(reaped)
	}()
	// Ensure we never leak the sleep, even if the assertion below fails.
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-reaped
	})

	// Give the shell a moment to install the HUP trap.
	time.Sleep(150 * time.Millisecond)

	// Sanity: child is alive.
	if err := syscall.Kill(pid, syscall.Signal(0)); err != nil {
		t.Fatalf("setup: pid %d not alive: %v", pid, err)
	}

	// The contract: when this call returns, the pid is dead. No polling,
	// no sleep-loops in the caller. 3s timeout is well above the
	// SIGTERM→SIGKILL escalation window (~1.5s) inside EnsurePIDsDead.
	EnsurePIDsDead([]int{pid}, 3*time.Second)

	if err := syscall.Kill(pid, syscall.Signal(0)); err == nil {
		name, _ := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
		t.Errorf("pid %d (comm=%q) still alive after EnsurePIDsDead — must be synchronous",
			pid, strings.TrimSpace(string(name)))
	}
}

// A nil/empty PID list must be a no-op, returning immediately. Callers
// in the remove path often fetch `getPaneProcessTree()` which returns
// an empty slice for already-torn-down sessions; that must not block.
func TestEnsurePIDsDead_NoopOnEmptyPIDs(t *testing.T) {
	start := time.Now()
	EnsurePIDsDead(nil, 10*time.Second)
	EnsurePIDsDead([]int{}, 10*time.Second)
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Errorf("EnsurePIDsDead on empty PID list must return immediately; took %v", elapsed)
	}
}
