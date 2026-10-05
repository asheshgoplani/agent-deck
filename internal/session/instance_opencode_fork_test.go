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

// TestOpenCodeV2ForkUsesSessionAPI: OpenCode 2's TUI rejects --fork, so a fork
// calls the session API, then resumes the child. Worktree forks also move the
// child onto the new tree so detection (matched by ProjectPath) can see it.
func TestOpenCodeV2ForkUsesSessionAPI(t *testing.T) {
	pinOpenCodeMajorVersion(t, 2, true)
	parent := NewInstanceWithTool("oc", `/tmp/project with "quote"`, "opencode")
	parent.OpenCodeSessionID = "ses_parent_123"
	parent.OpenCodeDetectedAt = time.Now()
	if err := parent.SetOpenCodeOptions(&OpenCodeOptions{Model: "openai/gpt-5.5", Agent: "build"}); err != nil {
		t.Fatalf("SetOpenCodeOptions: %v", err)
	}

	cmd, err := parent.ForkOpenCodeWithOptions("oc fork", "", nil)
	if err != nil {
		t.Fatalf("ForkOpenCodeWithOptions: %v", err)
	}

	for _, want := range []string{
		"cd -- " + shellescape.Quote(`/tmp/project with "quote"`),
		"opencode api session.fork --param sessionID=ses_parent_123",
		`exec opencode -s "$new_id"`,
		"session.update",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("v2 fork command missing %q:\n%s", want, cmd)
		}
	}
	for _, gone := range []string{" --fork", " -m ", " --agent ", " --port ", "opencode export", "opencode import"} {
		if strings.Contains(cmd, gone) {
			t.Errorf("v2 fork command must not contain %q:\n%s", gone, cmd)
		}
	}
	// Same directory as the parent: no move. A no-op move can fail on the API.
	if strings.Contains(cmd, "session.move") {
		t.Fatalf("same-directory v2 fork must not move the session: %s", cmd)
	}
}

func TestOpenCodeV2ForkMovesOntoWorktree(t *testing.T) {
	pinOpenCodeMajorVersion(t, 2, true)
	parent := NewInstanceWithTool("oc", "/tmp/original", "opencode")
	parent.OpenCodeSessionID = "ses_parent"
	parent.OpenCodeDetectedAt = time.Now()

	opts := &ClaudeOptions{
		WorkDir:          `/tmp/wt with space`,
		WorktreePath:     `/tmp/wt with space`,
		WorktreeRepoRoot: "/tmp/original",
		WorktreeBranch:   "fork/oc",
	}
	forked, cmd, err := parent.CreateForkedInstanceForTool(`say "hi"`, "experiments", opts)
	if err != nil {
		t.Fatalf("CreateForkedInstanceForTool: %v", err)
	}
	if forked.ProjectPath != `/tmp/wt with space` {
		t.Fatalf("ProjectPath = %q, want worktree dir", forked.ProjectPath)
	}
	if forked.WorktreeBranch != "fork/oc" || forked.WorktreeRepoRoot != "/tmp/original" {
		t.Fatalf("worktree metadata not copied: %+v", forked)
	}
	if !strings.Contains(cmd, "session.move") {
		t.Fatalf("worktree fork must move the child onto the worktree: %s", cmd)
	}
	if !strings.Contains(cmd, shellescape.Quote(`{"directory":"/tmp/wt with space"}`)) &&
		!strings.Contains(cmd, `"/tmp/wt with space"`) {
		t.Fatalf("move body must carry the worktree path: %s", cmd)
	}
	if !strings.Contains(cmd, "session.update") || !strings.Contains(cmd, `say \"hi\"`) {
		t.Fatalf("fork title must be JSON-encoded in the update body: %s", cmd)
	}
	if strings.Contains(cmd, " --fork") {
		t.Fatalf("v2 worktree fork must not pass the 1.x --fork flag: %s", cmd)
	}
}

func TestOpenCode2CommandForcesV2ForkWithoutProbe(t *testing.T) {
	// Probe stays "unknown" (TestMain). The shim name is enough.
	parent := NewInstanceWithTool("oc", "/tmp/original", "opencode")
	parent.Command = "opencode2"
	parent.OpenCodeSessionID = "ses_parent"
	parent.OpenCodeDetectedAt = time.Now()

	forked, cmd, err := parent.CreateForkedOpenCodeInstanceWithOptionsAndWorkDir(
		"child", "", nil, "/tmp/original-wt", "/tmp/original", "fork/child",
	)
	if err != nil {
		t.Fatalf("create fork: %v", err)
	}
	if forked.Command != "opencode2" {
		t.Fatalf("forked.Command = %q, want opencode2 so restart uses the shim", forked.Command)
	}
	if !strings.Contains(cmd, "opencode2 api session.fork") || !strings.Contains(cmd, "session.move") {
		t.Fatalf("opencode2 fork must use the v2 API and move onto the worktree: %s", cmd)
	}
	if strings.Contains(cmd, " --fork") {
		t.Fatalf("opencode2 fork must not use the 1.x --fork flag: %s", cmd)
	}
}

func TestOpenCodeFork_SSHKeepsV1FlagWhenProbeWouldBeV2(t *testing.T) {
	pinOpenCodeMajorVersion(t, 2, true)
	parent := &Instance{
		Tool:               "opencode",
		ProjectPath:        "/tmp/original",
		OpenCodeSessionID:  "ses_parent",
		OpenCodeDetectedAt: time.Now(),
		SSHHost:            "devbox",
	}
	cmd, err := parent.ForkOpenCode("child", "")
	if err != nil {
		t.Fatalf("ForkOpenCode: %v", err)
	}
	if !strings.Contains(cmd, "opencode -s ses_parent --fork") {
		t.Fatalf("SSH fork cannot see the remote binary, so it keeps the 1.x --fork command: %s", cmd)
	}
}

func TestSetField_OpenCode2KeepsToolIdentity(t *testing.T) {
	inst := NewInstanceWithTool("oc", t.TempDir(), "claude")
	if _, _, err := SetField(inst, FieldTool, "opencode2", nil); err != nil {
		t.Fatalf("SetField: %v", err)
	}
	if inst.Tool != "opencode" || inst.Command != "opencode2" {
		t.Fatalf("tool=%q command=%q, want opencode/opencode2", inst.Tool, inst.Command)
	}
	if inst.OpenCodeCommandName() != "opencode2" {
		t.Fatalf("OpenCodeCommandName = %q, want opencode2", inst.OpenCodeCommandName())
	}
	if _, _, err := SetField(inst, FieldTool, "opencode", nil); err != nil {
		t.Fatalf("SetField opencode: %v", err)
	}
	if inst.Command != "opencode" {
		t.Fatalf("switching back to opencode left command %q", inst.Command)
	}
}

func TestOpenCode2CommandOmitsV1LaunchFlags(t *testing.T) {
	inst := newOpenCodeResumeInstance(t)
	inst.Command = "opencode2"
	cmd := inst.buildOpenCodeCommand("opencode2")
	if !strings.Contains(cmd, "opencode2 -s ses_ABC123") {
		t.Fatalf("opencode2 resume = %q, want opencode2 -s", cmd)
	}
	for _, flag := range []string{" -m ", " --agent ", " --port "} {
		if strings.Contains(cmd, flag) {
			t.Errorf("opencode2 launch must not carry %q: %q", strings.TrimSpace(flag), cmd)
		}
	}
}
