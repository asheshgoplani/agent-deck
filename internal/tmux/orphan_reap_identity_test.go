package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The orphan sweep decides to kill a pid on the strength of facts it read out
// of /proc, and only signals it after asking a tmux server about it — a query
// that can take seconds on exactly the unhealthy host that produced the orphans
// in the first place. A pid is not a stable name for a process across that gap.
//
// These tests cover the gap end to end: that the facts a verdict rests on all
// belong to ONE incarnation of the pid (readOrphanCandidate), and that a pid
// which changes hands after those facts are taken is refused rather than
// killed (sweepOrphanCandidates). The unit-level guarantees either side of it —
// the identity re-check immediately before each signal — live in
// softkill_pid_reuse_test.go.
//
// Candidates here are real reparented processes with a real "tmux" comm, not
// synthetic structs: the collector's pre-filter, the comm classification and
// isControlClientOrphan's parentage check all read /proc for themselves, and a
// test that fed them strings would prove nothing about the code that runs in
// production.

// fakeTmuxCandidate starts the test binary in helper mode through a symlink
// named "tmux", with an argv shaped like the leaked one-shot cadence client the
// sweep exists to reap, and returns its pid.
//
// Two properties are bought by the awkward spawn:
//
//   - comm. The kernel takes comm from the basename of the path handed to
//     execve, so a symlink named "tmux" is enough to make a Go test binary
//     present to the sweep exactly as a tmux client does (the same mechanism
//     that produces the truncated "tmux-3.5a:" comm this package documents).
//   - parentage. The helper is launched from an `sh -c '… & exit 0'` wrapper
//     that exits immediately, so the kernel reparents it to init. A direct
//     child of the test binary would be preserved by isControlClientOrphan for
//     the wrong reason — its parent's exe IS the agent-deck-shaped binary — and
//     every assertion below would pass without the code under test doing
//     anything.
//
// The returned pid is not this process's child, so it cannot be reaped with
// Wait; teardown SIGKILLs it and waits for the pid to disappear, which init
// makes prompt.
func fakeTmuxCandidate(t *testing.T, role string, extraEnv ...string) int {
	t.Helper()
	// argv the sweep must recognise: an agent-deck session target
	// (SessionPrefix) and a cadence verb from reapableOneShotVerbs.
	return fakeTmuxCandidateArgv(t, role,
		[]string{"-L", "orphan-identity-test", "list-clients", "-t", SessionPrefix + "identity_probe"},
		extraEnv...)
}

// fakeTmuxCandidateArgv is fakeTmuxCandidate with the tmux argv left to the
// caller, for the tests that need a candidate the sweep must NOT act on:
// proving a scope guard holds needs a live process the guard is the only thing
// protecting, and a guard is only pinned by an argv that fails it.
//
// tmuxArgs are the arguments after argv[0]; the "-test.run" guard is inserted
// here and keeps the child out of the test framework even if the TestMain
// helper dispatch is ever reordered.
func fakeTmuxCandidateArgv(t *testing.T, role string, tmuxArgs []string, extraEnv ...string) int {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("candidate collection reads procfs; linux-only by construction")
	}

	exe, err := os.Executable()
	require.NoError(t, err)
	dir := t.TempDir()
	link := filepath.Join(dir, "tmux")
	require.NoError(t, os.Symlink(exe, link))

	ready := filepath.Join(dir, "ready")
	env := []string{
		"SOFTKILL_TEST_HELPER=" + role,
		softkillReadyEnv + "=" + ready,
	}
	env = append(env, extraEnv...)

	argv := append([]string{link, "-test.run=^$"}, tmuxArgs...)

	quoted := make([]string, 0, len(env)+len(argv))
	for _, kv := range env {
		key, value, _ := strings.Cut(kv, "=")
		quoted = append(quoted, key+"="+shQuote(value))
	}
	for _, arg := range argv {
		quoted = append(quoted, shQuote(arg))
	}
	// Background the helper and exit at once: when sh goes, the kernel
	// reparents the helper to init.
	//
	// The pid travels through a file and the helper's own output goes to a
	// log, rather than either riding sh's stdout: a backgrounded child
	// inherits the stdout pipe, so an exec.Command().Output() here would not
	// return until the helper itself exited — which for these roles is never.
	pidPath := filepath.Join(dir, "pid")
	logPath := filepath.Join(dir, "out.log")
	script := strings.Join(quoted, " ") + " > " + shQuote(logPath) + " 2>&1 & echo $! > " + shQuote(pidPath) + "; exit 0"
	wrapper := exec.Command("sh", "-c", script)
	out, err := wrapper.CombinedOutput()
	require.NoError(t, err, "spawning the reparented candidate: %s", out)
	wrapperPID := wrapper.Process.Pid

	var pidBytes []byte
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		pidBytes, err = os.ReadFile(pidPath)
		if err == nil && strings.TrimSpace(string(pidBytes)) != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		logContents, _ := os.ReadFile(logPath)
		t.Fatalf("read pid file: %v (helper log: %s)", err, logContents)
	}

	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		// init reaps it, so the pid genuinely disappears rather than
		// lingering as a zombie that still answers signal 0.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if err := syscall.Kill(pid, 0); err != nil {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Errorf("reparented helper pid %d survived SIGKILL; a test child was leaked", pid)
	})

	waitForReady(t, ready, 10*time.Second)

	// Confirm the reparent actually happened before handing the pid back. A
	// pid still parented to the wrapper shell would satisfy isControlClientOrphan
	// for the wrong reason on a slow scheduler, silently weakening every test
	// that follows.
	//
	// "Reparented" is "no longer the wrapper's child", not "ppid == 1": the
	// kernel hands an orphan to the nearest subreaper, which under a
	// `systemd --user` session (or inside a container) is not init. Waiting for
	// pid 1 specifically would spin out the deadline on such hosts and prove
	// less than this does.
	require.Eventually(t, func() bool {
		ppid, err := readParentPID(pid)
		return err == nil && ppid != wrapperPID
	}, 5*time.Second, 10*time.Millisecond,
		"pid %d never left the wrapper shell — cannot prove this is a genuine orphan", pid)

	// Pin the two properties the spawn exists to buy, so a future change to
	// either mechanism fails here rather than silently making every test below
	// vacuous.
	comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	require.NoError(t, err)
	require.True(t, isReapableTmuxClientComm(string(comm)),
		"the symlink must give the helper a tmux client's comm, got %q", strings.TrimSpace(string(comm)))
	require.Equal(t, parentageOrphaned, isControlClientOrphan(pid),
		"the helper must be a genuine parentage orphan, or the sweep would spare it for the wrong reason")

	return pid
}

// stubLiveTmuxIdentity replaces the tmux-server query with a fixed verdict.
// The real query needs a live server and is exercised against one in
// orphan_reap_live_identity_test.go; what these tests are about is what the
// gauntlet does with the answer, and what happens to the pid while it is being
// fetched.
func stubLiveTmuxIdentity(t *testing.T, fn func(budget context.Context, pid int, cmdlineFields []string) (live, ok bool)) {
	t.Helper()
	original := liveTmuxIdentityOf
	t.Cleanup(func() { liveTmuxIdentityOf = original })
	liveTmuxIdentityOf = fn
}

// TestReadOrphanCandidate_AcceptsAStableCandidate is the no-regression half:
// bracketing the classify reads with identity reads must not stop the collector
// from collecting. Without this, every "refuses to kill" test below could pass
// against a collector that simply never returns a candidate.
func TestReadOrphanCandidate_AcceptsAStableCandidate(t *testing.T) {
	pid := fakeTmuxCandidate(t, "ignore")

	c, identifiable, ok := readOrphanCandidate(context.Background(), pid)

	require.True(t, ok, "a live tmux-shaped orphan must be collected as a candidate")
	assert.True(t, identifiable)
	assert.Equal(t, pid, c.pid)
	assert.Contains(t, c.cmdline, SessionPrefix, "the candidate must carry the argv its verdict rests on")
	identity, err := readProcessIdentity(context.Background(), pid)
	require.NoError(t, err)
	assert.Equal(t, identity, c.identity, "the candidate must carry this incarnation's identity")
}

// TestReadOrphanCandidate_DropsWhenIdentityChangesBetweenReads is the reason the
// identity is read twice. If a pid changes hands while the collector is reading
// it, the comm and cmdline describe the process that died and the identity
// describes the one that took its number — a chimera that would then be judged,
// and whose identity would still match at signal time because it belongs to the
// live process. Facts from two processes must produce no candidate at all.
func TestReadOrphanCandidate_DropsWhenIdentityChangesBetweenReads(t *testing.T) {
	pid := fakeTmuxCandidate(t, "ignore")

	original := processIdentityOf
	t.Cleanup(func() { processIdentityOf = original })
	calls := 0
	processIdentityOf = func(ctx context.Context, p int) (string, error) {
		calls++
		if calls == 1 {
			return "identity-of-the-process-that-died", nil
		}
		return original(ctx, p)
	}

	_, identifiable, ok := readOrphanCandidate(context.Background(), pid)

	assert.False(t, ok, "facts spanning two incarnations must not become a candidate")
	assert.False(t, identifiable, "the drop must be reported, not silent")
	assert.GreaterOrEqual(t, calls, 2, "the collector must re-read the identity after the classify reads")
}

// TestReadOrphanCandidate_DropsWhenIdentityUnreadable pins the fail-closed rule
// at the collector: a pid whose identity cannot be read cannot be tied to any
// later signal, so it never becomes a candidate — and is counted, so a sweep
// that has gone inert on such a host says so.
func TestReadOrphanCandidate_DropsWhenIdentityUnreadable(t *testing.T) {
	pid := fakeTmuxCandidate(t, "ignore")

	original := processIdentityOf
	t.Cleanup(func() { processIdentityOf = original })
	processIdentityOf = func(context.Context, int) (string, error) {
		return "", errors.New("no /proc and no ps on this host")
	}

	_, identifiable, ok := readOrphanCandidate(context.Background(), pid)

	assert.False(t, ok)
	assert.False(t, identifiable, "an unidentifiable candidate must be reported, not silently skipped")
}

// TestSweepOrphanCandidates_KillsAnIdentifiedOrphan is the positive control for
// the whole file. The guard is only interesting if the sweep still reaps what
// it is supposed to reap: a real orphaned process with a tmux client's comm, an
// agent-deck cadence argv, no live registration on the server, and an identity
// that never changes must be killed.
func TestSweepOrphanCandidates_KillsAnIdentifiedOrphan(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "term-handled")
	pid := fakeTmuxCandidate(t, "clean", "MARKER="+marker)

	c, _, ok := readOrphanCandidate(context.Background(), pid)
	require.True(t, ok)
	stubLiveTmuxIdentity(t, func(context.Context, int, []string) (bool, bool) { return false, true })

	killed, unclassifiable, notSignalled, unknownParent, unexamined := sweepOrphanCandidates(context.Background(), []orphanCandidate{c})

	assert.Equal(t, 1, killed, "an identified orphan must still be reaped")
	assert.Equal(t, 0, unclassifiable)
	assert.Equal(t, 0, notSignalled)
	assert.Equal(t, 0, unknownParent)
	assert.Equal(t, 0, unexamined)
	assert.NoError(t, waitForFile(marker, 5*time.Second),
		"the victim must have received the SIGTERM its handler writes the marker from")
}

// TestSweepOrphanCandidates_RefusesWhenPIDChangesHandsDuringTheLiveQuery is the
// boundary this composed change exists for.
//
// The tmux-server query is the slowest step in the gauntlet — two subprocesses,
// each bounded by tmuxLiveQueryTimeout, and slowest precisely on the unhealthy
// host that produces orphans. A pid that exits during it and has its number
// reissued must not be signalled on the strength of facts belonging to the
// process that is gone.
//
// The stub makes the pid appear to change hands while the query runs, which is
// what a reissue looks like from here; forcing a real one would take a full
// pid_max wrap inside the window and prove no more. The helper ignores SIGTERM,
// so had the sweep signalled it the escalation would have SIGKILL'd it and its
// survival would be unambiguous.
//
// This is the assertion that fails if the identity is ever captured at kill
// time again instead of before classification: a late capture reads the new
// occupant's identity, matches it against itself, and kills.
func TestSweepOrphanCandidates_RefusesWhenPIDChangesHandsDuringTheLiveQuery(t *testing.T) {
	pid := fakeTmuxCandidate(t, "ignore")

	c, _, ok := readOrphanCandidate(context.Background(), pid)
	require.True(t, ok)

	original := processIdentityOf
	t.Cleanup(func() { processIdentityOf = original })
	stubLiveTmuxIdentity(t, func(context.Context, int, []string) (bool, bool) {
		// The pid changes hands mid-query: every identity read from here on
		// belongs to whoever holds the number now.
		processIdentityOf = func(context.Context, int) (string, error) { return "identity-of-the-new-occupant", nil }
		return false, true
	})

	killed, unclassifiable, notSignalled, unknownParent, unexamined := sweepOrphanCandidates(context.Background(), []orphanCandidate{c})

	assert.Equal(t, 0, killed, "a pid that changed hands during classification must not be killed")
	assert.Equal(t, 1, notSignalled, "the refusal must be counted, not silent")
	assert.Equal(t, 0, unclassifiable)
	assert.Equal(t, 0, unknownParent)
	assert.Equal(t, 0, unexamined)

	processIdentityOf = original
	time.Sleep(300 * time.Millisecond)
	assert.NoError(t, syscall.Kill(pid, 0),
		"the new occupant of the pid was signalled; it is not this sweep's to kill")
}

// TestReapStaleControlClients_RefusesAPIDRecycledDuringAnEarlierVictimsGrace
// covers the other sweep's snapshot-to-signal window, which is the widest one
// in this file: reapStaleControlClients kills sequentially and each victim can
// hold the loop for a full controlClientKillGrace, so the last pid in a
// `list-clients` snapshot is acted on N × grace after tmux reported it.
//
// That sweep has no comm check and no live-server query — the checks the orphan
// sweep leans on — so if the identity were captured at each pid's turn rather
// than up front, a pid recycled during an earlier victim's grace would be
// identified as its NEW occupant, agree with itself at signal time, and be
// killed on the strength of a `list-clients` line that described someone else.
// isControlClientOrphan does not stop that: it says "orphan" for anything not
// parented to an agent-deck binary, which a stranger process generally is not.
//
// The seam plays the recycle: the second victim's identity changes as soon as
// the first victim's kill begins. With the capture up front the change lands
// after the identity was taken, so the pre-signal check refuses; with the
// capture inline it would land BEFORE, and the sweep would kill the stranger.
func TestReapStaleControlClients_RefusesAPIDRecycledDuringAnEarlierVictimsGrace(t *testing.T) {
	firstVictim := fakeTmuxCandidate(t, "ignore")  // ignores SIGTERM: holds the loop for a full grace
	secondVictim := fakeTmuxCandidate(t, "ignore") // must survive

	realFirst, err := readProcessIdentity(context.Background(), firstVictim)
	require.NoError(t, err)
	realSecond, err := readProcessIdentity(context.Background(), secondVictim)
	require.NoError(t, err)

	original := processIdentityOf
	t.Cleanup(func() { processIdentityOf = original })

	// Keyed on which pid is being read and how far the sweep has got, not on a
	// raw call count: the second read of the FIRST victim is its pre-signal
	// check, which can only happen once the capture phase is over.
	firstVictimReads := 0
	recycled := false
	processIdentityOf = func(_ context.Context, p int) (string, error) {
		switch p {
		case firstVictim:
			firstVictimReads++
			if firstVictimReads >= 2 {
				recycled = true
			}
			return realFirst, nil
		case secondVictim:
			if recycled {
				return "identity-of-the-new-occupant", nil
			}
			return realSecond, nil
		}
		return original(context.Background(), p)
	}

	snapshot := fmt.Sprintf("1 %d\n1 %d\n", firstVictim, secondVictim)
	killCount, _ := reapStaleControlClients(context.Background(), snapshot, "(test)")

	assert.Equal(t, 1, killCount, "only the victim whose identity held may be counted as killed")
	assert.True(t, recycled, "the first victim's kill must have run, or this test proves nothing")
	// The first victim ignores SIGTERM, so it dies to the escalation's SIGKILL;
	// its subreaper needs a moment to reap it before the pid stops answering.
	require.Eventually(t, func() bool { return syscall.Kill(firstVictim, 0) != nil },
		5*time.Second, 10*time.Millisecond,
		"the first victim's identity never changed; it must have been killed")
	assert.NoError(t, syscall.Kill(secondVictim, 0),
		"a pid recycled during an earlier victim's grace must not be signalled")
}
