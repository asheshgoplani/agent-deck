package session

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// Codex (0.155+) creates a thread when the composer first appears, but writes
// its rollout only when the first turn starts. Until then the thread's only
// durable trace is the writer lock the live process holds open:
//
//	$CODEX_HOME/thread-writer-locks/<thread-id>.lock
//
// Ephemeral helper threads (thread-title generation) never write a rollout and
// never take a writer lock, so a held lock names a thread that will own the
// rollout.
var codexThreadWriterLockPathRE = regexp.MustCompile(`/thread-writer-locks/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.lock`)

// codexPaneOpenPaths lists the paths the pane's live Codex processes hold
// open. A non-nil error means the list may be incomplete. Test seam.
var codexPaneOpenPaths = (*Instance).openCodexProcessPaths

func (i *Instance) openCodexProcessPaths() ([]string, error) {
	pids, probeErr := i.collectCodexProcessCandidates()
	var paths []string
	for _, pid := range pids {
		var open []string
		var err error
		if runtime.GOOS == "linux" {
			open, err = procFDLinkTargets(pid)
		} else {
			open, err = procfdOpenVnodePaths(pid)
		}
		paths = append(paths, open...)
		probeErr = errors.Join(probeErr, err)
	}
	return paths, probeErr
}

// procFDLinkTargets lists the targets of /proc/<pid>/fd. Descriptors that
// close mid-scan are skipped.
func procFDLinkTargets(pid int) ([]string, error) {
	fdDir := filepath.Join("/proc", strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return nil, err
	}
	var targets []string
	for _, entry := range entries {
		if target, err := os.Readlink(filepath.Join(fdDir, entry.Name())); err == nil {
			targets = append(targets, target)
		}
	}
	return targets, nil
}

// LiveCodexThreadID returns the single Codex thread the pane's live Codex
// process owns, from the rollout and writer lock it holds open under this
// instance's Codex home. It returns "" when the evidence is incomplete or
// names more than one thread (for example after /new, while the old rollout is
// still open), so callers fail closed rather than guess.
func (i *Instance) LiveCodexThreadID() string {
	if i == nil || !IsCodexCompatible(i.Tool) || !i.CodexRolloutIsResolvableLocally() {
		return ""
	}
	paths, err := codexPaneOpenPaths(i)
	if err != nil {
		return ""
	}
	lockDir := filepath.Join(ExpandPath(i.getCodexHomeDir()), "thread-writer-locks")
	if resolved, err := filepath.EvalSymlinks(lockDir); err == nil {
		lockDir = resolved
	}
	lockDir += string(filepath.Separator)
	owned := ""
	for _, path := range paths {
		id := extractCodexSessionIDFromPath(path)
		if id == "" && strings.HasPrefix(path, lockDir) {
			if m := codexThreadWriterLockPathRE.FindStringSubmatch(path); m != nil {
				id = m[1]
			}
		}
		if id == "" {
			continue
		}
		if owned != "" && owned != id {
			return ""
		}
		owned = id
	}
	return owned
}
