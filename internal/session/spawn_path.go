package session

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"al.essio.dev/pkg/shellescape"

	"github.com/asheshgoplani/agent-deck/internal/shellwords"
)

// Spawn PATH (remote parity walk on g14, 2026-09-18).
//
// A remote agent-deck runs its `session start` under the PATH of a non-login,
// non-interactive SSH shell, and the tmux server it starts (and every pane on
// it) inherits that PATH. The user's own tools are usually installed under
// ~/.local/bin (`claude` on g14 is a symlink there), which a login shell adds
// and this environment does not, so the pane ran `exec ... claude ...`, got
// "command not found", and died in 261ms as a generic spawn_died_fast. The
// same gap is why `remote update` warns that a deployed ~/.local/bin binary
// is off the non-interactive PATH.
//
// The rule: the spawn environment prepends the standard user bin dirs that
// exist and are missing from PATH ($HOME/.local/bin, $HOME/bin, the directory
// of the running agent-deck binary, /opt/homebrew/bin on macOS, and the
// directory of an explicitly configured agent_deck_path) exactly once, in that
// order, without reordering anything the user already has. It is applied on
// every host, remote or local, so a session behaves the same wherever it is
// spawned. The prepend happens inside the pane command (buildSpawnPathExport)
// rather than in the deck process's own environment because a tmux server
// that is already running keeps the PATH it was born with: only the command
// itself is guaranteed to run in the pane.
//
// A tool that still cannot be found is reported as
// "tool not found on PATH: <tool> (searched: <PATH>)" instead of the generic
// fast death (spawnToolNotFoundReason).

// spawnPathCandidates returns the user bin dirs to consider, in the order they
// are prepended. exe is the running binary (os.Executable, symlinks resolved)
// and configured an explicit agent-deck path from config; either may be empty.
func spawnPathCandidates(home, exe, configured, goos string) []string {
	var dirs []string
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".local", "bin"), filepath.Join(home, "bin"))
	}
	if exe != "" && filepath.IsAbs(exe) {
		dirs = append(dirs, filepath.Dir(exe))
	}
	if goos == "darwin" {
		dirs = append(dirs, "/opt/homebrew/bin")
	}
	if configured != "" && strings.Contains(configured, string(os.PathSeparator)) {
		if dir := filepath.Dir(configured); filepath.IsAbs(dir) {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// missingPathDirs filters candidates down to the directories that exist and
// are not already on pathEnv, deduplicated and in candidate order.
func missingPathDirs(pathEnv string, candidates []string, isDir func(string) bool) []string {
	onPath := map[string]bool{}
	for _, d := range filepath.SplitList(pathEnv) {
		if d != "" {
			onPath[filepath.Clean(d)] = true
		}
	}
	var missing []string
	seen := map[string]bool{}
	for _, dir := range candidates {
		dir = filepath.Clean(dir)
		if seen[dir] || onPath[dir] || !isDir(dir) {
			continue
		}
		seen[dir] = true
		missing = append(missing, dir)
	}
	return missing
}

// prependPathDirs returns pathEnv with dirs in front, in order. It is the
// Go-side view of what buildSpawnPathExport does in the pane and is what the
// "searched:" part of the not-found reason reports.
func prependPathDirs(pathEnv string, dirs []string) string {
	if len(dirs) == 0 {
		return pathEnv
	}
	joined := strings.Join(dirs, string(os.PathListSeparator))
	if pathEnv == "" {
		return joined
	}
	return joined + string(os.PathListSeparator) + pathEnv
}

// buildSpawnPathExport renders the shell prelude that prepends dirs to the
// pane's PATH. Each dir is added only when it exists AND is not already on
// the PATH the pane actually has (which can differ from the deck process's
// when the tmux server predates this spawn), so evaluating it is idempotent
// and never reorders existing entries. Empty when there is nothing to add.
func buildSpawnPathExport(dirs []string) string {
	words := make([]string, 0, len(dirs))
	for _, d := range dirs {
		words = append(words, shellescape.Quote(d))
	}
	return buildSpawnPathExportWords(words)
}

// buildSpawnPathExportWords is buildSpawnPathExport over shell words that
// are already quoted, so an --ssh session can hand over "$HOME/.local/bin"
// for the remote shell to expand.
func buildSpawnPathExportWords(words []string) string {
	if len(words) == 0 {
		return ""
	}
	// __p accumulates the dirs to add in order; a single prepend at the end
	// keeps A:B:$PATH rather than the reversed order a per-dir prepend gives.
	return `for __d in ` + strings.Join(words, " ") +
		`; do case ":$PATH:" in *":$__d:"*) ;; *) [ -d "$__d" ] && __p="${__p:+$__p:}$__d";; esac; done; ` +
		`[ -n "$__p" ] && PATH="$__p:$PATH"; export PATH; unset __d __p; `
}

// spawnPathDirs resolves the dirs this process would add for a local spawn:
// the candidates that exist and are missing from the process PATH.
func (i *Instance) spawnPathDirs() []string {
	exe := ""
	if p, err := os.Executable(); err == nil {
		// Only a real agent-deck binary's directory counts; a `go test`
		// binary's build directory must not leak into every pane.
		if strings.HasPrefix(strings.ToLower(filepath.Base(p)), "agent-deck") {
			exe = normalizeExecutablePath(p)
		}
	}
	candidates := spawnPathCandidates(os.Getenv("HOME"), exe, "", runtime.GOOS)
	return missingPathDirs(os.Getenv("PATH"), candidates, func(dir string) bool {
		info, err := os.Stat(dir)
		return err == nil && info.IsDir()
	})
}

// sshSpawnPathWords is the --ssh counterpart of spawnPathDirs. The command
// runs on the remote through `ssh host '<program>'`, a non-login shell with
// the same PATH gap, but this process cannot see that host's directories:
// the home dirs travel as $HOME expressions the remote shell expands, the
// configured agent_deck_path's directory as a literal, and the prelude's
// own -d test decides what exists there.
func (i *Instance) sshSpawnPathWords() []string {
	words := []string{`"$HOME/.local/bin"`, `"$HOME/bin"`}
	if cfg, _ := LoadUserConfig(); cfg != nil {
		for _, rc := range cfg.Remotes {
			if rc.Host != i.SSHHost {
				continue
			}
			if dirs := spawnPathCandidates("", "", rc.AgentDeckPath, ""); len(dirs) > 0 {
				words = append(words, shellescape.Quote(dirs[0]))
			}
			break
		}
	}
	return words
}

// spawnSearchPath is the PATH the spawned pane resolves its tool on, as far
// as this process can tell: the process PATH with the missing user bin dirs
// in front.
func (i *Instance) spawnSearchPath() string {
	return prependPathDirs(os.Getenv("PATH"), i.spawnPathDirs())
}

// wrapSpawnPath prepends the PATH prelude to a non-empty pane command.
func (i *Instance) wrapSpawnPath(command string) string {
	if command == "" {
		return command
	}
	if i.IsSSH() {
		return buildSpawnPathExportWords(i.sshSpawnPathWords()) + command
	}
	return buildSpawnPathExport(i.spawnPathDirs()) + command
}

// spawnPathCoversUserBinDir reports whether dir is one of the standard user
// bin dirs the spawn prelude adds on any host: ~/.local/bin or ~/bin under
// home. With an unknown home the shape /home/<u>, /Users/<u> or /root is
// accepted instead.
func spawnPathCoversUserBinDir(dir, home string) bool {
	dir = cleanResolved(dir)
	if home = strings.TrimSpace(home); home != "" {
		home = cleanResolved(home)
		return dir == filepath.Join(home, ".local", "bin") || dir == filepath.Join(home, "bin")
	}
	return strings.HasSuffix(dir, "/.local/bin") || userHomeBinRe.MatchString(dir)
}

// cleanResolved cleans path and follows symlinks when it exists here (a
// resolved deploy target and the home it was derived from must compare
// equal even when one of them went through /private/var-style links).
func cleanResolved(path string) string {
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

var userHomeBinRe = regexp.MustCompile(`^/(?:home/[^/]+|Users/[^/]+|root)/bin$`)

// spawnToolLookup returns what the fast-death watcher needs to name a tool
// that is not on PATH: the program the bare command execs and the PATH the
// pane resolves it on. Both are "" for a sandboxed or --ssh session, whose
// program runs on a filesystem this process cannot check.
func (i *Instance) spawnToolLookup(command string) (binary, searchPath string) {
	if i.IsSandboxed() || i.IsSSH() {
		return "", ""
	}
	return spawnToolBinaryFromCommand(command), i.spawnSearchPath()
}

// spawnToolBinaryFromCommand returns the program a built pane command execs,
// or "" when it cannot be told without evaluating shell syntax. Statements
// are split on `;` and `&&`; the env prelude (`export`, `unset`, sourced env
// files, `[ -f ... ]` tests) is skipped, as are `exec`, an `env -u NAME`
// wrapper and inline assignments. Anything that reaches an expansion, a
// pipe, a redirection or a subshell first is refused.
func spawnToolBinaryFromCommand(command string) string {
	words, ok := shellwords.Split(command)
	if !ok {
		return ""
	}
	var statements [][]string
	var current []string
	flush := func() {
		if len(current) > 0 {
			statements = append(statements, current)
			current = nil
		}
	}
	for _, w := range words {
		if w == "&&" || w == ";" {
			flush()
			continue
		}
		if strings.HasSuffix(w, ";") {
			if trimmed := strings.TrimSuffix(w, ";"); trimmed != "" {
				current = append(current, trimmed)
			}
			flush()
			continue
		}
		current = append(current, w)
	}
	flush()
	for _, statement := range statements {
		switch statement[0] {
		case "export", "unset", ".", "source", "[", "test":
			continue
		}
		skipNext := false
		for _, w := range statement {
			if skipNext {
				skipNext = false
				continue
			}
			switch {
			case w == "exec" || w == "env":
				continue
			case w == "-u":
				skipNext = true
				continue
			case isShellAssignment(w):
				continue
			case strings.ContainsAny(w, "&|<>$`(){}"):
				return ""
			}
			return w
		}
	}
	return ""
}

// isShellAssignment reports whether word is a NAME=value shell assignment.
func isShellAssignment(word string) bool {
	eq := strings.IndexByte(word, '=')
	if eq <= 0 {
		return false
	}
	for i, r := range word[:eq] {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || (i > 0 && r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

// spawnToolNotFoundReason returns the explicit failure reason when binary
// cannot be resolved on searchPath, or "" when it can (or is unknown).
func spawnToolNotFoundReason(binary, searchPath string) string {
	if binary == "" {
		return ""
	}
	if strings.Contains(binary, string(os.PathSeparator)) {
		if isExecutableFile(binary) {
			return ""
		}
	} else {
		for _, dir := range filepath.SplitList(searchPath) {
			if dir != "" && isExecutableFile(filepath.Join(dir, binary)) {
				return ""
			}
		}
	}
	return fmt.Sprintf("%s%s (searched: %s)", spawnToolNotFoundPrefix, binary, searchPath)
}

// isExecutableFile reports whether path is a regular file with an execute bit.
func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0
}

// spawnToolNotFoundPrefix is how a not-found reason starts; the record and
// its renderers key on it.
const spawnToolNotFoundPrefix = "tool not found on PATH: "

// IsToolNotFound reports whether the record's reason is the explicit
// tool-not-on-PATH failure rather than a generic fast death.
func (r *SpawnFailureRecord) IsToolNotFound() bool {
	return r != nil && strings.HasPrefix(r.Reason, spawnToolNotFoundPrefix)
}
