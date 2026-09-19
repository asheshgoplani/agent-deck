package procowner

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// signalTable is a scripted kill(pid, 0): every pid in it answers with the
// recorded error (nil = exists), anything else answers ESRCH.
func signalTable(answers map[int]error) func(int) error {
	return func(pid int) error {
		if err, ok := answers[pid]; ok {
			return err
		}
		return syscall.ESRCH
	}
}

// TestAliveWith_ProcessStates pins the verdict per process state the two
// readings can produce. The one that matters is the zombie: kill(pid, 0)
// still succeeds for it, and treating that as alive is what kept a dead
// sweep's marker and update.lock "held" (v1.16.11 rollout).
func TestAliveWith_ProcessStates(t *testing.T) {
	p := newFakeProber()
	p.add(10, 1, "100", 1000)                            // running
	p.add(50, 1, "500", 0)                               // root's: EPERM on the signal
	p.procs[20] = ProcInfo{PID: 20, PPID: 1, State: "Z"} // exited, unreaped
	p.procs[21] = ProcInfo{PID: 21, PPID: 1, State: "Z+"}
	p.setErr(30, errors.New("ps timed out: "+ErrUnreadable.Error()))
	p.setErr(31, ErrUnreadable)
	p.setErr(40, ErrUnsupported)
	signals := signalTable(map[int]error{
		10: nil, 20: nil, 21: nil, 30: nil, 31: nil, 40: nil,
		50: syscall.EPERM, // someone else's process: exists
		60: nil,           // exists per signal, but /proc says gone (raced its exit)
	})
	p.setErr(60, ErrNoProcess)

	cases := []struct {
		name string
		pid  int
		want bool
	}{
		{"running", 10, true},
		{"zombie", 20, false},
		{"zombie with flags", 21, false},
		{"identity unreadable falls back to the signal", 30, true},
		{"unreadable sentinel falls back to the signal", 31, true},
		{"platform unsupported falls back to the signal", 40, true},
		{"other user's process", 50, true},
		{"signal says exists, prober says gone", 60, false},
		{"no such process", 70, false},
		{"pid 0", 0, false},
		{"negative pid", -1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := aliveWith(p, signals, tc.pid); got != tc.want {
				t.Fatalf("aliveWith(%d) = %v, want %v", tc.pid, got, tc.want)
			}
		})
	}
}

// TestAliveWith_ESRCHNeverInspects pins the cheap path: a pid the kernel
// already says is gone is never handed to ps or /proc.
func TestAliveWith_ESRCHNeverInspects(t *testing.T) {
	p := newFakeProber()
	p.add(10, 1, "100", 1000)
	if aliveWith(p, signalTable(nil), 10) {
		t.Fatal("ESRCH must win over a stale prober entry")
	}
	if p.inspects != 0 {
		t.Fatalf("prober consulted %d times after ESRCH, want 0", p.inspects)
	}
}

// TestAlive_RealZombieIsDead is the end-to-end proof on this kernel: a child
// that exited but has not been waited for answers kill(pid, 0), and Alive
// still says dead. Skipped where the platform prober cannot look.
func TestAlive_RealZombieIsDead(t *testing.T) {
	if _, err := NewProber().Inspect(1); errors.Is(err, ErrUnsupported) {
		t.Skip("no process prober on this platform")
	}
	live := exec.Command("sleep", "30")
	if err := live.Start(); err != nil {
		t.Skip("cannot start a child:", err)
	}
	defer func() { _ = live.Process.Kill(); _ = live.Wait() }()
	if !Alive(live.Process.Pid) {
		t.Fatalf("a running child (pid %d) must be alive", live.Process.Pid)
	}

	child := exec.Command("true")
	if err := child.Start(); err != nil {
		t.Skip("cannot start a child:", err)
	}
	pid := child.Process.Pid
	// Let it exit without reaping it: that is exactly the state the re-exec'd
	// TUI left the update child in.
	deadline := time.Now().Add(5 * time.Second)
	for Alive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if syscall.Kill(pid, 0) != nil {
		t.Fatalf("pid %d should still be signalable as a zombie", pid)
	}
	if Alive(pid) {
		t.Fatalf("unreaped child %d answers kill(0) but must not count as alive", pid)
	}
	_ = child.Wait()
	if Alive(pid) {
		t.Fatalf("reaped child %d must be dead", pid)
	}
}
