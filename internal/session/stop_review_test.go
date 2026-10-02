package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"github.com/stretchr/testify/require"
)

func TestReviewStop_EmptyShellStartClearsStop(t *testing.T) {
	skipIfNoTmuxBinary(t)
	inst := NewInstanceWithTool("review-empty-shell", t.TempDir(), "shell")
	inst.Command = ""
	require.NoError(t, inst.Start())
	t.Cleanup(func() { _ = inst.Kill() })
	require.NoError(t, inst.Kill())
	require.NoError(t, inst.UpdateStatus())
	inst.mu.Lock()
	inst.lastErrorCheck = time.Now()
	inst.mu.Unlock()
	require.NoError(t, inst.Start())
	require.NoError(t, inst.VerifySpawned(time.Second))
	require.NotEqual(t, StatusStopped, inst.GetStatusThreadSafe())
	require.True(t, inst.lastErrorCheck.IsZero(), "start must clear stopped polling throttle")
	// Crash before a status refresh can observe the new shell alive.
	require.NoError(t, inst.tmuxSession.Kill())
	inst.mu.Lock()
	inst.lastStartTime = time.Now().Add(-time.Minute)
	inst.mu.Unlock()
	tmux.RefreshExistingSessions()
	require.NoError(t, inst.UpdateStatus())
	require.Equal(t, StatusError, inst.GetStatusThreadSafe())
}

// First has-session is the ordinary Exists probe; the second is stopped
// revival validation. No real sessions or processes are owned by this fake.
func reviewStoppedProbe(t *testing.T, mode string) (*Instance, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("STOP_REVIEW_DIR", dir)
	t.Setenv("STOP_REVIEW_MODE", mode)
	script := `#!/bin/sh
if [ "$1" = -u ]; then shift; fi
if [ "$1" = -L ]; then shift 2; fi
case "$1" in
has-session)
 if [ ! -f "$STOP_REVIEW_DIR/first" ]; then
  touch "$STOP_REVIEW_DIR/first"
  exit 0
 fi
 case "$STOP_REVIEW_MODE" in
 gate)
  if [ -f "$STOP_REVIEW_DIR/entered" ]; then
   echo "can't find session: stopped" >&2
   exit 1
  fi
  touch "$STOP_REVIEW_DIR/entered"
  while [ ! -f "$STOP_REVIEW_DIR/released" ]; do sleep 0.01; done
  exit 0;;
 timeout) sleep 3; exit 0;;
 prefix)
  case "$3" in =*) echo "can't find session: $3" >&2; exit 1;; *) exit 0;; esac;;
 esac;;
list-panes|display-message) printf '0\n';;
capture-pane) exit 1;;
*) exit 0;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0700))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	inst := NewInstanceWithTool("review-stopped-probe", dir, "shell")
	inst.tmuxSession.SocketName = "review-probe"
	inst.Status = StatusStopped
	inst.CreatedAt = time.Now().Add(-time.Minute)
	return inst, dir
}

func TestReviewStop_ProbeDoesNotInventRestart(t *testing.T) {
	for _, mode := range []string{"timeout", "prefix"} {
		t.Run(mode, func(t *testing.T) {
			inst, _ := reviewStoppedProbe(t, mode)
			require.NoError(t, inst.UpdateStatus())
			require.Equal(t, StatusStopped, inst.GetStatusThreadSafe())
		})
	}
}

func TestReviewStop_ConcurrentKillInvalidatesPositiveProbe(t *testing.T) {
	inst, dir := reviewStoppedProbe(t, "gate")
	done := make(chan error, 1)
	go func() { done <- inst.UpdateStatus() }()
	released := false
	joined := false
	t.Cleanup(func() {
		if !released {
			_ = os.WriteFile(filepath.Join(dir, "released"), nil, 0600)
		}
		if !joined {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("status worker did not finish")
			}
		}
	})
	require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(dir, "entered")); return err == nil }, time.Second, time.Millisecond)
	require.NoError(t, inst.Kill())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "released"), nil, 0600))
	released = true
	select {
	case err := <-done:
		joined = true
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("status worker did not finish")
	}
	require.Equal(t, StatusStopped, inst.GetStatusThreadSafe())
}

func TestReviewStop_ManualTmuxRestartIsDetected(t *testing.T) {
	skipIfNoTmuxBinary(t)
	inst := NewInstanceWithTool("review-manual-restart", t.TempDir(), "shell")
	require.NoError(t, inst.Start())
	t.Cleanup(func() { _ = inst.Kill() })
	require.NoError(t, inst.Kill())
	// Recreate the same named session without using Instance.Start.
	require.NoError(t, inst.tmuxSession.Start(""))
	inst.mu.Lock()
	inst.lastErrorCheck = time.Time{}
	inst.mu.Unlock()
	require.NoError(t, inst.UpdateStatus())
	require.NotEqual(t, StatusStopped, inst.GetStatusThreadSafe())
	// tmux can report starting until the interactive prompt is ready.
	require.Eventually(t, func() bool {
		_ = inst.UpdateStatus()
		return isLiveSessionStatus(inst.GetStatusThreadSafe())
	}, 10*time.Second, 50*time.Millisecond)
}

func TestReviewStop_NilTmuxDuringGrace(t *testing.T) {
	inst := &Instance{Status: StatusStopped, CreatedAt: time.Now()}
	require.NoError(t, inst.UpdateStatus())
	require.Equal(t, StatusStopped, inst.GetStatusThreadSafe())
}
