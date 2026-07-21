package tmux

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The post-respawn `getPaneProcessTree` probe is now bounded (2026-07-21 fd-leak
// incident). Bounding introduced a new failure mode: a timed-out probe returns
// panePID 0, and 0 never matches a real PID, so the `pid == newPanePID` guard in
// ensureProcessesDead silently disengages. If tmux hands the fresh pane process
// a PID that was in the pre-respawn tree — the reuse case the guard exists for —
// the escalation SIGTERM→SIGKILLs the process the user just restarted.
//
// The rule these tests pin: an INDETERMINATE probe must not be read as
// "the new pane is not among the old PIDs". Skipping the escalation leaves at
// worst a logged orphan; guessing kills a live agent mid-work.

// startSigtermImmuneChild spawns a child that survives SIGTERM but not SIGKILL,
// so "still alive after the escalation" is an unambiguous signal that the
// escalation never reached its SIGKILL stage.
//
// Two things about the command are load-bearing, both learned the hard way:
//
// bash, not sh: the respawn path consults isOurProcess (tmux.go), whose narrow
// allowlist matches "bash" but not the "dash" that /bin/sh is on Debian/Ubuntu.
//
// A LOOP, not a bare `sleep 30`: bash exec-optimizes `bash -c '<single
// command>'` into the command itself, replacing the process — so the child's
// comm becomes "sleep", which isOurProcess does not match, and the escalation
// skips it. Whether that fires depends on the bash version (5.0 kept bash
// resident, the CI runner's newer bash did not), so it flipped between
// developer machines and CI. The loop keeps a real bash in place everywhere.
// isOurProcessLoose (ensure_pids_dead.go) carries "sleep"/"dash" in its
// allowlist for this same harness hazard.
func startSigtermImmuneChild(t *testing.T) int {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("posix signal semantics only; GOOS=%s", runtime.GOOS)
	}

	cmd := exec.Command("bash", "-c", "trap '' TERM HUP; while :; do sleep 1; done")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	// Reap on exit so kill(pid, 0) reports ESRCH instead of finding a zombie
	// that Go has not waited on yet.
	reaped := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(reaped)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-reaped
	})

	time.Sleep(150 * time.Millisecond) // let the trap install
	pid := cmd.Process.Pid
	if !pidIsAlive(pid) {
		t.Fatalf("setup: child %d not alive", pid)
	}

	// Guard the guard. Every assertion below reduces to "did ensureProcessesDead
	// signal this PID", and it only signals PIDs isOurProcess vouches for — so if
	// the child is not recognized, the survival tests pass while proving nothing
	// and the reap test fails for a reason that looks nothing like its message.
	// Both happened here before this check existed.
	comm, psErr := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if psErr != nil {
		t.Skipf("ps unavailable (%v): isOurProcess cannot classify anything on this "+
			"host, so the escalation is a no-op by construction and nothing here is testable", psErr)
	}
	if !isOurProcess(pid) {
		t.Fatalf("setup: child comm is %q, which isOurProcess does not match — the "+
			"escalation would skip it and every assertion in this file would be vacuous",
			strings.TrimSpace(string(comm)))
	}
	return pid
}

func pidIsAlive(pid int) bool {
	return syscall.Kill(pid, syscall.Signal(0)) == nil
}

// waitForDeath polls rather than sleeping a fixed span: the escalation SIGKILLs
// and the reaper goroutine clears the zombie asynchronously.
//
// It is also the correct way to assert the NEGATIVE. kill(pid, 0) keeps
// succeeding for the moment between SIGKILL and the wait() that clears the
// zombie, so a bare pidIsAlive() immediately after the escalation returns "alive"
// even for a process that was just killed — that false green is exactly what
// this helper exists to prevent.
func waitForDeath(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !pidIsAlive(pid) {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}

// survivalGrace is how long a killed process is given to actually disappear
// before "still alive" is believed. SIGKILL + wait() is sub-millisecond; 500ms
// is generous.
const survivalGrace = 500 * time.Millisecond

// deadSessionForEscalation returns a Session whose tmux session does not exist,
// so the escalation's own re-probe fails for real (no stubbing): `list-panes`
// against a missing session exits non-zero immediately.
func deadSessionForEscalation() *Session {
	return &Session{
		Name:       "agentdeck-escalation-probe-absent",
		SocketName: DefaultSocketName(),
	}
}

// An indeterminate post-respawn probe must NOT be treated as "new pane PID is
// not in oldPIDs". This is M1: the freshly respawned process must survive.
func TestEscalateAfterRespawn_SkipsEscalationWhenPanePIDUnknown(t *testing.T) {
	pid := startSigtermImmuneChild(t)

	s := deadSessionForEscalation()
	// probeErr non-nil == the bounded pane probe timed out or failed, so the
	// new process tree is unknown (nil).
	s.escalateAfterRespawn([]int{pid}, nil, errors.New("signal: killed"))

	if waitForDeath(pid, survivalGrace) {
		t.Fatal("escalation killed a process while the new pane PID was unknown: " +
			"an indeterminate probe must skip the SIGTERM→SIGKILL stage, not assume " +
			"the fresh process is absent from oldPIDs")
	}
}

// The guard the unknown-PID case disengages: a PID that IS the new pane process
// must never be escalated against.
func TestEscalateAfterRespawn_SparesTheNewPaneProcess(t *testing.T) {
	pid := startSigtermImmuneChild(t)

	s := deadSessionForEscalation()
	s.escalateAfterRespawn([]int{pid}, []int{pid}, nil)

	if waitForDeath(pid, survivalGrace) {
		t.Fatal("escalation killed the process it was told is the new pane process")
	}
}

// The pane process is not the only thing the respawn creates: `bash -lc <agent>`
// forks children (node, claude) that pass isOurProcess just as readily. Sparing
// only the pane PID leaves those exposed to a PID collision with the old tree,
// so the whole new tree is excluded, not just its root.
func TestEscalateAfterRespawn_SparesEveryPIDInTheNewTree(t *testing.T) {
	paneLike := startSigtermImmuneChild(t)
	childLike := startSigtermImmuneChild(t)

	s := deadSessionForEscalation()
	// Both appear in the stale tree AND in the fresh one — the collision case.
	s.escalateAfterRespawn([]int{paneLike, childLike}, []int{paneLike, childLike}, nil)

	if waitForDeath(childLike, survivalGrace) {
		t.Fatal("escalation killed a descendant of the freshly respawned process: " +
			"the exclusion must cover the whole new tree, not just the pane PID")
	}
	if !pidIsAlive(paneLike) {
		t.Fatal("escalation killed the new pane process")
	}
}

// Negative control: with a resolved pane PID that is genuinely not in the old
// tree, the escalation must still do its job — the SIGHUP-immune-agent reap
// (Claude Code 2.1.27+) is why this code exists at all.
func TestEscalateAfterRespawn_StillReapsSurvivorsWhenPanePIDKnown(t *testing.T) {
	pid := startSigtermImmuneChild(t)

	s := deadSessionForEscalation()
	// os.Getpid() is a live PID that is certainly not the child.
	s.escalateAfterRespawn([]int{pid}, []int{os.Getpid()}, nil)

	if !waitForDeath(pid, 2*time.Second) {
		t.Fatal("escalation left a SIGTERM-immune survivor alive")
	}
}
