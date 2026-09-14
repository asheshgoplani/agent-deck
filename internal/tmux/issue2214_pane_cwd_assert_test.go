package tmux

// Regression tests for #2214: every pane inherits the tmux server's cwd
// instead of the session's project dir.
//
// Root cause (extends #1713): even when -c points at a valid directory, a tmux
// server whose own cwd has been deleted births every new pane in that dead
// directory. The pane's shell prints
//
//	shell-init: error retrieving current directory: getcwd: ...
//
// and the agent exits in ~256 ms. SpawnBaseDir ("/") prevents this for servers
// agent-deck starts, but an external server started before the fix was deployed
// (or by another tool) remains poisoned indefinitely, failing every spawn.
//
// Fix (workdir_guard.go): when verifyPaneWorkDir returns ErrPaneCwdDeleted,
// retry the spawn once with the command wrapped so the pane asserts its own
// directory:
//
//	/bin/sh -c 'cd -- <project dir> && <original command>'
//
// The shell builtin cd bypasses the inherited dead vnode and puts the agent in
// the right tree. Traps avoided:
//   - "exec"-prefixed commands: cd is a builtin inside /bin/sh; the outer exec
//     is never parsed as the top-level statement.
//   - Worktree sessions: the dir passed to wrapCommandForCwdAssert is already
//     the final resolved path (worktrees are resolved before Start() is called).

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- wrapCommandForCwdAssert -------------------------------------------------

func TestWrapCommandForCwdAssert_PlainPathAndCommand(t *testing.T) {
	got := wrapCommandForCwdAssert("/home/user/project", "claude --session-id abc")
	assert.Equal(t, `/bin/sh -c 'cd -- "/home/user/project" && claude --session-id abc'`, got)
}

func TestWrapCommandForCwdAssert_ExecPrefixedCommand(t *testing.T) {
	// "exec"-prefixed commands were the original trap (#2214): `exec cd '<dir>'`
	// execs a binary named "cd" and exits 127. Inside /bin/sh -c the exec is a
	// shell builtin, so `cd` is also a builtin and the sequence works correctly.
	got := wrapCommandForCwdAssert("/project", "exec claude --resume 91fd7978")
	assert.Equal(t, `/bin/sh -c 'cd -- "/project" && exec claude --resume 91fd7978'`, got)
}

func TestWrapCommandForCwdAssert_EmptyCommand(t *testing.T) {
	// An empty command (shell sessions) must produce a valid /bin/sh invocation
	// that just changes directory. The shell exits 0 after the cd.
	got := wrapCommandForCwdAssert("/project", "")
	assert.Equal(t, `/bin/sh -c 'cd -- "/project"'`, got)
}

func TestWrapCommandForCwdAssert_SingleQuoteInDir(t *testing.T) {
	// Paths containing single quotes must be embedded safely.
	got := wrapCommandForCwdAssert("/path/to/it's project", "claude")
	// Single quotes are escaped as '"'"': it's -> it'"'"'s
	assert.Contains(t, got, "/bin/sh -c '")
	assert.Contains(t, got, "it'\"'\"'s")
	assert.Contains(t, got, "claude")
}

func TestWrapCommandForCwdAssert_SingleQuoteInCommand(t *testing.T) {
	got := wrapCommandForCwdAssert("/project", "bash -c 'stty susp undef; claude'")
	assert.Contains(t, got, "/bin/sh -c '")
	assert.Contains(t, got, "/project")
	// The single quotes inside the command must be escaped with '"'"'.
	// Check that the escaped form of the opening quote appears ('"'"'stty...) and
	// that the raw unescaped opening sequence bash -c 'stty is not present.
	assert.Contains(t, got, `'"'"'stty`, "inner single quote must use the end-quote/literal/reopen escape")
	assert.NotContains(t, got, "bash -c 'stty", "unescaped inner single quote would break the outer shell string")
}

func TestWrapCommandForCwdAssert_PathWithSpaces(t *testing.T) {
	// Spaces in directory paths are valid and must survive the embedding.
	// The dir is double-quoted inside the inner sh script so the space is not
	// treated as a word boundary by the shell.
	got := wrapCommandForCwdAssert("/home/user/my project", "claude")
	assert.Equal(t, `/bin/sh -c 'cd -- "/home/user/my project" && claude'`, got)
}

// --- recovery in Start() when server is poisoned (#2214) --------------------

// TestStart_RetriesWithCwdAssertOnPoisonedServer verifies that when the first
// spawn lands in a deleted cwd (poisoned server), Start() retries once with a
// command that explicitly cds into the project directory. The second pane probe
// returns a valid directory, so Start() succeeds instead of failing.
func TestStart_RetriesWithCwdAssertOnPoisonedServer(t *testing.T) {
	skipIfNoTmuxBinary(t)

	workDir := t.TempDir()
	deleted := filepath.Join(t.TempDir(), "server-cwd")
	require.NoError(t, os.Mkdir(deleted, 0o755))
	require.NoError(t, os.RemoveAll(deleted))

	// Probe sequence: first call returns the deleted path (simulating a poisoned
	// server on the first spawn), subsequent calls return the good workDir (the
	// cd-wrapped retry landed correctly).
	probeCallCount := 0
	prev := panePathProbe
	prevDelay := paneCwdRecheckDelay
	panePathProbe = func(*Session) (string, error) {
		probeCallCount++
		// paneCwdProbeAttempts = 3 re-probes per verifyPaneWorkDir call.
		// The first three probes belong to the first attempt; everything after
		// that belongs to the retry.
		if probeCallCount <= paneCwdProbeAttempts {
			return deleted, nil
		}
		return workDir, nil
	}
	paneCwdRecheckDelay = 0
	t.Cleanup(func() {
		panePathProbe = prev
		paneCwdRecheckDelay = prevDelay
	})

	socket := privateSocketName2214(t)
	s := &Session{
		Name:        "agentdeck_2214_retry",
		DisplayName: "retry-cwd-assert",
		SocketName:  socket,
		WorkDir:     workDir,
	}

	err := s.Start("")

	require.NoError(t, err, "Start must succeed when the retry with cd-assert lands in the right directory")
	assert.True(t, s.Exists(), "the session must be alive after the successful retry")
	t.Cleanup(func() { _ = s.Kill() })

	// The wrapped command must reference the project dir.
	assert.Contains(t, s.Command, workDir,
		"Start must store the cwd-assert wrapped command so Restart uses the same safe form")
}

// TestStart_PropagatesErrorWhenRetryAlsoFails verifies that when both the
// initial spawn and the cwd-assert retry land in a deleted directory, Start()
// still returns ErrPaneCwdDeleted and leaves no live session behind.
func TestStart_PropagatesErrorWhenRetryAlsoFails(t *testing.T) {
	skipIfNoTmuxBinary(t)

	workDir := t.TempDir()
	deleted := filepath.Join(t.TempDir(), "server-cwd")
	require.NoError(t, os.Mkdir(deleted, 0o755))
	require.NoError(t, os.RemoveAll(deleted))

	// All probe calls return the deleted path — the retry also fails.
	stubPanePathProbe(t, deleted)

	socket := privateSocketName2214(t)
	s := &Session{
		Name:        "agentdeck_2214_fail",
		DisplayName: "retry-also-fails",
		SocketName:  socket,
		WorkDir:     workDir,
	}

	err := s.Start("")

	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPaneCwdDeleted))
	assert.False(t, s.Exists(), "no broken session must be left behind after a failed retry")
}

// TestWrapCommandForCwdAssert_IsRoundTrippable verifies that the shell string
// produced by wrapCommandForCwdAssert is parseable by /bin/sh. It runs
// `sh -c <wrapped>` and checks the exit code. A malformed quoting would cause
// a parse error and non-zero exit.
func TestWrapCommandForCwdAssert_IsRoundTrippable(t *testing.T) {
	dir := t.TempDir()
	// The command just exits 0.
	wrapped := wrapCommandForCwdAssert(dir, "true")
	// sh -c interprets the wrapped string.
	assert.True(t, strings.HasPrefix(wrapped, "/bin/sh -c '"),
		"wrapper must always start with /bin/sh -c '")
	// Actually execute it: pass the entire wrapped string to sh -c so the outer
	// shell parses the quoting, then runs /bin/sh -c '<inner>'. Exit code 0 means
	// both the quoting was syntactically valid and cd succeeded.
	cmd := exec.Command("sh", "-c", wrapped)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "wrapped command must execute without error (output: %s)", out)
}

// --- helpers -----------------------------------------------------------------

func privateSocketName2214(t *testing.T) string {
	t.Helper()
	socket := "ad2214-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	if len(socket) > 40 {
		socket = socket[:40]
	}
	kill := func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() }
	kill()
	t.Cleanup(kill)
	return socket
}
