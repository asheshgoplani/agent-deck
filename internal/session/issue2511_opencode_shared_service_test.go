package session

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asheshgoplani/agent-deck/internal/procowner"
)

// #2511 item 4: on OpenCode 2.x the first `opencode` in a pane starts the
// host-wide `opencode serve --service`, which outlives the TUI (reparented to
// pid 1) and serves every other OpenCode TUI on the host. The receipt records it
// as a descendant; the gate must not count it, and reconcile must not kill it.

var openCodeServiceArgv = []string{"/opt/homebrew/Cellar/opencode/2.0.20/bin/opencode", "serve", "--service"}

// commandFakeProber adds the optional procowner.CommandReader to the session
// fake.
type commandFakeProber struct {
	*sessionFakeProber
	args map[int][]string
}

func (p *commandFakeProber) Commands() (map[int]procowner.ProcCommand, error) {
	out := make(map[int]procowner.ProcCommand, len(p.procs))
	for pid, info := range p.procs {
		out[pid] = procowner.ProcCommand{PPID: info.PPID, Args: p.args[pid]}
	}
	return out, nil
}

// sharedServiceFixture is the issue's shape after the TUI exited: the pane
// leader is gone, the service runs under pid 1, an MCP server runs under the
// service.
func sharedServiceFixture(t *testing.T, serviceArgv []string) (*commandFakeProber, *Instance) {
	t.Helper()
	uid := os.Getuid()
	prober := &commandFakeProber{sessionFakeProber: newSessionFakeProber(), args: map[int][]string{}}
	prober.alive(5243, "5200", uid)
	prober.args[5243] = serviceArgv
	prober.alive(5244, "5300", uid)
	mcp := prober.procs[5244]
	mcp.PPID = 5243
	prober.procs[5244] = mcp
	prober.args[5244] = []string{"npm", "exec", "@playwright/mcp@latest"}

	inst := NewInstance("test-2511-"+sanitizeTestID(t.Name()), t.TempDir())
	inst.Tool = "opencode"
	store := ownershipStore()
	receipt := &procowner.Receipt{
		Version:    procowner.ReceiptVersion,
		InstanceID: inst.ID,
		Generation: 1,
		State:      procowner.StateLive,
		Provider:   "session_fake",
		BootID:     "boot-1",
		CreatedAt:  time.Now().Unix(),
		Leader:     procowner.Member{PID: 5242, StartID: "5000", UID: uid, Role: procowner.RoleLeader},
		Members: []procowner.Member{
			{PID: 5243, StartID: "5200", UID: uid, Role: procowner.RoleDescendant},
			{PID: 5244, StartID: "5300", UID: uid, Role: procowner.RoleDescendant},
		},
	}
	_, err := store.Commit(inst.ID, func(*procowner.Receipt) (*procowner.Receipt, error) { return receipt, nil })
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.ForceClear(inst.ID) })
	return prober, inst
}

func TestIssue2511_SharedOpenCodeServiceDoesNotBlockRestart(t *testing.T) {
	prober, inst := sharedServiceFixture(t, openCodeServiceArgv)
	signaler := &countingSignaler{}
	defer swapOwnershipProbe(t, prober, signaler)()

	status := inst.OwnershipStatus()
	require.True(t, status.Admissible(), status.Reason())
	assert.Empty(t, status.Survivors)

	require.NoError(t, inst.guardOwnedProcessesBeforeSpawn("restart"))
	assert.Empty(t, signaler.calls, "the gate never signals, and the service is not the session's")
	assert.Nil(t, inst.OwnershipStatus().Receipt, "a receipt that owns nothing alive is retired")
}

func TestIssue2511_ReconcileLeavesSharedOpenCodeServiceRunning(t *testing.T) {
	prober, inst := sharedServiceFixture(t, openCodeServiceArgv)
	signaler := &countingSignaler{}
	defer swapOwnershipProbe(t, prober, signaler)()

	report, err := inst.ReconcileOwnership()
	require.NoError(t, err)
	assert.Equal(t, procowner.VerdictClear, report.Verdict, report.Describe())
	assert.Empty(t, signaler.calls, "reconcile must not signal the shared service or its MCP children")
	assert.Nil(t, inst.OwnershipStatus().Receipt)
}

// Control: the 1.x per-TUI server is the session's own process, so a survivor
// of it still refuses the restart exactly as before.
func TestIssue2511_OpenCode1xServerStillBlocksRestart(t *testing.T) {
	prober, inst := sharedServiceFixture(t, []string{"opencode", "serve", "--port", "4096"})
	signaler := &countingSignaler{}
	defer swapOwnershipProbe(t, prober, signaler)()

	err := inst.guardOwnedProcessesBeforeSpawn("restart")
	require.Error(t, err)
	assert.True(t, IsOwnedProcessRecoveryRequired(err), err.Error())
	assert.ElementsMatch(t, []int{5243, 5244}, survivorPIDs(inst.OwnershipStatus()))
	assert.Empty(t, signaler.calls)
}
