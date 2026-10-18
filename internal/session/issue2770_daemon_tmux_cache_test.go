package session

import (
	"fmt"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// daemonTmuxPassStarts starts n live shell sessions on the test's private
// tmux socket, saves them in a profile with no live TUI (so the daemon
// probes every instance itself), and returns the tmux subprocesses one
// steady-state daemon pass starts.
func daemonTmuxPassStarts(t *testing.T, n int) int64 {
	t.Helper()
	skipIfNoTmuxBinary(t)
	inboxTestHome(t)
	profile := fmt.Sprintf("_test-2770-%d", n)
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer storage.Close()

	var instances []*Instance
	for i := 0; i < n; i++ {
		inst := NewInstanceWithTool(fmt.Sprintf("tmux-2770-%d-%d", n, i), t.TempDir(), "shell")
		if err := inst.tmuxSession.Start("exec sleep 3600"); err != nil {
			t.Fatalf("tmux start: %v", err)
		}
		sess := inst.tmuxSession
		t.Cleanup(func() { _ = sess.Kill() })
		inst.Status = StatusIdle
		instances = append(instances, inst)
	}
	if err := storage.SaveWithGroups(instances, nil); err != nil {
		t.Fatalf("save: %v", err)
	}

	d := NewTransitionDaemon()
	d.syncProfile(profile) // first pass: baseline and one-time warm-up
	d.syncProfile(profile)
	before := tmux.SubprocessStarts()
	d.syncProfile(profile)
	return tmux.SubprocessStarts() - before
}

// Issue #2770: the notify daemon never refreshed the tmux session or pane
// caches, so every Exists() (hook rebind gate, turn liveness) and
// IsPaneDead() in a pass ran its own has-session or list-panes: five tmux
// processes per live session per pass. A pass must refresh the caches once
// and answer from them. The one per-session command left is the first
// status sample's capture-pane (the daemon loads fresh instances each pass).
func TestIssue2770_DaemonPassTmuxStartsDoNotScaleWithSessions(t *testing.T) {
	small := daemonTmuxPassStarts(t, 2)
	large := daemonTmuxPassStarts(t, 10)
	perSession := float64(large-small) / 8
	t.Logf("tmux starts per daemon pass: 2 sessions=%d, 10 sessions=%d (%.1f per session)", small, large, perSession)
	if perSession > 1 {
		t.Fatalf("a daemon pass starts %.1f tmux commands per live session (2 sessions=%d, 10=%d); want at most 1: per-session probes bypass the cache", perSession, small, large)
	}
}
