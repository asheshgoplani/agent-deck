package tmux

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Issue #1713 — "add -w spawns a shell that dies with shell-init: error
// retrieving current directory".
//
// tmux never fails a spawn because of a bad working directory; it silently
// substitutes one. Two observable behaviours (verified on tmux 3.7b, macOS,
// and reproducible with a bare tmux) made agent-deck report a healthy session
// that had never run the agent:
//
//  1. `new-session -c <missing dir>` → tmux starts the pane in $HOME instead.
//     The agent boots, looks alive, and operates on the WRONG tree (the user's
//     home directory) while agent-deck records success.
//
//  2. If the tmux SERVER's own working directory has been unlinked, tmux stops
//     honouring `-c` altogether: every new pane inherits the server's dead cwd.
//     The pane's first process then prints
//
//         shell-init: error retrieving current directory: getcwd: cannot
//         access parent directories: No such file or directory
//
//     and the agent never runs. This is the reported failure, and it explains
//     every observation in #1713: the worktree itself was fine (verified from
//     another shell), the same error followed a restart, it reproduced across
//     three different worktree paths, it reproduced WITHOUT -w when pointing at
//     an already-good directory, and plain shells on the host worked fine —
//     because none of those touch the poisoned tmux server.
//
//     A server picks up a deletable cwd by inheriting it from whatever process
//     first started it. A worktree is exactly the kind of directory that later
//     gets removed (`agent-deck rm`, `worktree cleanup`, a manual cleanup
//     during resource pressure), which is why worktree users hit this first.
//
// The three guards below address that, in order of preference:
//
//   - SpawnBaseDir: every tmux spawn agent-deck issues runs from "/", so a
//     server agent-deck starts can never inherit a directory that is later
//     deleted. Prevention — no diagnosis needed.
//   - resolveStartWorkDir: refuse to start at all when the session's directory
//     is gone, instead of silently running the agent in $HOME.
//   - verifyPaneWorkDir: after creation, notice a pane that was born in an
//     unlinked directory (an already-poisoned server, which the prevention above
//     cannot retro-fix) and fail loudly instead of recording it as alive.

// SpawnBaseDir is the working directory agent-deck uses for the tmux
// subprocesses it spawns. "/" is the one directory that cannot be deleted or
// renamed, so a tmux server started by agent-deck can never end up holding an
// unlinked cwd (behaviour 2 above).
//
// It is safe for the tmux argv agent-deck builds: start directories are passed
// as absolute paths via -c (resolveStartWorkDir absolutises them first), so no
// tmux argument is interpreted relative to the client's cwd.
const SpawnBaseDir = "/"

// newSpawnCommand builds a tmux (or systemd-run) spawn command that runs from
// SpawnBaseDir. Every server-creating invocation must go through this — see
// TestNewSpawnCommand_RunsFromSpawnBaseDir and the Start() lint in
// issue1713_workdir_guard_test.go.
func newSpawnCommand(launcher string, args ...string) *exec.Cmd {
	cmd := execCommand(launcher, args...)
	cmd.Dir = SpawnBaseDir
	return cmd
}

// ErrWorkDirUnavailable marks a start refused because the session's working
// directory could not be used. Callers can test for it with errors.Is.
var ErrWorkDirUnavailable = errors.New("session working directory is unavailable")

// ErrPaneCwdDeleted marks a session whose pane was born in a directory that no
// longer exists — the pane holds only a broken shell and the agent never ran.
var ErrPaneCwdDeleted = errors.New("tmux pane started in a deleted working directory")

// resolveStartWorkDir absolutises and validates the directory a session is
// about to start in. It returns the path to hand to `tmux new-session -c`, or
// an error describing exactly what is wrong.
//
// Absolutising matters for two reasons: tmux resolves a relative -c against the
// spawning client's cwd (which is SpawnBaseDir now, not agent-deck's cwd), and
// an absolute path is the only form the pane-cwd verification below can compare
// against.
func resolveStartWorkDir(workDir string) (string, error) {
	dir := strings.TrimSpace(workDir)
	if dir == "" {
		return "", fmt.Errorf(
			"%w: no working directory is recorded for this session and $HOME is unset — "+
				"set the session's path (agent-deck session show <id>) before starting it",
			ErrWorkDirUnavailable)
	}

	if !filepath.IsAbs(dir) {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return "", fmt.Errorf(
				"%w: cannot resolve %q to an absolute path: %v",
				ErrWorkDirUnavailable, dir, err)
		}
		dir = abs
	}

	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf(
				"%w: %s does not exist. The session's project directory (or worktree) was "+
					"deleted or renamed. Starting anyway would not fail — tmux silently starts "+
					"the pane in $HOME instead, so the agent would run against the wrong tree. "+
					"Recreate the directory, or point the session at a new path, then start again",
				ErrWorkDirUnavailable, dir)
		}
		return "", fmt.Errorf("%w: cannot access %s: %v", ErrWorkDirUnavailable, dir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: %s is not a directory", ErrWorkDirUnavailable, dir)
	}
	return dir, nil
}

// paneCwdVerdict classifies where a freshly created pane actually landed.
type paneCwdVerdict int

const (
	// paneCwdOK — the pane is in the directory we asked for.
	paneCwdOK paneCwdVerdict = iota
	// paneCwdUnknown — we could not tell (empty/unreadable report). Never a
	// reason to fail a start.
	paneCwdUnknown
	// paneCwdDeleted — the pane is sitting in a path that does not exist. Its
	// shell cannot getcwd(); the agent did not run.
	paneCwdDeleted
	// paneCwdElsewhere — the pane is in a real but different directory (tmux
	// substituted one, or the pane command changed directory itself).
	paneCwdElsewhere
)

// classifyPaneCwd compares the requested start directory against the path tmux
// reports for the pane. Pure apart from stat()ing the two paths, so it is
// directly unit-testable.
//
// Only a path that provably does not exist yields paneCwdDeleted: an
// unreadable or ambiguous reading must never fail a start. Linux tmux reports
// an unlinked cwd as "/path (deleted)" and macOS reports the stale path
// verbatim; both fail the stat, so both are caught.
func classifyPaneCwd(requested, panePath string) paneCwdVerdict {
	panePath = strings.TrimSpace(panePath)
	if panePath == "" {
		return paneCwdUnknown
	}
	if _, err := os.Stat(panePath); err != nil {
		if os.IsNotExist(err) {
			return paneCwdDeleted
		}
		return paneCwdUnknown
	}
	if sameDirectory(requested, panePath) {
		return paneCwdOK
	}
	return paneCwdElsewhere
}

// sameDirectory reports whether two paths name the same directory, tolerating
// symlinked prefixes (macOS reports /private/tmp/x for /tmp/x).
func sameDirectory(a, b string) bool {
	if a == b {
		return true
	}
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ai, bi)
}

// panePathProbe reads the pane's live working directory. Swappable seam so the
// verification logic can be tested without a tmux server.
var panePathProbe = func(s *Session) (string, error) {
	out, err := s.runBoundedOutput("display-message", "-t", s.Name, "-p", "#{pane_current_path}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// paneCwdRecheckDelay spaces the re-probes below. tmux creates the pane process
// before it has chdir()ed, so a single early read can legitimately still show
// the server's cwd.
var paneCwdRecheckDelay = 60 * time.Millisecond

// paneCwdProbeAttempts is how many times a "deleted" reading is re-confirmed
// before it is believed. Only the broken path pays this cost.
const paneCwdProbeAttempts = 3

// verifyPaneWorkDirUnlessPlaceholder is the Start() entry point: it skips the
// check for sessions whose local directory is only a placeholder (SSH), whose
// pane runs an ssh client that does not care where it started.
func (s *Session) verifyPaneWorkDirUnlessPlaceholder(workDir string) error {
	if s.WorkDirIsPlaceholder {
		return nil
	}
	return s.verifyPaneWorkDir(workDir)
}

// verifyPaneWorkDir confirms a freshly created session's pane is not sitting in
// an unlinked directory. It returns an error only when that is provable, so a
// slow or unreadable probe can never fail an otherwise healthy start.
func (s *Session) verifyPaneWorkDir(workDir string) error {
	var panePath string
	verdict := paneCwdUnknown

	for attempt := 0; attempt < paneCwdProbeAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(paneCwdRecheckDelay)
		}
		reported, err := panePathProbe(s)
		if err != nil {
			statusLog.Debug("pane_cwd_probe_failed",
				slog.String("session", s.Name),
				slog.String("error", err.Error()))
			return nil
		}
		panePath = reported
		verdict = classifyPaneCwd(workDir, panePath)
		// Only a "deleted" reading is worth re-confirming: it is the one
		// verdict that fails the start, and the one a too-early read can
		// manufacture (the pane process exists but has not chdir()ed yet, so
		// it still shows the server's cwd).
		if verdict != paneCwdDeleted {
			break
		}
	}

	switch verdict {
	case paneCwdDeleted:
		statusLog.Error("pane_born_in_deleted_cwd",
			slog.String("session", s.Name),
			slog.String("requested_workdir", workDir),
			slog.String("pane_cwd", panePath),
			slog.String("socket", socketLabel(s.SocketName)),
			slog.String("issue", "1713"))
		return deletedPaneCwdError(s.SocketName, workDir, panePath)
	case paneCwdElsewhere:
		// Not a failure: a pane command may legitimately change directory
		// (the fork/multi-repo paths build a `cd <dir> && …` command), and
		// tmux's $HOME substitution is already prevented by
		// resolveStartWorkDir. Logged so the mismatch is diagnosable.
		statusLog.Warn("pane_cwd_differs_from_requested",
			slog.String("session", s.Name),
			slog.String("requested_workdir", workDir),
			slog.String("pane_cwd", panePath))
	}
	return nil
}

// socketLabel renders a socket name for human-facing messages.
func socketLabel(socketName string) string {
	name := strings.TrimSpace(socketName)
	if name == "" {
		return "default"
	}
	return name
}

// deletedPaneCwdError explains the poisoned-server failure and what to do about
// it. Written for someone reading a CLI error, not a stack trace.
func deletedPaneCwdError(socketName, workDir, panePath string) error {
	return fmt.Errorf(
		"%w: tmux placed the new pane in %s, which no longer exists, so the pane holds only a "+
			"broken shell (\"shell-init: error retrieving current directory\") and the agent never "+
			"started. The requested directory %s is fine — the tmux server on socket %q is itself "+
			"running from a deleted directory, and tmux then ignores the -c start directory for every "+
			"new pane. Restart that tmux server when its sessions can be recreated (all sessions on "+
			"it are affected); servers agent-deck starts now run from %s, so this cannot recur for "+
			"them (issue #1713)",
		ErrPaneCwdDeleted, panePath, workDir, socketLabel(socketName), SpawnBaseDir)
}
