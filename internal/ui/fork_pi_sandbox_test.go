package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The TUI fork (Shift+F, f) sets Sandbox on the forked instance in completeFork,
// after the fork's start command was built. A pi fork's command carries the host
// socket export computed for a local launch, so a sandboxed fork would export it
// and record PiSocketExported = true though its extension lives in the
// container; the daemon would then never type its doorbell. As in the CLI, a
// sandboxed TUI fork never exports and records false.
func TestCompleteFork_SandboxedPiForkNeverExportsTheSocket(t *testing.T) {
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

	source := session.NewInstanceWithGroupAndTool("pi-parent", home, "pi-group", "pi")
	source.Command = "pi"
	source.IdentityInjectionDisabled = true
	sessionDir := filepath.Join(home, ".pi", "agent-deck", source.ID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "s.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	fork := func(toggles forkToggles) *session.Instance {
		t.Helper()
		deps := defaultForkInstanceDeps()
		deps.startInstance = func(*session.Instance) error { return nil } // no tmux
		inst, err := completeFork(source, "child", "pi-group", toggles, nil, "", "", false, deps)
		if err != nil {
			t.Fatalf("completeFork: %v", err)
		}
		return inst
	}

	local := fork(forkToggles{})
	if !strings.Contains(local.ForkStartCommand, "AGENTDECK_PI_SOCKET=/") || !local.PiSocketExported {
		t.Fatalf("test setup: a local pi fork must export the socket (flag %v): %q", local.PiSocketExported, local.ForkStartCommand)
	}
	sandboxed := fork(forkToggles{Sandbox: true})
	if !sandboxed.IsSandboxed() {
		t.Fatal("test setup: the fork is not sandboxed")
	}
	if strings.Contains(sandboxed.ForkStartCommand, "AGENTDECK_PI_SOCKET") {
		t.Errorf("a sandboxed TUI fork exports the host socket: %q", sandboxed.ForkStartCommand)
	}
	if sandboxed.PiSocketExported {
		t.Error("a sandboxed TUI fork recorded PiSocketExported = true: the daemon would never type its doorbell")
	}
}

// A sandboxed pi fork whose start command cannot be rebuilt fails like any other
// step after a with-state worktree was created: the worktree and branch are
// rolled back, and nothing is started.
func TestCompleteFork_AFailedPiRebuildRollsTheWorktreeBack(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // the source has no pi session file: it cannot be forked
	source := session.NewInstanceWithGroupAndTool("pi-parent", t.TempDir(), "pi-group", "pi")
	source.Command = "pi"
	opts := newForkStateOpts()
	rec := &rollbackRecorder{}
	started := false
	deps := forkInstanceDeps{
		createInstance: func(_ *session.Instance, title, groupPath string, _ *session.ClaudeOptions, _ session.ForkParent) (*session.Instance, error) {
			inst := session.NewInstanceWithGroupAndTool(title, t.TempDir(), groupPath, "pi")
			return inst, nil
		},
		createMultiRepoDir: func(_, _ *session.Instance) error { return nil },
		startInstance:      func(*session.Instance) error { started = true; return nil },
		rollback:           rec.fn,
	}
	inst, err := completeFork(source, "child", "pi-group", forkToggles{Sandbox: true}, opts, "", "", true, deps)
	if err == nil || inst != nil {
		t.Fatalf("a failed rebuild must fail the fork: inst %v err %v", inst, err)
	}
	if started {
		t.Error("the fork was started after its command failed to rebuild")
	}
	if rec.calls != 1 || rec.repoRoot != opts.WorktreeRepoRoot || rec.wtPath != opts.WorktreePath || rec.branch != opts.WorktreeBranch {
		t.Errorf("the fork worktree was left behind: rollback calls %d (%q %q %q), want one for %q %q %q",
			rec.calls, rec.repoRoot, rec.wtPath, rec.branch, opts.WorktreeRepoRoot, opts.WorktreePath, opts.WorktreeBranch)
	}
}
