package procowner

import (
	"fmt"
	"strconv"
	"sync"
	"syscall"
)

// fakeProber is a scriptable process table. Every failure shape this package
// has to fail closed on — a reused pid, an unreadable entry, a uid change, a
// process that exits between the check and the signal — is a two-line setup
// here, which is the point: none of them can be built reliably by spawning real
// processes and hoping.
type fakeProber struct {
	mu       sync.Mutex
	name     string
	boot     string
	bootErr  error
	procs    map[int]ProcInfo
	errs     map[int]error
	children map[int][]int
	descErr  error
	// onInspect fires before each Inspect, so a test can mutate the table
	// mid-verification (pid reuse racing a signal).
	onInspect func(pid int)
	inspects  int
}

func newFakeProber() *fakeProber {
	return &fakeProber{
		name:     ProviderLinuxProc,
		boot:     "boot-1",
		procs:    map[int]ProcInfo{},
		errs:     map[int]error{},
		children: map[int][]int{},
	}
}

func (f *fakeProber) Name() string { return f.name }

func (f *fakeProber) BootID() (string, error) {
	if f.bootErr != nil {
		return "", f.bootErr
	}
	return f.boot, nil
}

func (f *fakeProber) add(pid, ppid int, start string, uid int) ProcInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	info := ProcInfo{PID: pid, PPID: ppid, PGID: pid, UID: uid, StartID: start, Comm: "fake"}
	f.procs[pid] = info
	if ppid > 0 {
		f.children[ppid] = append(f.children[ppid], pid)
	}
	return info
}

func (f *fakeProber) remove(pid int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.procs, pid)
}

func (f *fakeProber) setErr(pid int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[pid] = err
}

func (f *fakeProber) Inspect(pid int) (ProcInfo, error) {
	if f.onInspect != nil {
		f.onInspect(pid)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inspects++
	if err, ok := f.errs[pid]; ok {
		return ProcInfo{}, err
	}
	info, ok := f.procs[pid]
	if !ok {
		return ProcInfo{}, fmt.Errorf("%w: pid %d", ErrNoProcess, pid)
	}
	return info, nil
}

func (f *fakeProber) Descendants(root ProcInfo) ([]ProcInfo, error) {
	if f.descErr != nil {
		return nil, f.descErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ProcInfo
	queue := []int{root.PID}
	seen := map[int]bool{root.PID: true}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, child := range f.children[parent] {
			if seen[child] {
				continue
			}
			seen[child] = true
			if info, ok := f.procs[child]; ok {
				out = append(out, info)
				queue = append(queue, child)
			}
		}
	}
	return out, nil
}

// CompareStart orders the numeric start identities the fake hands out.
func (f *fakeProber) CompareStart(a, b string) (int, error) {
	av, err := strconv.ParseUint(a, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q", ErrUnreadable, a)
	}
	bv, err := strconv.ParseUint(b, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q", ErrUnreadable, b)
	}
	switch {
	case av < bv:
		return -1, nil
	case av > bv:
		return 1, nil
	default:
		return 0, nil
	}
}

// recordingSignaler records every signal it is asked to deliver and, by
// default, kills the target in the fake table. Tests assert on `sent` — what
// was signalled matters more than what died.
type recordingSignaler struct {
	mu     sync.Mutex
	sent   []signalCall
	err    map[int]error
	onSend func(pid int, sig syscall.Signal)
}

type signalCall struct {
	PID int
	Sig syscall.Signal
}

func newRecordingSignaler() *recordingSignaler {
	return &recordingSignaler{err: map[int]error{}}
}

func (r *recordingSignaler) Signal(pid int, sig syscall.Signal) error {
	r.mu.Lock()
	r.sent = append(r.sent, signalCall{PID: pid, Sig: sig})
	err := r.err[pid]
	r.mu.Unlock()
	if r.onSend != nil {
		r.onSend(pid, sig)
	}
	return err
}

func (r *recordingSignaler) calls() []signalCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]signalCall, len(r.sent))
	copy(out, r.sent)
	return out
}

func (r *recordingSignaler) sentTo(pid int) int {
	n := 0
	for _, c := range r.calls() {
		if c.PID == pid {
			n++
		}
	}
	return n
}

// liveReceipt builds a valid receipt around a leader and optional members.
func liveReceipt(instanceID string, leader Member, members ...Member) *Receipt {
	return &Receipt{
		Version:    ReceiptVersion,
		InstanceID: instanceID,
		Generation: 1,
		State:      StateLive,
		Provider:   ProviderLinuxProc,
		BootID:     "boot-1",
		CreatedAt:  1,
		Leader:     leader,
		Members:    members,
	}
}

func memberOf(info ProcInfo, role string) Member {
	return Member{
		PID:     info.PID,
		StartID: info.StartID,
		PGID:    info.PGID,
		UID:     info.UID,
		Comm:    info.Comm,
		Role:    role,
	}
}
