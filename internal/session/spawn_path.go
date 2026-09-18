package session

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"

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
// are missing from PATH, exactly once, in this documented order, without
// reordering anything the user already has:
//
//  1. $HOME/.local/bin
//  2. $HOME/bin
//  3. the directory of the running agent-deck binary
//  4. /opt/homebrew/bin (macOS only)
//  5. the directory of an explicitly configured agent_deck_path (--ssh)
//
// A candidate is prepended only when it is an absolute path to an existing
// directory that is not world-writable and is owned by the current user or
// root (spawnPathDirUsable): the prelude puts these dirs in FRONT of every
// tool the session and its children launch, so a directory anyone else can
// write to (/tmp, a shared checkout) must never get there, whichever binary
// happened to be run. It is applied on every host, remote or local, so a
// session behaves the same wherever it is spawned. The prepend happens inside
// the pane command (buildSpawnPathExport) rather than in the deck process's
// own environment because a tmux server that is already running keeps the
// PATH it was born with: only the command itself is guaranteed to run in the
// pane.
//
// A tool that still cannot be found is reported as
// "tool not found on PATH: <tool> (searched: <PATH>)" instead of the generic
// fast death, but only on the pane's own evidence: a probe run inside the
// pane environment after the PATH prelude (spawnToolProbe). The deck
// process's PATH is never consulted for that verdict, because the pane may
// resolve the tool through a richer tmux PATH, a launch shell's aliases and
// functions, or an env file; without the pane's evidence the generic reason
// stands together with the dying output.

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

// missingPathDirs filters candidates down to the directories usable
// reports as safe to prepend (spawnPathDirUsable for real spawns) and not
// already on pathEnv, deduplicated and in candidate order. A relative
// candidate is dropped here too: the pane's working directory is not known.
func missingPathDirs(pathEnv string, candidates []string, usable func(string) bool) []string {
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
		if seen[dir] || onPath[dir] || !filepath.IsAbs(dir) || !usable(dir) {
			continue
		}
		seen[dir] = true
		missing = append(missing, dir)
	}
	return missing
}

// spawnPathDirUsable reports whether dir may be put in front of a spawned
// session's PATH: an existing directory that nobody but its owner (this
// user or root) can write to. A world-writable directory would let any
// other user on the host plant a binary that every session then runs; a
// directory owned by someone else is theirs to change. The sticky bit is no
// exemption (/tmp is sticky and still world-writable).
func spawnPathDirUsable(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	return spawnPathDirInfoUsable(info, os.Geteuid())
}

// spawnPathDirInfoUsable is spawnPathDirUsable's verdict on a stat result.
func spawnPathDirInfoUsable(info os.FileInfo, uid int) bool {
	if info.Mode().Perm()&0o002 != 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return int(stat.Uid) == uid || stat.Uid == 0
}

// buildSpawnPathExport renders the shell prelude that prepends dirs to the
// pane's PATH. Each dir is added only when it exists AND is not already on
// the PATH the pane actually has (which can differ from the deck process's
// when the tmux server predates this spawn), so evaluating it is idempotent
// and never reorders existing entries. Empty when there is nothing to add.
// dirs have already passed spawnPathDirUsable here.
func buildSpawnPathExport(dirs []string) string {
	words := make([]string, 0, len(dirs))
	for _, d := range dirs {
		words = append(words, shellescape.Quote(d))
	}
	return buildSpawnPathExportWords(words, `[ -d "$__d" ]`)
}

// buildSpawnPathExportWords is buildSpawnPathExport over shell words that
// are already quoted, so an --ssh session can hand over "$HOME/.local/bin"
// for the remote shell to expand. test is the per-dir shell test that
// decides whether a dir is added; the local prelude only asks whether it
// exists (the deck process already checked ownership and mode), the --ssh
// prelude also asks whether it is owned by the remote user (sshSpawnPathTest).
func buildSpawnPathExportWords(words []string, test string) string {
	if len(words) == 0 {
		return ""
	}
	// __p accumulates the dirs to add in order; a single prepend at the end
	// keeps A:B:$PATH rather than the reversed order a per-dir prepend gives.
	return `for __d in ` + strings.Join(words, " ") +
		`; do case ":$PATH:" in *":$__d:"*) ;; *) ` + test + ` && __p="${__p:+$__p:}$__d";; esac; done; ` +
		`[ -n "$__p" ] && PATH="$__p:$PATH"; export PATH; unset __d __p; `
}

// sshSpawnPathTest is the per-dir test of the --ssh prelude. The controller
// cannot stat the remote's directories, so the remote shell checks what it
// can: the dir exists and is owned by the user the session runs as (-O).
// A configured agent_deck_path directory owned by root (/usr/local/bin) is
// therefore not added there; such a directory is on the remote's PATH
// already, or the user put the binary there knowing it is not.
const sshSpawnPathTest = `[ -d "$__d" ] && [ -O "$__d" ]`

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
	return missingPathDirs(os.Getenv("PATH"), candidates, spawnPathDirUsable)
}

// sshSpawnPathWords is the --ssh counterpart of spawnPathDirs. The command
// runs on the remote through `ssh host '<program>'`, a non-login shell with
// the same PATH gap, but this process cannot see that host's directories:
// the home dirs travel as $HOME expressions the remote shell expands, the
// configured agent_deck_path's directory as a literal, and the prelude's
// own test (sshSpawnPathTest) decides what exists there.
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

// wrapSpawnPath prepends the PATH prelude, then the tool probe, to a
// non-empty pane command. With nothing to add the command is returned
// byte-identical: no prelude, and no probe either (see spawnToolLookup).
func (i *Instance) wrapSpawnPath(command string) string {
	if command == "" {
		return command
	}
	if i.IsSSH() {
		return buildSpawnPathExportWords(i.sshSpawnPathWords(), sshSpawnPathTest) + command
	}
	dirs := i.spawnPathDirs()
	if len(dirs) == 0 {
		return command
	}
	prelude := buildSpawnPathExport(dirs)
	if tool, marker := i.spawnToolLookup(command); tool != "" {
		// A marker left by an earlier spawn of this instance (the tool was
		// missing then, and an exit-to-shell pane never died) must not be
		// read as evidence about this one.
		_ = os.Remove(marker)
		if err := os.MkdirAll(filepath.Dir(marker), 0o700); err == nil {
			prelude += buildSpawnToolProbe(tool, marker)
		}
	}
	return prelude + command
}

// Tool probe.
//
// Whether the pane can find its tool is decided in the pane: right after
// the PATH prelude, `command -v <tool>` runs in the pane's own shell (so a
// launch shell's rc files, functions and aliases count, and so does a tmux
// server born with a richer PATH than this process has) and, only when it
// fails, writes the pane's PATH to a marker file next to the spawn-failure
// records. The fast-death watcher promotes a death to "tool not found on
// PATH" only when that marker exists, and reports the PATH the pane
// searched, never this process's. Without a marker the generic reason stands.
//
// The probe accompanies the PATH prelude: it answers whether the dirs the
// deck added were enough. When the deck has nothing to add the pane command
// stays byte-identical to what it was before this file existed, and a fast
// death keeps the generic reason with the dying output.
//
// The probe is emitted only when the command's shape lets it prove
// something: the first program to run is a plain word and nothing before it
// can change how that word resolves (spawnToolProbeTarget). A sourced env
// file, an init script, an `eval`, or a PATH assignment ahead of the tool
// disables the probe rather than risking a wrong verdict.

// spawnToolLookup returns the tool the probe checks and the marker it
// writes, or "" when no probe accompanies this spawn: nothing to prepend, a
// command whose shape allows no verdict, or a sandboxed or --ssh session,
// whose program runs where this process cannot read the marker.
func (i *Instance) spawnToolLookup(command string) (tool, marker string) {
	if i.IsSandboxed() || i.IsSSH() || len(i.spawnPathDirs()) == 0 {
		return "", ""
	}
	tool = spawnToolProbeTarget(command)
	if tool == "" {
		return "", ""
	}
	return tool, spawnToolProbeMarkerPath(i.ID)
}

// spawnToolProbeMarkerPath is where the probe for instanceID records a
// missing tool: the pane's PATH, one line, beside the instance's
// spawn-failure record (spawnFailureRecordPath).
func spawnToolProbeMarkerPath(instanceID string) string {
	return filepath.Join(spawnFailureDir(), instanceID+".notfound")
}

// buildSpawnToolProbe renders the in-pane probe for tool: when the shell
// cannot resolve it, the pane's PATH is written to marker. The command's
// own failure is left to happen (and to print the shell's own message);
// the probe only records the evidence.
func buildSpawnToolProbe(tool, marker string) string {
	return `command -v ` + shellescape.Quote(tool) + ` >/dev/null 2>&1 || printf "%s\n" "$PATH" > ` + shellescape.Quote(marker) + ` 2>/dev/null; `
}

// spawnToolProbeTarget returns the program a built pane command runs first,
// or "" when the shape allows no verdict. Statements are split on `;` and
// `&&`; `export` and `unset` statements and `[ ... ]` tests are skipped, as
// are `exec`, an `env -u NAME` wrapper and inline assignments in front of
// the program. Anything that could change how the program resolves before
// it runs (a sourced file, `eval`, a PATH assignment), a shell builtin or
// keyword as the program, or an expansion, pipe, redirection or subshell
// reached first, refuses the probe.
func spawnToolProbeTarget(command string) string {
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
		case ".", "source", "eval":
			return ""
		case "export", "unset", "[", "test":
			for _, w := range statement[1:] {
				if w == "PATH" || strings.HasPrefix(w, "PATH=") {
					return ""
				}
			}
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
			case strings.HasPrefix(w, "PATH="):
				return ""
			case isShellAssignment(w):
				continue
			case strings.ContainsAny(w, "&|<>$`(){}"):
				return ""
			case spawnShellBuiltins[w]:
				return ""
			}
			return w
		}
	}
	return ""
}

// spawnShellBuiltins are words a custom command may start with that no
// PATH lookup answers for (a builtin or keyword the shell runs itself); a
// probe for them proves nothing.
var spawnShellBuiltins = map[string]bool{
	"cd": true, "echo": true, "printf": true, "eval": true, "exit": true, "set": true,
	"true": true, "false": true, ":": true, "trap": true, "wait": true, "read": true,
	"if": true, "while": true, "until": true, "for": true, "case": true, "!": true,
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

// spawnToolNotFoundReason reads the probe's verdict for tool: the
// explicit failure reason when marker exists (the pane could not resolve
// the tool on the PATH the marker holds), or "" when there is no evidence.
// The marker is consumed either way.
func spawnToolNotFoundReason(tool, marker string) string {
	if tool == "" || marker == "" {
		return ""
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		return ""
	}
	_ = os.Remove(marker)
	searched := strings.TrimSpace(string(data))
	if searched == "" {
		searched = "(empty PATH)"
	}
	return fmt.Sprintf("%s%s (searched: %s)", spawnToolNotFoundPrefix, tool, searched)
}

// clearSpawnToolProbeMarker drops the marker for instanceID, if any (a
// pane that outlived the fast-death window keeps its verdict to itself).
func clearSpawnToolProbeMarker(instanceID string) {
	_ = os.Remove(spawnToolProbeMarkerPath(instanceID))
}

// spawnToolNotFoundPrefix is how a not-found reason starts; the record and
// its renderers key on it.
const spawnToolNotFoundPrefix = "tool not found on PATH: "

// IsToolNotFound reports whether the record's reason is the explicit
// tool-not-on-PATH failure rather than a generic fast death.
func (r *SpawnFailureRecord) IsToolNotFound() bool {
	return r != nil && strings.HasPrefix(r.Reason, spawnToolNotFoundPrefix)
}
