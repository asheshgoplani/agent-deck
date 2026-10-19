package session

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/procowner"
)

// gatedProber holds the next Descendants call once armed, so a test can catch
// the ownership attribution pass inside its receipt write (store.Update runs
// the attribution, Descendants included, under the receipt lock).
type gatedProber struct {
	procowner.Prober
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (p *gatedProber) Descendants(root procowner.ProcInfo) ([]procowner.ProcInfo, error) {
	if p.armed.CompareAndSwap(true, false) {
		close(p.entered)
		select {
		case <-p.release:
		case <-time.After(shellTestJoinDeadline):
		}
	}
	return p.Prober.Descendants(root)
}

// TestStopShellTestInstance_JoinsOwnershipAttribution reproduces the
// "TempDir RemoveAll cleanup: .../runtime/ownership: directory not empty"
// flake of TestEnsureClaudeConfigDirEnv_SetsHostSideEnv and its siblings
// deterministically: Start leaves an ownership attribution goroutine writing
// the receipt under the isolated HOME for up to the spawn window, and the
// test's teardown must not return (letting t.TempDir remove HOME) while that
// goroutine is mid-write.
func TestStopShellTestInstance_JoinsOwnershipAttribution(t *testing.T) {
	skipIfNoTmuxBinary(t)
	isolateUserHomeForShellRestart(t)

	gate := &gatedProber{Prober: ownershipProber, entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(gate.release) }) }
	prev := ownershipProber
	ownershipProber = gate
	t.Cleanup(func() {
		unblock()
		ownershipProber = prev
	})

	title := uniqueShellTestTitle("JoinOwnership")
	inst := NewInstance(title, t.TempDir())
	inst.Command = ""
	if err := inst.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	gate.armed.Store(true)
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		cleanupShellSessions(title)
		t.Fatal("ownership attribution pass never ran after Start")
	}

	stopped := make(chan struct{})
	go func() {
		stopShellTestInstance(t, inst, title)
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Error("teardown returned while the ownership attribution pass was still inside its receipt write; t.TempDir cleanup would race it")
	case <-time.After(300 * time.Millisecond):
	}
	unblock()
	select {
	case <-stopped:
	case <-time.After(shellTestJoinDeadline + 5*time.Second):
		t.Fatal("teardown did not return after the attribution pass was released")
	}
}
