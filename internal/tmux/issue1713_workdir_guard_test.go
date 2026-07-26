package tmux

// Regression tests for #1713: `add -w` (worktree creation) produced a session
// whose pane held only a broken shell —
//
//	shell-init: error retrieving current directory: getcwd: cannot access
//	parent directories: No such file or directory
//
// — while agent-deck reported the session as created, started and alive. The
// agent process never ran, and nothing short of `tmux capture-pane` revealed it.
//
// Root cause (verified against a bare tmux 3.7b, no agent-deck involved):
//
//	mkdir -p /tmp/x/srv /tmp/x/good
//	( cd /tmp/x/srv && tmux -L probe new-session -d -s seed )   # server cwd = /tmp/x/srv
//	rm -rf /tmp/x/srv                                           # unlink the server's cwd
//	tmux -L probe new-session -d -s t -c /tmp/x/good bash -c pwd
//	# -> pane_current_path is /tmp/x/srv (NOT the requested -c), and the pane
//	#    prints the shell-init getcwd error above.
//
// Once a tmux server's own working directory is unlinked, tmux stops honouring
// the requested -c start directory and every new pane inherits the server's dead
// cwd. That accounts for each observation in the report: the worktree really was
// fine, a restart hit the same server, three different worktree paths behaved
// identically, plain `add <existing good path>` without -w failed the same way,
// and host shells were unaffected. A worktree is a prime candidate for the
// unlinked directory, since worktrees get removed routinely.
//
// The sibling defect on the same code path: with a HEALTHY server,
// `new-session -c <missing dir>` does not fail either — tmux silently starts the
// pane in $HOME, so a session whose project directory was deleted came up
// "successfully" with the agent pointed at the user's home directory.
//
// Guards under test (workdir_guard.go):
//   - SpawnBaseDir/newSpawnCommand — servers agent-deck starts run from "/",
//     which can never be unlinked (prevention).
//   - resolveStartWorkDir — refuse to start on a missing directory instead of
//     silently landing in $HOME.
//   - verifyPaneWorkDir — after creation, detect a pane born in an unlinked
//     directory and fail loudly rather than recording it as alive.

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- resolveStartWorkDir -----------------------------------------------------

func TestResolveStartWorkDir_AcceptsExistingDirectory(t *testing.T) {
	dir := t.TempDir()

	got, err := resolveStartWorkDir(dir)
	require.NoError(t, err)
	assert.Equal(t, dir, got)
}

func TestResolveStartWorkDir_RejectsDeletedDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "worktree")
	require.NoError(t, os.Mkdir(dir, 0o755))
	require.NoError(t, os.RemoveAll(dir))

	_, err := resolveStartWorkDir(dir)

	require.Error(t, err, "a deleted project directory must refuse the start; tmux would "+
		"otherwise silently run the agent in $HOME (#1713)")
	assert.True(t, errors.Is(err, ErrWorkDirUnavailable))
	assert.Contains(t, err.Error(), dir, "the message must name the directory the user has to fix")
	assert.Contains(t, err.Error(), "$HOME", "the message must explain what tmux would have done instead")
}

func TestResolveStartWorkDir_RejectsFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))

	_, err := resolveStartWorkDir(file)

	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrWorkDirUnavailable))
	assert.Contains(t, err.Error(), "not a directory")
}

func TestResolveStartWorkDir_RejectsEmpty(t *testing.T) {
	for _, in := range []string{"", "   ", "\t"} {
		_, err := resolveStartWorkDir(in)
		require.Error(t, err, "input %q", in)
		assert.True(t, errors.Is(err, ErrWorkDirUnavailable), "input %q", in)
	}
}

// A relative path must be absolutised against agent-deck's own cwd. tmux
// resolves a relative -c against the SPAWNING client's cwd, which is now
// SpawnBaseDir ("/") — so leaving it relative would silently retarget the pane
// to /<relative-path>.
// Resolved against this process's cwd, not tmux's — deliberately without
// chdir()ing the test process, which would leak into anything else running.
func TestResolveStartWorkDir_AbsolutisesRelativePaths(t *testing.T) {
	cwd, err := os.Getwd()
	require.NoError(t, err)

	got, err := resolveStartWorkDir(".")
	require.NoError(t, err)
	assert.True(t, filepath.IsAbs(got), "resolved path must be absolute, got %q", got)

	gotInfo, err := os.Stat(got)
	require.NoError(t, err)
	wantInfo, err := os.Stat(cwd)
	require.NoError(t, err)
	assert.True(t, os.SameFile(gotInfo, wantInfo),
		"%q must resolve against agent-deck's cwd (%q), not tmux's", got, cwd)
}

// --- classifyPaneCwd --------------------------------------------------------

func TestClassifyPaneCwd_Verdicts(t *testing.T) {
	live := t.TempDir()
	other := t.TempDir()

	deleted := filepath.Join(t.TempDir(), "gone")
	require.NoError(t, os.Mkdir(deleted, 0o755))
	require.NoError(t, os.RemoveAll(deleted))

	assert.Equal(t, paneCwdOK, classifyPaneCwd(live, live))
	assert.Equal(t, paneCwdOK, classifyPaneCwd(live, live+string(filepath.Separator)),
		"a trailing separator names the same directory")
	assert.Equal(t, paneCwdElsewhere, classifyPaneCwd(live, other))
	assert.Equal(t, paneCwdDeleted, classifyPaneCwd(live, deleted))
	assert.Equal(t, paneCwdUnknown, classifyPaneCwd(live, ""),
		"an empty reading is indeterminate and must never fail a start")
	assert.Equal(t, paneCwdUnknown, classifyPaneCwd(live, "   "))
}

// macOS reports /private/tmp/x for a session started in /tmp/x. A symlinked
// prefix is the same directory and must not be reported as a mismatch.
func TestClassifyPaneCwd_ToleratesSymlinkedPrefix(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(real, link))

	assert.Equal(t, paneCwdOK, classifyPaneCwd(link, real))
	assert.Equal(t, paneCwdOK, classifyPaneCwd(real, link))
}

// --- verifyPaneWorkDir ------------------------------------------------------

// stubPanePathProbe replaces the pane-cwd probe with a scripted sequence of
// answers (the last one repeats) and shortens the re-check delay.
func stubPanePathProbe(t *testing.T, answers ...string) *int {
	t.Helper()
	calls := 0
	prevProbe := panePathProbe
	prevDelay := paneCwdRecheckDelay
	panePathProbe = func(*Session) (string, error) {
		idx := calls
		calls++
		if idx >= len(answers) {
			idx = len(answers) - 1
		}
		return answers[idx], nil
	}
	paneCwdRecheckDelay = time.Millisecond
	t.Cleanup(func() {
		panePathProbe = prevProbe
		paneCwdRecheckDelay = prevDelay
	})
	return &calls
}

func TestVerifyPaneWorkDir_FailsLoudlyOnDeletedPaneCwd(t *testing.T) {
	workDir := t.TempDir()
	deleted := filepath.Join(t.TempDir(), "poisoned-server-cwd")
	require.NoError(t, os.Mkdir(deleted, 0o755))
	require.NoError(t, os.RemoveAll(deleted))

	stubPanePathProbe(t, deleted)
	s := &Session{Name: "agentdeck_wt", SocketName: "ad1713"}

	err := s.verifyPaneWorkDir(workDir)

	require.Error(t, err, "a pane born in an unlinked directory holds only a broken shell; "+
		"reporting it as started is the #1713 failure")
	assert.True(t, errors.Is(err, ErrPaneCwdDeleted))
	assert.Contains(t, err.Error(), deleted, "must name the dead directory the pane landed in")
	assert.Contains(t, err.Error(), workDir, "must name the directory that was requested")
	assert.Contains(t, err.Error(), "ad1713", "must name the tmux server that needs restarting")
	assert.Contains(t, err.Error(), "shell-init", "must connect to the symptom the user sees in the pane")
}

func TestVerifyPaneWorkDir_AcceptsRequestedDirectory(t *testing.T) {
	workDir := t.TempDir()
	stubPanePathProbe(t, workDir)

	s := &Session{Name: "agentdeck_ok"}
	assert.NoError(t, s.verifyPaneWorkDir(workDir))
}

// A pane command may legitimately change directory (the fork / multi-repo paths
// build `cd <dir> && …`), so a real-but-different directory is a warning, never
// a failed start.
func TestVerifyPaneWorkDir_ToleratesDifferentLiveDirectory(t *testing.T) {
	workDir := t.TempDir()
	elsewhere := t.TempDir()
	stubPanePathProbe(t, elsewhere)

	s := &Session{Name: "agentdeck_cd"}
	assert.NoError(t, s.verifyPaneWorkDir(workDir))
}

// tmux creates the pane process before it has chdir()ed, so the first reading
// can still show the server's cwd. A transient "deleted" reading must be
// re-confirmed rather than failing a healthy start.
func TestVerifyPaneWorkDir_RetriesTransientDeletedReading(t *testing.T) {
	workDir := t.TempDir()
	stale := filepath.Join(t.TempDir(), "stale")
	require.NoError(t, os.Mkdir(stale, 0o755))
	require.NoError(t, os.RemoveAll(stale))

	calls := stubPanePathProbe(t, stale, workDir)

	s := &Session{Name: "agentdeck_race"}
	require.NoError(t, s.verifyPaneWorkDir(workDir))
	assert.Equal(t, 2, *calls, "a deleted reading must be re-probed before it is believed")
}

func TestVerifyPaneWorkDir_ProbeFailureIsNotAFailedStart(t *testing.T) {
	prev := panePathProbe
	panePathProbe = func(*Session) (string, error) { return "", fmt.Errorf("server busy") }
	t.Cleanup(func() { panePathProbe = prev })

	s := &Session{Name: "agentdeck_probe_err"}
	assert.NoError(t, s.verifyPaneWorkDir(t.TempDir()),
		"an unreadable probe is indeterminate and must never fail a start")
}

// --- Start() integration ----------------------------------------------------

// A deleted working directory must be refused before anything is spawned, so no
// half-alive session is left behind for the status layer to call healthy.
func TestStart_RefusesDeletedWorkDirWithoutSpawning(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "worktree")
	require.NoError(t, os.Mkdir(dir, 0o755))
	require.NoError(t, os.RemoveAll(dir))

	spawns := 0
	prev := execCommand
	execCommand = func(name string, arg ...string) *exec.Cmd {
		spawns++
		return prev("true")
	}
	t.Cleanup(func() { execCommand = prev })

	s := &Session{Name: "agentdeck_missing_wt", SocketName: "ad1713-none", WorkDir: dir}
	err := s.Start("claude")

	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrWorkDirUnavailable))
	assert.Zero(t, spawns, "nothing may be spawned once the working directory is known to be gone")
}

// An SSH session's local path is only a placeholder — the pane runs an ssh
// client and the project lives on the remote host — so a missing local
// directory must not make the session unstartable. It keeps tmux's historical
// $HOME landing, and the pane check is skipped for the same reason.
func TestStart_PlaceholderWorkDirIsNotRefused(t *testing.T) {
	skipIfNoTmuxBinary(t)

	dir := filepath.Join(t.TempDir(), "local-placeholder")
	require.NoError(t, os.Mkdir(dir, 0o755))
	require.NoError(t, os.RemoveAll(dir))

	socket := privateSocketName1713(t)
	s := &Session{
		Name:                 "agentdeck_1713_ssh",
		DisplayName:          "ssh-placeholder",
		SocketName:           socket,
		WorkDir:              dir,
		WorkDirIsPlaceholder: true,
	}

	require.NoError(t, s.Start(""),
		"a remote session must not be blocked by a local placeholder directory")
	t.Cleanup(func() { _ = s.Kill() })
	assert.True(t, s.Exists())
}

func TestVerifyPaneWorkDir_SkippedForPlaceholderWorkDir(t *testing.T) {
	deleted := filepath.Join(t.TempDir(), "gone")
	require.NoError(t, os.Mkdir(deleted, 0o755))
	require.NoError(t, os.RemoveAll(deleted))
	stubPanePathProbe(t, deleted)

	placeholder := &Session{Name: "agentdeck_ssh", WorkDirIsPlaceholder: true}
	assert.NoError(t, placeholder.verifyPaneWorkDirUnlessPlaceholder(t.TempDir()))

	local := &Session{Name: "agentdeck_local"}
	assert.Error(t, local.verifyPaneWorkDirUnlessPlaceholder(t.TempDir()),
		"a local session must still be checked")
}

// End-to-end on a real tmux server: when the pane is not in a usable directory,
// Start must report the failure AND leave no session behind. The poisoned-server
// condition itself is simulated through the probe seam so the test does not
// depend on tmux internals, but everything else — session creation, the guard,
// the teardown — is real.
func TestStart_KillsSessionBornInDeletedCwd(t *testing.T) {
	skipIfNoTmuxBinary(t)

	socket := privateSocketName1713(t)
	workDir := t.TempDir()

	deleted := filepath.Join(t.TempDir(), "server-cwd")
	require.NoError(t, os.Mkdir(deleted, 0o755))
	require.NoError(t, os.RemoveAll(deleted))
	stubPanePathProbe(t, deleted)

	s := &Session{
		Name:        "agentdeck_1713_dead",
		DisplayName: "dead-cwd",
		SocketName:  socket,
		WorkDir:     workDir,
	}

	err := s.Start("")

	require.Error(t, err, "Start must not report success for a pane that cannot run the agent")
	assert.True(t, errors.Is(err, ErrPaneCwdDeleted))
	assert.False(t, s.Exists(),
		"the broken session must be torn down, not left registered as alive (#1713)")
}

// The real probe against a real tmux session: a pane whose directory is deleted
// after birth is classified as paneCwdDeleted. macOS keeps reporting the stale
// path and Linux tmux strips the " (deleted)" suffix from /proc/<pid>/cwd, so
// both platforms hand us a path that no longer exists.
func TestVerifyPaneWorkDir_RealTmuxPaneWithDeletedDirectory(t *testing.T) {
	skipIfNoTmuxBinary(t)

	socket := privateSocketName1713(t)
	workDir := filepath.Join(t.TempDir(), "worktree")
	require.NoError(t, os.Mkdir(workDir, 0o755))

	s := &Session{
		Name:        "agentdeck_1713_real",
		DisplayName: "real-probe",
		SocketName:  socket,
		WorkDir:     workDir,
	}
	require.NoError(t, s.Start(""), "session must start while the directory still exists")

	// tmux creates the pane process before it has chdir()ed, so wait for the
	// pane to actually settle in the requested directory before deleting it —
	// otherwise this test could delete a directory the pane never entered.
	requirePaneSettledIn(t, s, workDir)
	require.NoError(t, s.verifyPaneWorkDir(workDir),
		"sanity: the pane must be in the requested directory to begin with")

	require.NoError(t, os.RemoveAll(workDir))

	err := s.verifyPaneWorkDir(workDir)
	require.Error(t, err, "a pane sitting in a deleted directory must be reported, not ignored")
	assert.True(t, errors.Is(err, ErrPaneCwdDeleted))
}

// --- prevention: spawns run from a directory that cannot be deleted ---------

func TestNewSpawnCommand_RunsFromSpawnBaseDir(t *testing.T) {
	cmd := newSpawnCommand("tmux", "-V")
	assert.Equal(t, SpawnBaseDir, cmd.Dir,
		"a tmux server inherits its cwd from the process that starts it; spawning from %q "+
			"is what keeps a deleted directory from poisoning the server (#1713)", SpawnBaseDir)

	info, err := os.Stat(SpawnBaseDir)
	require.NoError(t, err, "SpawnBaseDir must exist on every supported platform")
	assert.True(t, info.IsDir())
}

// Lint: every subprocess spawn inside Session.Start must go through
// newSpawnCommand. A bare execCommand there re-introduces the poisoning — the
// tmux server would inherit agent-deck's own cwd, which is frequently a
// worktree that later gets removed.
func TestStartSpawnsGoThroughNewSpawnCommand(t *testing.T) {
	root := moduleRoot(t)
	path := filepath.Join(root, "internal", "tmux", "tmux.go")

	src, err := os.ReadFile(path)
	require.NoError(t, err)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, 0)
	require.NoError(t, err)

	var body string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Start" || fn.Recv == nil || fn.Body == nil {
			continue
		}
		body = string(src[fset.Position(fn.Body.Pos()).Offset:fset.Position(fn.Body.End()).Offset])
		break
	}
	require.NotEmpty(t, body, "Session.Start not found in %s", path)

	assert.NotContains(t, body, "execCommand(",
		"Session.Start must spawn via newSpawnCommand so the spawn (and any tmux server it "+
			"starts) runs from %q — see workdir_guard.go (#1713)", SpawnBaseDir)
	assert.Contains(t, body, "newSpawnCommand(")
	assert.Contains(t, body, "verifyPaneWorkDirUnlessPlaceholder(",
		"Start must verify where the pane actually landed before reporting success (#1713)")
	assert.Contains(t, body, "resolveStartWorkDir(",
		"Start must validate the working directory before spawning (#1713)")
}

// requirePaneSettledIn blocks until tmux reports the pane's cwd as the given
// directory, so tests that depend on the pane really being there are not racing
// the child's chdir().
func requirePaneSettledIn(t *testing.T, s *Session, dir string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		reported, err := panePathProbe(s)
		if err == nil {
			last = reported
			if classifyPaneCwd(dir, reported) == paneCwdOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("pane never settled in %q (last reading: %q)", dir, last)
}

// privateSocketName1713 returns a deterministic -L socket name for this test and
// registers teardown that kills the server on the SAME socket. An env mismatch
// between spawn and cleanup makes kill-server a silent no-op and leaks a server
// plus its ptys (the 2026-07-18 pty-exhaustion incident).
func privateSocketName1713(t *testing.T) string {
	t.Helper()
	socket := "ad1713-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	if len(socket) > 40 {
		socket = socket[:40]
	}
	kill := func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() }
	kill() // clear anything a previously aborted run stranded
	t.Cleanup(kill)
	return socket
}
