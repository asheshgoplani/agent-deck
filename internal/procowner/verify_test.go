package procowner

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The failure shapes below are the ones a PID-based ownership scheme actually
// dies of. Each has its own test because each has its own correct answer, and
// "fails closed" is not one single behaviour: a reused pid means the recorded
// process is DEAD (never signal the new holder, do not block a replacement),
// while an unreadable entry means we do not know (never signal, and do block).

func TestVerify_OwnedProcessIsOwned(t *testing.T) {
	p := newFakeProber()
	leader := p.add(100, 1, "5000", 1000)
	report := Verify(p, liveReceipt("inst", memberOf(leader, RoleLeader)))

	require.Equal(t, VerdictOwned, report.Verdict)
	require.Len(t, report.Owned(), 1)
	assert.Equal(t, 100, report.Owned()[0].PID)
}

func TestVerify_MissingProcessIsProvenGone(t *testing.T) {
	p := newFakeProber()
	leader := p.add(100, 1, "5000", 1000)
	receipt := liveReceipt("inst", memberOf(leader, RoleLeader))
	p.remove(100)

	report := Verify(p, receipt)
	require.Equal(t, VerdictClear, report.Verdict)
	assert.Equal(t, StateGone, report.Members[0].State)
}

// A recycled pid: the pid exists, the start identity does not match. The
// recorded process is dead — pids are only reused after their holder exits —
// so the receipt clears, and the impostor must never be signalled.
func TestVerify_RecycledPIDIsAStrangerAndDoesNotBlock(t *testing.T) {
	p := newFakeProber()
	leader := p.add(100, 1, "5000", 1000)
	receipt := liveReceipt("inst", memberOf(leader, RoleLeader))
	// Same pid, different start identity: an unrelated process moved in.
	p.remove(100)
	p.add(100, 1, "9999", 1000)

	report := Verify(p, receipt)
	require.Equal(t, VerdictClear, report.Verdict)
	require.Equal(t, StateStranger, report.Members[0].State)
	assert.Empty(t, report.Owned(), "a stranger is never reported as ours")
	assert.Contains(t, report.Reason, "reused")
}

func TestVerify_UnreadableIdentityIsUnknownAndBlocks(t *testing.T) {
	p := newFakeProber()
	leader := p.add(100, 1, "5000", 1000)
	receipt := liveReceipt("inst", memberOf(leader, RoleLeader))
	p.setErr(100, fmt.Errorf("%w: /proc/100/stat", ErrUnreadable))

	report := Verify(p, receipt)
	require.Equal(t, VerdictUnknown, report.Verdict)
	assert.Equal(t, StateUnknown, report.Members[0].State)
	assert.Empty(t, report.Owned())
}

// A pid whose identity matches but which now runs as another user: we may not
// signal across that boundary, and we cannot claim it either.
func TestVerify_PIDNowOwnedByAnotherUserIsUnknown(t *testing.T) {
	p := newFakeProber()
	leader := p.add(100, 1, "5000", 1000)
	receipt := liveReceipt("inst", memberOf(leader, RoleLeader))
	p.remove(100)
	p.add(100, 1, "5000", 4242) // same identity, different uid

	report := Verify(p, receipt)
	require.Equal(t, VerdictUnknown, report.Verdict)
	require.Equal(t, StateUnknown, report.Members[0].State)
	assert.Contains(t, report.Members[0].Detail, "uid 4242")
}

func TestVerify_BootChangeClearsEveryIdentity(t *testing.T) {
	p := newFakeProber()
	leader := p.add(100, 1, "5000", 1000)
	receipt := liveReceipt("inst", memberOf(leader, RoleLeader))
	// The recorded pid is still live and identical — but from a different boot,
	// where start ticks mean something else entirely.
	p.boot = "boot-2"

	report := Verify(p, receipt)
	require.Equal(t, VerdictClear, report.Verdict)
	assert.True(t, report.BootChanged)
	assert.Empty(t, report.Owned(), "nothing from a previous boot may be signalled")
}

func TestVerify_UnreadableBootIDIsUnknown(t *testing.T) {
	p := newFakeProber()
	leader := p.add(100, 1, "5000", 1000)
	p.bootErr = errors.New("boom")

	report := Verify(p, liveReceipt("inst", memberOf(leader, RoleLeader)))
	assert.Equal(t, VerdictUnknown, report.Verdict)
}

func TestVerify_ProviderMismatchIsUnknown(t *testing.T) {
	p := newFakeProber()
	leader := p.add(100, 1, "5000", 1000)
	receipt := liveReceipt("inst", memberOf(leader, RoleLeader))
	receipt.Provider = "some_other_provider"

	report := Verify(p, receipt)
	require.Equal(t, VerdictUnknown, report.Verdict)
	assert.Contains(t, report.Reason, "provider")
}

func TestVerify_CorruptReceiptIsUnknown(t *testing.T) {
	p := newFakeProber()
	report := Verify(p, &Receipt{Version: ReceiptVersion, InstanceID: "inst"})
	assert.Equal(t, VerdictUnknown, report.Verdict)
}

func TestVerify_MixedMembersFailClosedOnTheUnknownOne(t *testing.T) {
	p := newFakeProber()
	leader := p.add(100, 1, "5000", 1000)
	child := p.add(101, 100, "5100", 1000)
	receipt := liveReceipt("inst", memberOf(leader, RoleLeader), memberOf(child, RoleDescendant))
	p.setErr(101, fmt.Errorf("%w: /proc/101/stat", ErrUnreadable))

	report := Verify(p, receipt)
	assert.Equal(t, VerdictUnknown, report.Verdict,
		"one unverifiable member is enough to make the whole receipt unverifiable")
}

func TestReap_TerminatesOnlyIdentityMatchedProcesses(t *testing.T) {
	p := newFakeProber()
	leader := p.add(100, 1, "5000", 1000)
	child := p.add(101, 100, "5100", 1000)
	receipt := liveReceipt("inst", memberOf(leader, RoleLeader), memberOf(child, RoleDescendant))

	sig := newRecordingSignaler()
	sig.onSend = func(pid int, _ syscall.Signal) { p.remove(pid) }

	report := Reap(p, sig, receipt, fastReapOptions())
	require.Equal(t, VerdictClear, report.Verdict, report.Describe())
	assert.Equal(t, 2, report.Signalled())
	calls := sig.calls()
	require.Len(t, calls, 2)
	// Descendants before the leader, so the tree is not re-parented mid-reap.
	assert.Equal(t, 101, calls[0].PID)
	assert.Equal(t, 100, calls[1].PID)
	for _, c := range calls {
		assert.Equal(t, syscall.SIGTERM, c.Sig, "a cooperative process is never SIGKILLed")
	}
}

// The headline safety property: a receipt whose pid now belongs to somebody
// else must not produce a single signal.
func TestReap_NeverSignalsARecycledPID(t *testing.T) {
	p := newFakeProber()
	leader := p.add(100, 1, "5000", 1000)
	receipt := liveReceipt("inst", memberOf(leader, RoleLeader))
	p.remove(100)
	p.add(100, 1, "7777", 1000) // stranger moves in

	sig := newRecordingSignaler()
	report := Reap(p, sig, receipt, fastReapOptions())

	assert.Empty(t, sig.calls(), "the stranger must not be signalled")
	assert.Equal(t, VerdictClear, report.Verdict)
	assert.Equal(t, OutcomeAlreadyGone, report.Outcomes[0].Outcome)
}

func TestReap_NeverSignalsAnUnreadableIdentity(t *testing.T) {
	p := newFakeProber()
	leader := p.add(100, 1, "5000", 1000)
	receipt := liveReceipt("inst", memberOf(leader, RoleLeader))
	p.setErr(100, fmt.Errorf("%w: /proc/100/stat", ErrUnreadable))

	sig := newRecordingSignaler()
	report := Reap(p, sig, receipt, fastReapOptions())

	assert.Empty(t, sig.calls())
	assert.Equal(t, VerdictUnknown, report.Verdict)
	assert.Equal(t, OutcomeNotSignalled, report.Outcomes[0].Outcome)
}

func TestReap_NeverSignalsAcrossAUserBoundary(t *testing.T) {
	p := newFakeProber()
	leader := p.add(100, 1, "5000", 1000)
	receipt := liveReceipt("inst", memberOf(leader, RoleLeader))
	p.remove(100)
	p.add(100, 1, "5000", 999)

	sig := newRecordingSignaler()
	report := Reap(p, sig, receipt, fastReapOptions())

	assert.Empty(t, sig.calls())
	assert.Equal(t, VerdictUnknown, report.Verdict)
}

// The process exits between the receipt read and the signal: the kernel reports
// ESRCH and that is a clean reap, not a failure.
func TestReap_ProcessExitingBeforeTheSignalIsReaped(t *testing.T) {
	p := newFakeProber()
	leader := p.add(100, 1, "5000", 1000)
	receipt := liveReceipt("inst", memberOf(leader, RoleLeader))

	sig := newRecordingSignaler()
	sig.err[100] = syscall.ESRCH
	sig.onSend = func(pid int, _ syscall.Signal) { p.remove(pid) }

	report := Reap(p, sig, receipt, fastReapOptions())
	assert.Equal(t, VerdictClear, report.Verdict)
	assert.Equal(t, OutcomeReaped, report.Outcomes[0].Outcome)
	assert.Equal(t, 1, sig.sentTo(100))
}

// The pid is recycled during the SIGTERM grace period. The escalation must
// notice on its re-check and stop — SIGKILL to that pid would hit a stranger.
func TestReap_PIDRecycledDuringGraceStopsTheEscalation(t *testing.T) {
	p := newFakeProber()
	leader := p.add(100, 1, "5000", 1000)
	receipt := liveReceipt("inst", memberOf(leader, RoleLeader))

	sig := newRecordingSignaler()
	sig.onSend = func(pid int, _ syscall.Signal) {
		p.remove(pid)
		p.add(pid, 1, "8888", 1000) // an unrelated process takes the pid
	}

	report := Reap(p, sig, receipt, fastReapOptions())
	assert.Equal(t, 1, sig.sentTo(100), "SIGKILL must not follow the pid into a stranger's hands")
	assert.Equal(t, OutcomeReaped, report.Outcomes[0].Outcome)
	assert.Equal(t, VerdictClear, report.Verdict)
}

func TestReap_EscalatesToKillAndVerifies(t *testing.T) {
	p := newFakeProber()
	leader := p.add(100, 1, "5000", 1000)
	receipt := liveReceipt("inst", memberOf(leader, RoleLeader))

	sig := newRecordingSignaler()
	sig.onSend = func(pid int, s syscall.Signal) {
		if s == syscall.SIGKILL {
			p.remove(pid) // ignores SIGTERM, dies on SIGKILL
		}
	}

	report := Reap(p, sig, receipt, fastReapOptions())
	require.Equal(t, VerdictClear, report.Verdict, report.Describe())
	calls := sig.calls()
	require.Len(t, calls, 2)
	assert.Equal(t, syscall.SIGTERM, calls[0].Sig)
	assert.Equal(t, syscall.SIGKILL, calls[1].Sig)
}

func TestReap_UnkillableProcessKeepsTheReceiptOwned(t *testing.T) {
	p := newFakeProber()
	leader := p.add(100, 1, "5000", 1000)
	receipt := liveReceipt("inst", memberOf(leader, RoleLeader))

	sig := newRecordingSignaler() // signals land, nothing dies
	report := Reap(p, sig, receipt, fastReapOptions())

	assert.Equal(t, VerdictOwned, report.Verdict)
	assert.Equal(t, OutcomeStillAlive, report.Outcomes[0].Outcome)
}

func TestReap_EPERMIsReportedNotEscalated(t *testing.T) {
	p := newFakeProber()
	leader := p.add(100, 1, "5000", 1000)
	receipt := liveReceipt("inst", memberOf(leader, RoleLeader))

	sig := newRecordingSignaler()
	sig.err[100] = syscall.EPERM

	report := Reap(p, sig, receipt, fastReapOptions())
	assert.Equal(t, 1, sig.sentTo(100), "a refused signal is not retried harder")
	assert.Equal(t, OutcomeNotSignalled, report.Outcomes[0].Outcome)
	assert.Equal(t, VerdictUnknown, report.Verdict)
}

func TestReap_RefusesReceiptFromAnotherBoot(t *testing.T) {
	p := newFakeProber()
	leader := p.add(100, 1, "5000", 1000)
	receipt := liveReceipt("inst", memberOf(leader, RoleLeader))
	p.boot = "boot-2"

	sig := newRecordingSignaler()
	report := Reap(p, sig, receipt, fastReapOptions())
	assert.Empty(t, sig.calls(), "a live pid that merely matches a pre-reboot receipt is a stranger")
	assert.Equal(t, VerdictClear, report.Verdict)
}

func fastReapOptions() ReapOptions {
	return ReapOptions{
		TermGrace: 20 * time.Millisecond,
		KillGrace: 20 * time.Millisecond,
		Poll:      time.Millisecond,
		Sleep:     func(time.Duration) {},
	}
}

// Reaping is batched by phase, not serialised per process: every owned member
// gets SIGTERM before any gets SIGKILL. Serialising would multiply the grace
// period by the size of the tree on a path that runs at every session stop, and
// would give a parent no chance to shut its children down itself.
func TestReap_SignalsTheWholeSetPerPhase(t *testing.T) {
	p := newFakeProber()
	leader := p.add(100, 1, "5000", 1000)
	childA := p.add(101, 100, "5100", 1000)
	childB := p.add(102, 100, "5200", 1000)
	receipt := liveReceipt("inst", memberOf(leader, RoleLeader),
		memberOf(childA, RoleDescendant), memberOf(childB, RoleDescendant))

	sig := newRecordingSignaler() // nothing dies: forces both phases
	report := Reap(p, sig, receipt, fastReapOptions())

	calls := sig.calls()
	require.Len(t, calls, 6, "three members, two phases")
	for i := 0; i < 3; i++ {
		assert.Equal(t, syscall.SIGTERM, calls[i].Sig, "call %d must still be in the SIGTERM phase", i)
	}
	for i := 3; i < 6; i++ {
		assert.Equal(t, syscall.SIGKILL, calls[i].Sig, "call %d must be in the SIGKILL phase", i)
	}
	// Descendants before the leader, within each phase.
	assert.Equal(t, 100, calls[2].PID)
	assert.Equal(t, 100, calls[5].PID)
	assert.Equal(t, VerdictOwned, report.Verdict)
}

// A receipt naming pid 1 or agent-deck itself is never actioned, whatever else
// it says. This is the guard that makes a corrupted-but-parseable receipt
// harmless rather than catastrophic.
func TestReap_NeverSignalsInitOrItself(t *testing.T) {
	p := newFakeProber()
	self := os.Getpid()
	p.add(self, 1, "5000", os.Getuid())
	receipt := liveReceipt("inst",
		Member{PID: self, StartID: "5000", UID: os.Getuid(), Role: RoleLeader},
		Member{PID: 1, StartID: "1", UID: 0, Role: RoleDescendant})

	sig := newRecordingSignaler()
	report := Reap(p, sig, receipt, fastReapOptions())

	assert.Empty(t, sig.calls(), "neither init nor agent-deck itself may be signalled")
	assert.Equal(t, VerdictUnknown, report.Verdict)
}
