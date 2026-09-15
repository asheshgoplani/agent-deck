//go:build linux

package procowner

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The pidfd path against a real process: the handle is opened on a live child,
// the reap signals through it, and the child is verified dead.
func TestOSSignaler_PinsAndReapsARealProcess(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	p := NewProber()
	leader, err := p.Inspect(cmd.Process.Pid)
	require.NoError(t, err)

	pin, err := OSSignaler{}.Pin(cmd.Process.Pid)
	if errors.Is(err, syscall.ENOSYS) {
		t.Skip("kernel has no pidfd_open")
	}
	require.NoError(t, err)
	require.NoError(t, pin.Close())

	// A real receipt, so the boot id matches this host and the reap actually
	// reaches the member instead of retiring it as pre-boot.
	receipt, err := Claim(p, ClaimInput{InstanceID: "real", Generation: 1, PanePID: cmd.Process.Pid})
	require.NoError(t, err)
	report := Reap(p, OSSignaler{}, receipt,
		ReapOptions{TermGrace: 2 * time.Second, KillGrace: 2 * time.Second})
	require.Equal(t, VerdictClear, report.Verdict, report.Describe())
	require.Len(t, report.Outcomes, 1)
	assert.Equal(t, OutcomeReaped, report.Outcomes[0].Outcome)

	// Reap the zombie so the assertion below sees a real exit, then prove the
	// pid no longer verifies as ours.
	_, _ = cmd.Process.Wait()
	assert.NotEqual(t, StateOwned, VerifyMember(p, memberOf(leader, RoleLeader)).State)
}

// A handle opened on a process that has since exited must refuse to signal
// rather than reach whoever holds the pid now.
func TestPidfd_SignalAfterExitIsESRCH(t *testing.T) {
	cmd := exec.Command("true")
	require.NoError(t, cmd.Start())
	pin, err := OSSignaler{}.Pin(cmd.Process.Pid)
	if errors.Is(err, syscall.ENOSYS) {
		t.Skip("kernel has no pidfd_open")
	}
	require.NoError(t, err)
	defer func() { _ = pin.Close() }()
	require.NoError(t, cmd.Wait())

	err = pin.Signal(syscall.SIGTERM)
	assert.True(t, errors.Is(err, syscall.ESRCH), "want ESRCH after exit, got %v", err)
}
