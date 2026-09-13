package procowner

import (
	"errors"
	"sync"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pinningSignaler is a Signaler that can also hand out process handles. It
// records the order of pins, inspections and signals so a test can assert the
// one property that matters: a signal only ever travels through a handle that
// was bound BEFORE the identity check passed.
type pinningSignaler struct {
	recordingSignaler
	mu      sync.Mutex
	pinErr  error
	pinned  []int
	viaPin  []signalCall
	closed  []int
	prober  *fakeProber
	pinFail map[int]error
}

func newPinningSignaler(p *fakeProber) *pinningSignaler {
	return &pinningSignaler{
		recordingSignaler: recordingSignaler{err: map[int]error{}},
		prober:            p,
		pinFail:           map[int]error{},
	}
}

type fakePin struct {
	s   *pinningSignaler
	pid int
}

func (f *fakePin) Signal(sig syscall.Signal) error {
	f.s.mu.Lock()
	f.s.viaPin = append(f.s.viaPin, signalCall{PID: f.pid, Sig: sig})
	f.s.mu.Unlock()
	// Deliver: the fake table drops the process like the recording signaler
	// does, so the reap can verify death.
	if f.s.recordingSignaler.onSend != nil {
		f.s.recordingSignaler.onSend(f.pid, sig)
	}
	return nil
}

func (f *fakePin) Close() error {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	f.s.closed = append(f.s.closed, f.pid)
	return nil
}

func (s *pinningSignaler) Pin(pid int) (PinnedProcess, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pinErr != nil {
		return nil, s.pinErr
	}
	if err, ok := s.pinFail[pid]; ok {
		return nil, err
	}
	s.pinned = append(s.pinned, pid)
	return &fakePin{s: s, pid: pid}, nil
}

func TestReap_SignalsOnlyThroughAHandlePinnedBeforeVerification(t *testing.T) {
	p := newFakeProber()
	leader := p.add(4242, 1, "5000", 1000)
	child := p.add(4243, 4242, "5001", 1000)
	sig := newPinningSignaler(p)
	sig.onSend = func(pid int, _ syscall.Signal) { p.remove(pid) }

	// Every identity check of a member must find that member already pinned:
	// otherwise the check vouches for a pid, not for the process the handle
	// will signal.
	var unpinnedInspects []int
	p.mu.Lock()
	p.onInspect = func(pid int) {
		sig.mu.Lock()
		defer sig.mu.Unlock()
		for _, pinned := range sig.pinned {
			if pinned == pid {
				return
			}
		}
		unpinnedInspects = append(unpinnedInspects, pid)
	}
	p.mu.Unlock()

	report := Reap(p, sig, liveReceipt("i", memberOf(leader, RoleLeader), memberOf(child, RoleDescendant)), fastReapOptions())

	require.Equal(t, VerdictClear, report.Verdict, report.Describe())
	assert.Empty(t, sig.calls(), "with a pinning signaler no signal may be sent by raw pid")
	assert.ElementsMatch(t, []int{4242, 4243}, sig.pinned, "every member is pinned")
	require.Len(t, sig.viaPin, 2, "each member is signalled once, through its handle")
	assert.Empty(t, unpinnedInspects, "a member was identity-checked before it was pinned")
	assert.ElementsMatch(t, []int{4242, 4243}, sig.closed, "every handle is released")
}

func TestReap_PinnedStrangerIsNeverSignalled(t *testing.T) {
	p := newFakeProber()
	leader := p.add(4242, 1, "5000", 1000)
	sig := newPinningSignaler(p)
	// The pid changes hands between the pin and the verification. The pin now
	// refers to the stranger; verification sees the stranger; nothing is sent.
	p.mu.Lock()
	p.onInspect = func(pid int) {
		if pid == 4242 {
			p.mu.Lock()
			p.procs[4242] = ProcInfo{PID: 4242, PPID: 1, PGID: 4242, UID: 1000, StartID: "9999"}
			p.mu.Unlock()
		}
	}
	p.mu.Unlock()

	report := Reap(p, sig, liveReceipt("i", memberOf(leader, RoleLeader)), fastReapOptions())
	assert.Empty(t, sig.viaPin, "a handle to a pid that verified as a stranger is never signalled")
	assert.Empty(t, sig.calls())
	require.Len(t, report.Outcomes, 1)
	assert.Equal(t, OutcomeAlreadyGone, report.Outcomes[0].Outcome)
}

func TestReap_PinFailureFallsBackToVerifiedRawSignal(t *testing.T) {
	p := newFakeProber()
	leader := p.add(4242, 1, "5000", 1000)
	sig := newPinningSignaler(p)
	sig.pinErr = errors.New("pidfd_open: ENOSYS")
	sig.onSend = func(pid int, _ syscall.Signal) { p.remove(pid) }

	report := Reap(p, sig, liveReceipt("i", memberOf(leader, RoleLeader)), fastReapOptions())
	require.Equal(t, VerdictClear, report.Verdict, report.Describe())
	assert.Empty(t, sig.viaPin)
	assert.Equal(t, 1, sig.sentTo(4242), "without a handle the verified raw signal is still delivered")
}

func TestReap_PinESRCHMeansAlreadyGone(t *testing.T) {
	p := newFakeProber()
	leader := p.add(4242, 1, "5000", 1000)
	sig := newPinningSignaler(p)
	sig.pinFail[4242] = syscall.ESRCH
	p.remove(4242)

	report := Reap(p, sig, liveReceipt("i", memberOf(leader, RoleLeader)), fastReapOptions())
	require.Equal(t, VerdictClear, report.Verdict, report.Describe())
	assert.Empty(t, sig.calls())
}
