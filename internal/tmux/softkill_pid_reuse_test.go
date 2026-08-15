package tmux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"

	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A PID is not a stable name for a process. The sweeps in this file signal PIDs
// they read out of a `list-clients` snapshot, and between that read and the
// signal the client can exit and the kernel can hand its number to something
// else. Two windows exist:
//
//   - Snapshot → SIGTERM. reapStaleControlClients kills sequentially and
//     softKillProcess blocks up to controlClientKillGrace per victim, so with N
//     stale clients the last line of the snapshot is up to N × 500ms old by the
//     time it is acted on.
//   - SIGTERM → SIGKILL. The escalation fires grace later, and the poll that
//     decides it uses kill(pid, 0) — which cannot tell "still alive" from
//     "died and the number was reissued".
//
// The guard is an identity captured with the decision and re-checked against
// the live process immediately before each signal. These tests pin that the
// guard blocks a signal when the identity no longer matches, and — just as
// important — that it does not otherwise change who gets killed.

// TestReadProcessIdentity_StableForOneIncarnation pins the property the guard
// rests on: reading the same live process twice yields the same identity, so a
// mismatch means the PID changed hands rather than that the read is noisy.
func TestReadProcessIdentity_StableForOneIncarnation(t *testing.T) {
	first, err := readProcessIdentity(context.Background(), os.Getpid())
	require.NoError(t, err, "own identity must be readable")
	require.NotEmpty(t, first, "identity must not be empty for a live process")

	time.Sleep(20 * time.Millisecond)

	second, err := readProcessIdentity(context.Background(), os.Getpid())
	require.NoError(t, err)
	assert.Equal(t, first, second, "identity must not drift while the process lives")
}

// TestReadProcessIdentity_FailsForDeadProcess pins that a reaped PID has no
// identity to compare against, which is what makes the guard fail closed for a
// victim that is already gone.
func TestReadProcessIdentity_FailsForDeadProcess(t *testing.T) {
	cmd := spawnHelper(t, "clean")
	pid := cmd.Process.Pid
	require.NoError(t, cmd.Process.Kill())
	_, _ = cmd.Process.Wait()

	_, err := readProcessIdentity(context.Background(), pid)
	assert.Error(t, err, "a reaped pid must not report an identity")
}

// TestSoftKillProcessChecked_SignalsWhenIdentityMatches is the no-regression
// half: the guard must not turn the sweep inert. The child's SIGTERM handler
// writes a marker, and SIGKILL cannot be trapped, so the marker is proof the
// TERM path ran.
func TestSoftKillProcessChecked_SignalsWhenIdentityMatches(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "term-handled")
	cmd := spawnHelper(t, "clean", "MARKER="+marker)
	pid := cmd.Process.Pid

	// Reap concurrently so the kill(pid, 0) poll sees ESRCH rather than a
	// zombie — production has tmux reaping its own clients.
	waitForExit(t, cmd)

	identity, err := readProcessIdentity(context.Background(), pid)
	require.NoError(t, err)

	_, signalled := softKillProcessChecked(context.Background(), pid, identity, 500*time.Millisecond)

	assert.True(t, signalled, "a matching identity must report the kill as delivered")
	assert.NoError(t, waitForFile(marker, 5*time.Second),
		"a matching identity must not block the SIGTERM")
}

// TestSoftKillProcessChecked_SkipsSignalWhenIdentityChanged is the guard doing
// its job on the snapshot→SIGTERM window. A stale identity stands in for the
// real hazard — the PID we validated died and its number now belongs to an
// unrelated process — because forcing a genuine PID wrap inside a test would
// take pid_max allocations and prove no more than this does.
//
// Both assertions matter: the process outliving the call proves no signal of
// either kind landed, and the marker's absence identifies which one would have
// (the "clean" helper writes it from its SIGTERM handler).
func TestSoftKillProcessChecked_SkipsSignalWhenIdentityChanged(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "term-handled")
	cmd := spawnHelper(t, "clean", "MARKER="+marker)
	pid := cmd.Process.Pid

	// Survival is asserted by waiting on the process rather than by
	// kill(pid, 0): between a signal landing and the parent's wait() the pid
	// is a zombie that still answers signal 0, so a bare liveness probe would
	// pass against the unguarded code and prove nothing.
	exited := waitForExit(t, cmd)

	usedSIGKILL, signalled := softKillProcessChecked(context.Background(), pid, "not-the-identity-we-validated", 100*time.Millisecond)

	assert.False(t, usedSIGKILL, "a process we refused to signal cannot have been SIGKILL'd")
	// The caller counts kills and logs one line per victim from this flag, so a
	// skipped signal must not be reported as a kill.
	assert.False(t, signalled, "a skipped signal must not be reported as delivered")

	select {
	case <-exited:
		t.Fatal("a pid whose identity changed was signalled anyway")
	case <-time.After(300 * time.Millisecond):
	}
	_, statErr := os.Stat(marker)
	assert.Error(t, statErr, "no SIGTERM may reach a pid whose identity changed")
}

// TestSoftKillProcessChecked_RefusesWhenIdentityUnknown pins the fail-closed
// rule at the boundary where it is cheapest to get wrong: an identity the
// caller never established.
//
// An earlier revision of this guard proceeded here, on the argument that a host
// unable to report start times should keep the #595 cleanup it had before.
// That is the wrong way round for code whose failure mode is SIGKILLing a live
// process: an inert sweep is recoverable and, since every refusal is logged,
// visible — a kill aimed at a pid nothing can tie to a real decision is
// neither. Nothing may be signalled, and the caller must not be told a kill
// happened.
func TestSoftKillProcessChecked_RefusesWhenIdentityUnknown(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "term-handled")
	cmd := spawnHelper(t, "clean", "MARKER="+marker)
	exited := waitForExit(t, cmd)

	usedSIGKILL, signalled := softKillProcessChecked(context.Background(), cmd.Process.Pid, "", 500*time.Millisecond)

	assert.False(t, signalled, "an unidentified pid must not be reported as signalled")
	assert.False(t, usedSIGKILL, "an unidentified pid must not be escalated to SIGKILL")
	select {
	case <-exited:
		t.Fatal("an unidentified pid was signalled anyway")
	case <-time.After(300 * time.Millisecond):
	}
	_, statErr := os.Stat(marker)
	assert.Error(t, statErr, "no SIGTERM may reach a pid the caller could not identify")
}

// TestSoftKillProcessChecked_RefusesWhenTheCurrentIdentityCannotBeRead is the
// same rule on the other side of the comparison: the caller has an identity,
// but the live process can no longer be read to check it against. A host on
// which neither /proc nor `ps` answers must lose the kill, not the guard.
func TestSoftKillProcessChecked_RefusesWhenTheCurrentIdentityCannotBeRead(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "term-handled")
	cmd := spawnHelper(t, "clean", "MARKER="+marker)
	exited := waitForExit(t, cmd)

	identity, err := readProcessIdentity(context.Background(), cmd.Process.Pid)
	require.NoError(t, err)

	original := processIdentityOf
	t.Cleanup(func() { processIdentityOf = original })
	processIdentityOf = func(context.Context, int) (string, error) {
		return "", errors.New("no /proc and no ps on this host")
	}

	usedSIGKILL, signalled := softKillProcessChecked(context.Background(), cmd.Process.Pid, identity, 100*time.Millisecond)

	assert.False(t, signalled, "a pid that cannot be re-identified must not be signalled")
	assert.False(t, usedSIGKILL, "nor escalated to SIGKILL")
	select {
	case <-exited:
		t.Fatal("a pid whose identity could not be read was signalled anyway")
	case <-time.After(300 * time.Millisecond):
	}
	_, statErr := os.Stat(marker)
	assert.Error(t, statErr, "no SIGTERM may reach a pid whose identity could not be read")
}

// TestSoftKillProcessChecked_SkipsEscalationWhenPIDRecycledDuringGrace covers the
// second window, which no caller can close for itself: the victim accepted the
// SIGTERM, exited inside the grace period, and its number was reissued before
// the escalation. kill(pid, 0) still succeeds, so the unguarded code reads that
// as "ignored my SIGTERM" and SIGKILLs whoever holds the number now.
//
// The seam supplies the real identity for the pre-SIGTERM check and a different
// one afterwards, which is what a PID handed to a new process looks like from
// here. The "ignore" helper drains SIGTERM and blocks forever, so it survives
// to the escalation and its death would be unambiguous evidence of a SIGKILL.
func TestSoftKillProcessChecked_SkipsEscalationWhenPIDRecycledDuringGrace(t *testing.T) {
	cmd := spawnHelper(t, "ignore")
	pid := cmd.Process.Pid

	live, err := readProcessIdentity(context.Background(), pid)
	require.NoError(t, err)

	original := processIdentityOf
	t.Cleanup(func() { processIdentityOf = original })

	// One read happens before the SIGTERM — the pre-signal check — and it must
	// agree with the captured identity or the SIGTERM never goes out and the
	// escalation is never reached. Every read after that is the escalation
	// check, which must see a stranger.
	calls := 0
	processIdentityOf = func(ctx context.Context, p int) (string, error) {
		calls++
		if calls <= 1 {
			return live, nil
		}
		return "recycled-during-grace", nil
	}

	exited := waitForExit(t, cmd)

	usedSIGKILL, signalled := softKillProcessChecked(context.Background(), pid, live, 100*time.Millisecond)

	assert.False(t, usedSIGKILL, "escalation must not fire once the pid changed hands")
	assert.True(t, signalled, "the SIGTERM half did go out; only the escalation was refused")
	// Without this, the test passes just as happily when the SIGTERM never went
	// out at all — a pre-signal check that started failing would skip straight
	// past the escalation this test exists to cover, and usedSIGKILL would be
	// false for the wrong reason. The second read IS the escalation check, so
	// reaching it proves the guarded path ran end to end.
	assert.GreaterOrEqual(t, calls, 2,
		"the escalation check must have been reached (pre-signal check, then escalation)")
	select {
	case <-exited:
		t.Fatal("the new occupant of the pid was SIGKILL'd by the escalation")
	case <-time.After(300 * time.Millisecond):
	}
}

// waitForExit reaps cmd in ONE owning goroutine and returns a channel closed
// when it exits. Waiting is the only conclusive way to assert a process was NOT
// signalled: a killed-but-unwaited child stays a zombie that answers
// kill(pid, 0) with nil, so a liveness probe passes whether or not the signal
// was sent (see the bash/zombie notes in CLAUDE.md).
//
// It also registers the teardown for the survival tests, which is not
// decoration: every one of them asserts that a helper is STILL RUNNING at the
// end, and two of the helper roles ("ignore", "clean" pre-SIGTERM) block
// forever by design. Left to itself such a helper outlives the test binary.
// spawnHelper's registerOrphanReaper is the backstop for that, but it Waits on
// the same process this goroutine is already blocked in — two concurrent Waits
// on one os.Process, where whichever loses gets ECHILD and the reap that
// actually happened is a matter of scheduling. Stopping the child here, and
// blocking until this goroutine's Wait returns, makes the teardown a single
// ordered kill-then-reap instead. Cleanups run LIFO, so this one runs before
// the backstop, which then finds nothing to do.
func waitForExit(t *testing.T, cmd *exec.Cmd) <-chan struct{} {
	t.Helper()
	exited := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			t.Errorf("helper pid %d did not exit after SIGKILL; a test child was leaked",
				cmd.Process.Pid)
		}
	})
	return exited
}
