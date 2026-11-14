//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// fakeTmuxSessionName puts a tmux on PATH whose display-message answers name,
// and marks the process as running inside tmux.
func fakeTmuxSessionName(t *testing.T, name string) {
	t.Helper()
	fakeBin := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' '" + name + "'\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("PATH", fakeBin)
	t.Setenv("TMUX", "/tmp/tmux-test/default,1,0")
}

// The tmux session name ends in a random suffix (tmux.generateShortID), not in
// the session ID, so the calling session must be found by its full tmux name.
func TestResolveSessionOrCurrent_FindsCallingSessionByTmuxName(t *testing.T) {
	inst := session.NewInstance("worker", "/tmp/project")
	other := session.NewInstance("other", "/tmp/other")
	instances := []*session.Instance{other, inst}

	t.Setenv("AGENTDECK_INSTANCE_ID", "")
	fakeTmuxSessionName(t, inst.GetTmuxSession().Name)

	got, errMsg, _ := ResolveSessionOrCurrent("", instances)
	if got != inst {
		t.Fatalf("ResolveSessionOrCurrent(\"\") inside tmux session %q = %v, %q; want instance %s",
			inst.GetTmuxSession().Name, got, errMsg, inst.ID)
	}
}

// AGENTDECK_INSTANCE_ID is injected into every agent-deck session and names
// the calling session exactly; it wins over the tmux probe.
func TestResolveSessionOrCurrent_PrefersInstanceIDEnv(t *testing.T) {
	inst := session.NewInstance("worker", "/tmp/project")
	other := session.NewInstance("other", "/tmp/other")
	instances := []*session.Instance{other, inst}

	t.Setenv("AGENTDECK_INSTANCE_ID", inst.ID)
	fakeTmuxSessionName(t, other.GetTmuxSession().Name)

	got, errMsg, _ := ResolveSessionOrCurrent("", instances)
	if got != inst {
		t.Fatalf("ResolveSessionOrCurrent(\"\") with AGENTDECK_INSTANCE_ID=%s = %v, %q; want that instance",
			inst.ID, got, errMsg)
	}
}

// Outside any agent-deck session there is still nothing to auto-detect.
func TestResolveSessionOrCurrent_NotInsideSession(t *testing.T) {
	inst := session.NewInstance("worker", "/tmp/project")
	t.Setenv("AGENTDECK_INSTANCE_ID", "")
	fakeTmuxSessionName(t, "plain-shell")

	got, errMsg, code := ResolveSessionOrCurrent("", []*session.Instance{inst})
	if got != nil || code != ErrCodeNotFound || errMsg != "no session specified and not inside an agent-deck session" {
		t.Fatalf("ResolveSessionOrCurrent(\"\") outside agent-deck = %v, %q, %q", got, errMsg, code)
	}
}
