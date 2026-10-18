package main

import (
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/slashcmd"
)

// Review 2736 round 2: a verified /model switch is stored on the session,
// "default" clears a model stored earlier, and a display label is not
// stored.
func TestPersistSwitchedModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("AGENTDECK_PROFILE", "")
	session.ClearUserConfigCache()
	const profile = "_test-persist-model"
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	inst := session.NewInstanceWithTool("worker", t.TempDir(), "claude")
	if err := saveSessionData(storage, []*session.Instance{inst}, nil); err != nil {
		t.Fatal(err)
	}
	storage.Close()

	stored := func() string {
		t.Helper()
		s, err := session.NewStorageWithProfile(profile)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		instances, err := s.Load()
		if err != nil || len(instances) != 1 {
			t.Fatalf("load: %d %v", len(instances), err)
		}
		return instances[0].LaunchModelID()
	}

	var res slashcmd.Result
	persistSwitchedModel(profile, inst.ID, "haiku", &res)
	if got := stored(); got != "haiku" {
		t.Fatalf("after /model haiku stored model = %q, want haiku (%+v)", got, res)
	}
	persistSwitchedModel(profile, inst.ID, "Haiku 5.5", &res)
	if got := stored(); got != "haiku" {
		t.Fatalf("a display label must not be stored: %q", got)
	}
	persistSwitchedModel(profile, inst.ID, "default", &res)
	if got := stored(); got != "" {
		t.Fatalf("after /model default stored model = %q, want cleared (%+v)", got, res)
	}
}

// The config dir is the session's own CLAUDE_CONFIG_DIR from its tmux
// environment when set, else the one agent-deck resolves.
func TestCommandConfigDir(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	inst := session.NewInstanceWithTool("cfgdir-probe", t.TempDir(), "claude")
	if got, want := commandConfigDir(inst, nil), session.ClaudeConfigDirForSend(inst); got != want {
		t.Fatalf("without a pane: %q, want %q", got, want)
	}
	ts := inst.GetTmuxSession()
	if err := ts.Start("sleep 30"); err != nil {
		t.Fatalf("tmux start: %v", err)
	}
	t.Cleanup(func() { _ = ts.Kill() })
	time.Sleep(200 * time.Millisecond)
	if got, want := commandConfigDir(inst, ts), session.ClaudeConfigDirForSend(inst); got != want {
		t.Fatalf("pane without CLAUDE_CONFIG_DIR: %q, want %q", got, want)
	}
	custom := filepath.Join(t.TempDir(), "wrapper-claude")
	if err := ts.SetEnvironment("CLAUDE_CONFIG_DIR", custom); err != nil {
		t.Fatal(err)
	}
	if got := commandConfigDir(inst, ts); got != custom {
		t.Fatalf("pane with CLAUDE_CONFIG_DIR: %q, want %q", got, custom)
	}
}
