package tmux

import (
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

// TestReapWithEOFGrace_StillGroupKillsAWedgedLiveChild is the positive control.
// Failing closed is only worth anything if the fallback still does its job in
// the case it exists for: a child that ignores both stdin EOF and SIGTERM, and
// whose handle has NOT been waited on.
func TestReapWithEOFGrace_StillGroupKillsAWedgedLiveChild(t *testing.T) {
	cmd := spawnHelperInOwnGroup(t, "ignore")
	exited := waitForExit(t, cmd)

	usedFallback := reapWithEOFGrace(func() { <-exited }, cmd.Process, cmd.Process.Pid,
		50*time.Millisecond, 500*time.Millisecond)

	assert.True(t, usedFallback)
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("a wedged live child survived the fallback; the guard has made the path inert")
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
	kill := func(_ int, sig syscall.Signal) error {
		sent = append(sent, sig)
		return syscall.EPERM
	}

	usedSIGKILL := softKillProcessGroupWith(kill, 4242, 50*time.Millisecond)

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
	kill := func(int, syscall.Signal) error { return syscall.ESRCH }

	assert.False(t, softKillProcessGroupWith(kill, 4242, 50*time.Millisecond))
}
