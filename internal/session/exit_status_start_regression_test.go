package session

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// CORE-CHANGES 13: a tracked one-shot command that exits right away has run to
// completion. Start + VerifySpawned must report a successful start and leave no
// spawn-failure record, for a zero and a non-zero exit alike; the result is the
// process-exited row. Untracked remain-on-exit panes keep the #2202 verdict
// (TestIssue2202_VerifySpawnedRemainOnExitDiesFast).
func TestExitStatus1628_VerifySpawnedAcceptsCompletedTrackedCommand(t *testing.T) {
	skipIfNoTmuxBinary(t)
	for _, tc := range []struct {
		name    string
		command string
		code    int
	}{
		{"zero", "echo ok; exit 0", 0},
		{"three", "echo no; exit 3", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := NewInstance("tracked-verify-"+tc.name, t.TempDir())
			i.Tool, i.Command, i.TrackCommandExit = "shell", tc.command, true
			t.Cleanup(func() {
				if pane := i.GetTmuxSession(); pane != nil {
					_ = pane.Kill()
				}
				clearSpawnFailureRecord(i.ID)
			})
			require.NoError(t, i.Start())
			require.NoError(t, i.VerifySpawned(5*time.Second))

			// Let the fast-death watcher take at least two ticks on the dead pane.
			time.Sleep(3 * spawnFastDeathTick)
			require.Nil(t, i.SpawnFailure(), "a completed tracked command is not a spawn failure")
			got, known := i.trackedCommandExitReceipt()
			require.True(t, known)
			require.Equal(t, tc.code, got)
		})
	}
}
