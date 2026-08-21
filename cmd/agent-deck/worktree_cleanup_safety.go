package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/vcs"
)

// worktreeCleanupFacts is the reality check applied before an unregistered
// worktree may enter the orphan set. Inspection errors fail closed: cleanup is
// a destructive convenience operation, so uncertainty is not permission.
type worktreeCleanupFacts struct {
	Worktree   vcs.Worktree
	Unpushed   int
	Dirty      bool
	LivePID    int
	InspectErr error
}

// classifyUnregisteredWorktrees runs before --force is considered. Consequently
// force can only authorize removal of the returned orphan slice; it can never
// promote a protected worktree into that slice.
func classifyUnregisteredWorktrees(worktrees []vcs.Worktree, occupied map[string]bool) ([]vcs.Worktree, []worktreeCleanupFacts) {
	var orphans []vcs.Worktree
	var protected []worktreeCleanupFacts
	for i, wt := range worktrees {
		if i == 0 || occupied[wt.Path] { // first entry is the main worktree
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
	return f.Unpushed == 0 && !f.Dirty && f.LivePID == 0 && f.InspectErr == nil
}

func (f worktreeCleanupFacts) summary() string {
	parts := []string{fmt.Sprintf("%d unpushed", f.Unpushed), fmt.Sprintf("dirty=%t", f.Dirty)}
	if f.LivePID != 0 {
		parts = append(parts, fmt.Sprintf("pid %d", f.LivePID))
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
		"unpushed": f.Unpushed, "dirty": f.Dirty, "live_pid": f.LivePID,
	}
	if f.InspectErr != nil {
		data["inspection_error"] = f.InspectErr.Error()
	}
	return data
}

func inspectWorktreeForCleanup(wt vcs.Worktree) worktreeCleanupFacts {
	facts := worktreeCleanupFacts{Worktree: wt}
	out, err := exec.Command("git", "-C", wt.Path, "rev-list", "--count", "HEAD", "--not", "--remotes").Output()
	if err != nil {
		facts.InspectErr = fmt.Errorf("count unpushed commits: %w", err)
		return facts
	}
	facts.Unpushed, err = strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		facts.InspectErr = fmt.Errorf("parse unpushed count: %w", err)
		return facts
	}
	out, err = exec.Command("git", "-C", wt.Path, "status", "--porcelain", "--untracked-files=normal").Output()
	if err != nil {
		facts.InspectErr = fmt.Errorf("inspect working tree: %w", err)
		return facts
	}
	facts.Dirty = len(out) != 0
	facts.LivePID, err = processWithCWDInside(wt.Path)
	if err != nil {
		facts.InspectErr = fmt.Errorf("inspect live processes: %w", err)
	}
	return facts
}

func processWithCWDInside(root string) (int, error) {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return processWithCWDInsideLsof(root)
	}
	for _, entry := range entries {
		pid, convErr := strconv.Atoi(entry.Name())
		if convErr != nil || pid == os.Getpid() {
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
	out, err := exec.Command("lsof", "-a", "-d", "cwd", "+D", root, "-F", "p").Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 && len(out) == 0 {
			return 0, nil
		}
		return 0, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "p") {
			if pid, convErr := strconv.Atoi(strings.TrimPrefix(line, "p")); convErr == nil && pid != os.Getpid() {
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
