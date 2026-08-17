package session

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/git"
)

// ProjectDataDirName is the repository-local directory agent-deck keeps a
// project's run artifacts in. Everything scoped to one repository — handoff
// prompts, design docs, orchestrate runs — belongs under it, not under the
// machine-global data dir: those artifacts are about the user's project, they
// travel with it, and a global directory keyed only by session id turns into
// an unattributable pile the moment the sessions are gone.
//
// The machine-global data dir keeps what genuinely is machine-global: state.db,
// logs, sockets, config, conductor state.
const ProjectDataDirName = ".agent-deck"

// ProjectRoot resolves the single stable root for a session's working
// directory. Every worktree of a repository resolves to the same root — the
// main worktree — so a session running in .worktrees/feature-x and one running
// in the main checkout share one .agent-deck tree. This is the same root the
// brainstorming and orchestrate skills compute, deliberately: the Go side and
// the skills must not disagree about where a run's artifacts live.
//
// A working directory that is not in a git repository is its own root. That is
// not a fallback to somewhere global — a non-repo project still keeps its
// artifacts beside itself.
func ProjectRoot(workingDir string) (string, error) {
	dir := strings.TrimSpace(workingDir)
	if dir == "" {
		return "", fmt.Errorf("resolve project root: empty working directory")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve project root for %q: %w", dir, err)
	}
	if base, err := git.GetWorktreeBaseRoot(abs); err == nil && strings.TrimSpace(base) != "" {
		return base, nil
	}
	return abs, nil
}

// ProjectDataDir returns <project-root>/.agent-deck for a working directory.
func ProjectDataDir(workingDir string) (string, error) {
	root, err := ProjectRoot(workingDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, ProjectDataDirName), nil
}

// ProjectDataPath joins parts under a working directory's .agent-deck.
//
// Each part must be a single, literal path element. Session ids reach this
// function straight from the registry, and a separator or ".." in one would
// otherwise let a crafted id place a file anywhere in the user's repository.
func ProjectDataPath(workingDir string, parts ...string) (string, error) {
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return "", fmt.Errorf("project data path: empty path element")
		}
		if part != filepath.Base(part) || part == "." || part == ".." ||
			strings.ContainsAny(part, `/\`) {
			return "", fmt.Errorf("project data path: %q is not a single path element", part)
		}
	}
	base, err := ProjectDataDir(workingDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{base}, parts...)...), nil
}

// EnsureProjectDataPath creates the directory at ProjectDataPath, first making
// sure git ignores .agent-deck/ so agent-deck's artifacts never surface as
// untracked files in the user's own project — or get swept into a `git add -A`.
//
// The ignore rule goes through ensureRepositoryGitExclude, the same global
// excludes entry the per-session repository temp dir already relies on. One
// rule, one mechanism: a second per-repository one would only be a way for the
// two to disagree.
//
// An ignore failure is not fatal: an un-ignored directory is untidy, a missing
// handoff prompt loses work.
func EnsureProjectDataPath(workingDir string, parts ...string) (string, error) {
	dir, err := ProjectDataPath(workingDir, parts...)
	if err != nil {
		return "", err
	}
	if ignoreErr := ensureRepositoryGitExclude(); ignoreErr != nil {
		sessionLog.Warn("project_data_dir_ignore_failed",
			slog.String("dir", dir),
			slog.String("error", ignoreErr.Error()))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	return dir, nil
}

// instanceProjectDir returns the directory a session's project-scoped
// artifacts are keyed to. ProjectPath is the session's own working directory —
// the worktree path for a worktree session, the symlink tree's primary repo
// for a multi-repo one — and is what ProjectRoot then folds back to the main
// worktree.
func instanceProjectDir(inst *Instance) string {
	if inst == nil {
		return ""
	}
	if p := strings.TrimSpace(inst.ProjectPath); p != "" {
		return p
	}
	if p := strings.TrimSpace(inst.WorktreePath); p != "" {
		return p
	}
	return strings.TrimSpace(inst.EffectiveWorkingDir())
}
