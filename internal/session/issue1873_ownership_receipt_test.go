package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asheshgoplani/agent-deck/internal/procowner"
)

// The two-wave proof the issue asks for, plus the fail-closed cases that a
// PID-based ownership scheme actually dies of. The process-table ones use an
// injected prober rather than real processes: "the /proc entry is unreadable"
// and "this pid now belongs to another user" cannot be produced on demand by
// spawning something and hoping.

// TestIssue1873_TwoFastDeathWavesLeaveExactlyOneOwnedTree runs the reproduction
// twice, which is the part that proves survivors do not accumulate: after each
// wave the restart is refused, reconciliation reaps exactly the owned tree, and
// the next wave starts from one live tree — never two.
func TestIssue1873_TwoFastDeathWavesLeaveExactlyOneOwnedTree(t *testing.T) {
	requireEscapedWrapperSupport(t)

	w := newEscapedWrapper(t, 2*time.Second)
	inst, wave1 := startEscapedInstance(t, w, "test-1873-two-wave")

	// --- Wave 1 -----------------------------------------------------------
	status := inst.OwnershipStatus()
	require.NoError(t, status.LoadErr)
	require.NotNil(t, status.Receipt, "the spawn must have left a durable receipt")
	firstLeader := status.Receipt.Leader.Key()
	// The survivor is a TREE: the detached child plus whatever it currently has
	// running. What matters is that the recorded child is among the owned, and
	// that they all belong to the one escaped tree.
	require.Contains(t, survivorPIDs(status), wave1.PID,
		"the escaped tree must be reported as an owned survivor: %s", status.Report.Describe())
	assert.False(t, status.PaneAttached, "the pane this tree escaped from is gone")

	err := inst.Restart()
	require.Error(t, err, "restart must be refused while the owned tree is alive")
	assert.True(t, IsOwnedProcessRecoveryRequired(err), "want a typed recovery error, got %T: %v", err, err)
	requireChildAlive(t, wave1, "a refused restart signals nothing")
	assert.Len(t, w.children(), 1, "no replacement tree may have been spawned")

	// Safe reaping: identity-checked, verified dead, receipt cleared.
	report, err := inst.ReconcileOwnership()
	require.NoError(t, err)
	require.Equal(t, procowner.VerdictClear, report.Verdict, report.Describe())
	requireChildGone(t, wave1, "reconciliation must terminate the owned tree")
	assert.Nil(t, inst.OwnershipStatus().Receipt, "a verified teardown clears the receipt")

	// --- Wave 2 -----------------------------------------------------------
	require.NoError(t, inst.Restart(), "restart must be admitted once nothing is owned")
	kids := w.waitForChildren(2, 15*time.Second)
	wave2 := kids[len(kids)-1]
	require.True(t, paneGoneWithin(inst, 20*time.Second), "the second wave dies the same way")
	requireChildAlive(t, wave2, "the second wrapped tree escaped its pane too")

	// Exactly one owned tree, not two: this is the accumulation the issue asks
	// us to disprove.
	assert.False(t, childAlive(wave1), "the first wave's tree must still be dead")
	assert.Equal(t, 1, liveWrappedTrees(w), "exactly one wrapped tree may be alive after two waves")

	status = inst.OwnershipStatus()
	require.NotNil(t, status.Receipt)
	// The replacement spawn commits a NEW receipt rather than editing the old
	// one. Its generation counts from 1 again because the previous receipt was
	// verifiably cleared (a cleared receipt is an absent file, by design), so
	// the identity of the leader — not the number — is what distinguishes it.
	assert.NotEqual(t, firstLeader, status.Receipt.Leader.Key(),
		"the new receipt must name the replacement's leader, not the reaped one")
	require.Contains(t, survivorPIDs(status), wave2.PID)
	assert.NotContains(t, survivorPIDs(status), wave1.PID,
		"the reaped generation leaves nothing behind in the new receipt")

	err = inst.Restart()
	require.Error(t, err, "the second wave's survivor blocks a restart exactly like the first")
	assert.True(t, IsOwnedProcessRecoveryRequired(err))

	report, err = inst.ReconcileOwnership()
	require.NoError(t, err)
	require.Equal(t, procowner.VerdictClear, report.Verdict, report.Describe())
	requireChildGone(t, wave2, "reconciliation must terminate the second wave's tree too")
	assert.Equal(t, 0, liveWrappedTrees(w), "no survivor from either wave is left behind")
	assert.Nil(t, inst.OwnershipStatus().Receipt)
}

// Two restarts racing the same escaped tree: both must be refused, and neither
// may spawn a replacement. The spawn lock serialises them; the gate is what
// makes the second one's answer the same as the first's.
func TestIssue1873_ConcurrentRestartsAreBothRefused(t *testing.T) {
	requireEscapedWrapperSupport(t)

	w := newEscapedWrapper(t, 800*time.Millisecond)
	inst, survivor := startEscapedInstance(t, w, "test-1873-race")

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for idx := range errs {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			errs[n] = inst.Restart()
		}(idx)
	}
	wg.Wait()

	for n, err := range errs {
		require.Error(t, err, "racing restart %d must be refused", n)
		assert.True(t, IsOwnedProcessRecoveryRequired(err), "racing restart %d: %v", n, err)
	}
	requireChildAlive(t, survivor, "neither racing restart may signal the survivor")
	assert.Equal(t, 1, liveWrappedTrees(w), "no racing restart may spawn a second tree")
}

// The guard must not fire on a healthy session: its pane process and every live
// descendant are accounted for, so a normal restart still restarts.
func TestIssue1873_HealthySessionWithLiveDescendantsStillRestarts(t *testing.T) {
	requireEscapedWrapperSupport(t)

	inst := NewInstance("test-1873-healthy", t.TempDir())
	inst.Tool = "customlive1873"
	// A pane process with a live descendant — the shape a real session has, and
	// the shape a naive "any owned process blocks a restart" guard breaks on.
	inst.Command = "/bin/sh -c 'sleep 120 & wait'"
	require.NoError(t, inst.Start())
	t.Cleanup(func() { _ = inst.Kill() })

	// Give attribution a moment to record the descendant.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st := inst.OwnershipStatus(); st.Receipt != nil && len(st.Receipt.Members) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	status := inst.OwnershipStatus()
	require.NotNil(t, status.Receipt)
	require.NotEmpty(t, status.Receipt.Members, "the live descendant must be attributed")
	assert.True(t, status.PaneAttached)
	assert.Empty(t, status.Survivors, "a live session's own processes are not survivors")

	require.NoError(t, inst.Restart(), "a healthy session must still restart")
	after := inst.OwnershipStatus()
	require.NotNil(t, after.Receipt, "the replacement pane is owned in turn")
	assert.Greater(t, after.Receipt.Generation, status.Receipt.Generation)
	assert.NotEqual(t, status.Receipt.Leader.PID, after.Receipt.Leader.PID,
		"the receipt names the replacement process, not the one respawn killed")
}

// A wrapper whose child detaches and outlives its whole ancestry before any
// attribution pass can see it is, by construction, not attributable to a
// PID+start-time receipt. The contract's answer must be "not owned" — never a
// guess — and nothing may be signalled on its behalf.
//
// This is the failure shape the fixtures would otherwise never build: the
// boundary of the ruled contract, asserted rather than assumed.
func TestIssue1873_ChildThatDetachesBeforeAttributionIsNeverClaimed(t *testing.T) {
	requireEscapedWrapperSupport(t)

	w := newEscapedWrapper(t, 0) // exits the instant its child has published
	inst, child := startEscapedInstance(t, w, "test-1873-unattributable")

	status := inst.OwnershipStatus()
	if status.Receipt != nil && status.Receipt.HasMember(procowner.Member{PID: child.PID, StartID: child.Start}) {
		// Attribution won the race: then the tree IS owned and must be reported.
		assert.Contains(t, survivorPIDs(status), child.PID)
		return
	}
	// Attribution lost the race. The honest outcome: the process is not claimed,
	// so it is not signalled and not reported as ours.
	for _, m := range status.Survivors {
		assert.NotEqual(t, child.PID, m.PID)
	}
	report, err := inst.ReconcileOwnership()
	require.NoError(t, err)
	for _, outcome := range report.Outcomes {
		assert.NotEqual(t, child.PID, outcome.Member.PID,
			"an unattributed process must never appear in a reap report")
	}
	assert.True(t, childAlive(child),
		"an unattributed survivor is left strictly alone rather than killed on a guess")
}

// --- Fail-closed shapes, driven through an injected process table ----------

func TestIssue1873_GateFailsClosedOnEveryUnverifiableShape(t *testing.T) {
	leader := procowner.Member{PID: 4242, StartID: "5000", UID: os.Getuid(), Role: procowner.RoleLeader}

	cases := []struct {
		name       string
		arrange    func(p *sessionFakeProber)
		wantRefuse bool
		wantSignal bool
		reason     string
	}{
		{
			name:       "owned survivor is alive",
			arrange:    func(p *sessionFakeProber) { p.alive(leader.PID, leader.StartID, os.Getuid()) },
			wantRefuse: true,
			wantSignal: true, // reconciliation may terminate what it can prove is ours
			reason:     "alive",
		},
		{
			name:       "proc entry unreadable",
			arrange:    func(p *sessionFakeProber) { p.fail(leader.PID, procowner.ErrUnreadable) },
			wantRefuse: true,
			wantSignal: false,
			reason:     "could not be verified",
		},
		{
			name: "pid now belongs to another user",
			arrange: func(p *sessionFakeProber) {
				p.alive(leader.PID, leader.StartID, os.Getuid()+1)
			},
			wantRefuse: true,
			wantSignal: false,
			reason:     "uid",
		},
		{
			name:       "boot id unreadable",
			arrange:    func(p *sessionFakeProber) { p.bootErr = fmt.Errorf("%w: boot", procowner.ErrUnreadable) },
			wantRefuse: true,
			wantSignal: false,
			reason:     "boot id",
		},
		{
			name: "pid recycled by an unrelated process",
			arrange: func(p *sessionFakeProber) {
				p.alive(leader.PID, "9999", os.Getuid()) // different start identity
			},
			wantRefuse: false, // the recorded process is provably dead
			wantSignal: false, // ...and the new occupant is never touched
			reason:     "",
		},
		{
			name:       "process is gone",
			arrange:    func(p *sessionFakeProber) {},
			wantRefuse: false,
			wantSignal: false,
			reason:     "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prober := newSessionFakeProber()
			tc.arrange(prober)
			signaler := &countingSignaler{}
			restore := swapOwnershipProbe(t, prober, signaler)
			defer restore()

			inst := NewInstance("test-1873-"+sanitizeTestID(tc.name), t.TempDir())
			writeTestReceipt(t, inst.ID, leader)

			err := inst.guardOwnedProcessesBeforeSpawn("restart")
			if tc.wantRefuse {
				require.Error(t, err, "this shape must fail closed")
				require.True(t, IsOwnedProcessRecoveryRequired(err), "want a typed recovery error: %v", err)
				assert.Contains(t, err.Error(), tc.reason)
			} else {
				require.NoError(t, err, "a provably dead receipt must not block a restart")
			}
			assert.Empty(t, signaler.calls, "the admission gate never signals anything")

			// Reconciliation is the only path allowed to signal, and only for
			// identities it can still prove are ours.
			_, reconcileErr := inst.ReconcileOwnership()
			require.NoError(t, reconcileErr)
			if tc.wantSignal {
				assert.NotEmpty(t, signaler.calls, "an identity-matched owned process must be reaped")
				for _, c := range signaler.calls {
					assert.Equal(t, leader.PID, c.pid, "only the recorded identity may be signalled")
				}
			} else {
				assert.Empty(t, signaler.calls, "nothing unverified may ever be signalled")
			}
		})
	}
}

// A corrupt or truncated receipt is not the same as no receipt: it means we
// recorded ownership and can no longer read it.
func TestIssue1873_CorruptReceiptFailsClosed(t *testing.T) {
	prober := newSessionFakeProber()
	signaler := &countingSignaler{}
	restore := swapOwnershipProbe(t, prober, signaler)
	defer restore()

	inst := NewInstance("test-1873-corrupt", t.TempDir())
	store := ownershipStore()
	path, err := store.Path(inst.ID)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(`{"version":1,"instance_i`), 0o600))
	t.Cleanup(func() { _ = os.Remove(path) })

	err = inst.guardOwnedProcessesBeforeSpawn("restart")
	require.Error(t, err)
	assert.True(t, IsOwnedProcessRecoveryRequired(err))
	assert.Contains(t, err.Error(), "could not be read")
	assert.Empty(t, signaler.calls)

	// The explicit operator escape hatch: discard the unreadable claim without
	// signalling anything, and only then admit a replacement.
	require.NoError(t, inst.AbandonOwnership())
	assert.Empty(t, signaler.calls, "abandoning a receipt never kills anything")
	require.NoError(t, inst.guardOwnedProcessesBeforeSpawn("restart"))
}

// A receipt written before a reboot names pids that cannot exist. Without this,
// every session that did not shut down cleanly would be blocked forever after a
// restart of the machine.
func TestIssue1873_ReceiptFromAPreviousBootIsCleared(t *testing.T) {
	prober := newSessionFakeProber()
	// The pid is live and its start identity matches — but the boot differs.
	prober.alive(4242, "5000", os.Getuid())
	prober.boot = "boot-after-reboot"
	signaler := &countingSignaler{}
	restore := swapOwnershipProbe(t, prober, signaler)
	defer restore()

	inst := NewInstance("test-1873-reboot", t.TempDir())
	writeTestReceipt(t, inst.ID, procowner.Member{PID: 4242, StartID: "5000", UID: os.Getuid(), Role: procowner.RoleLeader})

	require.NoError(t, inst.guardOwnedProcessesBeforeSpawn("restart"))
	assert.Empty(t, signaler.calls, "a live pid that merely matches a pre-reboot receipt is a stranger")
	assert.Nil(t, inst.OwnershipStatus().Receipt, "the stale receipt is retired")
}

// --- helpers ---------------------------------------------------------------

func survivorPIDs(status OwnershipStatus) []int {
	pids := make([]int, 0, len(status.Survivors))
	for _, m := range status.Survivors {
		pids = append(pids, m.PID)
	}
	return pids
}

func liveWrappedTrees(w *escapedWrapper) int {
	n := 0
	for _, c := range w.children() {
		if childAlive(c) {
			n++
		}
	}
	return n
}

func sanitizeTestID(name string) string {
	out := make([]rune, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out = append(out, r)
		default:
			out = append(out, '-')
		}
	}
	return string(out)
}

func writeTestReceipt(t *testing.T, instanceID string, leader procowner.Member) {
	t.Helper()
	store := ownershipStore()
	receipt := &procowner.Receipt{
		Version:    procowner.ReceiptVersion,
		InstanceID: instanceID,
		Generation: 1,
		State:      procowner.StateLive,
		Provider:   "session_fake",
		BootID:     "boot-1",
		CreatedAt:  time.Now().Unix(),
		Leader:     leader,
	}
	require.NoError(t, store.Save(receipt))
	t.Cleanup(func() { _ = store.ForceClear(instanceID) })
}

func swapOwnershipProbe(t *testing.T, p procowner.Prober, s procowner.Signaler) func() {
	t.Helper()
	prevProber, prevSignaler := ownershipProber, ownershipSignaler
	ownershipProber, ownershipSignaler = p, s
	return func() { ownershipProber, ownershipSignaler = prevProber, prevSignaler }
}

// sessionFakeProber is a scriptable process table for the session-level gate.
type sessionFakeProber struct {
	boot    string
	bootErr error
	procs   map[int]procowner.ProcInfo
	errs    map[int]error
}

func newSessionFakeProber() *sessionFakeProber {
	return &sessionFakeProber{boot: "boot-1", procs: map[int]procowner.ProcInfo{}, errs: map[int]error{}}
}

func (p *sessionFakeProber) alive(pid int, start string, uid int) {
	p.procs[pid] = procowner.ProcInfo{PID: pid, PPID: 1, PGID: pid, UID: uid, StartID: start}
}

func (p *sessionFakeProber) fail(pid int, err error) {
	p.errs[pid] = fmt.Errorf("%w: pid %d", err, pid)
}

func (p *sessionFakeProber) Name() string { return "session_fake" }

func (p *sessionFakeProber) BootID() (string, error) {
	if p.bootErr != nil {
		return "", p.bootErr
	}
	return p.boot, nil
}

func (p *sessionFakeProber) Inspect(pid int) (procowner.ProcInfo, error) {
	if err, ok := p.errs[pid]; ok {
		return procowner.ProcInfo{}, err
	}
	info, ok := p.procs[pid]
	if !ok {
		return procowner.ProcInfo{}, fmt.Errorf("%w: pid %d", procowner.ErrNoProcess, pid)
	}
	return info, nil
}

func (p *sessionFakeProber) Descendants(root procowner.ProcInfo) ([]procowner.ProcInfo, error) {
	return nil, nil
}

type signalRecord struct {
	pid int
	sig syscall.Signal
}

// countingSignaler records signals and delivers none of them.
type countingSignaler struct {
	mu    sync.Mutex
	calls []signalRecord
}

func (c *countingSignaler) Signal(pid int, sig syscall.Signal) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, signalRecord{pid: pid, sig: sig})
	return nil
}
