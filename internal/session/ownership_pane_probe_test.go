package session

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asheshgoplani/agent-deck/internal/procowner"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func TestOwnershipPaneProbeAdmission(t *testing.T) {
	const pid = 5291
	prober := newSessionFakeProber()
	prober.alive(pid, "pane-leader", 1000)
	restore := swapOwnershipProbe(t, prober, ownershipSignaler)
	t.Cleanup(restore)
	originalProbe := ownershipPanePID
	t.Cleanup(func() { ownershipPanePID = originalProbe })

	for _, tc := range []struct {
		name string
		pid  int
		err  error
		real bool
		// receiptGone names a tmux server that does not exist in the receipt,
		// so its probe answers "gone" while the current one has failed.
		receiptGone bool
	}{
		{name: "timeout", err: context.DeadlineExceeded},
		{name: "probe failure", err: errors.New("permission denied")},
		{name: "timeout with receipt server gone", err: context.DeadlineExceeded, receiptGone: true},
		{name: "missing tmux session", real: true},
		{name: "different leader", pid: pid + 1},
		{name: "matching leader", pid: pid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inst := &Instance{ID: sanitizeTestID(t.Name()), tmuxSession: tmux.NewSession("pane-probe", t.TempDir())}
			writeTestReceipt(t, inst.ID, procowner.Member{PID: pid, StartID: "pane-leader", UID: 1000})
			ownershipPanePID = func(*tmux.Session) (int, error) { return tc.pid, tc.err }
			if tc.real {
				ownershipPanePID = originalProbe
				inst.tmuxSession.SocketName = sanitizeTestID(t.Name())
			}
			if tc.receiptGone {
				_, err := ownershipStore().Commit(inst.ID, func(r *procowner.Receipt) (*procowner.Receipt, error) {
					r.TmuxName, r.TmuxSocket = "pane-probe", sanitizeTestID(t.Name())
					return r, nil
				})
				require.NoError(t, err)
			}

			status := inst.OwnershipStatus()
			refusal := inst.guardOwnedProcessesBeforeSpawn("restart")
			if tc.pid == pid {
				assert.True(t, status.Admissible())
				assert.True(t, status.PaneAttached)
				assert.Empty(t, status.Survivors)
				require.NoError(t, refusal)
				return
			}
			assert.False(t, status.Admissible())
			require.True(t, IsOwnedProcessRecoveryRequired(refusal), "want ownership refusal, got %v", refusal)
			if tc.err != nil {
				assert.Empty(t, status.Survivors)
				assert.Contains(t, status.Reason(), "could not check the session's tmux pane")
				assert.Contains(t, status.Reason(), tc.err.Error())
				assert.Contains(t, refusal.Error(), status.Reason())
				assert.Contains(t, refusal.Error(), "run the restart again")
				assert.Contains(t, refusal.Error(), "ownership inspect "+inst.ID)
				assert.NotContains(t, refusal.Error(), "alive outside the session's pane")
				assert.NotContains(t, refusal.Error(), "ownership reconcile")
				return
			}
			assert.Equal(t, []int{pid}, survivorPIDs(status))
			assert.Contains(t, status.Reason(), "alive outside the session's pane")
			assert.Contains(t, refusal.Error(), status.Reason())
		})
	}
}
