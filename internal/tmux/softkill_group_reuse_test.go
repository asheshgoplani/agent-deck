package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubGroupKillSyscall swaps the syscall seam softKillProcessGroup itself calls,
// so these tests drive the PRODUCTION entry point. Testing an injectable core
// instead leaves the wrapper free to stop calling it: the pre-fix EPERM
// escalation can be pasted straight back into softKillProcessGroup and a
// core-only test suite stays green.
func stubGroupKillSyscall(t *testing.T, fn func(pid int, sig syscall.Signal) error) {
	t.Helper()
	original := groupKillSyscall
	t.Cleanup(func() { groupKillSyscall = original })
	groupKillSyscall = fn
}

// alwaysOurs is the ownership predicate for tests that are not about ownership.
func alwaysOurs() bool { return true }

// reapWithEOFGrace's fallback has two arms and only the narrow one was ever
// hardened. The else arm — one pid, resolved through its handle — is guarded
// against the pid having been recycled. The if arm resolved a whole PROCESS
// GROUP from that same raw pid (syscall.Getpgid(proc.Pid)) and then SIGTERMed
// it, SIGKILLing 500ms later. Same hazard, strictly wider blast radius, on what
// was the unguarded arm.
//
// A recycled pid cannot be forced here — it would take a pid_max wrap inside
// the window and prove no more — so these tests hand the fallback a group id
// that names a stranger, which is what a stored pgid becomes once the number
// has been handed on. The victim is real: a live helper in a group that is
// nobody's business but its own.

// TestReapWithEOFGrace_RefusesTheGroupKillOnceTheChildHasBeenWaitedOn is the
// friendly-fire case. The child has already been reaped, the reap function has
// not returned yet so the fallback fires, and the group id it was given now
// belongs to someone else.
func TestReapWithEOFGrace_RefusesTheGroupKillOnceTheChildHasBeenWaitedOn(t *testing.T) {
	// The stranger. "ignore" drains SIGTERM, so only the escalation's SIGKILL
	// can end it — which makes its death unambiguous rather than a race with
	// its own shutdown.
	bystander := spawnHelperInOwnGroup(t, "ignore")
	bystanderExited := waitForExit(t, bystander)
	strangerPgid := bystander.Process.Pid

	cmd := spawnHelperInOwnGroup(t, "ignore")
	proc := cmd.Process
	require.NoError(t, proc.Kill())
	_, err := proc.Wait() // reaped: from here the number belongs to whoever gets it next
	require.NoError(t, err)

	release := make(chan struct{})
	fellBack := make(chan bool, 1)
	go func() {
		// A reap that has not returned is the only way into the fallback.
		fellBack <- reapWithEOFGrace(func() { <-release }, proc, strangerPgid,
			50*time.Millisecond, 200*time.Millisecond)
	}()

	// Long enough for the fallback's SIGTERM, its full grace and the SIGKILL
	// that follows to have landed, if they were ever sent.
	select {
	case <-bystanderExited:
		t.Fatal("a process group that never belonged to this pipe was signalled")
	case <-time.After(600 * time.Millisecond):
	}

	close(release)
	assert.True(t, <-fellBack, "the fallback path must still report that it ran")
}

// spawnHelperJoiningGroup starts a helper INTO an existing process group rather
// than leading one of its own, so a test can tell a group kill from a pid kill.
//
// Not registerOrphanReaper: that helper signals -pid for anything started with
// Setpgid, which is right for a leader (pid == pgid) and wrong here — this
// child's pid names no group, and a raw negative signal on a number that is not
// a pgid is the friendly-fire shape this whole file is about. Teardown goes
// through the handle instead.
func spawnHelperJoiningGroup(t *testing.T, pgid int, role string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.Command(exe, "-test.run=^$")
	ready := filepath.Join(t.TempDir(), "ready")
	cmd.Env = append(os.Environ(), "SOFTKILL_TEST_HELPER="+role, softkillReadyEnv+"="+ready)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: pgid}
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	waitForReady(t, ready, 5*time.Second)

	// The premise of every assertion that follows.
	got, err := syscall.Getpgid(cmd.Process.Pid)
	require.NoError(t, err)
	require.Equal(t, pgid, got, "the helper did not join the group under test")
	return cmd
}

// TestReapWithEOFGrace_StillGroupKillsAWedgedLiveChild is the positive control.
// Failing closed is only worth anything if the fallback still does its job in
// the case it exists for: a child that ignores both stdin EOF and SIGTERM, and
// whose handle has NOT been waited on.
//
// It asserts the GROUP dies, not just the child. Killing the group is the whole
// reason this arm exists — a `tmux -C` client that spawned descendants leaves
// them orphaned otherwise — and with a single-process group in the fixture,
// "kill the pid" and "kill the group" are indistinguishable, so a regression
// that quietly drops to the handle path would pass.
func TestReapWithEOFGrace_StillGroupKillsAWedgedLiveChild(t *testing.T) {
	leader := spawnHelperInOwnGroup(t, "ignore")
	pgid := leader.Process.Pid
	joiner := spawnHelperJoiningGroup(t, pgid, "ignore")

	leaderExited := waitForExit(t, leader)
	joinerExited := waitForExit(t, joiner)

	// A reap that gives up rather than blocking forever. `func() { <-exited }`
	// deadlocks reapWithEOFGrace's closing `<-reapDone` if a regression makes
	// the fallback inert, and the test then hangs the whole package until the
	// 10-minute suite panic — which also strands TestMain's tmux server,
	// because the deferred kill-server never runs. A test must fail, not hang.
	reap := func() {
		select {
		case <-leaderExited:
		case <-time.After(5 * time.Second):
		}
	}

	usedFallback := reapWithEOFGrace(reap, leader.Process, pgid, 50*time.Millisecond, 500*time.Millisecond)

	assert.True(t, usedFallback)
	select {
	case <-leaderExited:
	case <-time.After(2 * time.Second):
		t.Fatal("a wedged live child survived the fallback; the guard has made the path inert")
	}
	select {
	case <-joinerExited:
	case <-time.After(2 * time.Second):
		t.Fatal("a process sharing the child's group survived: the fallback killed the pid, " +
			"not the group, and a client's descendants would be left behind")
	}
}

// TestSoftKillProcessGroup_RefusesAGroupItMayNotSignal covers the second half of
// the same finding. EPERM from kill(2) is the kernel saying "not one member of
// that group is yours" — the exact answer a recycled pgid produces. The old
// branch treated it as a reason to try harder: it escalated to a group-wide
// SIGKILL and returned true, reporting a kill it had no right to make and could
// not have made.
func TestSoftKillProcessGroup_RefusesAGroupItMayNotSignal(t *testing.T) {
	var sent []syscall.Signal
	stubGroupKillSyscall(t, func(_ int, sig syscall.Signal) error {
		sent = append(sent, sig)
		return syscall.EPERM
	})

	usedSIGKILL := softKillProcessGroup(4242, 50*time.Millisecond, alwaysOurs)

	assert.False(t, usedSIGKILL,
		"a group we may not signal must not be reported as killed")
	assert.Equal(t, []syscall.Signal{syscall.SIGTERM}, sent,
		"escalating after EPERM cannot succeed — the permission check is identical for "+
			"SIGKILL — so the only thing a second signal can do is reach a group that is not ours")
}

// TestSoftKillProcessGroup_ReportsNothingForAnEmptyGroup keeps the ESRCH
// contract pinned alongside the new EPERM one, so a later simplification of the
// error handling cannot collapse the two into one answer.
func TestSoftKillProcessGroup_ReportsNothingForAnEmptyGroup(t *testing.T) {
	stubGroupKillSyscall(t, func(int, syscall.Signal) error { return syscall.ESRCH })

	assert.False(t, softKillProcessGroup(4242, 50*time.Millisecond, alwaysOurs))
}

// TestSoftKillProcessGroup_RefusesToEscalateOnceTheGroupIsNoLongerOurs covers
// the window the spawn-time pgid alone does not close. The gate in
// reapWithEOFGrace is a point-in-time check; the escalation happens a full grace
// LATER, and the child can exit and be reaped inside that window — freeing its
// pid, which is also the pgid, for reuse. The SIGKILL at the deadline must not
// go out on a number that stopped being ours in the meantime.
func TestSoftKillProcessGroup_RefusesToEscalateOnceTheGroupIsNoLongerOurs(t *testing.T) {
	var sent []syscall.Signal
	stubGroupKillSyscall(t, func(_ int, sig syscall.Signal) error {
		sent = append(sent, sig)
		return nil // the group answers throughout: only ownership changes
	})

	// Ours when the SIGTERM goes out, not ours by the time the grace expires.
	ours := true
	usedSIGKILL := softKillProcessGroup(4242, 100*time.Millisecond, func() bool {
		defer func() { ours = false }()
		return ours
	})

	assert.False(t, usedSIGKILL, "an escalation onto a pgid that changed hands must not be reported as a kill")
	assert.NotContains(t, sent, syscall.SIGKILL,
		"the group-wide SIGKILL went out after the pgid stopped naming our child")
}
