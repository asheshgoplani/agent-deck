package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/asheshgoplani/agent-deck/internal/vcs"
)

type worktreeCleanupLock struct{ file *os.File }

func acquireWorktreeCleanupLock(repoDir string) (*worktreeCleanupLock, error) {
	out, err := exec.Command("git", "-C", repoDir, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return nil, fmt.Errorf("resolve git common directory: %w", err)
	}
	commonDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(repoDir, commonDir)
	}
	file, err := os.OpenFile(filepath.Join(commonDir, "agent-deck-worktree-cleanup.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &worktreeCleanupLock{file: file}, nil
}

func (l *worktreeCleanupLock) release() {
	if l == nil || l.file == nil {
		return
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	_ = l.file.Close()
}

// worktreeCleanupFacts is the reality check applied before an unregistered
// worktree may enter the orphan set. Inspection errors fail closed: cleanup is
// a destructive convenience operation, so uncertainty is not permission.
type worktreeCleanupFacts struct {
	Worktree   vcs.Worktree
	Unpushed   *int
	Dirty      *bool
	LivePID    *int
	InspectErr error
}

// classifyUnregisteredWorktrees runs before --force is considered. Consequently
// force can only authorize removal of the returned orphan slice; it can never
// promote a protected worktree into that slice.
func classifyUnregisteredWorktrees(worktrees []vcs.Worktree, occupied map[string]bool) ([]vcs.Worktree, []worktreeCleanupFacts) {
	occupied = canonicalizePathSet(occupied)
	var orphans []vcs.Worktree
	var protected []worktreeCleanupFacts
	for i, wt := range worktrees {
		if i == 0 || occupied[canonicalPathKey(wt.Path)] { // first entry is the main worktree
			continue
		}
		facts := inspectWorktreeForCleanup(wt)
		if facts.safeToRemove() {
			orphans = append(orphans, wt)
		} else {
			protected = append(protected, facts)
		}
	}
	return orphans, protected
}

func (f worktreeCleanupFacts) safeToRemove() bool {
	return f.Unpushed != nil && *f.Unpushed == 0 &&
		f.Dirty != nil && !*f.Dirty &&
		f.LivePID != nil && *f.LivePID == 0 &&
		f.InspectErr == nil
}

func (f worktreeCleanupFacts) summary() string {
	parts := make([]string, 0, 4)
	if f.Unpushed == nil {
		parts = append(parts, "unpushed unknown")
	} else {
		parts = append(parts, fmt.Sprintf("%d unpushed", *f.Unpushed))
	}
	if f.Dirty == nil {
		parts = append(parts, "dirty=unknown")
	} else {
		parts = append(parts, fmt.Sprintf("dirty=%t", *f.Dirty))
	}
	if f.LivePID == nil {
		parts = append(parts, "pid unknown")
	} else if *f.LivePID != 0 {
		parts = append(parts, fmt.Sprintf("pid %d", *f.LivePID))
	} else {
		parts = append(parts, "pid none")
	}
	if f.InspectErr != nil {
		parts = append(parts, "inspection failed: "+f.InspectErr.Error())
	}
	return strings.Join(parts, ", ")
}

func (f worktreeCleanupFacts) jsonData() map[string]interface{} {
	data := map[string]interface{}{
		"path": f.Worktree.Path, "branch": f.Worktree.Branch,
	}
	if f.Unpushed != nil {
		data["unpushed"] = *f.Unpushed
	}
	if f.Dirty != nil {
		data["dirty"] = *f.Dirty
	}
	if f.LivePID != nil {
		data["live_pid"] = *f.LivePID
	}
	if f.InspectErr != nil {
		data["inspection_error"] = f.InspectErr.Error()
	}
	return data
}

func inspectWorktreeForCleanup(wt vcs.Worktree) worktreeCleanupFacts {
	facts := worktreeCleanupFacts{Worktree: wt}
	// #nosec G204 -- "git" is a fixed binary invoked with an argv (no shell);
	// wt.Path comes from `git worktree list --porcelain` on the local repo.
	out, err := exec.Command("git", "-C", wt.Path, "rev-list", "--count", "HEAD", "--not", "--remotes").Output()
	if err != nil {
		facts.InspectErr = fmt.Errorf("count unpushed commits: %w", err)
		return facts
	}
	unpushed, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		facts.InspectErr = fmt.Errorf("parse unpushed count: %w", err)
		return facts
	}
	facts.Unpushed = &unpushed
	// #nosec G204 -- same as above: fixed binary, argv exec, repo-derived path.
	out, err = exec.Command("git", "-C", wt.Path, "status", "--porcelain", "--untracked-files=normal").Output()
	if err != nil {
		facts.InspectErr = fmt.Errorf("inspect working tree: %w", err)
		return facts
	}
	dirty := len(out) != 0
	facts.Dirty = &dirty
	livePID, err := processWithCWDInside(wt.Path)
	if err != nil {
		facts.InspectErr = fmt.Errorf("inspect live processes: %w", err)
		return facts
	}
	facts.LivePID = &livePID
	return facts
}

func canonicalizePathSet(paths map[string]bool) map[string]bool {
	canonical := make(map[string]bool, len(paths))
	for path, occupied := range paths {
		if occupied {
			canonical[canonicalPathKey(path)] = true
		}
	}
	return canonical
}

func canonicalPathKey(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(abs)
}

// revalidateCleanupCandidate is the last gate before removal. The caller must
// supply freshly loaded registry occupancy and a freshly listed worktree set.
func revalidateCleanupCandidate(candidate vcs.Worktree, current []vcs.Worktree, occupied map[string]bool) (worktreeCleanupFacts, string) {
	candidateKey := canonicalPathKey(candidate.Path)
	found := false
	for _, wt := range current {
		if canonicalPathKey(wt.Path) == candidateKey {
			found = true
			break
		}
	}
	if !found {
		return worktreeCleanupFacts{Worktree: candidate}, "no longer registered as a worktree"
	}
	if canonicalizePathSet(occupied)[candidateKey] {
		return worktreeCleanupFacts{Worktree: candidate}, "now associated with a session"
	}
	facts := inspectWorktreeForCleanup(candidate)
	if !facts.safeToRemove() {
		return facts, "reality changed: " + facts.summary()
	}
	return facts, ""
}

// removeCleanupCandidate serializes the deletion boundary across cleanup
// processes, then reloads every input that can have changed since dry-run.
func removeCleanupCandidate(backend vcs.Backend, candidate vcs.Worktree, loadOccupied func() (map[string]bool, error)) (bool, string, error) {
	lock, err := acquireWorktreeCleanupLock(backend.RepoDir())
	if err != nil {
		return false, "could not acquire cleanup lock", err
	}
	defer lock.release()
	occupied, err := loadOccupied()
	if err != nil {
		return false, "could not revalidate session ownership", err
	}
	current, err := backend.ListWorktrees()
	if err != nil {
		return false, "could not relist worktrees", err
	}
	_, reason := revalidateCleanupCandidate(candidate, current, occupied)
	if reason != "" {
		return false, reason, nil
	}
	if err := backend.RemoveWorktree(candidate.Path, false); err != nil {
		return false, "removal failed", err
	}
	return true, "", nil
}

func processWithCWDInside(root string) (int, error) {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return 0, err
	}
	// Check self explicitly before platform-specific enumeration. Besides
	// closing the original self-deletion hole, this protects platforms where
	// process listing tools omit or cannot inspect their caller.
	if cwd, cwdErr := os.Getwd(); cwdErr == nil && pathInside(root, cwd) {
		return os.Getpid(), nil
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return processWithCWDInsideLsof(root)
	}
	for _, entry := range entries {
		pid, convErr := strconv.Atoi(entry.Name())
		if convErr != nil {
			continue
		}
		cwd, linkErr := os.Readlink(filepath.Join("/proc", entry.Name(), "cwd"))
		if linkErr == nil && pathInside(root, cwd) {
			return pid, nil
		}
	}
	return 0, nil
}

func processWithCWDInsideLsof(root string) (int, error) {
	// #nosec G204 G702 -- "lsof" is a fixed binary invoked with an argv (no
	// shell); root is a worktree path from the repo's own worktree list.
	out, err := exec.Command("lsof", "-a", "-d", "cwd", "+D", root, "-F", "p").Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 && len(out) == 0 {
			return 0, nil
		}
		return 0, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "p") {
			if pid, convErr := strconv.Atoi(strings.TrimPrefix(line, "p")); convErr == nil {
				return pid, nil
			}
		}
	}
	return 0, nil
}

func pathInside(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
