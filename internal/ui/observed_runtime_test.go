package ui

import (
	"os/exec"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"github.com/stretchr/testify/require"
)

func TestObservedRuntimeRenderingPreservesLaunchTool(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	for _, tc := range []struct{ configured, foreground, display string }{
		{"shell", "opencode", "opencode"},
		{"claude", "bash", "claude"},
	} {
		t.Run(tc.configured+"/"+tc.foreground, func(t *testing.T) {
			inst := session.NewInstance("observed-runtime-render", t.TempDir())
			inst.Tool = "shell"
			inst.Command = "sleep 60"
			require.NoError(t, inst.Start())
			t.Cleanup(func() { _ = inst.Kill() })
			time.Sleep(2 * time.Second)
			inst.SetToolThreadSafe(tc.configured)
			ts := inst.GetTmuxSession()
			tmux.ExpireStartupWindowForTest(t, ts)
			tmux.SeedPaneInfoCacheForTest(t, map[string]tmux.PaneInfo{
				ts.Name: {CurrentCommand: tc.foreground},
			})
			require.NoError(t, inst.UpdateStatus())
			for _, snapshot := range []bool{false, true} {
				h := newHomeForSnapshotTest()
				if snapshot {
					h.refreshSessionRenderSnapshot([]*session.Instance{inst})
				}
				require.Equal(t, tc.display, h.getSessionRenderState(inst).tool)
				require.Contains(t, tmux.StripANSI(h.renderSessionInfoCard(inst, 240, 40)), tc.display)
				require.Equal(t, tc.configured, inst.GetToolThreadSafe())
			}
		})
	}
}
