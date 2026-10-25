package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Forge R5: `session fork --sandbox` sets Sandbox on the forked instance after
// the fork's start command is built. A pi fork's command carries the host
// socket export computed for a local launch, so a sandboxed fork would run with
// AGENTDECK_PI_SOCKET and record PiSocketExported = true, though its extension
// lives in the container and the daemon can never reach it: the typed doorbell
// would be forbidden. A sandboxed fork never exports and records false.
func TestSessionFork_SandboxedPiForkNeverExportsTheSocket(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(bin, "pi"), []byte("#!/bin/sh\necho 1.3.0\n"), 0o755); err != nil {
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
	profile := "pi_fork_sandbox"
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	if err := storage.SaveWithGroups([]*session.Instance{parent}, session.NewGroupTreeWithGroups([]*session.Instance{parent}, nil)); err != nil {
		t.Fatal(err)
	}

	fork := func(extra ...string) *session.Instance {
		t.Helper()
		var captured *session.Instance
		old := sessionForkBeforeStartHook
		sessionForkBeforeStartHook = func(_ *session.Instance, forked *session.Instance, _ git.WorktreeStateOptions) { captured = forked }
		defer func() { sessionForkBeforeStartHook = old }()
		handleSessionFork(profile, append([]string{"pi-parent", "-t", "pi-child"}, extra...))
		if captured == nil || captured.Tool != "pi" {
			t.Fatalf("the fork did not reach the pi dispatcher: %+v", captured)
		}
		return captured
	}

	local := fork()
	if !strings.Contains(local.ForkStartCommand, "AGENTDECK_PI_SOCKET=") || !local.PiSocketExported {
		t.Fatalf("test setup: a local pi fork must export the socket (flag %v): %q", local.PiSocketExported, local.ForkStartCommand)
	}
	sandboxed := fork("--sandbox")
	if !sandboxed.IsSandboxed() {
		t.Fatal("test setup: the fork is not sandboxed")
	}
	if strings.Contains(sandboxed.ForkStartCommand, "AGENTDECK_PI_SOCKET") {
		t.Errorf("a sandboxed fork exports the host socket: %q", sandboxed.ForkStartCommand)
	}
	if sandboxed.PiSocketExported {
		t.Error("a sandboxed fork recorded PiSocketExported = true: the daemon would never type its doorbell")
	}
}
