package session

import (
	"strings"
	"testing"
	"time"

	"al.essio.dev/pkg/shellescape"
)

// TestCreateForkedOpenCode_DefersLaunchViaForkStartCommand guards the fork-restart
// invariant: the `opencode -s <parent> --fork` command must run exactly once. If
// it were stored as the persistent Command, a restart after the tmux session dies
// (before async session-id detection completes) would re-run `--fork` and fork the
// parent *again* into yet another session. The fork therefore uses the Pi/Codex
// deferred pattern — the one-shot command in ForkStartCommand (transient), a stable
// base in Command — so restart resumes the child via OpenCodeSessionID / bare
// opencode instead.
func TestCreateForkedOpenCode_DefersLaunchViaForkStartCommand(t *testing.T) {
	parent := NewInstanceWithTool("oc", t.TempDir(), "opencode")
	parent.OpenCodeSessionID = "ses_parent_123"
	parent.OpenCodeDetectedAt = time.Now()

	forked, cmd, err := parent.CreateForkedOpenCodeInstanceWithOptions("oc fork", "", nil)
	if err != nil {
		t.Fatalf("CreateForkedOpenCodeInstanceWithOptions: %v", err)
	}

	if forked.Command != "opencode" {
		t.Fatalf("forked.Command = %q, want \"opencode\" (stable base survives restart)", forked.Command)
	}
	if !forked.IsForkAwaitingStart || forked.ForkStartCommand != cmd {
		t.Fatalf("opencode fork must defer launch via ForkStartCommand/IsForkAwaitingStart (Pi pattern); got awaiting=%v forkCmd=%q cmd=%q",
			forked.IsForkAwaitingStart, forked.ForkStartCommand, cmd)
	}
	// The deferred command is the native one-shot fork, not a persistable resume.
	if !strings.Contains(cmd, "opencode -s ses_parent_123 --fork") {
		t.Fatalf("fork command should use native `opencode -s <parent> --fork`, got: %q", cmd)
	}
}

// TestOpenCodeForkUsesNativeForkFlag verifies the OpenCode fork command uses
// OpenCode's native `--fork` flag against the parent session id, rather than the
// older `opencode export | sed | import` clone. The launch is still anchored to
// the requested working dir with a `cd` (the multi-repo fork path depends on it
// because async session detection matches by ProjectPath), so the workDir must
// be shell-quoted to stay injection-safe.
func TestOpenCodeForkUsesNativeForkFlag(t *testing.T) {
	parent := NewInstanceWithTool("oc", `/tmp/project with "quote"`, "opencode")
	parent.OpenCodeSessionID = "ses_parent_123"
	parent.OpenCodeDetectedAt = time.Now()

	cmd, err := parent.ForkOpenCodeWithOptions("oc fork", "", nil)
	if err != nil {
		t.Fatalf("ForkOpenCodeWithOptions: %v", err)
	}

	if !strings.Contains(cmd, "opencode -s ses_parent_123 --fork") {
		t.Fatalf("fork command should use native `opencode -s <parent> --fork`, got: %q", cmd)
	}
	// The export/import clone path must be gone.
	for _, gone := range []string{"opencode export", "opencode import", "sed "} {
		if strings.Contains(cmd, gone) {
			t.Fatalf("fork command should not reference the old clone path (%q): %q", gone, cmd)
		}
	}
	// workDir is anchored via `cd`, but must be shell-quoted so a project path with
	// shell metacharacters cannot break out of the cd.
	if strings.Contains(cmd, `cd "/tmp/project with "quote""`) {
		t.Fatalf("workDir must not be interpolated raw (double-quoted): %q", cmd)
	}
	if want := "cd " + shellescape.Quote(`/tmp/project with "quote"`); !strings.Contains(cmd, want) {
		t.Fatalf("workDir should be shell-quoted in the cd anchor; want %q in %q", want, cmd)
	}
}
