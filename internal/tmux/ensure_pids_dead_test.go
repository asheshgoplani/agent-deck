package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
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

// Session cleanup must not depend on a list of familiar executable names.
// HLS ffmpeg encoders are legitimate pane descendants and must be reaped just
// like an agent binary. A symlink to sleep keeps this test hermetic while
// making ps report the process as "ffmpeg".
func TestEnsurePIDsDead_ReapsFFmpegNamedDescendant(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("posix signal semantics only; GOOS=%s", runtime.GOOS)
	}

	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.Symlink("/bin/sleep", ffmpeg); err != nil {
		t.Fatalf("create ffmpeg fixture: %v", err)
	}
	cmd := exec.Command(ffmpeg, "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start ffmpeg fixture: %v", err)
	}
	pid := cmd.Process.Pid
	reaped := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(reaped)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-reaped
	})

	EnsurePIDsDead([]int{pid}, 3*time.Second)

	if err := syscall.Kill(pid, syscall.Signal(0)); err == nil {
		name, _ := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
		t.Errorf("ffmpeg descendant %d (comm=%q) still alive after session cleanup",
			pid, strings.TrimSpace(string(name)))
	}
}

// A new tool launched by an agent must not require an Agent Deck release
// before session stop can clean it up. The ownership comes from the captured
// pane tree, not its executable name.
func TestEnsurePIDsDead_ReapsArbitrarilyNamedCapturedChild(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("posix signal semantics only; GOOS=%s", runtime.GOOS)
	}

	child := filepath.Join(t.TempDir(), "session-owned-helper")
	if err := os.Symlink("/bin/sleep", child); err != nil {
		t.Fatalf("create helper fixture: %v", err)
	}
	cmd := exec.Command(child, "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper fixture: %v", err)
	}
	pid := cmd.Process.Pid
	reaped := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(reaped)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-reaped
	})

	EnsurePIDsDead([]int{pid}, 3*time.Second)

	if err := syscall.Kill(pid, syscall.Signal(0)); err == nil {
		t.Errorf("arbitrarily named captured child %d still alive after session cleanup", pid)
	}
}

// Interactive callers use Session.Kill rather than KillAndWait. It may return
// before cleanup finishes, but it still must capture identities before tmux
// tears down the pane and hand those identities to the asynchronous reaper.
func TestSessionKillUsesIdentityReaper(t *testing.T) {
	raw, err := os.ReadFile("tmux.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	start := strings.Index(src, "func (s *Session) Kill() error")
	end := strings.Index(src[start:], "func (s *Session) getPaneProcessTree()")
	if start < 0 || end < 0 {
		t.Fatal("could not locate Session.Kill")
	}
	body := src[start : start+end]
	if !strings.Contains(body, "CaptureProcessIdentities(oldPIDs)") {
		t.Fatal("Session.Kill must capture descendant identities before tmux kill")
	}
	if !strings.Contains(body, "EnsureProcessIdentitiesDead(oldProcesses") {
		t.Fatal("Session.Kill must reap captured identities without an executable-name allowlist")
	}
}

// A PID can be reused after its original descendant exits. A changed start
// time must therefore be treated as a different, unrelated process.
func TestFilterAliveProcessIdentitiesSkipsChangedStartTime(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("posix process metadata only; GOOS=%s", runtime.GOOS)
	}

	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fixture: %v", err)
	}
	reaped := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(reaped)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-reaped
	})

	identities := CaptureProcessIdentities([]int{cmd.Process.Pid})
	if len(identities) != 1 {
		t.Fatalf("captured identities = %v, want one", identities)
	}
	identities[0].StartedAt += " different-process"
	if got := filterAliveProcessIdentities(identities); len(got) != 0 {
		t.Fatalf("changed process identity was considered owned: %v", got)
	}
}
