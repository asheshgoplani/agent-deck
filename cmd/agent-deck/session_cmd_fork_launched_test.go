package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// `session fork` starts the fork before its first save, so the launch's
// targeted write finds no row: the save that follows must record what the fork
// launched (and its socket decision), through the real fork and start.
func TestSessionFork_TheForkRecordsItsLaunchedTool(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	short, err := os.MkdirTemp("/tmp", "pd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", short) // the socket path must fit sun_path
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	if _, err := session.InstallPiHooks(session.PiExtensionsDir()); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(bin, "pi"), []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 1.3.0; exit 0; fi\nexec sleep 300\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	parent := session.NewInstanceWithGroupAndTool("pi-parent", home, "pi-group", "pi")
	parent.Command = "pi"
	parent.IdentityInjectionDisabled = true
	sessionDir := filepath.Join(home, ".pi", "agent-deck", parent.ID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "s.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := "pi_fork_launched"
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	if err := storage.SaveWithGroups([]*session.Instance{parent}, session.NewGroupTreeWithGroups([]*session.Instance{parent}, nil)); err != nil {
		t.Fatal(err)
	}

	handleSessionFork(profile, []string{"pi-parent", "-t", "pi-child"})

	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, inst := range instances {
		if inst.Title != "pi-child" {
			continue
		}
		t.Cleanup(func() { _ = inst.Kill() })
		if inst.LaunchedTool != "pi" {
			t.Errorf("the fork holds LaunchedTool = %q, want \"pi\": its first save did not record its launch", inst.LaunchedTool)
		}
		if !inst.PiSocketExported {
			t.Error("the fork's socket decision was not recorded")
		}
		return
	}
	t.Fatal("the fork is missing from storage")
}
