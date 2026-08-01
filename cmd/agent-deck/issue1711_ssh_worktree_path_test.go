package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestResolveSSHAddPaths covers the --ssh path-routing rule for `agent-deck
// add`. Regression for asheshgoplani/agent-deck#1711 / #1710: `add
// <remote-worktree-path> --ssh <host>` silently dropped the positional path
// on the floor instead of using it as the remote directory, so the launched
// session never cd'd into the intended remote worktree and instead ran in
// the SSH login shell's default directory.
//
// The routing rule takes the RAW positional argument, never a locally
// resolved one (session.ExpandPath + filepath.Abs describe the controller
// machine, not the remote host): a `~/x` or `./x` positional path is
// refused rather than silently misresolved, because wrapForSSH single-quotes
// SSHRemotePath before handing it to the remote shell, so a stored `~/x` or
// `$VAR/x` would reach the remote host inert (no tilde/variable expansion
// happens inside single quotes).
func TestResolveSSHAddPaths(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}

	tests := []struct {
		name                 string
		explicitPathProvided bool
		positionalPath       string
		explicitRemotePath   string
		wantRemotePath       string
		wantErr              bool
	}{
		{
			name:                 "absolute positional path with --ssh becomes the remote path",
			explicitPathProvided: true,
			positionalPath:       "/home/liam/pt-worktrees/some-feature",
			explicitRemotePath:   "",
			wantRemotePath:       "/home/liam/pt-worktrees/some-feature",
		},
		{
			name:                 "explicit --remote-path wins over positional path",
			explicitPathProvided: true,
			positionalPath:       "/home/liam/pt-worktrees/some-feature",
			explicitRemotePath:   "/home/liam/other-repo",
			wantRemotePath:       "/home/liam/other-repo",
		},
		{
			name:                 "no positional path, only --remote-path (documented pattern)",
			explicitPathProvided: false,
			positionalPath:       cwd, // matches handleAdd's cwd-fallback default
			explicitRemotePath:   "/home/liam/PointyTooling",
			wantRemotePath:       "/home/liam/PointyTooling",
		},
		{
			name:                 "neither positional path nor --remote-path given",
			explicitPathProvided: false,
			positionalPath:       cwd,
			explicitRemotePath:   "",
			wantRemotePath:       "",
		},
		{
			name:                 "tilde positional path is refused, not locally expanded",
			explicitPathProvided: true,
			positionalPath:       "~/pt-worktrees/tilde-feature",
			explicitRemotePath:   "",
			wantErr:              true,
		},
		{
			name:                 "relative positional path is refused, not resolved against local CWD",
			explicitPathProvided: true,
			positionalPath:       "./sub/rel-feature",
			explicitRemotePath:   "",
			wantErr:              true,
		},
		{
			name:                 "remote env-var positional path is refused",
			explicitPathProvided: true,
			positionalPath:       "$REMOTE_HOME/pt-worktrees/var-feature",
			explicitRemotePath:   "",
			wantErr:              true,
		},
		{
			name:                 "non-absolute positional path is refused even when explicit --remote-path is also non-absolute-shaped but present (--remote-path still wins, no validation applied to it)",
			explicitPathProvided: true,
			positionalPath:       "~/ignored",
			explicitRemotePath:   "/home/liam/other-repo",
			wantRemotePath:       "/home/liam/other-repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotLocal, gotRemote, err := resolveSSHAddPaths(tt.explicitPathProvided, tt.positionalPath, tt.explicitRemotePath)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveSSHAddPaths() expected an error refusing a non-absolute remote path, got remote=%q", gotRemote)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveSSHAddPaths() error = %v", err)
			}
			if gotLocal != cwd {
				t.Fatalf("resolveSSHAddPaths() local placeholder = %q, want CWD %q (an --ssh session's local\n"+
					"placeholder path must always be CWD, never the remote path, so tmux never launches\n"+
					"into a path that only exists on the remote host)", gotLocal, cwd)
			}
			if gotRemote != tt.wantRemotePath {
				t.Fatalf("resolveSSHAddPaths() remote path = %q, want %q", gotRemote, tt.wantRemotePath)
			}
		})
	}
}

// buildAgentDeckBinaryForSSHTest builds the agent-deck binary once for the
// subprocess tests below and returns its path. Fails (does not skip) on
// build failure: a silent skip here would let this regression coverage
// disappear without a trace whenever the build is broken, which is exactly
// when a CI run most needs it to fail loudly instead.
func buildAgentDeckBinaryForSSHTest(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell/ssh-flag fixture")
	}
	dir := t.TempDir()
	binPath := filepath.Join(dir, "agent-deck")
	cmd := exec.Command("go", "build", "-o", binPath, "./cmd/agent-deck")
	cmd.Dir = repoRootForSSHTest(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("could not build agent-deck binary for subprocess test: %v\n%s", err, out)
	}
	return binPath
}

func repoRootForSSHTest(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	// This test file lives in cmd/agent-deck; the module root is two levels up.
	return filepath.Join(wd, "..", "..")
}

// TestSSHWorktreeCreationRefused proves that `add --ssh <host> --remote-path
// <path> -w <branch> -b` refuses cleanly instead of silently creating a git
// worktree in the local checkout the test happens to run from (failure mode
// 2 from asheshgoplani/agent-deck#1711 / #1710: this combination previously
// created the worktree on the LOCAL Mac, ignoring --remote-path entirely).
func TestSSHWorktreeCreationRefused(t *testing.T) {
	bin := buildAgentDeckBinaryForSSHTest(t)

	home := t.TempDir()
	profileDir := filepath.Join(home, ".agent-deck-test-profile")
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatalf("mkdir profile dir: %v", err)
	}

	// A real repo with a commit, so any accidental local worktree creation
	// would be independently detectable (it must NOT appear).
	repo := filepath.Join(home, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"-c", "user.name=T", "-c", "user.email=t@test", "commit", "--allow-empty", "-m", "init"},
	} {
		gc := exec.Command("git", args...)
		gc.Dir = repo
		gc.Env = append(os.Environ(), "HOME="+home)
		if out, err := gc.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	cmd := exec.Command(bin,
		"--profile", "_test_1711",
		"add",
		"--ssh", "test-host",
		"--remote-path", "/home/remote-user/some-repo",
		"-w", "some-feature-branch",
		"-b",
		"-t", "should-not-be-created",
		"-c", "claude",
	)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "HOME="+home, "AGENTDECK_PROFILE=_test_1711")
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatalf("expected `add --ssh --remote-path -w -b` to exit non-zero (refused), got success:\n%s", out)
	}
	if !strings.Contains(string(out), "cannot be combined with --ssh") {
		t.Fatalf("expected a clear refusal message naming the --ssh/-w conflict, got:\n%s", out)
	}

	// The critical regression check: no worktree may have been created in
	// the LOCAL repo the subprocess ran from.
	wtOut, wtErr := exec.Command("git", "-C", repo, "worktree", "list").CombinedOutput()
	if wtErr != nil {
		t.Fatalf("git worktree list: %v\n%s", wtErr, wtOut)
	}
	if strings.Count(strings.TrimSpace(string(wtOut)), "\n") != 0 {
		t.Fatalf("expected no worktrees to be created locally, got:\n%s", wtOut)
	}
}

// TestSSHAddPositionalPathCLI drives the actual failure mode #1711/#1710
// were filed about through the full CLI, not just the resolveSSHAddPaths
// unit: `agent-deck add <remote-path> --ssh <host>` (bare positional path,
// no --remote-path). Prior to this fix the positional path was silently
// dropped into the session's local ProjectPath placeholder and never
// reached SSHRemotePath. handleAdd prints the resolved remote path back on
// the "  Remote:  <path>" line, so asserting on that output line proves the
// raw positional path survives CLI parsing, add-command flag handling, and
// resolveSSHAddPaths unmodified.
func TestSSHAddPositionalPathCLI(t *testing.T) {
	bin := buildAgentDeckBinaryForSSHTest(t)

	home := t.TempDir()
	profileDir := filepath.Join(home, ".agent-deck-test-profile")
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatalf("mkdir profile dir: %v", err)
	}

	const remotePath = "/home/remote-user/pt-worktrees/some-feature"
	cmd := exec.Command(bin,
		"--profile", "_test_1711_positional",
		"add", remotePath,
		"--ssh", "test-host",
		"-t", "positional-remote-path-case",
		"-c", "claude",
	)
	cmd.Env = append(os.Environ(), "HOME="+home, "AGENTDECK_PROFILE=_test_1711_positional")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("add --ssh <host> <positional-path>: %v\n%s", err, out)
	}
	wantLine := "Remote:  " + remotePath
	if !strings.Contains(string(out), wantLine) {
		t.Fatalf("expected output to report the unmodified positional path as the remote path (%q), got:\n%s", wantLine, out)
	}
}
