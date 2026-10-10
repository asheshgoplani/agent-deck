package session

import (
	"fmt"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The package TestMain owns an isolated tmux socket and tears down that socket.
func TestCustomCommandExitStatus(t *testing.T) {
	skipIfNoTmuxBinary(t)
	for _, tc := range []struct {
		name    string
		command string
		code    int
		status  Status
	}{
		{"zero", "exit 0", 0, StatusIdle},
		{"three", "exit 3", 3, StatusError},
		{"signal", "exit 130", 130, StatusError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := NewInstance("custom-exit-"+tc.name, t.TempDir())
			i.Tool, i.Command, i.TrackCommandExit = "shell", tc.command, true
			i.tmuxSession = tmux.NewSession(i.Title, i.ProjectPath)
			i.tmuxSession.OptionOverrides = i.buildTmuxOptionOverrides()
			require.Equal(t, "on", i.tmuxSession.OptionOverrides["remain-on-exit"])
			i.tmuxSession.RunCommandAsInitialProcess = true
			t.Cleanup(func() { _ = i.tmuxSession.Kill() })
			require.NoError(t, i.tmuxSession.Start(tc.command))
			i.CreatedAt = time.Now().Add(-time.Minute)
			require.Eventually(t, func() bool {
				_ = i.UpdateStatus()
				code := i.ExitCode()
				return i.Status == tc.status && i.Substate() == SubstateProcessExited && code != nil && *code == tc.code
			}, 5*time.Second, 100*time.Millisecond, fmt.Sprintf("exit %d was not observed", tc.code))
		})
	}
}

func TestCustomCommandStillRunningHasNoExitCode(t *testing.T) {
	skipIfNoTmuxBinary(t)
	i := NewInstance("custom-still-running", t.TempDir())
	i.Tool, i.Command, i.TrackCommandExit = "shell", "sleep 60", true
	i.tmuxSession = tmux.NewSession(i.Title, i.ProjectPath)
	i.tmuxSession.OptionOverrides = i.buildTmuxOptionOverrides()
	i.tmuxSession.RunCommandAsInitialProcess = true
	t.Cleanup(func() { _ = i.tmuxSession.Kill() })
	require.NoError(t, i.tmuxSession.Start(i.Command))
	i.CreatedAt = time.Now().Add(-time.Minute)
	require.NoError(t, i.UpdateStatus())
	require.Nil(t, i.ExitCode())
	require.NotEqual(t, SubstateProcessExited, i.Substate())
}

func TestTrackedCommandImmediateExitThree(t *testing.T) {
	skipIfNoTmuxBinary(t)
	i := NewInstance("tracked-immediate-exit", t.TempDir())
	i.Tool, i.Command, i.TrackCommandExit = "shell", "exit 3", true
	t.Cleanup(func() {
		if pane := i.GetTmuxSession(); pane != nil {
			_ = pane.Kill()
		}
	})
	require.NoError(t, i.Start())
	i.CreatedAt = time.Now().Add(-time.Minute)
	if !assert.Eventually(t, func() bool {
		_ = i.UpdateStatus()
		code := i.ExitCode()
		return i.Status == StatusError && i.Substate() == SubstateProcessExited && code != nil && *code == 3
	}, 5*time.Second, 25*time.Millisecond) {
		paneCode, known := i.GetTmuxSession().PaneDeadExitStatus()
		receiptCode, receiptKnown := i.trackedCommandExitReceipt()
		t.Fatalf("status=%s substate=%s exit=%v pane_exit=%d pane_known=%v receipt_exit=%d receipt_known=%v", i.Status, i.Substate(), i.ExitCode(), paneCode, known, receiptCode, receiptKnown)
	}
}

func TestTrackedCommandExitProvenanceSurvivesReloadAndClear(t *testing.T) {
	s := newTestStorage(t)
	i := NewInstance("tracked-command", t.TempDir())
	i.Tool, i.Command, i.TrackCommandExit = "shell", "exit 3", true
	require.NoError(t, s.SaveWithGroups([]*Instance{i}, nil))
	loaded, _, err := s.LoadWithGroups()
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.True(t, loaded[0].TrackCommandExit)
	require.True(t, loaded[0].tracksCommandExit())

	i.TrackCommandExit = false
	require.NoError(t, s.SaveWithGroups([]*Instance{i}, nil))
	loaded, _, err = s.LoadWithGroups()
	require.NoError(t, err)
	require.False(t, loaded[0].TrackCommandExit)
}
