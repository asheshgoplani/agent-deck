package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// tmuxInstallDirs are the well-known locations a `tmux` binary lands in but that
// the launchd default PATH (/usr/bin:/bin:/usr/sbin:/sbin) omits: Homebrew on
// Apple Silicon and Intel, then MacPorts.
var tmuxInstallDirs = []string{"/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin"}

// resolveTmuxPATH returns path augmented with the first candidate dir that holds
// a tmux binary, when tmux is not already resolvable on path. It never
// duplicates a dir already present and is a no-op when tmux is resolvable or no
// candidate has it. Pure (deps injected) so it is unit-testable.
func resolveTmuxPATH(path string, tmuxResolvable bool, candidates []string, hasTmux func(dir string) bool) string {
	if tmuxResolvable {
		return path
	}
	onPath := map[string]bool{}
	for _, d := range strings.Split(path, string(os.PathListSeparator)) {
		onPath[d] = true
	}
	for _, dir := range candidates {
		if onPath[dir] || !hasTmux(dir) {
			continue
		}
		if path == "" {
			return dir
		}
		return dir + string(os.PathListSeparator) + path
	}
	return path
}

// ensureTmuxOnPath augments the process $PATH so bare `tmux` invocations resolve
// even when agent-deck was launched from a minimal environment. The critical
// case: a notification click runs `terminal-notifier -execute "... agent-deck
// session focus <id> --attach"`, which inherits the launchd default PATH with no
// Homebrew dir — so every tmux call (switch-client, detach-client, list-clients)
// silently fails and the notification switch can never fire (it falls back to a
// focus_request the paused TUI only consumes on Ctrl+Q). Idempotent and a no-op
// when tmux is already resolvable.
func ensureTmuxOnPath() {
	_, err := exec.LookPath("tmux")
	newPath := resolveTmuxPATH(os.Getenv("PATH"), err == nil, tmuxInstallDirs, func(dir string) bool {
		info, statErr := os.Stat(filepath.Join(dir, "tmux"))
		return statErr == nil && !info.IsDir()
	})
	if newPath != os.Getenv("PATH") {
		_ = os.Setenv("PATH", newPath)
	}
}
